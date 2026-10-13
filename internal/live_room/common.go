package live_room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/shr-go/bili_live_tui/api"
)

func boundedClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	if c.Timeout <= 0 || c.Timeout > ioTimeout {
		c.Timeout = ioTimeout
	}
	return &c
}
func getJSON(ctx context.Context, client *http.Client, u string, out interface{}) error {
	ctx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := boundedClient(client).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame+1))
	if err != nil {
		return err
	}
	if len(body) > maxFrame {
		return fmt.Errorf("HTTP body too large")
	}
	return json.Unmarshal(body, out)
}
func AuthAndConnect(client *http.Client, id uint64) (*api.LiveRoom, error) {
	client = boundedClient(client)
	uid := uint64(0)
	if u := GetUserInfo(client); u != nil {
		uid = u.Data.Mid
	}
	info, err := GetRoomInfo(client, id)
	if err != nil {
		return nil, err
	}
	room := api.NewLiveRoom()
	room.Client = client
	room.UID = uid
	room.RoomID = uint64(info.Data.RoomId)
	room.Title = info.Data.Title
	room.ShortID = uint64(info.Data.ShortId)
	room.OwnerId = uint64(info.Data.Uid)
	room.CSRF = getCSRF(client)
	fail := func(err error) (*api.LiveRoom, error) { room.Finish(); room.Close(); return nil, err }
	if CheckAuth(client) {
		user, err := GetUserRoomInfo(client, room.RoomID)
		if err != nil {
			return fail(err)
		}
		room.RoomUserInfo = &user.Data.Property
	}
	token, err := getDanmuInfo(room.Context(), client, room.RoomID)
	if err != nil {
		return fail(err)
	}
	c, err := connect(room.Context(), uid, room.RoomID, token)
	if err != nil {
		return fail(err)
	}
	go supervise(room, c)
	return room, nil
}
func processHeartBeat(room *api.LiveRoom) {
	interval := 20
	for {
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-room.DoneChan:
			timer.Stop()
			return
		case <-timer.C:
		}
		interval = roomHeartBeat(room.Context(), room.Client, interval, room.RoomID)
	}
}
func roomHeartBeatReq(client *http.Client, interval int, id uint64) int {
	return roomHeartBeat(context.Background(), client, interval, id)
}
func roomHeartBeat(ctx context.Context, client *http.Client, interval int, id uint64) int {
	if interval <= 0 || interval > 3600 {
		interval = 20
	}
	hb := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%d|1|0", interval, id)))
	u := "https://live-trace.bilibili.com/xlive/rdata-interface/v1/heartbeat/webHeartBeat?" + url.Values{"hb": {hb}, "pf": {"web"}}.Encode()
	var result struct {
		Code int `json:"code"`
		Data struct {
			NextInterval int `json:"next_interval"`
		} `json:"data"`
	}
	if getJSON(ctx, client, u, &result) != nil || result.Code != 0 || result.Data.NextInterval <= 0 || result.Data.NextInterval > 3600 {
		return interval
	}
	return result.Data.NextInterval
}
