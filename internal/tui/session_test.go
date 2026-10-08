package tui

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
)

func uiTestRoom() *api.LiveRoom {
	return &api.LiveRoom{MessageChan: make(chan *api.DanmuMessage, 10), DoneChan: make(chan struct{})}
}

func uiDanmu(text string) *api.DanmuMessage {
	return &api.DanmuMessage{Cmd: "DANMU_MSG:4:0:2:2:2:0", Info: []interface{}{
		[]interface{}{nil, nil, nil, float64(16777215), float64(1700000000000)}, text,
		[]interface{}{float64(123), "name"}, []interface{}{},
	}}
}

func TestSubscriptionCloseAndStaleRoomMessages(t *testing.T) {
	previousConfig := LiveConfig
	defer func() { LiveConfig = previousConfig }()
	old, current := uiTestRoom(), uiTestRoom()
	m := InitialModel(current)
	LiveConfig.ChatBuffer = 200
	updated, _ := m.Update(receivedDanmuMsg{room: old, danmu: &danmuMsg{content: "old"}})
	if updated.(model).danmu.Len() != 0 {
		t.Fatal("old-room danmu entered current view")
	}
	finished := make(chan tea.Msg, 1)
	go func() { finished <- waitForDanmu(old)() }()
	old.Close()
	select {
	case msg := <-finished:
		if msg != nil {
			t.Fatal("closed subscription returned message")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscription did not stop")
	}
}

func TestUnclaimedSwitchRoomCleanup(t *testing.T) {
	sourceDone := make(chan struct{})
	claimed := make(chan struct{})
	room := uiTestRoom()
	finished := make(chan struct{})
	go func() { closeUnclaimedRoom(sourceDone, claimed, room); close(finished) }()
	close(sourceDone)
	<-finished
	select {
	case <-room.DoneChan:
	default:
		t.Fatal("late switch result leaked room")
	}

	sourceDone = make(chan struct{})
	claimed = make(chan struct{})
	room = uiTestRoom()
	close(claimed)
	close(sourceDone) // Both ready: an already-claimed room must stay alive.
	closeUnclaimedRoom(sourceDone, claimed, room)
	select {
	case <-room.DoneChan:
		t.Fatal("claimed room closed with previous session")
	default:
	}
}

func TestRoomSwitchDebouncesAndFailureRetainsOldRoom(t *testing.T) {
	previousConfig := LiveConfig
	defer func() { LiveConfig = previousConfig }()
	LiveConfig.RoomIDs = []uint64{1, 2}
	m := InitialModel(uiTestRoom())
	updated, first := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	got := updated.(model)
	if first == nil || !got.switching {
		t.Fatal("switch was not started")
	}
	updated, duplicate := got.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if duplicate != nil {
		t.Fatal("duplicate switch command")
	}
	got = updated.(model)
	updated, _ = got.Update(roomChangedMsg{source: got.room, err: fmt.Errorf("temporary failure")})
	got = updated.(model)
	if got.switching || got.room != m.room {
		t.Fatal("failed switch discarded current room")
	}
	select {
	case <-got.room.DoneChan:
		t.Fatal("failed switch closed working receiver")
	default:
	}
}

func TestSwitchCancellationDuringSetup(t *testing.T) {
	previousConfig := LiveConfig
	defer func() { LiveConfig = previousConfig }()
	LiveConfig.RoomIDs = []uint64{1, 2}
	room := uiTestRoom()
	started := make(chan struct{})
	room.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	m := InitialModel(room)
	finished := make(chan tea.Msg, 1)
	go func() { finished <- m.switchRoom()() }()
	<-started
	room.Close()
	select {
	case msg := <-finished:
		result := msg.(roomChangedMsg)
		if result.room != nil || result.err == nil {
			t.Fatal("cancelled setup created a live room")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("setup did not cancel with source room")
	}
}

func wirePacket(body []byte, operation uint32, version uint16) []byte {
	packet := make([]byte, 16+len(body))
	binary.BigEndian.PutUint32(packet, uint32(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], 16)
	binary.BigEndian.PutUint16(packet[6:8], version)
	binary.BigEndian.PutUint32(packet[8:12], operation)
	binary.BigEndian.PutUint32(packet[12:16], 1)
	copy(packet[16:], body)
	return packet
}

// Real local TCP auth/heartbeat/message transport + real UI parsing and Update,
// with all HTTP endpoints stubbed. No Bilibili network or account is used.
func TestUIReceivesAfterReconnect(t *testing.T) {
	previousConfig := LiveConfig
	defer func() { LiveConfig = previousConfig }()
	LiveConfig.ChatBuffer = 200
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://bilibili.com")
	jar.SetCookies(u, []*http.Cookie{{Name: "bili_jct", Value: "token", Domain: ".bilibili.com", Path: "/"}})
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/x/web-interface/nav":
			body = `{"code":0,"data":{"mid":123,"wbi_img":{"img_url":"https://example.com/0123456789abcdef0123456789abcdef.png","sub_url":"https://example.com/abcdef0123456789abcdef0123456789.png"}}}`
		case "/room/v1/Room/get_info":
			body = `{"code":0,"data":{"room_id":456}}`
		case "/xlive/web-room/v1/index/getInfoByUser":
			body = `{"code":0,"data":{"property":{"danmu":{"length":30}}}}`
		case "/xlive/web-room/v1/index/getDanmuInfo":
			body = fmt.Sprintf(`{"code":0,"data":{"token":"test","host_list":[{"host":"127.0.0.1","port":%s}]}}`, port)
		default:
			return nil, fmt.Errorf("unexpected HTTP endpoint %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	serverErrors := make(chan error, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer func() { releaseOnce.Do(func() { close(release) }) }()
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				serverErrors <- err
				return
			}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			header := make([]byte, 16)
			if _, err = io.ReadFull(conn, header); err == nil {
				_, err = io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint32(header))-16)
			}
			if err == nil {
				_, err = conn.Write(wirePacket([]byte(`{"code":0}`), 8, 1))
			}
			body, _ := json.Marshal(uiDanmu(fmt.Sprintf("message-%d", i)))
			if err == nil {
				_, err = conn.Write(wirePacket(body, 5, 0))
			}
			if err != nil {
				conn.Close()
				serverErrors <- err
				return
			}
			if i == 0 {
				if _, err = io.ReadFull(conn, header); err == nil {
					_, err = io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint32(header))-16)
				}
				conn.Close()
			} else {
				<-release
				conn.Close()
			}
		}
		serverErrors <- nil
	}()
	room, err := live_room.AuthAndConnect(client, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer room.Close()
	m := InitialModel(room)
	for i := 0; i < 2; i++ {
		result := make(chan tea.Msg, 1)
		go func() { result <- waitForDanmu(room)() }()
		select {
		case msg := <-result:
			updated, next := m.Update(msg)
			got := updated.(model)
			if next == nil || got.danmu.Len() != i+1 || got.danmu.Back().Value.(*danmuMsg).content != fmt.Sprintf("message-%d", i) {
				t.Fatalf("UI did not display message %d", i)
			}
			m = &got
		case <-time.After(5 * time.Second):
			t.Fatal("UI did not resume after reconnect")
		}
	}
	room.Close()
	releaseOnce.Do(func() { close(release) })
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
}
