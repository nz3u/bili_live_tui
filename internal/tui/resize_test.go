package tui

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type resizeProbeTimeoutMsg struct{}

// Drive the real Bubble Tea event loop and renderer, rather than calling
// model.Update recursively (which bypasses the renderer's resize handling).
type resizeRendererProbe struct {
	inner       model
	nativeSizes int
}

func (m resizeRendererProbe) Init() tea.Cmd {
	return func() tea.Msg { return tea.WindowSizeMsg{Width: 30, Height: 20} }
}

func (m resizeRendererProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(resizeProbeTimeoutMsg); ok {
		return m, tea.Quit
	}
	updated, cmd := m.inner.Update(msg)
	m.inner = updated.(model)
	switch msg.(type) {
	case tea.WindowSizeMsg:
		m.nativeSizes++
		if m.nativeSizes == 1 {
			return m, func() tea.Msg {
				return windowSizePollMsg{room: m.inner.room, size: tea.WindowSizeMsg{Width: 90, Height: 35}}
			}
		}
		return m, tea.Quit
	case windowSizePollMsg:
		return m, tea.Batch(cmd, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return resizeProbeTimeoutMsg{} }))
	}
	return m, cmd
}

func (m resizeRendererProbe) View() string {
	if m.nativeSizes == 0 {
		return ""
	}
	return strings.Repeat("X", 120)
}

func TestPolledResizeUpdatesBubbleTeaRenderer(t *testing.T) {
	room := uiTestRoom()
	defer room.Close()
	var output bytes.Buffer
	program := tea.NewProgram(resizeRendererProbe{inner: *InitialModel(room)},
		tea.WithInput(strings.NewReader("")), tea.WithOutput(&output))
	final, err := program.StartReturningModel()
	if err != nil {
		t.Fatal(err)
	}
	probe := final.(resizeRendererProbe)
	if probe.nativeSizes != 2 {
		t.Fatalf("renderer received %d native resize events, want 2; polled resize was hidden in a custom message", probe.nativeSizes)
	}
	if !strings.Contains(output.String(), strings.Repeat("X", 90)) || strings.Contains(output.String(), strings.Repeat("X", 91)) {
		t.Fatal("renderer did not clip output to exactly the new 90-column width")
	}
}

func TestResizePollingContinuesOnlyAfterNativeEvent(t *testing.T) {
	room := uiTestRoom()
	defer room.Close()
	m := InitialModel(room)
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(model)
	if cmd != nil {
		t.Fatal("initial native size started an extra polling chain")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 40}, {Width: 40, Height: 15}, {Width: 120, Height: 50}} {
		before := got.windowSize
		updated, cmd = got.Update(windowSizePollMsg{room: room, size: size})
		got = updated.(model)
		if cmd == nil || got.windowSize != before || got.pendingResize == nil {
			t.Fatal("poll bypassed native event delivery")
		}
		native, ok := cmd().(tea.WindowSizeMsg)
		if !ok || native != size {
			t.Fatal("polled resize was not forwarded as a native size message")
		}
		updated, next := got.Update(native)
		got = updated.(model)
		if got.pendingResize != nil || got.windowSize != size {
			t.Fatal("native resize was not acknowledged")
		}
		if runtime.GOOS == "windows" && next == nil {
			t.Fatal("polling stopped after a native resize")
		}
		updated, duplicate := got.Update(native)
		got = updated.(model)
		if duplicate != nil {
			t.Fatal("duplicate native event started a second polling chain")
		}
	}
}

