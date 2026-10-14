package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

func (m *model) layoutInputs() {
	width := max(1, m.width-2*focusMarginWidth)
	m.textInput.Prompt = m.inputPrompt(width)
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

func (m model) inputPrompt(width int) string {
	limit := max(1, width/2)
	audio, status := m.audioLabel(), singleLine(m.sendStatus)
	text := audio
	if status != "" && audio != "" {
		// Keep both one-second notices visible; a long missing-player message
		// must not swallow a simultaneous save/send result. Normal-width mute
		// notices still fit in full, and the steady icon uses only one cell.
		audio = truncate.StringWithTail(singleLine(audio), uint(max(1, limit/2)), "…")
		budget := max(1, limit-lipgloss.Width(audio)-3)
		text = truncate.StringWithTail(status, uint(budget), "…") + " · " + audio
	} else if status != "" {
		text = status
	}
	if text == "" {
		return "> "
	}
	return truncate.StringWithTail(singleLine(text), uint(limit), "…") + " > "
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
		if audio := m.audioLabel(); audio != "" {
			hint = audio + " · " + hint
		}
		return truncate.StringWithTail(hint, uint(width), "…")
	}
	ti.Prompt = m.inputPrompt(width)
	ti.Width = max(1, width-lipgloss.Width(ti.Prompt)-1)
	ti.SetCursor(ti.Cursor())
	return truncate.StringWithTail(ti.View(), uint(width), "")
}
