package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func settingsFixture(t *testing.T) model {
	t.Helper()
	roomConfig(t)
	oldStore := loadedConfig
	t.Cleanup(func() { loadedConfig = oldStore })
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# keep\nroom_id = 1\nroom_ids = [1, 2]\nchat_buffer = 20\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	m := InitialModel(fakeRoom(1))
	t.Cleanup(m.Close)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	return updated.(model)
}
func TestSettingsSaveAndCancel(t *testing.T) {
	m := settingsFixture(t)
	m.textInput.SetValue("原草稿")
	m.settings.fields[0].SetValue("3")
	m.settings.fields[1].SetValue("1, 3, 4")
	m.settings.fields[2].SetValue("10")
	m.settings.fields[3].SetValue("true")
	m.settings.fields[9].SetValue("test-agent")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(model)
	if m.settings != nil || LiveConfig.RoomID != 3 || len(LiveConfig.RoomIDs) != 3 || !LiveConfig.ShowRoomTitle || LiveConfig.UserAgent != "test-agent" {
		t.Fatalf("save not applied: %+v", LiveConfig)
	}
	if m.roomID != 1 || m.textInput.Value() != "原草稿" {
		t.Fatal("save changed active room or draft")
	}
	_, cfg, err := openConfigStore(loadedConfig.path)
	if err != nil || cfg.RoomID != 3 {
		t.Fatalf("not persisted: %v", err)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m = updated.(model)
	m.settings.fields[0].SetValue("8")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.settings != nil || LiveConfig.RoomID != 3 {
		t.Fatal("cancel changed config")
	}
}
func TestSettingsInvalidSaveRetainsForm(t *testing.T) {
	m := settingsFixture(t)
	before, err := os.ReadFile(loadedConfig.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0", "abc", "-1"} {
		m.settings.fields[0].SetValue(v)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
		m = updated.(model)
		if m.settings == nil || m.settings.errorText == "" || LiveConfig.RoomID != 1 {
			t.Fatal("invalid save applied")
		}
	}
	after, _ := os.ReadFile(loadedConfig.path)
	if string(before) != string(after) {
		t.Fatal("invalid save changed file")
	}
}
func TestSettingsExternalChangeDoesNotApply(t *testing.T) {
	m := settingsFixture(t)
	m.settings.fields[0].SetValue("9")
	if err := os.WriteFile(loadedConfig.path, []byte("room_id=8\nchat_buffer=20\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(model)
	if m.settings == nil || m.settings.errorText == "" || LiveConfig.RoomID != 1 {
		t.Fatal("conflicting save accepted")
	}
}
func TestSettingsKeysAndNoExtraRows(t *testing.T) {
	m := settingsFixture(t)
	originalHeight := lipgloss.Height(m.View())
	m.settings.selected = 3
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(model)
	if m.settings.fields[3].Value() != "true" {
		t.Fatal("checkbox not toggled")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	if m.settings.selected != 4 {
		t.Fatal("tab selection")
	}
	if strings.Contains(m.textInput.Value(), " ") {
		t.Fatal("settings key leaked into draft")
	}
	if lipgloss.Height(m.inputLine()) != 1 {
		t.Fatal("help added lines")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if originalHeight != lipgloss.Height(m.View()) {
		t.Fatal("settings added permanent rows")
	}
	for _, size := range [][2]int{{20, 8}, {8, 4}, {120, 40}} {
		updated, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(model)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyF2})
		m = updated.(model)
		m.settings.selected = 9
		view := m.settings.view(m.viewport.Width, m.viewport.Height)
		if m.viewport.Height > 0 && lipgloss.Height(view) > m.viewport.Height {
			t.Fatal("form exceeds viewport")
		}
		if lipgloss.Width(view) > m.viewport.Width {
			t.Fatal("form exceeds width")
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = updated.(model)
	}
}
