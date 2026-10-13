package tui

import tea "github.com/charmbracelet/bubbletea"

// Associate asynchronous clipboard/cursor results with the originating form
// and field, so closing a form cannot paste its value into the chat input.
type settingsMessage struct {
	form    *settingsModel
	field   int
	message tea.Msg
}

func settingsCommand(form *settingsModel, field int, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		result := cmd()
		return settingsMessage{form, field, result}
	}
}
