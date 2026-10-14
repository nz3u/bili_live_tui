package tui

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
)

// settingsKey batches the expiry/layout commands before the room-open command.
// Execute only that final command; never wait on a live room or a status timer.
func savedRoomOpen(t *testing.T, cmd tea.Cmd) roomOpened {
	t.Helper()
	if cmd == nil {
		t.Fatal("no room open command")
	}
	msg := cmd()
	// Bubble Tea 0.22 uses an unexported batchMsg slice. Inspect its commands
	// without relying on the name (or waiting for unrelated timer commands).
	if batch := reflect.ValueOf(msg); batch.Kind() == reflect.Slice {
		if batch.Len() == 0 {
			t.Fatal("empty command batch")
		}
		last, ok := batch.Index(batch.Len() - 1).Interface().(tea.Cmd)
		if !ok {
			t.Fatalf("unexpected batched command %T", msg)
		}
		msg = last()
	}
	result, ok := msg.(roomOpened)
	if !ok {
		t.Fatalf("expected roomOpened, got %T", msg)
	}
	return result
}

func TestSettingsSaveInsertsMissingDefaultFirst(t *testing.T) {
	for _, tc := range []struct {
		name, defaultID, list string
		want                  []uint64
	}{
		{"new-default", "3", "1, 2", []uint64{3, 1, 2}},
		{"empty-list", "1", "", []uint64{1}},
		{"existing-default-keeps-order", "2", "1, 2, 3", []uint64{1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := settingsFixture(t)
			m.settings.fields[0].SetValue(tc.defaultID)
			m.settings.fields[1].SetValue(tc.list)
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
			m = next.(model)
			if m.settings != nil || !reflect.DeepEqual(LiveConfig.RoomIDs, tc.want) {
				t.Fatalf("not normalized: %+v", LiveConfig)
			}
			_, cfg, err := openConfigStore(loadedConfig.path)
			if err != nil || !reflect.DeepEqual(cfg.RoomIDs, tc.want) {
				t.Fatalf("not persisted: %+v %v", cfg, err)
			}
			if m.switching || m.roomID != 1 {
				t.Fatal("current room was retained but still switched")
			}
		})
	}
}

func TestSettingsRemovingCurrentRoomSwitchesToDefaultNotFirst(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("default-missing=%v", missing), func(t *testing.T) {
			m := settingsFixture(t)
			first, target := m.room, fakeRoom(3)
			var requested uint64
			m.owner.open = func(client *http.Client, id uint64) (*api.LiveRoom, error) { requested = id; return target, nil }
			m.textInput.SetValue("删除前草稿")
			m.audioMuted = true
			m.audioMuteRevision = m.audio.setMuted(true)
			m.settings.fields[0].SetValue("3")
			m.settings.fields[1].SetValue("2, 3") // Default is not first.
			if missing {
				m.settings.fields[1].SetValue("2, 4")
			}
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
			m = next.(model)
			if m.settings != nil || !m.switching || m.room != first || m.textInput.Value() != "删除前草稿" {
				t.Fatal("save did not start a safe asynchronous switch")
			}
			select {
			case <-first.DoneChan:
				t.Fatal("active room closed before connection succeeded")
			default:
			}
			result := savedRoomOpen(t, cmd)
			if requested != 3 || result.id != 3 {
				t.Fatalf("switched to %d, want default 3", requested)
			}
			next, _ = m.Update(result)
			m = next.(model)
			if m.room != target || m.roomID != 3 || m.switching || !m.audioMuted {
				t.Fatal("room/audio state did not follow the saved default")
			}
			select {
			case <-first.DoneChan:
			default:
				t.Fatal("previous connection leaked")
			}
		})
	}
}

