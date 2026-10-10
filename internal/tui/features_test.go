package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
)

func TestRoomPageURLUsesShortIDThenFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		room    *api.LiveRoom
		wantURL string
	}{
		{"short id preferred", &api.LiveRoom{RoomID: 7777, ShortID: 2693345}, "https://live.bilibili.com/2693345"},
		{"physical id fallback", &api.LiveRoom{RoomID: 7777}, "https://live.bilibili.com/7777"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := roomPageURL(tc.room); got != tc.wantURL {
				t.Fatalf("roomPageURL=%q want %q", got, tc.wantURL)
			}
		})
	}
}

// Alt+F opens the current room's page and must not disturb the draft or focus.
func TestAltFOpensRoomPageWithoutTouchingDraft(t *testing.T) {
	room := sendTestRoom(`{"code":0}`, 200)
	room.RoomID, room.ShortID = 7777, 2693345
	defer room.Close()

	previous := openRoomPage
	defer func() { openRoomPage = previous }()
	var opened []string
	openRoomPage = func(r *api.LiveRoom) error {
		opened = append(opened, roomPageURL(r))
		return nil
	}

	m := InitialModel(room)
	m.state = inputView
	m.textInput.Focus()
	m.textInput.SetValue("未发送草稿")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true})
	got := updated.(model)
	if len(opened) != 1 || opened[0] != "https://live.bilibili.com/2693345" {
		t.Fatalf("alt+f did not open the room page: %v", opened)
	}
	if got.textInput.Value() != "未发送草稿" || !got.textInput.Focused() || got.state != inputView {
		t.Fatalf("alt+f disturbed the draft or focus: %q", got.textInput.Value())
	}
	if !strings.Contains(got.sendHint, "浏览器") {
		t.Fatalf("alt+f gave no feedback: %q", got.sendHint)
	}
	// The literal "f" must never reach the draft.
	if strings.Contains(got.textInput.Value(), "f") {
		t.Fatalf("alt+f leaked 'f' into the draft: %q", got.textInput.Value())
	}
}

// The shortcut is consumed even from the browsing view and reports failures.
func TestAltFWorksFromContentAndReportsFailure(t *testing.T) {
	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	previous := openRoomPage
	defer func() { openRoomPage = previous }()
	openRoomPage = func(*api.LiveRoom) error { return errTestOpen }

	m := InitialModel(room)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true})
	got := updated.(model)
	if !strings.Contains(got.sendHint, "失败") {
		t.Fatalf("failed open was not reported: %q", got.sendHint)
	}
	if _, cmd := got.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true}); cmd == nil {
		t.Fatal("alt+f was not consumed")
	}
}

var errTestOpen = &testOpenError{}

type testOpenError struct{}

func (*testOpenError) Error() string { return "no browser" }

// With a draft, the hint shares the send row without pushing it past the frame,
// and the draft keeps priority over the hint.
func TestHintSharesSendRowWithDraft(t *testing.T) {
	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	updated, _ := InitialModel(room).Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	got := updated.(model)
	got.focusInput()
	got.textInput.SetValue(strings.Repeat("草稿", 30))
	got.showHint("发送成功", sendResultHintDuration)
	got.layoutInputWidth()

	view := got.View()
	if lipgloss.Width(view) > 60 || lipgloss.Height(view) > 20 {
		t.Fatalf("send box overflowed the terminal: %dx%d", lipgloss.Width(view), lipgloss.Height(view))
	}
	// The hint must be visible and the draft must still fit in its own field.
	if !strings.Contains(view, "发送成功") {
		t.Fatal("hint was not rendered next to the draft")
	}
	if got.textInput.Width < 1 {
		t.Fatalf("draft lost all of its width: %d", got.textInput.Width)
	}
	// The hint can never take more than half the row, so the draft keeps priority.
	if hint := got.inputHintWidth(); hint > (got.inputRowWidth()-2)/2 {
		t.Fatalf("hint took too much of the row: %d", hint)
	}
}