func TestPollingRepairsDelayedNativeResize(t *testing.T) {
	for _, delayed := range []tea.WindowSizeMsg{{Width: 30, Height: 20}, {}} {
		t.Run("delayed", func(t *testing.T) {
			room := uiTestRoom()
			defer room.Close()
			actual := tea.WindowSizeMsg{Width: 100, Height: 40}
			updated, _ := InitialModel(room).Update(actual)
			got := updated.(model)
			state := got.terminalSize
			ticks := make(chan time.Time, 1)
			observed := make(chan tea.WindowSizeMsg)
			result := make(chan tea.Msg, 1)
			go func() {
				result <- pollWindowResize(room, func() tea.WindowSizeMsg {
					snapshot := state.load()
					observed <- snapshot
					return snapshot
				}, ticks, func() (int, int, error) { return actual.Width, actual.Height, nil })
			}()
			ticks <- time.Time{}
			if snapshot := <-observed; snapshot != actual {
				t.Fatal("incorrect polling baseline")
			}
			// A forwarded resize from the old room (or the initial size event)
			// arrives after the current physical size was already acknowledged.
			updated, _ = got.Update(delayed)
			got = updated.(model)
			ticks <- time.Time{}
			<-observed
			select {
			case msg := <-result:
				updated, forward := got.Update(msg)
				got = updated.(model)
				if forward == nil {
					t.Fatal("delayed size was not corrected")
				}
				updated, _ = got.Update(forward())
				if updated.(model).windowSize != actual {
					t.Fatal("renderer/model did not converge to the physical size")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("poll remained stuck on its original size snapshot")
			}
		})
	}
}

func TestPendingResizeIsCancelledBeforeForwarding(t *testing.T) {
	room := uiTestRoom()
	m := InitialModel(room)
	_, cmd := m.Update(windowSizePollMsg{room: room, size: tea.WindowSizeMsg{Width: 100, Height: 40}})
	room.Close()
	if cmd == nil || cmd() != nil {
		t.Fatal("closed room forwarded a pending resize")
	}
}

func TestResizeRebuildsViewportWithoutNewDanmu(t *testing.T) {
	room := uiTestRoom()
	defer room.Close()
	m := InitialModel(room)
	m.danmu.PushBack(&danmuMsg{uName: "name", content: "latest"})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	got := updated.(model)
	updated, _ = got.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	got = updated.(model)
	if got.viewport.Width != 98 || got.textInput.Width != 96 {
		t.Fatal("widths were not updated")
	}
	if !strings.Contains(got.viewport.View(), "latest") {
		t.Fatal("existing danmu disappeared during resize")
	}
	expected := got.viewport
	expected.SetContent(got.renderDanmu())
	expected.GotoBottom()
	if got.viewport.View() != expected.View() {
		t.Fatal("resizing did not rebuild padded viewport content for the new height")
	}
}

func TestWindowPollingKeepsWatchingAfterErrorsAndRepeatedSizes(t *testing.T) {
	room := uiTestRoom()
	defer room.Close()
	previous := tea.WindowSizeMsg{Width: 80, Height: 24}
	ticks := make(chan time.Time, 4)
	for i := 0; i < cap(ticks); i++ {
		ticks <- time.Time{}
	}
	reads := 0
	msg := pollWindowResize(room, func() tea.WindowSizeMsg { return previous }, ticks, func() (int, int, error) {
		reads++
		switch reads {
		case 1:
			return 0, 0, errors.New("temporary terminal error")
		case 2:
			return 0, 0, nil
		case 3:
			return 80, 24, nil
		default:
			return 100, 40, nil
		}
	}).(windowSizePollMsg)
	if reads != 4 || msg.room != room || msg.size != (tea.WindowSizeMsg{Width: 100, Height: 40}) {
		t.Fatalf("invalid first resize: reads=%d msg=%+v", reads, msg)
	}
	ticks <- time.Time{}
	second := pollWindowResize(room, func() tea.WindowSizeMsg { return msg.size }, ticks, func() (int, int, error) { return 40, 15, nil }).(windowSizePollMsg)
	if second.size != (tea.WindowSizeMsg{Width: 40, Height: 15}) {
		t.Fatal("second resize was lost")
	}
}

func TestWindowPollingCancellationAndOldRoomResult(t *testing.T) {
	old, current := uiTestRoom(), uiTestRoom()
	defer current.Close()
	old.Close()
	if msg := pollWindowResize(old, func() tea.WindowSizeMsg { return tea.WindowSizeMsg{} }, make(chan time.Time), func() (int, int, error) {
		t.Fatal("read terminal size after session closed")
		return 1, 1, nil
	}); msg != nil {
		t.Fatal("cancelled poll emitted a resize")
	}
	m := InitialModel(current)
	updated, cmd := m.Update(windowSizePollMsg{room: old, size: tea.WindowSizeMsg{Width: 100, Height: 40}})
	if updated.(model).ready || cmd != nil {
		t.Fatal("old room restarted resize polling")
	}
}

func TestResizePreservesDraftAndScrollback(t *testing.T) {
	room := uiTestRoom()
	defer room.Close()
	m := InitialModel(room)
	for i := 0; i < 80; i++ {
		m.danmu.PushBack(&danmuMsg{uName: "name", content: "message"})
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	got := updated.(model)
	got.lockBottom = false
	got.viewport.SetYOffset(70)
	got.textInput.SetValue("未发送草稿")
	updated, _ = got.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
	got = updated.(model)
	if got.danmu.Len() != 80 || got.textInput.Value() != "未发送草稿" || got.viewport.PastBottom() {
		t.Fatal("resize lost draft/history or left an invalid scroll offset")
	}
	before := got.windowSize
	updated, _ = got.Update(tea.WindowSizeMsg{})
	if updated.(model).windowSize != before {
		t.Fatal("zero terminal size overwrote the last valid layout")
	}
}

func TestNarrowTerminalFitsLongTitleAndStatus(t *testing.T) {
	previousConfig := LiveConfig
	defer func() { LiveConfig = previousConfig }()
	LiveConfig.ShowRoomTitle, LiveConfig.ShowRoomNumber = true, true
	room := uiTestRoom()
	defer room.Close()
	room.Title = strings.Repeat("很长的标题", 20)
	m := InitialModel(room)
	m.sendHint = strings.Repeat("很长的发送错误", 20)
	m.textInput.SetValue("弹幕草稿")
	for _, size := range []tea.WindowSizeMsg{{Width: 18, Height: 9}, {Width: 18, Height: 20}, {Width: 100, Height: 40}} {
		updated, _ := m.Update(size)
		got := updated.(model)
		view := got.View()
		if lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
			t.Fatalf("view %dx%d exceeds terminal %+v", lipgloss.Width(view), lipgloss.Height(view), size)
		}
		m = &got
	}
}

func TestTinyTerminalHasNonNegativeLayout(t *testing.T) {
	room := uiTestRoom()
	defer room.Close()
	m := InitialModel(room)
	m.textInput.SetValue("很小的窗口仍保留草稿")
	for _, size := range []tea.WindowSizeMsg{{Width: 1, Height: 1}, {Width: 4, Height: 3}} {
		updated, _ := m.Update(size)
		got := updated.(model)
		if got.viewport.Width < 1 || got.viewport.Height < 1 || got.textInput.Width < 1 {
			t.Fatalf("invalid layout for %+v", size)
		}
		view := got.View()
		if lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
			t.Fatalf("compact view exceeds terminal %+v", size)
		}
		m = &got
	}
}
