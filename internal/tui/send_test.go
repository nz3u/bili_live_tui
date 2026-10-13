package tui

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testSendRoom(body string, status int) *api.LiveRoom {
	return &api.LiveRoom{RoomID: 123, UID: 1, CSRF: "csrf", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func TestSendResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"success", `{"code":0}`, 200, true},
		{"success msg", `{"code":0,"msg":"ok"}`, 200, true},
		{"denied", `{"code":-101,"message":"请登录"}`, 200, false},
		{"moderation", `{"code":0,"msg":"f"}`, 200, false},
		{"missing", `{}`, 200, false},
		{"null", `{"code":null}`, 200, false},
		{"wrong type", `{"code":"0"}`, 200, false},
		{"malformed", `<html>`, 200, false},
		{"http error", `{"code":0}`, 503, false},
		{"too large", strings.Repeat("x", 65537), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := postDanmu(testSendRoom(tc.body, tc.status), "测试")
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v success=%v", err, tc.ok)
			}
		})
	}
}

func TestSendFieldsAndNoRetry(t *testing.T) {
	room := testSendRoom("", 200)
	calls := 0
	room.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if err := r.ParseMultipartForm(4096); err != nil {
			t.Fatal(err)
		}
		for k, v := range map[string]string{"roomid": "123", "csrf": "csrf", "csrf_token": "csrf", "msg": "中文 & =", "color": "16777215", "mode": "1"} {
			if r.FormValue(k) != v {
				t.Errorf("%s=%q want %q", k, r.FormValue(k), v)
			}
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("missing deadline")
		}
		return nil, errors.New("lost response")
	})
	if err := postDanmu(room, "中文 & ="); err == nil {
		t.Fatal("expected failure")
	}
	if calls != 1 {
		t.Fatalf("sent %d times", calls)
	}
}

func TestGuestAndLength(t *testing.T) {
	room := testSendRoom(`{"code":0}`, 200)
	room.UID = 0
	if postDanmu(room, "hello") == nil {
		t.Fatal("guest sent")
	}
	room.UID = 1
	if postDanmu(room, strings.Repeat("字", 21)) == nil {
		t.Fatal("length not checked")
	}
	room.RoomUserInfo = &api.UserRoomProperty{}
	room.RoomUserInfo.Danmu.Length = 30
	if err := postDanmu(room, strings.Repeat("字", 21)); err != nil {
		t.Fatal(err)
	}
}

func TestSendUIRetainsDraftAndBlocksDuplicate(t *testing.T) {
	m := InitialModel(testSendRoom(`{"code":0}`, 200))
	m.state = inputView
	m.textInput.SetValue("测试")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || !m.sending || m.textInput.Value() != "测试" {
		t.Fatal("missing pending draft")
	}
	_, duplicate := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if duplicate != nil {
		t.Fatal("duplicate send")
	}
	updated, _ = m.Update(sendResult{room: m.room, text: "测试", err: errors.New("失败")})
	m = updated.(model)
	if m.sending || m.textInput.Value() != "测试" || m.sendStatus != "失败" {
		t.Fatal("failed draft lost")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.sending || m.textInput.Value() != "" || m.sendStatus != "已发送" {
		t.Fatal("success not applied")
	}
}

func TestInputStatusUsesOneRow(t *testing.T) {
	m := InitialModel(testSendRoom(`{"code":0}`, 200))
	m.textInput.SetValue("保留草稿")
	m.sendStatus = "发送失败\n请重试"
	for _, width := range []int{1, 8, 40, 120} {
		m.width = width
		line := m.inputLine()
		if lipgloss.Height(line) > 1 || lipgloss.Width(line) > max(1, width-2) {
			t.Fatalf("width %d: %q", width, line)
		}
	}
}
