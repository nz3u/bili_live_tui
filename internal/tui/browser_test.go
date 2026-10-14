package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
)

func TestRoomURL(t *testing.T) {
	for _, tc := range []struct {
		room *api.LiveRoom
		want string
	}{
		{nil, ""},
		{&api.LiveRoom{}, ""},
		{&api.LiveRoom{RoomID: 123, ShortID: 3}, "https://live.bilibili.com/123"},
		{&api.LiveRoom{ShortID: 3}, "https://live.bilibili.com/3"},
	} {
		if got := roomURL(tc.room); got != tc.want {
			t.Errorf("roomURL = %q, want %q", got, tc.want)
		}
	}
}

func TestBrowserCommands(t *testing.T) {
	const url = "https://live.bilibili.com/123"
	for _, tc := range []struct {
		os   string
		args []string
	}{
		{"windows", []string{"rundll32.exe", "url.dll,FileProtocolHandler", url}},
		{"darwin", []string{"open", url}},
		{"linux", []string{"xdg-open", url}},
	} {
		cmd, err := browserCommand(tc.os, url)
		if err != nil || !reflect.DeepEqual(cmd.Args, tc.args) {
			t.Fatalf("%s: cmd=%v err=%v", tc.os, cmd, err)
		}
	}
	if _, err := browserCommand("unsupported", url); err == nil {
		t.Fatal("unsupported OS accepted")
	}
}

func TestF6OpensActiveRoomWithoutChangingDraft(t *testing.T) {
	roomConfig(t)
	for _, state := range []sessionState{contentView, inputView} {
		m := InitialModel(fakeRoom(123))
		t.Cleanup(m.Close)
		m.state = state
		m.textInput.SetValue("保留草稿")
		// Configured/default ID is deliberately different from the active room.
		LiveConfig.RoomID = 999
		var opened string
		m.openURL = func(url string) error { opened = url; return nil }
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF6})
		m = updated.(model)
		if cmd == nil || opened != "" {
			t.Fatal("opening must be an asynchronous command")
		}
		msg := cmd()
		if opened != "https://live.bilibili.com/123" || m.textInput.Value() != "保留草稿" || m.state != state {
			t.Fatalf("wrong URL or changed draft/focus: %q", opened)
		}
		updated, _ = m.Update(msg)
		if !strings.Contains(updated.(model).sendStatus, "已请求浏览器") {
			t.Fatal("missing success status")
		}
		// During a pending switch, the old room is still the active room.
		m.switching = true
		_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyF6})
		cmd()
		if opened != "https://live.bilibili.com/123" {
			t.Fatal("opened pending room")
		}
		m.switching = false
		m.room = fakeRoom(456)
		t.Cleanup(m.room.Close)
		_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyF6})
		cmd()
		if opened != "https://live.bilibili.com/456" {
			t.Fatal("did not follow active room change")
		}
	}
}

func TestBrowserFailureAndStaleResults(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(123))
	t.Cleanup(m.Close)
	m.openURL = func(string) error { return errors.New("launcher unavailable") }
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF6})
	m = updated.(model)
	result := cmd()
	updated, _ = m.Update(result)
	if !strings.Contains(updated.(model).sendStatus, "打开直播间失败：launcher unavailable") {
		t.Fatal("launcher error not visible")
	}
	m.sendStatus = "existing status"
	for _, mode := range []string{"other room", "sending", "switching", "settings"} {
		next := m
		msg := result.(browserOpened)
		switch mode {
		case "other room":
			msg.room = &api.LiveRoom{RoomID: 999}
		case "sending":
			next.sending = true
		case "switching":
			next.switching = true
		case "settings":
			next.settings = newSettings(LiveConfig)
		}
		updated, _ = next.Update(msg)
		if updated.(model).sendStatus != "existing status" {
			t.Fatalf("overwrote %s status", mode)
		}
	}
	m.room = nil
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyF6})
	if updated.(model).sendStatus != "当前没有可打开的直播间" {
		t.Fatal("missing room not handled")
	}
}
