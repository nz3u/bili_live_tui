package live_room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/go-querystring/query"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"io"
	"net/http"
	"time"
)

func AuthAndConnect(client *http.Client, roomID uint64) (*api.LiveRoom, error) {
	return AuthAndConnectContext(context.Background(), client, roomID)
}

// The context covers setup only; a returned room owns its own lifetime.
func AuthAndConnectContext(ctx context.Context, client *http.Client, roomID uint64) (room *api.LiveRoom, err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	userInfo, err := getUserInfoContext(ctx, client)
	if err != nil {
		return nil, err
	}
	uid := uint64(0)
	if userInfo != nil {
		uid = userInfo.Data.Mid
	}
	roomInfo, err := getRoomInfo(ctx, client, roomID)
	if err != nil {
		return
	}
	realRoomID := uint64(roomInfo.Data.RoomId)
	info, err := getDanmuInfo(ctx, client, realRoomID)
	if err != nil {
		return
	}
	// Fetch all metadata before starting workers. A second, transient auth
	// check must not silently turn an authenticated user into a guest.
	var property *api.UserRoomProperty
	csrf := ""
	if uid != 0 {
		userRoomInfo, err := getUserRoomInfo(ctx, client, realRoomID)
		if err != nil {
			return nil, err
		}
		property = &userRoomInfo.Data.Property
		csrf = getCSRF(client)
		if csrf == "" {
			return nil, fmt.Errorf("登录凭据缺少 bili_jct，请重新登录")
		}
	}
	room, err = newLiveRoomContext(ctx, uid, realRoomID, info, client)
	if err != nil {
		return
	}
	room.Title = roomInfo.Data.Title
	room.ShortID = uint64(roomInfo.Data.ShortId)
	room.OwnerId = uint64(roomInfo.Data.Uid)
	room.RoomUserInfo = property
	room.CSRF = csrf
	if err := ctx.Err(); err != nil {
		_ = room.StreamConn.Close()
		room.Close()
		return nil, err
	}
	go monitorConn(room)
	if property != nil {
		go processHeartBeat(room)
	}
	return
}

func processHeartBeat(room *api.LiveRoom) {
	nextInterval := 20
	heartBeatTicker := time.NewTicker(time.Duration(nextInterval) * time.Second)
	defer heartBeatTicker.Stop()
Loop:
	for {
		select {
		case <-room.DoneChan:
			break Loop
		case <-heartBeatTicker.C:
			newNextInterval := roomHeartBeatReq(room.Client, nextInterval, room.RoomID)
			if newNextInterval != nextInterval {
				nextInterval = newNextInterval
				heartBeatTicker.Reset(time.Duration(nextInterval) * time.Second)
			}
		}
	}
}

func roomHeartBeatReq(client *http.Client, nextInterval int, realRoomID uint64) int {
	logging.Debugf("roomHeartBeatReq, nextInterval=%d, realRoomID=%d", nextInterval, realRoomID)
	hb := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%d|1|0", nextInterval, realRoomID)))
	params := struct {
		HB string `url:"hb"`
		PF string `url:"pf"`
	}{
		HB: hb,
		PF: "web",
	}
	v, err := query.Values(params)
	if err != nil {
		logging.Errorf("heart beat error, err=%v", err)
	}
	baseURL := "https://live-trace.bilibili.com/xlive/rdata-interface/v1/heartbeat/webHeartBeat"
	realUrl := fmt.Sprintf("%s?%s", baseURL, v.Encode())
	resp, err := client.Get(realUrl)
	if err != nil {
		logging.Errorf("heart beat error, err=%v", err)
		return nextInterval
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logging.Errorf("heart beat error, err=%v", err)
		return nextInterval
	}
	var data struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Ttl     int    `json:"ttl"`
		Data    struct {
			NextInterval int `json:"next_interval"`
		} `json:"data"`
	}

	if err = json.Unmarshal(body, &data); err != nil || data.Code != 0 || data.Data.NextInterval <= 0 {
		logging.Errorf("heart beat error, err=%v, data=%v", err, data)
		return nextInterval
	}
	return data.Data.NextInterval
}