func TestSettingsDeletingCurrentNonDefaultRoomReturnsToUnchangedDefault(t *testing.T) {
	m := settingsFixture(t)
	m.settings = nil
	room2 := fakeRoom(2)
	m.owner.open = func(*http.Client, uint64) (*api.LiveRoom, error) { return room2, nil }
	next, cmd := m.nextRoom()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m = next.(model)
	m.settings.fields[1].SetValue("1")
	defaultRoom := fakeRoom(1)
	var requested uint64
	m.owner.open = func(_ *http.Client, id uint64) (*api.LiveRoom, error) { requested = id; return defaultRoom, nil }
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(model)
	if !m.switching || m.roomID != 2 || LiveConfig.RoomID != 1 {
		t.Fatal("save did not start returning to the unchanged default")
	}
	next, _ = m.Update(savedRoomOpen(t, cmd))
	m = next.(model)
	if requested != 1 || m.roomID != 1 || m.room != defaultRoom || m.switching {
		t.Fatal("did not return to default room 1")
	}
	select {
	case <-room2.DoneChan:
	default:
		t.Fatal("deleted room was not closed")
	}
}

func TestSettingsFailedAutoSwitchRetainsRoomDraftAndSavedConfig(t *testing.T) {
	m := settingsFixture(t)
	first := m.room
	m.textInput.SetValue("保留草稿")
	m.audioPlaying = true
	m.owner.open = func(*http.Client, uint64) (*api.LiveRoom, error) { return nil, errors.New("offline") }
	m.settings.fields[0].SetValue("3")
	m.settings.fields[1].SetValue("2")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(model)
	next, _ = m.Update(savedRoomOpen(t, cmd))
	m = next.(model)
	if m.room != first || m.roomID != 1 || m.switching || !m.audioPlaying || m.textInput.Value() != "保留草稿" || !strings.Contains(m.sendStatus, "切换失败") {
		t.Fatal("failed auto-switch changed active state")
	}
	if LiveConfig.RoomID != 3 || !reflect.DeepEqual(LiveConfig.RoomIDs, []uint64{3, 2}) {
		t.Fatal("failed connection rolled back a successful config save")
	}
	select {
	case <-first.DoneChan:
		t.Fatal("old room closed on failure")
	default:
	}
}

func TestSettingsDoesNotReconnectWhenDefaultRoomIsReinserted(t *testing.T) {
	m := settingsFixture(t)
	m.settings.fields[1].SetValue("2") // Current room 1 is also the unchanged default.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(model)
	if m.switching || !reflect.DeepEqual(LiveConfig.RoomIDs, []uint64{1, 2}) {
		t.Fatal("default was not restored without reconnecting")
	}
}

func TestSettingsRoomNormalizationDoesNotHideInvalidInput(t *testing.T) {
	for _, tc := range []struct{ name, list string }{
		{"duplicate", "1, 2, 2"},
		{"zero", "1, 0"},
		{"fifty-plus-default", func() string {
			ids := make([]string, 50)
			for i := range ids {
				ids[i] = fmt.Sprint(i + 2)
			}
			return strings.Join(ids, ",")
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := settingsFixture(t)
			m.settings.fields[1].SetValue(tc.list)
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
			m = next.(model)
			if m.settings == nil || m.settings.errorText == "" || m.switching || !reflect.DeepEqual(LiveConfig.RoomIDs, []uint64{1, 2}) {
				t.Fatal("invalid normalized config was applied")
			}
			_, cfg, err := openConfigStore(loadedConfig.path)
			if err != nil || !reflect.DeepEqual(cfg.RoomIDs, []uint64{1, 2}) {
				t.Fatal("invalid save changed disk config")
			}
		})
	}
}

func TestSettingsCancelRemovalDoesNotSwitchOrNormalize(t *testing.T) {
	m := settingsFixture(t)
	m.settings.fields[0].SetValue("3")
	m.settings.fields[1].SetValue("2")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.settings != nil || m.switching || cmd != nil || LiveConfig.RoomID != 1 || !reflect.DeepEqual(LiveConfig.RoomIDs, []uint64{1, 2}) {
		t.Fatal("cancel applied room edits")
	}
}
