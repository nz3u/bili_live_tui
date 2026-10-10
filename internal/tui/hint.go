package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
)

// The danmu area used to end with a dedicated status bar. It was removed to give
// that row back to the chat history, so every transient notice (send results,
// shortcut/validation feedback) is now shown inside the danmu send box.
const (
	// Send results are shown for one second, success and failure alike.
	sendResultHintDuration = time.Second
	// Shortcut/validation notices stay a little longer but never linger.
	noticeHintDuration = 3 * time.Second
	// hintSticky keeps a hint ("正在发送…") until a result replaces it.
	hintSticky = time.Duration(0)
	// promptWidth is the cell width of the text input's "> " prompt.
	promptWidth = 2
)

// hintExpiredMsg retires a transient hint. The room plus generation guard makes
// a stale timer harmless: it can never clear a newer hint, and it cannot leak
// across a room switch.
type hintExpiredMsg struct {
	room *api.LiveRoom
	seq  uint64
}

// showHint displays text in the send box and schedules its removal. The hint is
// decoration only: it never edits, clears or focuses the user's draft.
func (m *model) showHint(text string, duration time.Duration) tea.Cmd {
	m.sendHintSeq++
	m.sendHint = text
	m.refreshInputPlaceholder()
	m.layoutInputWidth()
	if text == "" || duration <= 0 {
		return nil
	}
	room, seq := m.room, m.sendHintSeq
	return tea.Tick(duration, func(time.Time) tea.Msg {
		return hintExpiredMsg{room: room, seq: seq}
	})
}

func (m *model) clearHint() {
	if m.sendHint == "" {
		return
	}
	m.sendHint = ""
	m.refreshInputPlaceholder()
	m.layoutInputWidth()
}

// refreshInputPlaceholder shows the active hint inside an empty send box. With
// no draft there is nothing to protect, so the hint can occupy the whole field
// and the shortcut hints wait until it expires. As soon as a draft exists the
// hint moves to the right of the text (see inputRow).
func (m *model) refreshInputPlaceholder() {
	if m.sendHint != "" && m.textInput.Value() == "" {
		// The leading space is required by Bubbles v0.14.0: its placeholder
		// cursor styling would otherwise split a multi-byte first rune.
		m.textInput.Placeholder = " " + m.sendHint
		return
	}
	if m.state == inputView {
		m.textInput.Placeholder = focusedInputPlaceholder
	} else {
		m.textInput.Placeholder = blurredInputPlaceholder
	}
}

// inputRowWidth is the printable width of the send box content row. The row is
// wrapped in a style that adds a one-cell border on each side, so the resulting
// box spans exactly the terminal width, matching the danmu area above it.
func (m model) inputRowWidth() int {
	return max(1, m.viewport.Width)
}

// inputHintWidth is the room reserved for the hint beside a draft. It is capped
// at half the row so the draft keeps priority, and drops to zero when the
// terminal is too narrow to show both.
func (m model) inputHintWidth() int {
	if m.sendHint == "" || m.textInput.Value() == "" {
		return 0
	}
	limit := (m.inputRowWidth() - 2) / 2
	if limit < 2 {
		return 0
	}
	return min(limit, lipgloss.Width(m.sendHint))
}

// layoutInputWidth keeps the draft inside the send box. A visible hint shrinks
// the field by exactly the cells the hint occupies, so both stay fully visible
// instead of one clipping the other.
func (m *model) layoutInputWidth() {
	width := m.inputRowWidth() - promptWidth
	if hint := m.inputHintWidth(); hint > 0 {
		width -= hint + 1
	}
	m.textInput.Width = max(1, width)
}

// inputRow renders the send box content: the draft first with the hint in the
// space it does not use, or the hint alone while the box is empty. The row is
// always padded to the full width so the box keeps a stable size, and clipped so
// a long placeholder or hint can never widen the frame.
func (m model) inputRow() string {
	width := m.inputRowWidth()
	row := m.textInput.View()
	if hint := m.inputHintWidth(); hint > 0 {
		// Reserve hint+1 cells on the right; the field itself was already
		// narrowed by layoutInputWidth, so this only fills the gap.
		left := lipgloss.NewStyle().MaxWidth(width - hint - 1).Render(row)
		padding := max(0, width-hint-1-lipgloss.Width(left))
		row = left + strings.Repeat(" ", padding+1) + lipgloss.NewStyle().MaxWidth(hint).Render(m.sendHint)
	}
	if pad := width - lipgloss.Width(row); pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(row)
}
