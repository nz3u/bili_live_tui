package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEnterStartsInputAfterResize(t *testing.T) {
	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	m := InitialModel(room)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(model)
	updated, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(model)
	if got.state != inputView || !got.textInput.Focused() || cmd == nil {
		t.Fatal("Enter did not focus the danmu input")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("弹幕 draft")})
	got = updated.(model)
	if got.textInput.Value() != "弹幕 draft" {
		t.Fatal("input did not receive keyboard text")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.state != contentView || got.textInput.Focused() || got.textInput.Value() != "弹幕 draft" {
		t.Fatal("Esc lost draft or failed to return to browsing")
	}
}

func TestInputPlaceholderUTF8AcrossFocusAndResize(t *testing.T) {
	// Force real SGR styles even when tests run with redirected stdout: the
	// byte split is hidden when cursor and placeholder styles emit no escapes.
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(profile)
	room := uiTestRoom()
	defer room.Close()
	got := *InitialModel(room)
	check := func() {
		t.Helper()
		for _, view := range []string{got.textInput.View(), got.View()} {
			if !utf8.ValidString(view) {
				t.Fatalf("cursor styling split UTF-8 placeholder: %q", view)
			}
		}
		if got.state == inputView && !strings.Contains(got.textInput.View(), "输入弹幕") {
			t.Fatal("focused hint lost its first Chinese character")
		}
	}
	for i := 0; i < 3; i++ {
		updated, _ := got.Update(tea.WindowSizeMsg{Width: 80 + i*10, Height: 24})
		got = updated.(model)
		check()
		for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyTab, tea.KeyEnter, tea.KeyEsc} {
			updated, _ = got.Update(tea.KeyMsg{Type: key})
			got = updated.(model)
			check()
			updated, _ = got.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
			got = updated.(model)
			check()
		}
	}
}

func TestRoomSwitchWorksWhileTyping(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig.RoomID, LiveConfig.RoomIDs = 1, []uint64{1, 2}
	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	room.RoomID = 1
	m := InitialModel(room)
	m.state = inputView
	m.textInput.Focus()
	m.textInput.SetValue("draft")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	got := updated.(model)
	if cmd == nil || !got.switching || got.textInput.Value() != "draft" {
		t.Fatal("room switch shortcut was ignored in the input area")
	}
}

func TestSingleRoomSwitchProvidesFeedback(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig.RoomID, LiveConfig.RoomIDs = 7777, nil
	m := InitialModel(uiTestRoom())
	defer m.Close()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	got := updated.(model)
	if got.switching || !strings.Contains(got.sendHint, "设置") {
		t.Fatal("single-room switch silently did nothing")
	}
}
