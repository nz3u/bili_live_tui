package live_room

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shr-go/bili_live_tui/api"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func offlineClient(t *testing.T, h http.HandlerFunc) *http.Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme = u.Scheme
		r.URL.Host = u.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}
func testInfo(addr net.Addr) *api.DanmuInfoResp {
	host, port, _ := net.SplitHostPort(addr.String())
	var n int
	fmt.Sscan(port, &n)
	info := new(api.DanmuInfoResp)
	info.Data.Token = "offline"
	info.Data.HostList = append(info.Data.HostList, struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		WssPort int    `json:"wss_port"`
		WsPort  int    `json:"ws_port"`
	}{Host: host, Port: n})
	return info
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}
func TestFragmentedAuth(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := l.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		if _, e = readFrame(c, time.Second); e != nil {
			return
		}
		p := packMessage([]byte(`{"code":0}`), 1, api.DanmuOpAuthResp, 1)
		for _, b := range p {
			if _, e = c.Write([]byte{b}); e != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
		// A following frame must remain untouched by authentication.
		_ = writeFrame(c, packMessage([]byte(`{"cmd":"AFTER_AUTH"}`), 0, api.DanmuOpNormal, 2))
	}()
	c, err := connectDanmuServer(0, 1, testInfo(l.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, err := readFrame(c, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(p, []byte("AFTER_AUTH")) {
		t.Fatal("auth consumed following frame")
	}
	await(t, done)
}
func TestPackets(t *testing.T) {
	room := api.NewLiveRoom()
	defer func() { room.Finish(); room.Close() }()
	raw := packMessage([]byte(`{"cmd":"TEST"}`), 0, api.DanmuOpNormal, 1)
	for _, proto := range []api.DanmuProtol{0, 2, 3} {
		p := raw
		if proto != 0 {
			p = packMessage(raw, proto, api.DanmuOpNormal, 1)
		}
		budget := maxDecoded
		if err := decodeMessages(context.Background(), room, p, 0, &budget); err != nil {
			t.Fatal(err)
		}
		if m := <-room.MessageChan; m.Cmd != "TEST" {
			t.Fatal(m)
		}
	}
	for _, size := range []uint32{0, 15, maxFrame + 1} {
		p := append([]byte(nil), raw...)
		binary.BigEndian.PutUint32(p, size)
		if _, err := parseHeader(p); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
	bad := [][]byte{raw[:8], raw[:len(raw)-1], packMessage([]byte("bad zlib"), 0, api.DanmuOpNormal, 1)}
	binary.BigEndian.PutUint16(bad[2][6:], 2)
	nested := raw
	for i := 0; i < maxDepth+1; i++ {
		nested = packMessage(nested, 2, api.DanmuOpNormal, 1)
	}
	bad = append(bad, nested)
	bomb := packMessage(bytes.Repeat([]byte("x"), maxDecoded+1), 2, api.DanmuOpNormal, 1)
	bad = append(bad, bomb)
	for i, p := range bad {
		budget := maxDecoded
		if err := decodeMessages(context.Background(), room, p, 0, &budget); err == nil {
			t.Fatalf("accepted malformed packet %d", i)
		}
	}
}
func TestReconnectAndIdempotentClose(t *testing.T) {
	wbiMu.Lock()
	wbiKeys = WbiKeys{}
	wbiMu.Unlock()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var connects, requests atomic.Int32
	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			n := connects.Add(1)
			if _, e = readFrame(c, time.Second); e != nil {
				c.Close()
				return
			}
			if n == 2 || n == 3 {
				_ = writeFrame(c, packMessage([]byte(`{"code":-1}`), 1, api.DanmuOpAuthResp, 1))
				c.Close()
				continue
			}
			_ = writeFrame(c, packMessage([]byte(`{"code":0}`), 1, api.DanmuOpAuthResp, 1))
			if n == 1 {
				c.Close()
				continue
			}
			_ = writeFrame(c, packMessage([]byte(`{"cmd":"RECOVERED"}`), 0, api.DanmuOpNormal, 1))
			_, _ = io.Copy(io.Discard, c)
			c.Close()
			return
		}
	}()
	info := testInfo(l.Addr())
	client := offlineClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprint(w, `{"code":-101,"data":{"wbi_img":{"img_url":"https://x/0123456789abcdef0123456789abcdef.png","sub_url":"https://x/abcdef0123456789abcdef0123456789.png"}}}`)
		case "/room/v1/Room/get_info":
			fmt.Fprint(w, `{"code":0,"data":{"room_id":123,"title":"offline"}}`)
		case "/site/getCoin":
			fmt.Fprint(w, `{"code":-101}`)
		case "/xlive/web-room/v1/index/getDanmuInfo":
			n := requests.Add(1)
			if n == 2 || n == 3 {
				http.Error(w, "temporary", 503)
				return
			}
			if r.URL.Query().Get("w_rid") == "" {
				t.Error("unsigned request")
			}
			fmt.Fprintf(w, `{"code":0,"data":{"token":"fresh-%d","host_list":[{"host":%q,"port":%d}]}}`, n, info.Data.HostList[0].Host, info.Data.HostList[0].Port)
		default:
			t.Errorf("unexpected HTTP %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	room, err := AuthAndConnect(client, 123)
	if err != nil {
		t.Fatal(err)
	}
	defer room.Close()
	done := room.DoneChan
	select {
	case m := <-room.MessageChan:
		if m.Cmd != "RECOVERED" {
			t.Fatal(m)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("did not recover")
	}
	if room.Title != "offline" || requests.Load() < 6 || connects.Load() != 4 {
		t.Fatalf("metadata/retries incorrect: %s %d %d", room.Title, requests.Load(), connects.Load())
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); room.Close() }()
	}
	wg.Wait()
	if room.DoneChan != done {
		t.Fatal("DoneChan replaced")
	}
	await(t, done)
	await(t, tcpDone)
	if _, ok := <-room.MessageChan; ok {
		t.Fatal("messages not closed")
	}
}
func TestHeartbeatAndCookies(t *testing.T) {
	client := offlineClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":0,"data":{"next_interval":0}}`) })
	if n := roomHeartBeatReq(client, 20, 1); n != 20 {
		t.Fatal(n)
	}
	broken := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("offline error") })}
	if n := roomHeartBeatReq(broken, 0, 1); n != 20 {
		t.Fatal(n)
	}
	parseCookieStr(client, "broken; =bad; bili_jct=abc=def; SESSDATA=one")
	if got := getCSRF(client); got != "abc=def" {
		t.Fatal(got)
	}
	if getCSRF(nil) != "" || getCSRF(&http.Client{}) != "" {
		t.Fatal("nil jar")
	}
	malformed := offlineClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":"bad"}`) })
	if CheckAuth(malformed) {
		t.Fatal("accepted malformed auth")
	}
}
func TestCloseCancelsRetryHTTP(t *testing.T) {
	a, b := net.Pipe()
	room := api.NewLiveRoom()
	started := make(chan struct{})
	room.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	go supervise(room, a)
	_ = b.Close()
	await(t, started)
	closed := make(chan struct{})
	go func() { room.Close(); close(closed) }()
	await(t, closed)
}

