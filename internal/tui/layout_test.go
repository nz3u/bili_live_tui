package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestFullViewBounds(t *testing.T) {
	roomConfig(t)
	LiveConfig.ShowRoomTitle, LiveConfig.ShowRoomNumber = true, true
	room := fakeRoom(1)
	room.Title = strings.Repeat("很长的直播间标题", 30)
	m := InitialModel(room)
	defer m.Close()
	for _, size := range [][2]int{{120, 40}, {20, 8}, {8, 4}, {1, 1}, {80, 24}, {40, 12}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(model)
		for _, settings := range []bool{false, true} {
			m.settings = nil
			if settings {
				m.settings = newSettings(LiveConfig)
			}
			view := m.View()
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatalf("%v settings=%v actual=%dx%d", size, settings, lipgloss.Width(view), lipgloss.Height(view))
			}
		}
	}
}
func TestLongFieldsKeepCursorTailVisible(t *testing.T) {
	m := settingsFixture(t)
	// The room list is the remaining free-text field that can exceed the width.
	m.settings.selected = 1
	m.settings.fields[0].Blur()
	m.settings.fields[1].Focus()
	m.settings.fields[1].SetValue(strings.Repeat("123, ", 25) + "TAIL")
	m.settings.fields[1].CursorEnd()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(model)
	if !strings.Contains(m.settings.view(m.viewport.Width, m.viewport.Height), "TAIL") {
		t.Fatal("settings tail hidden")
	}
	m.settings = nil
	m.textInput.Focus()
	m.textInput.CharLimit = 0
	m.textInput.SetValue(strings.Repeat("字", 30) + "TAIL")
	m.textInput.CursorEnd()
	m.sendStatus = "发送失败"
	m.layoutInputs()
	if !strings.Contains(m.inputLine(), "TAIL") {
		t.Fatal("failed draft tail hidden")
	}
}
func TestSettingsAsyncResultsStayWithForm(t *testing.T) {
	m := settingsFixture(t)
	m.settings.fields[0].SetValue("")
	form := m.settings
	cmd := settingsCommand(form, 0, func() tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("123")} })
	updated, _ := m.Update(cmd())
	m = updated.(model)
	if form.fields[0].Value() != "123" {
		t.Fatal("async input result dropped")
	}
	late := settingsCommand(form, 0, func() tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("456")} })
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	updated, _ = m.Update(late())
	m = updated.(model)
	if m.textInput.Value() != "" {
		t.Fatal("late settings input reached chat draft")
	}
}
func TestTerminalPollingUpdatesBothModels(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	updated, _ := m.Update(terminalPoll{90, 31})
	m = updated.(model)
	if m.width != 90 || m.height != 31 || !m.ready {
		t.Fatal("chat resize missing")
	}
	login := newLoginModel(nil)
	oldW, oldH := windowWidth, windowHeight
	defer func() { windowWidth, windowHeight = oldW, oldH }()
	login.Update(terminalPoll{75, 28})
	if windowWidth != 75 || windowHeight != 28 {
		t.Fatal("login resize missing")
	}
	login.Update(terminalPoll{})
	if windowWidth != 75 || windowHeight != 28 {
		t.Fatal("failed size probe applied")
	}
}
