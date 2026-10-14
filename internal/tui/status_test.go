package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSettingsSavedStatusExpires(t *testing.T) {
	m := settingsFixture(t)
	m.settings.fields[3].SetValue("true") // Saving display settings should not switch rooms.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(model)
	if m.settings != nil {
		t.Fatal("form still open after save")
	}
	if m.sendStatus != "设置已保存" {
		t.Fatalf("save status missing: %q", m.sendStatus)
	}
	if cmd == nil {
		t.Fatal("no expiry command scheduled")
	}
	seq := m.statusSeq
	updated, _ = m.Update(statusExpiredMsg{text: "设置已保存", seq: seq})
	m = updated.(model)
	if m.sendStatus != "" {
		t.Fatalf("status did not expire: %q", m.sendStatus)
	}
	if m.inputLine() == "" {
		t.Fatal("input row lost after expiry")
	}
}

func TestStaleStatusExpiryKeepsNewerStatus(t *testing.T) {
	m := settingsFixture(t)
	m.settings = nil
	m.showStatus("旧的提示")
	staleSeq := m.statusSeq
	m.showStatus("新的提示")
	// A tick that belongs to an earlier status must not erase the current one.
	updated, _ := m.Update(statusExpiredMsg{text: "旧的提示", seq: staleSeq})
	m = updated.(model)
	if m.sendStatus != "新的提示" {
		t.Fatalf("stale expiry cleared newer status: %q", m.sendStatus)
	}
	updated, _ = m.Update(statusExpiredMsg{text: "新的提示", seq: m.statusSeq})
	m = updated.(model)
	if m.sendStatus != "" {
		t.Fatalf("current status did not expire: %q", m.sendStatus)
	}
}
