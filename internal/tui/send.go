package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
)

type sendResult struct {
	room *api.LiveRoom
	text string
	err  error
}

// Never retry this POST automatically: a lost response may already have been delivered.
func sendDanmu(room *api.LiveRoom, text string) tea.Cmd {
	return func() tea.Msg {
		result := sendResult{room: room, text: text}
		result.err = postDanmu(room, text)
		return result
	}
}

func danmuLimit(room *api.LiveRoom) int {
	if room.RoomUserInfo != nil && room.RoomUserInfo.Danmu.Length > 0 {
		return room.RoomUserInfo.Danmu.Length
	}
	return 20
}

func postDanmu(room *api.LiveRoom, text string) error {
	if room == nil || room.Client == nil || room.UID == 0 || room.CSRF == "" {
		return fmt.Errorf("未登录，不能发送")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("请输入弹幕")
	}
	if utf8.RuneCountInString(text) > danmuLimit(room) {
		return fmt.Errorf("最多 %d 字", danmuLimit(room))
	}
	// Room property lookup is optional; default styling must not disable authenticated sending.
	contentType, form := packDanmuMsgForm(generateDanmuMsg(text, room))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.live.bilibili.com/msg/send", form)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Origin", "https://live.bilibili.com")
	req.Header.Set("Referer", fmt.Sprintf("https://live.bilibili.com/%d", room.RoomID))
	resp, err := room.Client.Do(req)
	if err != nil {
		return fmt.Errorf("结果未知，请确认后重试")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return fmt.Errorf("响应异常，请确认后重试")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d，请确认后重试", resp.StatusCode)
	}
	var data struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if json.Unmarshal(body, &data) != nil || data.Code == nil {
		return fmt.Errorf("响应异常，请确认后重试")
	}
	message := data.Message
	if message == "" {
		message = data.Msg
	}
	if *data.Code != 0 {
		return fmt.Errorf("发送失败 (%d) %s", *data.Code, singleLine(message))
	}
	// The send API can report moderation/duplicate rejection in msg even with code=0.
	if message != "" && message != "0" && !strings.EqualFold(message, "ok") && !strings.EqualFold(message, "success") {
		return fmt.Errorf("未确认发送：%s", singleLine(message))
	}
	return nil
}

func singleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
