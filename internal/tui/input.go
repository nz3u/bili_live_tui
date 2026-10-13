package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

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
	return truncate.StringWithTail(ti.View(), uint(width), "")
}
