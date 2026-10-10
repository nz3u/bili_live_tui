package tui

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
)

type keyboardPost struct{ room, content string }

// openedRoomPages records the pages Alt+F asked the system to open. The stub is
// installed for the whole integration test so no real browser is ever launched.
var (
	openedRoomPagesMu sync.Mutex
	openedRoomPagesNS []string
)

func openedRoomPages() []string {
	openedRoomPagesMu.Lock()
	defer openedRoomPagesMu.Unlock()
	return append([]string(nil), openedRoomPagesNS...)
}

func resetOpenedRoomPages() {
	openedRoomPagesMu.Lock()
	defer openedRoomPagesMu.Unlock()
	openedRoomPagesNS = nil
}

func stubRoomPageOpener(t *testing.T) {
	t.Helper()
	previous := openRoomPage
	t.Cleanup(func() { openRoomPage = previous })
	resetOpenedRoomPages()
	openRoomPage = func(room *api.LiveRoom) error {
		openedRoomPagesMu.Lock()
		defer openedRoomPagesMu.Unlock()
		openedRoomPagesNS = append(openedRoomPagesNS, roomPageURL(room))
		return nil
	}
}

type keyboardFixture struct {
	listener net.Listener
	mu       sync.Mutex
	conns    map[net.Conn]bool
	workers  sync.WaitGroup
	posts    chan keyboardPost
}

func newKeyboardFixture(t *testing.T) (*keyboardFixture, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &keyboardFixture{listener: listener, conns: make(map[net.Conn]bool), posts: make(chan keyboardPost, 10)}
	fixture.workers.Add(1)
	go func() {
		defer fixture.workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			fixture.mu.Lock()
			fixture.conns[conn] = true
			fixture.mu.Unlock()
			fixture.workers.Add(1)
			go fixture.serve(conn)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		fixture.mu.Lock()
		for conn := range fixture.conns {
			conn.Close()
		}
		fixture.mu.Unlock()
		fixture.workers.Wait()
	})
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://bilibili.com")
	jar.SetCookies(u, []*http.Cookie{{Name: "bili_jct", Value: "test-token", Domain: ".bilibili.com", Path: "/"}})
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/x/web-interface/nav":
			body = `{"code":0,"data":{"mid":123,"wbi_img":{"img_url":"https://example.invalid/0123456789abcdef0123456789abcdef.png","sub_url":"https://example.invalid/abcdef0123456789abcdef0123456789.png"}}}`
		case "/room/v1/Room/get_info":
			id, err := strconv.ParseUint(req.URL.Query().Get("room_id"), 10, 64)
			if err != nil {
				return nil, err
			}
			body = fmt.Sprintf(`{"code":0,"data":{"room_id":%d,"title":"Room %d"}}`, id, id)
		case "/xlive/web-room/v1/index/getInfoByUser":
			body = `{"code":0,"data":{"property":{"danmu":{"length":30}}}}`
		case "/xlive/web-room/v1/index/getDanmuInfo":
			body = fmt.Sprintf(`{"code":0,"data":{"token":"test","host_list":[{"host":"127.0.0.1","port":%s}]}}`, port)
		case "/msg/send":
			if err := req.ParseMultipartForm(1024 * 1024); err != nil {
				return nil, err
			}
			if req.MultipartForm != nil {
				defer req.MultipartForm.RemoveAll()
			}
			fixture.posts <- keyboardPost{room: req.FormValue("roomid"), content: req.FormValue("msg")}
			body = `{"code":0}`
		case "/xlive/rdata-interface/v1/heartbeat/webHeartBeat":
			body = `{"code":0,"data":{"next_interval":20}}`
		default:
			return nil, fmt.Errorf("blocked unexpected test HTTP endpoint %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return fixture, client
}

func (f *keyboardFixture) serve(conn net.Conn) {
	defer f.workers.Done()
	defer func() { conn.Close(); f.mu.Lock(); delete(f.conns, conn); f.mu.Unlock() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 16)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	size := binary.BigEndian.Uint32(header)
	if size < 16 || size > 4096 {
		return
	}
	body := make([]byte, int(size)-16)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	var auth struct {
		RoomID uint64 `json:"roomid"`
	}
	if json.Unmarshal(body, &auth) != nil {
		return
	}
	if _, err := conn.Write(wirePacket([]byte(`{"code":0}`), 8, 1)); err != nil {
		return
	}
	danmu, _ := json.Marshal(uiDanmu(fmt.Sprintf("room-%d", auth.RoomID)))
	if _, err := conn.Write(wirePacket(danmu, 5, 0)); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_, _ = io.Copy(io.Discard, conn)
}

