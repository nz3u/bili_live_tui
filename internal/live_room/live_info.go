package live_room

import (
	"context"
	"fmt"
	"github.com/shr-go/bili_live_tui/api"
	"net/http"
)

func GetRoomInfo(client *http.Client, id uint64) (*api.RoomInfoResp, error) {
	var info api.RoomInfoResp
	err := getJSON(context.Background(), client, fmt.Sprintf("https://api.live.bilibili.com/room/v1/Room/get_info?room_id=%d", id), &info)
	if err != nil {
		return nil, err
	}
	if info.Code != 0 {
		return nil, fmt.Errorf("room info: %s", info.Message)
	}
	return &info, nil
}

// GetUserRoomInfo also triggers the user-enter-room event.
func GetUserRoomInfo(client *http.Client, id uint64) (*api.UserRoomInfo, error) {
	var info api.UserRoomInfo
	err := getJSON(context.Background(), client, fmt.Sprintf("https://api.live.bilibili.com/xlive/web-room/v1/index/getInfoByUser?room_id=%d", id), &info)
	if err != nil {
		return nil, err
	}
	if info.Code != 0 {
		return nil, fmt.Errorf("user room info: %s", info.Message)
	}
	return &info, nil
}
