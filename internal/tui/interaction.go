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
// rune. Keep these hints shared by focus changes and relayouts; the shortcut
// list lives here because the danmu-area status bar that used to show it was
// removed (see hint.go).
//
// The separators are U+2022 bullets rather than U+00B7 middle dots: the Windows
// console draws U+00B7 two cells wide while go-runewidth reports one cell, so
// middle dots in this row make its measured width drift.
const focusedInputPlaceholder = " 输入弹幕，Enter 发送，Esc 返回"
const blurredInputPlaceholder = " Enter/Tab 输入 • Ctrl+N 切房 • F2 设置 • Alt+F 网页"

// Consumed shortcuts must not reach the text field or scroll viewport again.
// In particular, the settings panel is modal only for input, not subscriptions.
func (m *model) handleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		m.Close()
		return true, tea.Quit
	}
	// Alt+F opens the current room's web page. Handled before the settings panel
	// so the shortcut works from every view; it never touches the draft.
	if key == "alt+f" {
		if err := openRoomPage(m.room); err != nil {
			return true, m.showHint("打开直播间网页失败: "+err.Error(), noticeHintDuration)
		}
		return true, m.showHint("已在浏览器打开直播间网页", noticeHintDuration)
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
			return true, m.showHint("只有一个直播间；请按 F2 打开设置添加房间", noticeHintDuration)
		}
		m.switching = true
		return true, tea.Batch(m.showHint("正在切换直播间…", hintSticky), m.switchRoom())
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
			return true, m.showHint("正在切房，请稍后发送；草稿已保留", noticeHintDuration)
		}
		content := m.textInput.Value()
		if strings.TrimSpace(content) == "" {
			return true, m.showHint("请先输入弹幕内容", noticeHintDuration)
		}
		if utf8.RuneCountInString(content) > danmuLength(m.room) {
			return true, m.showHint(fmt.Sprintf("当前房间最多发送 %d 字；请缩短草稿后重试", danmuLength(m.room)), noticeHintDuration)
		}
		m.sending = true
		return true, tea.Batch(m.showHint("正在发送…", hintSticky), m.sendDanmu(content))
	}
	return false, nil
}

func (m *model) focusInput() tea.Cmd {
	m.state = inputView
	m.refreshInputPlaceholder()
	return m.textInput.Focus()
}

func (m *model) blurInput() {
	m.state = contentView
	m.textInput.Blur()
	m.refreshInputPlaceholder()
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
	// The placeholder/hint width depends on the viewport width, so refresh both
	// before measuring the send box height.
	m.refreshInputPlaceholder()
	m.layoutInputWidth()
	// The focused and unfocused styles each draw a top and a bottom border row.
	// They used to also wrap the removed status bar, so the viewport gains the
	// row that bar occupied.
	headerHeight := lipgloss.Height(m.headerView()) + focusMarginHeight
	inputHeight := lipgloss.Height(m.inputRow()) + 3*focusMarginHeight
	viewportHeight := max(1, size.Height-headerHeight-inputHeight)
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
