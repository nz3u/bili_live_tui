package live_room

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/pkg/logging"
)

func TestMain(m *testing.M) {
	logging.InitLogConfig()
	code := m.Run()
	logging.Cleanup()
	os.Exit(code)
}

func testRoom() *api.LiveRoom {
	return &api.LiveRoom{MessageChan: make(chan *api.DanmuMessage, 10), ReqChan: make(chan []byte, 10), DoneChan: make(chan struct{})}
}

func danmuPacket(text string) []byte {
	return packMessage([]byte(fmt.Sprintf(`{"cmd":"DANMU_MSG","info":[%q]}`, text)), api.DanmuProtolNormal, api.DanmuOpNormal, 1)
}

func awaitMessage(t *testing.T, room *api.LiveRoom, text string) {
	t.Helper()
	select {
	case msg := <-room.MessageChan:
		if len(msg.Info) != 1 || msg.Info[0] != text {
			t.Fatalf("unexpected message: %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for danmu")
	}
}

func TestUnpackCompressionAndConcatenation(t *testing.T) {
	for _, version := range []api.DanmuProtol{api.DanmuProtolNormalZlib, api.DanmuProtolNormalBrotli} {
		t.Run(strconv.Itoa(int(version)), func(t *testing.T) {
			room := testRoom()
			body := append(danmuPacket("one"), danmuPacket("two")...)
			packet := packMessage(body, version, api.DanmuOpNormal, 1)
			if int(binary.BigEndian.Uint32(packet)) != len(packet) {
				t.Fatal("compressed frame length is incorrect")
			}
			n, err := unpackMessages(room, packet, room.DoneChan, 0)
			if err != nil || int(n) != len(packet) {
				t.Fatalf("unpack: %d, %v", n, err)
			}
			awaitMessage(t, room, "one")
			awaitMessage(t, room, "two")
		})
	}
}

func TestHeaderValidationAndPartialFrames(t *testing.T) {
	room := testRoom()
	packet := danmuPacket("fragment")
	for n := 0; n < len(packet); n++ {
		consumed, err := unpackMessages(room, packet[:n], room.DoneChan, 0)
		if consumed != 0 || err != nil {
			t.Fatalf("partial length %d: %d %v", n, consumed, err)
		}
	}
	for _, size := range []uint32{0, 1, 15, maxPacketSize + 1} {
		bad := append([]byte(nil), packet...)
		binary.BigEndian.PutUint32(bad, size)
		if _, err := unpackMessages(room, bad, room.DoneChan, 0); !errors.Is(err, invalidMessageErr) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	bad := append([]byte(nil), packet...)
	binary.BigEndian.PutUint16(bad[4:6], 17)
	if _, err := unpackMessages(room, bad, room.DoneChan, 0); err == nil {
		t.Fatal("invalid header accepted")
	}
}

func TestProcessReadFragmentedAndCoalescedTCP(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	room := testRoom()
	done := make(chan error, 1)
	go func() { done <- processRead(room, client, room.DoneChan) }()
	packet := append(danmuPacket("first"), danmuPacket("second")...)
	go func() {
		defer server.Close()
		for _, segment := range [][]byte{packet[:3], packet[3:17], packet[17:]} {
			_, _ = server.Write(segment)
		}
	}()
	awaitMessage(t, room, "first")
	awaitMessage(t, room, "second")
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not exit")
	}
}

func TestAuthSplitResponseLeavesFollowingDanmu(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	serverResult := make(chan error, 1)
	go func() {
		header := make([]byte, 16)
		_, err := io.ReadFull(server, header)
		if err != nil {
			serverResult <- err
			return
		}
		body := make([]byte, int(binary.BigEndian.Uint32(header))-16)
		_, err = io.ReadFull(server, body)
		if err != nil {
			serverResult <- err
			return
		}
		response := append(packMessage([]byte(`{"code":0}`), api.DanmuProtolHeartBeat, api.DanmuOpAuthResp, 1), danmuPacket("after auth")...)
		_, err = server.Write(response[:5])
		if err == nil {
			_, err = server.Write(response[5:])
		}
		serverResult <- err
	}()
	if err := authenticateDanmu(client, 1, 2, "token"); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, len(danmuPacket("after auth")))
	if _, err := io.ReadFull(client, packet); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet, danmuPacket("after auth")) {
		t.Fatal("auth discarded following packet")
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestRoomCloseUnblocksFullMessageQueue(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	room := testRoom()
	for i := 0; i < cap(room.MessageChan); i++ {
		room.MessageChan <- &api.DanmuMessage{}
	}
	stopped := make(chan struct{})
	go func() { runConnection(room, client); close(stopped) }()
	written := make(chan error, 1)
	go func() { _, err := server.Write(danmuPacket("blocked")); written <- err }()
	// A successful net.Pipe write proves the reader has received the entire
	// packet; it must now enqueue it even if Close runs before parsing ends.
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not receive blocking packet")
	}
	room.Close()
	room.Close() // Idempotent, including while reconnecting.
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("session shutdown blocked")
	}
}

type shortWriteConn struct {
	net.Conn
	writes int
	data   []byte
}

func (c *shortWriteConn) SetWriteDeadline(time.Time) error { return nil }
func (c *shortWriteConn) Write(p []byte) (int, error) {
	c.writes++
	n := min(3, len(p))
	c.data = append(c.data, p[:n]...)
	return n, nil
}

