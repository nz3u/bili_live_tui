package tui

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientUsesBuiltInUserAgent(t *testing.T) {
	// Legacy UA is ignored even if it would be invalid as an HTTP header.
	path := configFixture(t, "room_id = 1\nchat_buffer = 20\nuser_agent = \"old\\nagent\"\n")
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(store.baseline), `user_agent = "old\nagent"`) {
		t.Fatal("legacy setting should be preserved as an unknown field")
	}
	client := GetCustomHttpClient()
	if client.Timeout != 15*time.Second {
		t.Fatal("lost HTTP timeout")
	}
	transport := client.Transport.(*userAgentTransport)
	transport.rt = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if ua := req.UserAgent(); !strings.HasPrefix(ua, "Mozilla/5.0") || strings.Contains(ua, "old") {
			t.Fatalf("unexpected UA %q", ua)
		}
		if req.Header.Get("X-Test") != "keep" {
			t.Fatal("lost other request headers")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	req, err := http.NewRequest(http.MethodGet, "https://live.bilibili.com/123", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "original")
	req.Header.Set("X-Test", "keep")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.UserAgent() != "original" {
		t.Fatal("transport mutated caller request")
	}
}

func TestNewConfigDoesNotWriteUserAgent(t *testing.T) {
	store, cfg, err := openConfigStore(configFixture(t, "room_id = 1\nchat_buffer = 20\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(store.baseline), "user_agent") {
		t.Fatal("removed setting was serialized")
	}
}
