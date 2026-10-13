package tui

import (
	"container/list"
	"fmt"
	"net/http"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
	"github.com/shr-go/bili_live_tui/pkg/logging"
)

func configuredRooms(cfg api.BiliLiveConfig) []uint64 {
	ids := make([]uint64, 0, len(cfg.RoomIDs)+1)
	seen := make(map[uint64]bool)
	for _, id := range cfg.RoomIDs {
		if id > 0 && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if cfg.RoomID > 0 && !seen[cfg.RoomID] {
		ids = append([]uint64{cfg.RoomID}, ids...)
	}
	return ids
}

type roomMemory struct {
	messages   *list.List
	draft      string
	offset     int
	lockBottom bool
}
type roomEvent struct {
	room    *api.LiveRoom
	message *api.DanmuMessage
	open    bool
}
type roomOpened struct {
	id   uint64
	room *api.LiveRoom
	err  error
}

// The shared owner also closes a connection that finishes opening after the UI exits.
// Only the active and (briefly) candidate connections are retained.
type roomOwner struct {
	mu     sync.Mutex
	closed bool
	rooms  map[*api.LiveRoom]bool
	open   func(*http.Client, uint64) (*api.LiveRoom, error)
}

func newRoomOwner(room *api.LiveRoom) *roomOwner {
	return &roomOwner{rooms: map[*api.LiveRoom]bool{room: true}, open: live_room.AuthAndConnect}
}
func (o *roomOwner) connect(client *http.Client, id uint64) tea.Cmd {
	return func() tea.Msg {
		room, err := o.open(client, id)
		if err != nil {
			return roomOpened{id: id, err: err}
		}
		o.mu.Lock()
		closed := o.closed
		if !closed {
			o.rooms[room] = true
		}
		o.mu.Unlock()
		if closed {
			room.Close()
			return roomOpened{id: id, err: fmt.Errorf("已退出")}
		}
		return roomOpened{id: id, room: room}
	}
}
func (o *roomOwner) release(room *api.LiveRoom) {
	o.mu.Lock()
	delete(o.rooms, room)
	o.mu.Unlock()
	room.Close()
}
func (o *roomOwner) close() {
	o.mu.Lock()
	o.closed = true
	rooms := o.rooms
	o.rooms = make(map[*api.LiveRoom]bool)
	o.mu.Unlock()
	for room := range rooms {
		room.Close()
	}
}
func (m model) Close() { m.owner.close() }
func waitRoom(room *api.LiveRoom) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-room.MessageChan
		return roomEvent{room, msg, ok}
	}
}
func (m model) nextRoom() (tea.Model, tea.Cmd) {
	if m.switching || m.sending {
		return m, nil
	}
	ids := configuredRooms(LiveConfig)
	if len(ids) < 2 {
		m.sendStatus = "请在设置中添加直播间"
		return m, nil
	}
	next := ids[0]
	for i, id := range ids {
		if id == m.roomID {
			next = ids[(i+1)%len(ids)]
			break
		}
	}
	m.switching = true
	m.sendStatus = fmt.Sprintf("切换到 %d", next)
	return m, m.owner.connect(m.room.Client, next)
}
func (m model) finishSwitch(result roomOpened) (tea.Model, tea.Cmd) {
	m.switching = false
	if result.err != nil {
		m.sendStatus = "切换失败：" + singleLine(result.err.Error())
		return m, nil
	}
	m.memories[m.roomID] = roomMemory{m.danmu, m.textInput.Value(), m.viewport.YOffset, m.lockBottom}
	m.owner.release(m.room)
	m.room, m.roomID = result.room, result.id
	memory, ok := m.memories[result.id]
	if !ok {
		memory = roomMemory{messages: list.New(), lockBottom: true}
	}
	m.danmu, m.lockBottom = memory.messages, memory.lockBottom
	for m.danmu.Len() > max(1, LiveConfig.ChatBuffer) {
		m.danmu.Remove(m.danmu.Front())
	}
	m.textInput.CharLimit = danmuLimit(m.room)
	// Preserve the entire draft when different rooms have different send limits.
	m.textInput.CharLimit = 0
	m.textInput.SetValue(memory.draft)
	m.textInput.CharLimit = danmuLimit(m.room)
	m.viewport.SetContent(m.renderDanmu())
	m.viewport.SetYOffset(memory.offset)
	if m.lockBottom {
		m.viewport.GotoBottom()
	}
	m.sendStatus = ""
	return m, waitRoom(m.room)
}
func (m *model) appendDanmu(danmu *danmuMsg) {
	m.danmu.PushBack(danmu)
	for m.danmu.Len() > max(1, LiveConfig.ChatBuffer) {
		m.danmu.Remove(m.danmu.Front())
	}
	if m.ready {
		m.viewport.SetContent(m.renderDanmu())
	}
}
func (m *model) receive(event roomEvent) tea.Cmd {
	if event.room != m.room || !event.open {
		return nil
	}
	msg := event.message
	if msg != nil {
		switch {
		case msg.Cmd == "DANMU_MSG" || len(msg.Cmd) > 10 && msg.Cmd[:10] == "DANMU_MSG:":
			if danmu := processDanmuMsg(msg); danmu != nil {
				m.appendDanmu(danmu)
			}
		case msg.Cmd == "PREPARING":
			logging.Rotate()
		}
	}
	return waitRoom(m.room)
}