func TestWritePacketHandlesShortWrite(t *testing.T) {
	conn := &shortWriteConn{}
	data := []byte("heartbeat packet")
	if err := writePacket(conn, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, conn.data) || conn.writes < 2 {
		t.Fatal("short writes lost bytes")
	}
}

type timeoutReadConn struct{ net.Conn }

func (c timeoutReadConn) SetReadDeadline(time.Time) error {
	return c.Conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
}

func TestIdleConnectionTriggersRecoveryNotRoomShutdown(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	room := testRoom()
	go io.Copy(io.Discard, server) // Accept outgoing heartbeats but send no replies.
	stopped := make(chan struct{})
	go func() { runConnection(room, timeoutReadConn{client}); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("idle connection was not timed out")
	}
	select {
	case <-room.DoneChan:
		t.Fatal("TCP recovery closed room DoneChan")
	default:
	}
}

func TestReconnectLoopRetriesAndCancels(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	calls := 0
	conn := reconnectLoop(context.Background(), func() (net.Conn, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("temporary error")
		}
		return client, nil
	}, time.Millisecond)
	if conn != client || calls != 3 {
		t.Fatalf("calls=%d, conn=%v", calls, conn)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if reconnectLoop(ctx, func() (net.Conn, error) { t.Fatal("dial after cancellation"); return nil, nil }, time.Hour) != nil {
		t.Fatal("expected cancellation")
	}
}

func TestCancellationDuringAuthClosesCandidate(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	info := &api.DanmuInfoResp{}
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"data":{"host_list":[{"host":"127.0.0.1","port":%s}]}}`, port)), info)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := connectDanmuServerContext(ctx, 1, 2, info); finished <- err }()
	candidate, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	_ = candidate.SetReadDeadline(time.Now().Add(3 * time.Second))
	header := make([]byte, 16)
	if _, err := io.ReadFull(candidate, header); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(io.Discard, candidate, int64(binary.BigEndian.Uint32(header))-16); err != nil {
		t.Fatal(err)
	}
	// Do not answer auth; cancelling must close this socket, not wait 10s.
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("auth result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authentication did not cancel")
	}
	if _, err := candidate.Read(make([]byte, 1)); err == nil {
		t.Fatal("cancelled candidate was left open")
	}
}

// Exercise the real supervisor with a local TCP server and offline HTTP API:
// initial receive -> disconnect -> failed info request -> reconnect -> receive.
func TestMonitorReconnectKeepsMessageChannelAlive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	var infoCalls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if infoCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"code":0,"data":{"token":"new","host_list":[{"host":"127.0.0.1","port":%s}]}}`, port)
	}))
	defer apiServer.Close()
	wbiKeysMu.Lock()
	previousKeys := wbiKeys
	wbiKeys = WbiKeys{Mixin: "test", lastUpdateTime: time.Now()}
	wbiKeysMu.Unlock()
	defer func() { wbiKeysMu.Lock(); wbiKeys = previousKeys; wbiKeysMu.Unlock() }()
	client := &http.Client{Transport: rewriteTransport{target: apiServer.URL}, Timeout: time.Second}
	info := &api.DanmuInfoResp{}
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"data":{"token":"old","host_list":[{"host":"127.0.0.1","port":%s}]}}`, port)), info); err != nil {
		t.Fatal(err)
	}
	secondDone := make(chan struct{})
	serverErrors := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				serverErrors <- err
				return
			}
			_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
			header := make([]byte, 16)
			if _, err = io.ReadFull(conn, header); err == nil {
				_, err = io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint32(header))-16)
			}
			if err == nil {
				_, err = conn.Write(packMessage([]byte(`{"code":0}`), api.DanmuProtolHeartBeat, api.DanmuOpAuthResp, 1))
			}
			if err == nil {
				_, err = conn.Write(danmuPacket(strconv.Itoa(i)))
			}
			if err != nil {
				conn.Close()
				serverErrors <- err
				return
			}
			if i == 0 {
				// Drain the initial heartbeat before closing to avoid a TCP reset
				// discarding the already-sent message on Windows.
				if _, err = io.ReadFull(conn, header); err == nil {
					_, err = io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint32(header))-16)
				}
				conn.Close()
			} else {
				<-secondDone
				conn.Close()
			}
		}
		serverErrors <- nil
	}()
	room, err := newLiveRoom(1, 2, info, client)
	if err != nil {
		t.Fatal(err)
	}
	defer room.Close()
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(secondDone) }) }
	defer releaseServer()
	originalDone := room.DoneChan
	monitorStopped := make(chan struct{})
	go func() { monitorConn(room); close(monitorStopped) }()
	awaitMessage(t, room, "0")
	// First retry waits 1s and fails; second waits 2s and succeeds.
	select {
	case message := <-room.MessageChan:
		if message.Info[0] != "1" {
			t.Fatal(message)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no messages after reconnection")
	}
	if room.DoneChan != originalDone {
		t.Fatal("room exit channel replaced")
	}
	select {
	case <-originalDone:
		t.Fatal("receiver was stopped by reconnect")
	default:
	}
	if infoCalls.Load() < 2 {
		t.Fatal("failed reconnect was not retried")
	}
	room.Close()
	select {
	case <-monitorStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not stop")
	}
	releaseServer()
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
}
