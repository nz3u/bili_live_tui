package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Bubbles v0.14.0 styles Placeholder[:1] and Placeholder[1:] separately.
// Give its cursor a one-byte ASCII cell so SGR escapes never split a Chinese
// rune. Keep this hint shared by focus changes and relayouts.
const focusedInputPlaceholder = " 输入弹幕，Enter 发送，Esc 返回"
const blurredInputPlaceholder = "Enter 输入 · Tab 切焦点 · F2 设置 · F6 切房"

// Consumed shortcuts must not reach the text field or scroll viewport again.
// In particular, the settings panel is modal only for input, not subscriptions.
func (m *model) handleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		m.Close()
		return true, tea.Quit
	}
	if key == "f2" || key == settingsKey(LiveConfig) {
		if m.settings != nil {
			if m.settings.saving {
				return true, nil
			}
			return true, m.closeSettings()
		}
		return true, m.openSettings()
	}
	if m.settings != nil {
		cmd, closePanel := m.settings.update(msg, m.switching)
		if closePanel {
			return true, m.closeSettings()
		}
		return true, cmd
	}
	if key == "f6" || key == roomSwitchKey(LiveConfig) {
		if m.switching {
			return true, nil
		}
		if len(configuredRooms(LiveConfig)) < 2 {
			m.sendStatus = "只有一个直播间；请按 F2 打开设置添加房间"
			return true, nil
		}
		m.switching, m.sendStatus = true, "正在切换直播间…"
		return true, m.switchRoom()
	}
	switch key {
	case "tab", "shift+tab":
		if m.state == contentView {
			return true, m.focusInput()
		}
		m.blurInput()
		return true, nil
	case "esc":
		if m.state == inputView {
			m.blurInput()
			return true, nil
		}
	case "enter", "ctrl+j":
		if m.state == contentView {
			return true, m.focusInput()
		}
		if m.sending {
			return true, nil
		}
		if m.switching {
			m.sendStatus = "正在切房，请稍后发送；草稿已保留"
			return true, nil
		}
		content := m.textInput.Value()
		if strings.TrimSpace(content) == "" {
			m.sendStatus = "请先输入弹幕内容"
			return true, nil
		}
		if utf8.RuneCountInString(content) > danmuLength(m.room) {
			m.sendStatus = fmt.Sprintf("当前房间最多发送 %d 字；请缩短草稿后重试", danmuLength(m.room))
			return true, nil
		}
		m.sending, m.sendStatus = true, "正在发送…"
		return true, m.sendDanmu(content)
	}
	return false, nil
}

func (m *model) focusInput() tea.Cmd {
	m.state = inputView
	m.textInput.Placeholder = focusedInputPlaceholder
	return m.textInput.Focus()
}

func (m *model) blurInput() {
	m.state = contentView
	m.textInput.Placeholder = blurredInputPlaceholder
	m.textInput.Blur()
}

func (m *model) trimHistory() {
	limit := LiveConfig.ChatBuffer
	if limit < 1 {
		limit = 200
	}
	for m.danmu.Len() > limit {
		m.danmu.Remove(m.danmu.Front())
	}
}

// Layout also changes when settings or room titles change, not only on a
// terminal resize. Do not synthesize another native event just to relayout.
func (m *model) resizeLayout(size tea.WindowSizeMsg) {
	if size.Width <= 0 || size.Height <= 0 {
		return
	}
	viewportWidth := max(1, size.Width-2*focusMarginWidth)
	m.viewport.Width = viewportWidth
	m.textInput.Width = max(1, viewportWidth-3)
	if m.state == inputView {
		m.textInput.Placeholder = focusedInputPlaceholder
	} else {
		m.textInput.Placeholder = blurredInputPlaceholder
	}
	headerHeight := lipgloss.Height(m.headerView()) + focusMarginHeight
	footerHeight := lipgloss.Height(m.footerView()) + lipgloss.Height(m.textInput.View()) + 3*focusMarginHeight
	viewportHeight := max(1, size.Height-headerHeight-footerHeight)
	if !m.ready {
		m.viewport = viewport.New(viewportWidth, viewportHeight)
		m.ready = true
	} else {
		m.viewport.Width, m.viewport.Height = viewportWidth, viewportHeight
	}
	m.viewport.HighPerformanceRendering = false
	m.viewport.YPosition = headerHeight
	m.viewport.SetContent(m.renderDanmu())
	m.viewport.SetYOffset(m.viewport.YOffset)
	if m.lockBottom {
		m.viewport.GotoBottom()
	}
}
