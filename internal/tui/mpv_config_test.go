package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMPVPathConfigRoundTripPreservesWindowsPath(t *testing.T) {
	path := configFixture(t, "# header\nroom_id=1\nchat_buffer=20\nmpv_path='C:\\old\\mpv.exe' # player\nunknown='keep'\n")
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MPVPath = `D:\播放器 with spaces\mpv.exe`
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	_, got, err := openConfigStore(path)
	if err != nil || got.MPVPath != cfg.MPVPath {
		t.Fatalf("path changed: %q %v", got.MPVPath, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "# player") || !strings.Contains(string(data), "unknown='keep'") {
		t.Fatal("path save lost comments or unknown fields")
	}
}

func TestSettingsMPVPathSaveReconnectsAudioAndPreservesMute(t *testing.T) {
	m := settingsFixture(t)
	o := fakeAudioOwner(t)
	m.audio = o
	first, second := newFakeAudioSession(), newFakeAudioSession()
	snapshots := make(chan [2]string, 8)
	o.find = func(path, base string) (string, error) { snapshots <- [2]string{path, base}; return path, nil }
	o.launch = func(_ context.Context, player, _, _ string, _ bool) (audioSession, error) {
		if player == "old-location" {
			return first, nil
		}
		return second, nil
	}
	LiveConfig.MPVPath = "old-location"
	m.audioMuted = true
	m.audioMuteRevision = o.setMuted(true)
	o.start(m.room, "ua")
	awaitAudioEvent(t, o, func(e audioEvent) bool { return e.playing })
	m.audioPlaying = true
	m.settings.fields[10].SetValue("  new-location  ")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(model)
	if m.settings != nil || LiveConfig.MPVPath != "new-location" || m.audioPlaying || !m.audioMuted || m.switching {
		t.Fatal("path save did not reset only audio")
	}
	awaitAudioEvent(t, o, func(e audioEvent) bool { return e.playing && o.accepts(e) })
	select {
	case <-first.Done():
	default:
		t.Fatal("previous player not closed")
	}
	awaitMute(t, second, true)
	for _, want := range []string{"old-location", "new-location"} {
		got := <-snapshots
		if got != [2]string{want, filepath.Dir(loadedConfig.path)} {
			t.Fatalf("worker did not receive config snapshot: %v", got)
		}
	}
	_, cfg, err := openConfigStore(loadedConfig.path)
	if err != nil || cfg.MPVPath != "new-location" {
		t.Fatal("mpv path not persisted")
	}
}

func TestMPVPathRejectsControlCharacters(t *testing.T) {
	roomConfig(t)
	for _, control := range []string{"\n", "\r", "\t", "\x00", "\x7f", "\u2028", "\u2029"} {
		cfg := LiveConfig
		cfg.MPVPath = "player" + control
		if validateConfig(cfg) == nil {
			t.Fatalf("control character accepted: %q", control)
		}
	}
}

func TestSettingsClearingMPVPathStopsAudio(t *testing.T) {
	m := settingsFixture(t)
	o := fakeAudioOwner(t)
	m.audio = o
	first := newFakeAudioSession()
	o.find = func(path, _ string) (string, error) {
		if path == "" {
			return "", errors.New("请在设置中填写 mpv_path")
		}
		return path, nil
	}
	o.launch = func(context.Context, string, string, string, bool) (audioSession, error) { return first, nil }
	LiveConfig.MPVPath = "old-location"
	o.start(m.room, "ua")
	awaitAudioEvent(t, o, func(e audioEvent) bool { return e.playing })
	m.settings.fields[10].SetValue("")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(model)
	if m.settings != nil || LiveConfig.MPVPath != "" {
		t.Fatal("empty path not saved")
	}
	awaitAudioEvent(t, o, func(e audioEvent) bool { return e.text == "请在设置中填写 mpv_path" && o.accepts(e) })
	select {
	case <-first.Done():
	default:
		t.Fatal("old player remained running after clearing path")
	}
}

func TestSettingsMPVPathCancelAndFailedSaveDoNotApply(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		m := settingsFixture(t)
		m.settings.fields[10].SetValue("new-location")
		loadedConfig.rename = func(string, string) error { return errors.New("save denied") }
		key := tea.KeyCtrlS
		if cancel {
			key = tea.KeyEsc
		}
		next, _ := m.Update(tea.KeyMsg{Type: key})
		m = next.(model)
		if LiveConfig.MPVPath != "" {
			t.Fatal("failed/canceled path applied")
		}
		if !cancel && (m.settings == nil || m.settings.errorText == "") {
			t.Fatal("failed save not reported")
		}
	}
}
