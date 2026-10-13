package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// Status lives in the existing input row, including when a failed draft is non-empty.
func (m model) inputLine() string {
	ti := m.textInput
	width := max(1, m.width-2*focusMarginWidth)
	if m.sendStatus != "" {
		statusWidth := max(1, width/2)
		status := truncate.StringWithTail(singleLine(m.sendStatus), uint(statusWidth), "…")
		ti.Prompt = status + " > "
	}
	ti.Width = max(1, width-lipgloss.Width(ti.Prompt)-1)
	return truncate.StringWithTail(ti.View(), uint(width), "")
}
