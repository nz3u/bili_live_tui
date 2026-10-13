package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

func (m *model) layoutInputs() {
	width := max(1, m.width-2*focusMarginWidth)
	m.textInput.Prompt = "> "
	if m.sendStatus != "" {
		status := truncate.StringWithTail(singleLine(m.sendStatus), uint(max(1, width/2)), "…")
		m.textInput.Prompt = status + " > "
	}
	m.textInput.Width = max(1, width-lipgloss.Width(m.textInput.Prompt)-1)
	m.textInput.SetCursor(m.textInput.Cursor())
	if m.settings != nil {
		for i := range m.settings.fields {
			label := truncate.StringWithTail(settingLabels[i]+": ", uint(max(0, m.viewport.Width/2-2)), "…")
			field := &m.settings.fields[i]
			field.Width = max(1, m.viewport.Width-2-lipgloss.Width(label)-1)
			field.SetCursor(field.Cursor())
		}
	}
}

// Status lives in the existing input row, including when a failed draft is non-empty.
func (m model) inputLine() string {
	ti := m.textInput
	width := max(1, m.width-2*focusMarginWidth)
	if m.settings != nil {
		hint := "Tab/↑↓ 选择 · 空格 切换 · Ctrl+S 保存 · Esc 取消"
		if m.settings.errorText != "" {
			hint = m.settings.errorText + " · Ctrl+S 重试 · Esc 取消"
		}
		return truncate.StringWithTail(hint, uint(width), "…")
	}
	if m.sendStatus != "" {
		statusWidth := max(1, width/2)
		status := truncate.StringWithTail(singleLine(m.sendStatus), uint(statusWidth), "…")
		ti.Prompt = status + " > "
	}
	ti.Width = max(1, width-lipgloss.Width(ti.Prompt)-1)
	ti.SetCursor(ti.Cursor())
	return truncate.StringWithTail(ti.View(), uint(width), "")
}
