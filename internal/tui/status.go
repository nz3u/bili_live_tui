package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// statusTimeout is how long a transient status stays in the input row.
const statusTimeout = 1 * time.Second

// statusExpiredMsg clears the status that showStatus displayed. text and seq
// identify that one status, so a stale tick can never erase a status that was
// shown (or overwritten) afterwards.
type statusExpiredMsg struct {
	text string
	seq  int
}

// showStatus displays text in the input row and returns a command that removes
// it again after statusTimeout.
func (m *model) showStatus(text string) tea.Cmd {
	m.sendStatus = text
	m.statusSeq++
	seq := m.statusSeq
	return tea.Tick(statusTimeout, func(time.Time) tea.Msg {
		return statusExpiredMsg{text: text, seq: seq}
	})
}

// expireStatus is the receiving half of showStatus.
func (m *model) expireStatus(msg statusExpiredMsg) {
	if msg.seq != m.statusSeq || msg.text != m.sendStatus {
		return
	}
	m.sendStatus = ""
}
