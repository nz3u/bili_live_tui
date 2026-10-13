package tui

import (
	"os"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

type terminalPoll struct{ width, height int }

// Bubble Tea 0.22 has no Windows resize signal. A command, rather than a
// Program.Send goroutine, lets Bubble Tea discard the final tick on shutdown.
func pollTerminal() tea.Cmd {
	if runtime.GOOS != "windows" {
		return nil
	}
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil {
			return terminalPoll{}
		}
		return terminalPoll{w, h}
	})
}