type keyboardEvent struct {
	kind, key, draft, defaultField, roomListField, status, content string
	input, panel, switching, sending                               bool
	room                                                           uint64
	width                                                          int
	cursor                                                         int
}

type keyboardDeadlineMsg struct{}

type keyboardProgramProbe struct {
	inner    model
	events   chan keyboardEvent
	external chan tea.Msg
	stop     <-chan struct{}
}

func (m keyboardProgramProbe) waitExternal() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.external:
			return msg
		case <-m.stop:
			return nil
		}
	}
}

func (m keyboardProgramProbe) Init() tea.Cmd {
	// Physical console-size polling has its own tests. Here a controlled
	// bridge delivers polled sizes while real raw bytes drive the input reader.
	return tea.Batch(waitForDanmu(m.inner.room), m.waitExternal(),
		func() tea.Msg { return tea.WindowSizeMsg{Width: 80, Height: 24} },
		tea.Tick(8*time.Second, func(time.Time) tea.Msg { return keyboardDeadlineMsg{} }))
}

func (m keyboardProgramProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(keyboardDeadlineMsg); ok {
		m.inner.Close()
		return m, tea.Quit
	}
	updated, cmd := m.inner.Update(msg)
	m.inner = updated.(model)
	event := keyboardEvent{draft: m.inner.textInput.Value(), input: m.inner.textInput.Focused(), panel: m.inner.settings != nil,
		room: m.inner.room.RoomID, switching: m.inner.switching, sending: m.inner.sending, width: m.inner.windowSize.Width, status: m.inner.sendHint}
	if m.inner.settings != nil {
		event.defaultField = m.inner.settings.fields[0].input.Value()
		event.roomListField = m.inner.settings.fields[1].input.Value()
		event.status = m.inner.settings.status
		event.cursor = m.inner.settings.cursor
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		event.kind, event.key = "key", msg.String()
	case tea.WindowSizeMsg:
		event.kind = "resize"
		cmd = nil // do not poll the test runner's physical stdout after this size
	case windowSizePollMsg:
		cmd = tea.Batch(cmd, m.waitExternal())
	case roomChangedMsg:
		event.kind = "room"
	case sendResultMsg:
		event.kind, event.content = "send", msg.content
	case settingsSavedMsg:
		event.kind = "save"
	case receivedDanmuMsg:
		event.kind, event.content = "receive", msg.danmu.content
	}
	if event.kind != "" {
		m.events <- event
	}
	return m, cmd
}

func (m keyboardProgramProbe) View() string { return m.inner.View() }