// The removed status bar must not come back: the danmu view is header + history +
// send box only, and the shortcut hints live in the send box placeholder.
func TestNoStatusBarAndShortcutsLiveInSendBox(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig = defaultConfig()
	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	updated, _ := InitialModel(room).Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got := updated.(model)

	// The viewport now owns the row the status bar used to occupy. With the
	// default header (3 rows) and a 1-row send box at height 30, the removed bar
	// used to leave 21 rows of history; it is 22 now.
	if got.viewport.Height != 22 {
		t.Fatalf("viewport did not reclaim the status-bar row: %d", got.viewport.Height)
	}
	if got.sendHint != "" {
		t.Fatalf("unexpected initial hint: %q", got.sendHint)
	}
	// Shortcut hints are the send box placeholder, and mention the new shortcut.
	for _, want := range []string{"Tab", "F2", "Alt+F"} {
		if !strings.Contains(got.textInput.Placeholder, want) {
			t.Fatalf("send box placeholder lost %q: %q", want, got.textInput.Placeholder)
		}
	}
	// The scroll indicator moved into the header instead of a separate row.
	if !strings.Contains(got.headerView(), "100%") {
		t.Fatalf("header lost the scroll indicator: %q", got.headerView())
	}
}

// The hint must never widen the frame at any size, including tiny terminals.
func TestHintNeverOverflowsNarrowTerminals(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig = defaultConfig()
	LiveConfig.ShowRoomTitle, LiveConfig.ShowRoomNumber = true, true

	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	room.Title = strings.Repeat("很长的标题", 20)

	m := InitialModel(room)
	m.textInput.SetValue("草稿")
	m.sendHint = strings.Repeat("很长的发送失败原因", 20)
	for _, size := range []tea.WindowSizeMsg{{Width: 18, Height: 9}, {Width: 18, Height: 20}, {Width: 24, Height: 12}, {Width: 100, Height: 40}} {
		updated, _ := m.Update(size)
		got := updated.(model)
		view := got.View()
		if lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
			t.Fatalf("view %dx%d exceeds terminal %+v", lipgloss.Width(view), lipgloss.Height(view), size)
		}
		// An empty draft lets the hint take the whole send box.
		got.textInput.Reset()
		got.refreshInputPlaceholder()
		got.layoutInputWidth()
		if view := got.View(); lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
			t.Fatalf("empty-draft view %dx%d exceeds terminal %+v", lipgloss.Width(view), lipgloss.Height(view), size)
		}
		m = &got
	}
}

// After a successful send the box is empty, so the success hint must appear as
// the send box placeholder. This guards the ordering between clearing the draft
// and refreshing the hint: clearing it afterwards used to hide the hint.
func TestSuccessHintVisibleAfterDraftCleared(t *testing.T) {
	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	updated, _ := InitialModel(room).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(model)
	got.focusInput()
	got.textInput.SetValue("hello")
	got.sending = true

	updated, _ = got.Update(sendResultMsg{room: room, content: "hello"})
	got = updated.(model)
	if got.textInput.Value() != "" {
		t.Fatalf("successful draft not cleared: %q", got.textInput.Value())
	}
	if got.sendHint != "发送成功" {
		t.Fatalf("success hint lost: %q", got.sendHint)
	}
	if !strings.Contains(got.textInput.Placeholder, "发送成功") {
		t.Fatalf("success hint is not visible in the empty box: %q", got.textInput.Placeholder)
	}
	if !strings.Contains(got.View(), "发送成功") {
		t.Fatal("success hint missing from the rendered view")
	}
}

// A failed send must also make the reason visible in the box.
func TestFailureHintVisibleInSendBox(t *testing.T) {
	room := sendTestRoom(`{"code":0}`, 200)
	defer room.Close()
	updated, _ := InitialModel(room).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(model)
	got.focusInput()
	got.textInput.SetValue("hello")
	got.sending = true

	updated, _ = got.Update(sendResultMsg{room: room, content: "hello", err: errTestOpen})
	got = updated.(model)
	if !strings.Contains(got.View(), "no browser") {
		t.Fatalf("failure reason missing from the send box: hint=%q", got.sendHint)
	}
}

// A hinted send must never overwrite a draft typed after the send started.
func TestHintExpiryLeavesDraftIntact(t *testing.T) {
	room := sendTestRoom(`{"code":0}`, 200)
	m := InitialModel(room)
	m.state = inputView
	m.textInput.Focus()
	m.sending = true
	updated, cmd := m.Update(sendResultMsg{room: room, content: "old", err: errTestOpen})
	got := updated.(model)
	expiry := hintExpiryFromCmd(t, cmd)
	got.textInput.SetValue("用户后续输入")
	updated, _ = got.Update(expiry)
	got = updated.(model)
	if got.textInput.Value() != "用户后续输入" || got.sendHint != "" {
		t.Fatalf("hint expiry disturbed the draft: %q %q", got.textInput.Value(), got.sendHint)
	}
}
