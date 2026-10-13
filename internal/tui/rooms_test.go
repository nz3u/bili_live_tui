package tui

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
)

func roomConfig(t *testing.T) {
	t.Helper()
	previous := LiveConfig
	t.Cleanup(func() { LiveConfig = previous })
	LiveConfig = api.BiliLiveConfig{RoomID: 1, RoomIDs: []uint64{1, 2}, ChatBuffer: 20}
}
func fakeRoom(id uint64) *api.LiveRoom {
	return &api.LiveRoom{RoomID: id, MessageChan: make(chan *api.DanmuMessage, 2), DoneChan: make(chan struct{}), Client: &http.Client{}}
}
func TestConfiguredRooms(t *testing.T) {
	got := configuredRooms(api.BiliLiveConfig{RoomID: 3, RoomIDs: []uint64{1, 0, 1, 2}})
	if !reflect.DeepEqual(got, []uint64{3, 1, 2}) {
		t.Fatal(got)
	}
	got = configuredRooms(api.BiliLiveConfig{RoomID: 2, RoomIDs: []uint64{1, 2}})
	if !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatal(got)
	}
}
func TestSwitchRetainsDraftHistoryAndDropsOldEvents(t *testing.T) {
	roomConfig(t)
	first, second := fakeRoom(1), fakeRoom(2)
	m := InitialModel(first)
	defer m.Close()
	m.owner.open = func(_ *http.Client, id uint64) (*api.LiveRoom, error) { return second, nil }
	m.textInput.SetValue("房间一草稿")
	m.appendDanmu(&danmuMsg{content: "房间一消息"})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true})
	m = updated.(model)
	if cmd == nil || !m.switching {
		t.Fatal("not switching")
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.room != second || m.danmu.Len() != 0 || m.textInput.Value() != "" {
		t.Fatal("state not isolated")
	}
	select {
	case <-first.DoneChan:
	default:
		t.Fatal("old room not closed")
	}
	updated, oldCmd := m.Update(roomEvent{room: first, message: &api.DanmuMessage{Cmd: "DANMU_MSG"}, open: true})
	m = updated.(model)
	if oldCmd != nil || m.danmu.Len() != 0 {
		t.Fatal("stale event accepted")
	}
	m.textInput.SetValue("房间二草稿")
	third := fakeRoom(1)
	m.owner.open = func(_ *http.Client, id uint64) (*api.LiveRoom, error) { return third, nil }
	updated, cmd = m.nextRoom()
	m = updated.(model)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.textInput.Value() != "房间一草稿" || m.danmu.Len() != 1 {
		t.Fatal("memory lost")
	}
	if m.memories[2].draft != "房间二草稿" {
		t.Fatal("second draft lost")
	}
}
func TestSwitchFailureKeepsCurrentAndSendBlocksSwitch(t *testing.T) {
	roomConfig(t)
	first := fakeRoom(1)
	m := InitialModel(first)
	defer m.Close()
	m.owner.open = func(_ *http.Client, id uint64) (*api.LiveRoom, error) { return nil, errors.New("offline") }
	m.sending = true
	_, cmd := m.nextRoom()
	if cmd != nil {
		t.Fatal("switched while sending")
	}
	m.sending = false
	updated, cmd := m.nextRoom()
	m = updated.(model)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.room != first || m.switching || m.sendStatus == "" {
		t.Fatal("failed switch corrupts state")
	}
	select {
	case <-first.DoneChan:
		t.Fatal("closed working room")
	default:
	}
}
func TestLateOpenedRoomClosedAfterExit(t *testing.T) {
	roomConfig(t)
	room := fakeRoom(2)
	m := InitialModel(fakeRoom(1))
	m.owner.open = func(_ *http.Client, id uint64) (*api.LiveRoom, error) { return room, nil }
	cmd := m.owner.connect(nil, 2)
	m.Close()
	result := cmd().(roomOpened)
	if result.err == nil {
		t.Fatal("accepted after exit")
	}
	select {
	case <-room.DoneChan:
	default:
		t.Fatal("late room leaked")
	}
}
func TestClosedRoomDoesNotResubscribe(t *testing.T) {
	roomConfig(t)
	room := fakeRoom(1)
	close(room.MessageChan)
	m := InitialModel(room)
	defer m.Close()
	event := waitRoom(room)()
	_, cmd := m.Update(event)
	if cmd != nil {
		t.Fatal("closed channel busy loop")
	}
}