// Covers the real reader/decoder, renderer and Bubble Tea command event loop,
// real local TCP rooms, mocked HTTP sends, settings disk writes and focus.
func TestRawKeyboardSettingsResizeSwitchAndSend(t *testing.T) {
	settingsTestConfig(t)
	stubRoomPageOpener(t)
	fixture, client := newKeyboardFixture(t)
	room, err := live_room.AuthAndConnect(client, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer room.Close()
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	events, external := make(chan keyboardEvent, 128), make(chan tea.Msg, 1)
	stop := make(chan struct{})
	var output bytes.Buffer
	program := tea.NewProgram(keyboardProgramProbe{inner: *InitialModel(room), events: events, external: external, stop: stop}, tea.WithInput(input), tea.WithOutput(&output))
	finished := make(chan error, 1)
	go func() {
		final, err := RunProgram(program)
		if probe, ok := final.(keyboardProgramProbe); ok {
			probe.inner.Close()
		}
		close(stop)
		finished <- err
	}()
	t.Cleanup(func() {
		writer.Close()
		input.Close()
		select {
		case <-stop:
		case <-time.After(9 * time.Second):
			t.Error("keyboard program did not stop")
		}
	})
	wait := func(match func(keyboardEvent) bool) keyboardEvent {
		t.Helper()
		deadline := time.After(3 * time.Second)
		var observed []keyboardEvent
		for {
			select {
			case event := <-events:
				observed = append(observed, event)
				if match(event) {
					return event
				}
			case err := <-finished:
				t.Fatalf("program exited early: %v", err)
			case <-deadline:
				t.Fatalf("raw keyboard event did not reach the expected UI state; observed=%+v", observed)
			}
		}
	}
	write := func(keys string) {
		t.Helper()
		if _, err := io.WriteString(writer, keys); err != nil {
			t.Fatal(err)
		}
	}
	wait(func(e keyboardEvent) bool { return e.kind == "resize" && e.width == 80 })
	write("\r")
	if e := wait(func(e keyboardEvent) bool { return e.kind == "key" && e.key == "enter" }); !e.input || e.sending {
		t.Fatal("raw Enter did not start input")
	}
	write("hello")
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.draft == "hello" })
	write("\x1bOQ") // actual F2 escape sequence
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.key == "f2" && e.panel })
	external <- windowSizePollMsg{room: room, size: tea.WindowSizeMsg{Width: 120, Height: 30}}
	wait(func(e keyboardEvent) bool { return e.kind == "resize" && e.width == 120 && e.panel })
	write("\x01\x0b2") // Ctrl+A, Ctrl+K, 2: replace default room field
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.defaultField == "2" })
	write("\t\x01\x0b2, 3")
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.roomListField == "2, 3" })
	write("\x13") // Ctrl+S
	if e := wait(func(e keyboardEvent) bool { return e.kind == "save" }); !strings.Contains(e.status, "保存成功") || e.room != 1 || e.draft != "hello" {
		t.Fatalf("bad save: %+v", e)
	}
	write("\x1b")
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.key == "esc" && !e.panel && e.input })
	write("\x0e") // Ctrl+N while still focused on the input field
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.key == "ctrl+n" && e.switching })
	wait(func(e keyboardEvent) bool {
		return e.kind == "room" && e.room == 2 && e.draft == "hello" && e.input
	})
	wait(func(e keyboardEvent) bool { return e.kind == "receive" && e.content == "room-2" })
	write("\r")
	if e := wait(func(e keyboardEvent) bool { return e.kind == "send" }); e.room != 2 || e.draft != "" || e.status != "发送成功" {
		t.Fatalf("bad send: %+v", e)
	}
	select {
	case post := <-fixture.posts:
		if post.room != "2" || post.content != "hello" {
			t.Fatalf("wrong room/draft posted: %+v", post)
		}
	case <-time.After(time.Second):
		t.Fatal("no mocked HTTP send")
	}
	write("\x1b[17~") // F6 fallback, also while typing
	wait(func(e keyboardEvent) bool { return e.kind == "room" && e.room == 3 && e.input })
	wait(func(e keyboardEvent) bool { return e.kind == "receive" && e.content == "room-3" })
	// Alt+F must be decoded from the real ESC-prefixed sequence, open the page of
	// the room currently subscribed to, and leave the draft/focus alone. Assert on
	// the one event that carries the key: wait() consumes it from the channel.
	write("\x1bf")
	if e := wait(func(e keyboardEvent) bool { return e.kind == "key" && e.key == "alt+f" }); !strings.Contains(e.status, "浏览器") || !e.input || e.draft != "" {
		t.Fatalf("alt+f gave no feedback or changed focus/draft: %+v", e)
	}
	if urls := openedRoomPages(); len(urls) != 1 || urls[0] != "https://live.bilibili.com/3" {
		t.Fatalf("alt+f opened %v, want the current room page", urls)
	}
	write("\t")
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.key == "tab" && !e.input })
	write("\n") // LF/ctrl+j also enters the field
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.key == "ctrl+j" && e.input && !e.sending })
	write("again")
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.draft == "again" })
	write("\n")
	wait(func(e keyboardEvent) bool {
		return e.kind == "send" && e.room == 3 && e.draft == "" && e.status == "发送成功"
	})
	select {
	case post := <-fixture.posts:
		if post.room != "3" || post.content != "again" {
			t.Fatalf("wrong F6/LF send: %+v", post)
		}
	case <-time.After(time.Second):
		t.Fatal("no F6/LF HTTP send")
	}
	write("\x1bOQ")
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.panel && e.key == "f2" })
	write("\x1b[Z\x1b[Z") // Shift+Tab twice: cancel -> save-and-enter
	wait(func(e keyboardEvent) bool { return e.kind == "key" && e.panel && e.cursor == 13 })
	write("\r")
	wait(func(e keyboardEvent) bool { return e.kind == "save" && !e.panel && e.switching })
	wait(func(e keyboardEvent) bool { return e.kind == "room" && e.room == 2 && e.input })
	wait(func(e keyboardEvent) bool { return e.kind == "receive" && e.content == "room-2" })
	write("\x03")
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl+C did not quit")
	}
}
