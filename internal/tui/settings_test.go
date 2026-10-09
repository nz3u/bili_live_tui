package tui

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
)

func settingsTestConfig(t *testing.T) string {
	t.Helper()
	oldConfig, oldPath := cloneConfig(LiveConfig), configFilePath
	t.Cleanup(func() { LiveConfig, configFilePath = oldConfig, oldPath })
	cfg := defaultConfig()
	cfg.RoomID, cfg.RoomIDs = 1, []uint64{1, 2}
	raw, err := encodeConfig(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func setSetting(t *testing.T, panel *settingsPanel, key, value string) {
	t.Helper()
	for i := range panel.fields {
		if panel.fields[i].key == key {
			panel.fields[i].input.SetValue(value)
			return
		}
	}
	t.Fatalf("setting %s not found", key)
}

func TestConfigValidationAndDefaultRoomOrder(t *testing.T) {
	cfg, err := decodeConfig([]byte("room_ids = [1, 2]\n"))
	if err != nil || cfg.RoomID != 1 || cfg.ChatBuffer != 200 || !cfg.ColorMode {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	cfg.RoomID, cfg.RoomIDs = 2, []uint64{1, 2, 3}
	if ids := configuredRooms(cfg); !reflect.DeepEqual(ids, []uint64{1, 2, 3}) {
		t.Fatal("list order changed")
	}
	cfg.RoomID = 4
	if ids := configuredRooms(cfg); !reflect.DeepEqual(ids, []uint64{4, 1, 2, 3}) {
		t.Fatal("default is unreachable")
	}
	for _, key := range []string{"ctrl+c", "enter", "tab", "ctrl+i", "ctrl+m", "abc", "f13"} {
		bad := cfg
		bad.RoomSwitchKey = key
		if _, err := normalizeConfig(bad); err == nil {
			t.Fatalf("unsafe shortcut accepted: %q", key)
		}
	}
	for _, mutate := range []func(*api.BiliLiveConfig){
		func(c *api.BiliLiveConfig) { c.RoomID, c.RoomIDs = 0, nil },
		func(c *api.BiliLiveConfig) { c.RoomIDs = []uint64{1, 1} },
		func(c *api.BiliLiveConfig) { c.RoomIDs = []uint64{0} },
		func(c *api.BiliLiveConfig) { c.ChatBuffer = 0 },
		func(c *api.BiliLiveConfig) { c.ChatBuffer = 100001 },
		func(c *api.BiliLiveConfig) { c.UserAgent = "injected\r\nHeader: bad" },
		func(c *api.BiliLiveConfig) { c.SettingsKey = "ctrl+n" },
		func(c *api.BiliLiveConfig) { c.RoomSwitchKey = "f2" },
	} {
		bad := cloneConfig(cfg)
		mutate(&bad)
		if _, err := normalizeConfig(bad); err == nil {
			t.Fatalf("invalid config accepted: %+v", bad)
		}
	}
	cfg.RoomSwitchKey, cfg.SettingsKey = " CTRL+P ", " F3 "
	got, err := normalizeConfig(cfg)
	if err != nil || got.RoomSwitchKey != "ctrl+p" || got.SettingsKey != "f3" {
		t.Fatalf("shortcut normalization: %+v %v", got, err)
	}
}

func TestSaveConfigBackupUnknownOptionsAndConflict(t *testing.T) {
	path := settingsTestConfig(t)
	original := []byte("# original comment\nroom_id = 1\nchat_buffer = 200\ncustom = 'kept'\n[experimental]\nenabled = true\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readConfigSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.RoomID, cfg.RoomIDs = 2, []uint64{2, 3}
	saved, err := saveConfig(snapshot, cfg)
	if err != nil {
		t.Fatal(err)
	}
	backup, _ := os.ReadFile(path + ".bak")
	if !bytes.Equal(backup, original) {
		t.Fatal("backup did not preserve the original bytes/comments")
	}
	var unknown map[string]interface{}
	if _, err := toml.Decode(string(saved.raw), &unknown); err != nil {
		t.Fatal(err)
	}
	if unknown["custom"] != "kept" || unknown["experimental"].(map[string]interface{})["enabled"] != true {
		t.Fatal("unknown settings were lost")
	}
	loaded, err := decodeConfig(saved.raw)
	if err != nil || loaded.RoomID != 2 || !reflect.DeepEqual(loaded.RoomIDs, []uint64{2, 3}) {
		t.Fatal("saved config did not round trip")
	}
	external := append(append([]byte(nil), saved.raw...), []byte("\n# external edit\n")...)
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := saveConfig(saved, cfg); err == nil || !strings.Contains(err.Error(), "外部修改") {
		t.Fatalf("missing conflict error: %v", err)
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, external) {
		t.Fatal("external modification was overwritten")
	}
}

func TestSaveFailureLeavesOriginalAndRuntimeUntouched(t *testing.T) {
	path := settingsTestConfig(t)
	before, _ := os.ReadFile(path)
	if err := os.Mkdir(path+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	room := uiTestRoom()
	room.RoomID = 1
	defer room.Close()
	m := InitialModel(room)
	m.openSettings()
	setSetting(t, m.settings, "room_id", "9")
	result := m.settings.save(false)()
	updated, _ := m.Update(result)
	got := updated.(model)
	current, _ := os.ReadFile(path)
	if !bytes.Equal(before, current) || LiveConfig.RoomID != 1 || got.settings.saving || !strings.Contains(got.settings.status, "备份") {
		t.Fatal("failed save changed disk/runtime or lost error feedback")
	}
}

func TestSettingsModalPreservesDraftAndSubscriptionsAcrossResize(t *testing.T) {
	settingsTestConfig(t)
	room := sendTestRoom(`{"code":0}`, 200)
	room.RoomID = 1
	defer room.Close()
	updated, _ := InitialModel(room).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(model)
	got.focusInput()
	got.textInput.SetValue("existing draft")
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyF2})
	got = updated.(model)
	if got.settings == nil || got.textInput.Focused() {
		t.Fatal("settings did not acquire keyboard focus")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(model)
	if got.sending || got.settings.cursor != 1 {
		t.Fatal("settings Enter sent danmu instead of navigating fields")
	}
	updated, _ = got.Update(receivedDanmuMsg{room: room, danmu: &danmuMsg{content: "received during settings"}})
	got = updated.(model)
	if got.danmu.Len() != 1 {
		t.Fatal("settings interrupted reception")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 15}, {Width: 25, Height: 8}, {Width: 1, Height: 1}, {Width: 100, Height: 40}} {
		updated, _ = got.Update(size)
		got = updated.(model)
		if view := got.View(); lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
			t.Fatalf("settings overflow terminal %+v", size)
		}
		if got.settings.cursor != 1 {
			t.Fatal("resize lost selected field")
		}
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.settings != nil || !got.textInput.Focused() || got.state != inputView || got.textInput.Value() != "existing draft" {
		t.Fatal("closing settings lost main input state")
	}
}

func TestSettingsSaveAppliesAllFieldsAndTrimsHistory(t *testing.T) {
	path := settingsTestConfig(t)
	room := uiTestRoom()
	room.RoomID = 1
	defer room.Close()
	updated, _ := InitialModel(room).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(model)
	for i := 0; i < 5; i++ {
		got.danmu.PushBack(&danmuMsg{content: fmt.Sprintf("message-%d", i)})
	}
	got.focusInput()
	got.textInput.SetValue("draft")
	got.openSettings()
	setSetting(t, got.settings, "room_id", "2")
	setSetting(t, got.settings, "room_ids", "[2, 3, 4]")
	setSetting(t, got.settings, "room_switch_key", "CTRL+P")
	setSetting(t, got.settings, "settings_key", "F3")
	setSetting(t, got.settings, "chat_buffer", "2")
	setSetting(t, got.settings, "user_agent", "settings-test-agent")
	for i := range got.settings.fields {
		if got.settings.fields[i].isBool {
			got.settings.fields[i].enabled = false
		}
	}
	updated, cmd := got.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	got = updated.(model)
	if cmd == nil || !got.settings.saving {
		t.Fatal("Ctrl+S did not start saving")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.settings == nil {
		t.Fatal("panel closed while saving")
	}
	updated, _ = got.Update(cmd())
	got = updated.(model)
	if got.settings == nil || got.settings.saving || got.danmu.Len() != 2 || got.roomIndex != -1 || got.textInput.Value() != "draft" {
		t.Fatal("settings were not applied safely")
	}
	raw, _ := os.ReadFile(path)
	disk, err := decodeConfig(raw)
	if err != nil || !reflect.DeepEqual(disk, LiveConfig) || disk.RoomID != 2 || disk.RoomSwitchKey != "ctrl+p" || disk.SettingsKey != "f3" || disk.UserAgent != "settings-test-agent" || disk.ColorMode || disk.ShowMedalLevel || disk.ShowRoomTitle {
		t.Fatalf("incomplete settings: %+v %v", disk, err)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyF3})
	got = updated.(model)
	if got.settings != nil || !got.textInput.Focused() {
		t.Fatal("custom settings shortcut did not close/restore focus")
	}
	updated, switchCmd := got.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if switchCmd == nil || !updated.(model).switching {
		t.Fatal("new switch shortcut did not apply")
	}
}

func TestInvalidSettingsDoNotSaveOrSend(t *testing.T) {
	path := settingsTestConfig(t)
	before, _ := os.ReadFile(path)
	m := InitialModel(uiTestRoom())
	defer m.Close()
	m.openSettings()
	setSetting(t, m.settings, "room_ids", "1, 1")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	got := updated.(model)
	if cmd != nil || got.settings.saving || got.sending || !strings.Contains(got.settings.status, "重复") {
		t.Fatal("invalid fields started save/send")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(before, current) {
		t.Fatal("validation failure wrote the config")
	}
}

func TestSettingsSavingKeepsNetworkResultsAndQuitResponsive(t *testing.T) {
	settingsTestConfig(t)
	room := uiTestRoom()
	room.RoomID = 1
	defer room.Close()
	m := InitialModel(room)
	m.focusInput()
	m.textInput.SetValue("newer draft")
	m.sending = true
	m.openSettings()
	m.settings.saving = true
	updated, receiveCmd := m.Update(receivedDanmuMsg{room: room, danmu: &danmuMsg{content: "still receiving"}})
	got := updated.(model)
	if got.danmu.Len() != 1 || receiveCmd == nil {
		t.Fatal("saving interrupted the receive subscription")
	}
	updated, _ = got.Update(sendResultMsg{room: room, content: "previous draft"})
	got = updated.(model)
	if got.sending || got.textInput.Value() != "newer draft" || got.settings == nil {
		t.Fatal("saving swallowed a send result or erased a newer draft")
	}
	updated, quit := got.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit == nil {
		t.Fatal("saving swallowed Ctrl+C")
	}
	if reflect.TypeOf(quit()) != reflect.TypeOf(tea.Quit()) {
		t.Fatal("Ctrl+C did not request exit")
	}
	select {
	case <-updated.(model).room.DoneChan:
	default:
		t.Fatal("Ctrl+C did not close the room")
	}
}

func TestSettingsButtonsArrowsNeverActivateSave(t *testing.T) {
	settingsTestConfig(t)
	panel := newSettingsPanel(LiveConfig, configSnapshot{})
	panel.focus(len(panel.fields))
	_, closePanel := panel.update(tea.KeyMsg{Type: tea.KeyRight}, false)
	if panel.saving || closePanel || panel.cursor != len(panel.fields)+1 {
		t.Fatal("button arrow activated save")
	}
	_, closePanel = panel.update(tea.KeyMsg{Type: tea.KeyEnter}, true)
	if panel.saving || closePanel || !strings.Contains(panel.status, "正在切房") {
		t.Fatal("save-and-enter interrupted an existing switch")
	}
	panel.focus(5)
	before := panel.fields[5].enabled
	panel.update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, false)
	if panel.fields[5].enabled == before {
		t.Fatal("Space did not toggle the display option")
	}
}

func TestSwitchCommandCapturesConfigBeforeExecution(t *testing.T) {
	settingsTestConfig(t)
	room := uiTestRoom()
	room.RoomID = 1
	defer room.Close()
	var requested string
	room.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/x/web-interface/nav" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":-101}`)), Header: make(http.Header)}, nil
		}
		requested = req.URL.Query().Get("room_id")
		return nil, fmt.Errorf("deliberate test stop")
	})}
	m := InitialModel(room)
	cmd := m.switchRoom()
	LiveConfig.RoomID, LiveConfig.RoomIDs = 8, []uint64{8, 9}
	msg := cmd().(roomChangedMsg)
	if msg.err == nil || requested != "2" {
		t.Fatalf("switch read changed global config: %s %v", requested, msg.err)
	}
}

func TestConfigSavingCanonicalizesCaseAliases(t *testing.T) {
	original := []byte("ROOM_ID=1\nCHAT_BUFFER=200\nROOM_SWITCH_KEY='ctrl+n'\n[custom]\nROOM_ID=7\n")
	cfg, err := decodeConfig(original)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RoomID, cfg.ChatBuffer, cfg.RoomSwitchKey = 2, 300, "ctrl+p"
	raw, err := encodeConfig(original, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]interface{}
	if _, err := toml.Decode(string(raw), &values); err != nil {
		t.Fatal(err)
	}
	if _, found := values["ROOM_ID"]; found {
		t.Fatal("old case alias competes with the saved value")
	}
	if values["custom"].(map[string]interface{})["ROOM_ID"] != int64(7) {
		t.Fatal("nested unknown option was changed")
	}
	for i := 0; i < 30; i++ {
		loaded, err := decodeConfig(raw)
		if err != nil || loaded.RoomID != 2 || loaded.ChatBuffer != 300 || loaded.RoomSwitchKey != "ctrl+p" {
			t.Fatal("saved case aliases restored old values")
		}
	}
	if _, err := decodeConfig([]byte("room_id=1\nROOM_ID=2\n")); err == nil {
		t.Fatal("ambiguous case aliases were accepted")
	}
}

func TestRoomAliasesPreserveSelectedListPosition(t *testing.T) {
	settingsTestConfig(t)
	LiveConfig.RoomID, LiveConfig.RoomIDs = 123, []uint64{123, 7, 2}
	old, next := uiTestRoom(), uiTestRoom()
	old.RoomID, old.ShortID, next.RoomID, next.ShortID = 123, 7, 123, 7
	defer old.Close()
	defer next.Close()
	m := InitialModel(old)
	updated, _ := m.Update(roomChangedMsg{source: old, room: next, index: 1, requestedID: 7, claimed: make(chan struct{})})
	got := updated.(model)
	if got.roomIndex != 1 {
		t.Fatal("short/long alias reset the selected list index")
	}
	ids := configuredRooms(LiveConfig)
	if target := ids[(roomIndexForRequest(got.room, ids, got.roomRequest)+1)%len(ids)]; target != 2 {
		t.Fatal("alias trapped room cycling")
	}
	LiveConfig.RoomID = 7
	if initialRoomIndex(next, LiveConfig) != 1 {
		t.Fatal("default short ID did not select its own list position")
	}
}

func TestRequestedRoomSurvivesListReorderDuringSwitch(t *testing.T) {
	settingsTestConfig(t)
	old, next := uiTestRoom(), uiTestRoom()
	old.RoomID, old.ShortID, next.RoomID, next.ShortID = 123, 7, 123, 7
	defer old.Close()
	defer next.Close()
	LiveConfig.RoomID, LiveConfig.RoomIDs = 123, []uint64{123, 7, 2}
	m := InitialModel(old)
	// This result was requested at index 1, but the saved list has changed.
	LiveConfig.RoomIDs = []uint64{7, 2, 123}
	updated, _ := m.Update(roomChangedMsg{source: old, room: next, index: 1, requestedID: 7, claimed: make(chan struct{})})
	got := updated.(model)
	if got.roomIndex != 0 || got.roomRequest != 7 {
		t.Fatal("in-flight result trusted a stale list position")
	}
	ids := configuredRooms(LiveConfig)
	if ids[(roomIndexForRequest(got.room, ids, got.roomRequest)+1)%len(ids)] != 2 {
		t.Fatal("reordered aliases trapped cycling")
	}
}

func TestMaximumRoomListIsNeverTruncated(t *testing.T) {
	cfg := defaultConfig()
	cfg.RoomID = 1000000000000000000
	for i := uint64(0); i < 1000; i++ {
		cfg.RoomIDs = append(cfg.RoomIDs, cfg.RoomID+i)
	}
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	panel := newSettingsPanel(cfg, configSnapshot{})
	got, err := panel.values()
	if err != nil || !reflect.DeepEqual(cfg.RoomIDs, got.RoomIDs) {
		t.Fatal("opening settings silently truncated a valid room list")
	}
	panel.resize(tea.WindowSizeMsg{Width: 80, Height: 6})
	panel.focus(len(panel.fields) - 1)
	if !strings.Contains(panel.view(), "resize") {
		t.Fatal("tiny settings window hides field identity without a resize prompt")
	}
	panel.resize(tea.WindowSizeMsg{Width: 80, Height: 7})
	if !strings.Contains(panel.view(), "User-Agent") {
		t.Fatal("scrolling hid the selected field label")
	}
}

func TestNewConfigCanBeSavedTwiceAndDetectsCreationConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.toml")
	snapshot, err := readConfigSnapshot(path)
	if err != nil || snapshot.exists {
		t.Fatalf("new config snapshot: %v", err)
	}
	cfg := defaultConfig()
	cfg.RoomID = 1
	created, err := saveConfig(snapshot, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RoomID = 2
	if _, err := saveConfig(created, cfg); err != nil {
		t.Fatalf("second save falsely detected external permissions: %v", err)
	}
	if _, err := saveConfig(snapshot, cfg); err == nil {
		t.Fatal("externally created file was overwritten by an old missing-file snapshot")
	}
}

func TestConflictDetectedAfterReplacementIsPrepared(t *testing.T) {
	path := settingsTestConfig(t)
	snapshot, err := readConfigSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	external := append(append([]byte(nil), snapshot.raw...), []byte("\n# late external edit\n")...)
	err = atomicWriteConfig(path, []byte("room_id=9\n"), snapshot.mode, func() error {
		if err := os.WriteFile(path, external, snapshot.mode); err != nil {
			return err
		}
		return snapshotUnchanged(snapshot)
	})
	if err == nil {
		t.Fatal("late external edit was not detected")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, external) {
		t.Fatal("late external edit was overwritten")
	}
}

func TestUserAgentControlCharactersRejected(t *testing.T) {
	cfg := defaultConfig()
	cfg.RoomID = 1
	for _, ua := range []string{"bad\x01ua", "bad\x7fua", "bad\tua", "bad\x1bua"} {
		cfg.UserAgent = ua
		if _, err := normalizeConfig(cfg); err == nil {
			t.Fatalf("HTTP control character accepted: %q", ua)
		}
	}
}

func TestUserAgentUpdatesWithoutMutatingRequests(t *testing.T) {
	old := LiveConfig
	defer func() { LiveConfig = old }()
	LiveConfig.UserAgent = "old-agent"
	client := GetCustomHttpClient()
	var seen []string
	client.Transport.(*userAgentTransport).rt = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.Header.Get("User-Agent"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	req, _ := http.NewRequest("GET", "https://example.invalid", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	applyUserAgent(client, "new-agent")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !reflect.DeepEqual(seen, []string{"old-agent", "new-agent"}) || req.Header.Get("User-Agent") != "" {
		t.Fatal("UA was not updated safely")
	}
}