func TestRejectedAuthClosesSocket(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if _, err = readFrame(c, time.Second); err != nil {
			t.Error(err)
			return
		}
		_ = writeFrame(c, packMessage([]byte(`{"code":-1}`), 1, api.DanmuOpAuthResp, 1))
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		if _, err = c.Read(b[:]); err != io.EOF {
			t.Errorf("rejected socket not closed: %v", err)
		}
	}()
	if c, err := connectDanmuServer(0, 1, testInfo(l.Addr())); err == nil {
		c.Close()
		t.Fatal("accepted rejection")
	}
	await(t, done)
}

func TestCloseCancelsBlockedDelivery(t *testing.T) {
	a, b := net.Pipe()
	room := api.NewLiveRoom()
	room.Client = http.DefaultClient
	go supervise(room, a)
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		defer b.Close()
		go io.Copy(io.Discard, b)
		for i := 0; i < 100; i++ {
			if writeFrame(b, packMessage([]byte(`{"cmd":"FULL"}`), 0, api.DanmuOpNormal, 1)) != nil {
				return
			}
		}
	}()
	deadline := time.After(time.Second)
	for len(room.MessageChan) < cap(room.MessageChan) {
		select {
		case <-deadline:
			t.Fatal("buffer not filled")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	closed := make(chan struct{})
	go func() { room.Close(); close(closed) }()
	await(t, closed)
	await(t, sent)
}
