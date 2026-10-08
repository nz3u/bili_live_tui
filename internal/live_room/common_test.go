package live_room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	target, _ := url.Parse(rt.target)
	copy.URL.Scheme, copy.URL.Host = target.Scheme, target.Host
	copy.Host = target.Host
	return http.DefaultTransport.RoundTrip(copy)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(text string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(text)), Header: make(http.Header)}
}

func TestCookieParsingAndCSRF(t *testing.T) {
	client := &http.Client{}
	parseCookieStr(client, "SESSDATA=a=b==; bili_jct=csrf; malformed; ;")
	u, _ := url.Parse("https://api.live.bilibili.com")
	cookies := client.Jar.Cookies(u)
	found := false
	for _, cookie := range cookies {
		if cookie.Name == "SESSDATA" {
			found = true
			if cookie.Value != "a=b==" {
				t.Fatalf("truncated cookie: %q", cookie.Value)
			}
		}
	}
	if !found || getCSRF(client) != "csrf" {
		t.Fatalf("cookies=%+v csrf=%q", cookies, getCSRF(client))
	}
	if getCSRF(&http.Client{}) != "" {
		t.Fatal("nil cookie jar must not panic")
	}
}

func TestRoomHeartbeatErrorsRetainInterval(t *testing.T) {
	for _, body := range []string{"network error", `{}`, `{"code":-1}`, `{"code":0,"data":{"next_interval":0}}`, `{"code":0,"data":{"next_interval":-2}}`, "not JSON"} {
		t.Run(body, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if body == "network error" {
					return nil, errors.New("temporary failure")
				}
				return jsonResponse(body), nil
			})}
			if next := roomHeartBeatReq(client, 20, 1); next != 20 {
				t.Fatalf("unsafe heartbeat interval %d", next)
			}
		})
	}
}

func TestAuthenticatedRoomDoesNotSilentlyDowngrade(t *testing.T) {
	wbiKeysMu.Lock()
	oldKeys := wbiKeys
	wbiKeys = WbiKeys{Mixin: "test", lastUpdateTime: time.Now()}
	wbiKeysMu.Unlock()
	defer func() { wbiKeysMu.Lock(); wbiKeys = oldKeys; wbiKeysMu.Unlock() }()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			return jsonResponse(`{"code":0,"data":{"mid":123}}`), nil
		case "/room/v1/Room/get_info":
			return jsonResponse(`{"code":0,"data":{"room_id":456}}`), nil
		case "/xlive/web-room/v1/index/getDanmuInfo":
			return jsonResponse(`{"code":0,"data":{}}`), nil
		case "/xlive/web-room/v1/index/getInfoByUser":
			return nil, errors.New("temporary property failure")
		default:
			t.Errorf("unexpected auth request: %s", r.URL.Path)
			return nil, fmt.Errorf("unexpected request")
		}
	})}
	parseCookieStr(client, "bili_jct=csrf")
	room, err := AuthAndConnect(client, 456)
	if room != nil || err == nil || !strings.Contains(err.Error(), "temporary property failure") {
		t.Fatalf("room=%v err=%v", room, err)
	}
}

func TestTransientNavFailureIsNotGuest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("temporary nav failure") })}
	room, err := AuthAndConnect(client, 1)
	if room != nil || err == nil {
		t.Fatal("temporary login check failure silently created a guest")
	}
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"code":-101,"message":"未登录"}`), nil
	})
	info, err := getUserInfo(client)
	if info != nil || err != nil {
		t.Fatalf("definite guest response: %v %v", info, err)
	}
}

func TestCancelledWBIRefresh(t *testing.T) {
	started := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var keys WbiKeys
	finished := make(chan error, 1)
	go func() { finished <- keys.updateContext(ctx, client, true) }()
	<-started
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected refresh result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WBI refresh did not cancel")
	}
}

func TestCheckAuthMalformedResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"code":null}`, `{"code":"0"}`} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(body), nil })}
		if CheckAuth(client) {
			t.Fatalf("accepted malformed auth response %s", body)
		}
	}
}

func TestGetWBIUsesReceiverAndCaches(t *testing.T) {
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	calls := 0
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(`{"code":0,"data":{"wbi_img":{"img_url":"https://example.com/0123456789abcdef0123456789abcdef.png","sub_url":"https://example.com/abcdef0123456789abcdef0123456789.png"}}}`), nil
	})
	var keys WbiKeys
	if err := keys.Update(); err != nil {
		t.Fatal(err)
	}
	if keys.Mixin == "" || keys.lastUpdateTime.IsZero() {
		t.Fatal("receiver was not updated")
	}
	u, _ := url.Parse("https://example.com/?id=3")
	if err := keys.Sign(u); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || u.Query().Get("w_rid") == "" {
		t.Fatal("key cache/signature failed")
	}
}

func FuzzParseHeader(f *testing.F) {
	f.Add(danmuPacket("seed"))
	f.Add(make([]byte, 16))
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		header, err := parseHeader(data)
		if err == nil && (header.Size < uint32(header.HeaderSize) || header.Size > maxPacketSize) {
			t.Fatalf("unsafe header: %+v", header)
		}
	})
}
