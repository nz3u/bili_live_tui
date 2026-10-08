package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shr-go/bili_live_tui/api"
)

type sendResultMsg struct {
	room    *api.LiveRoom
	content string
	reply   string
	err     error
}

// A successful HTTP response is not necessarily a successful send. Keep the
// API's status text so rejections and unusual replies are visible in the UI.
func postDanmu(room *api.LiveRoom, content string) (string, error) {
	select {
	case <-room.DoneChan:
		return "", fmt.Errorf("直播间已关闭，未发送弹幕")
	default:
	}
	if room.RoomUserInfo == nil {
		return "", fmt.Errorf("未登录，未发送弹幕，请登录后重试")
	}
	if room.CSRF == "" {
		return "", fmt.Errorf("登录凭据缺少 CSRF，请重新登录")
	}
	contentType, form := packDanmuMsgForm(generateDanmuMsg(content, room))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() {
		select {
		case <-room.DoneChan:
			cancel()
		case <-ctx.Done():
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.live.bilibili.com/msg/send", form)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := room.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("发送接口 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return "", fmt.Errorf("读取发送结果失败: %w", err)
	}
	if len(body) > 1024*1024 {
		return "", fmt.Errorf("发送接口返回内容过大")
	}
	var result struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Code == nil {
		return "", fmt.Errorf("发送接口返回无效结果")
	}
	reply := strings.TrimSpace(result.Msg)
	if reply == "" {
		reply = strings.TrimSpace(result.Message)
	}
	if *result.Code != 0 {
		return "", fmt.Errorf("发送失败 (%d): %s", *result.Code, reply)
	}
	if reply == "0" || strings.EqualFold(reply, "success") {
		reply = ""
	}
	return reply, nil
}
