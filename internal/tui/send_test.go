package tui

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("response interrupted") }

func sendTestRoom(body string, status int) *api.LiveRoom {
	property := &api.UserRoomProperty{}
	property.Danmu.Length = 30
	return &api.LiveRoom{RoomID: 123, CSRF: "token", RoomUserInfo: property, DoneChan: make(chan struct{}), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func TestPostDanmuResponses(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		wantErr    bool
		reply      string
	}{
		{"accepted", `{"code":0,"msg":"","message":"0","data":{}}`, 200, false, ""},
		{"success text", `{"code":0,"message":"success"}`, 200, false, ""},
		{"API status text", `{"code":0,"msg":"f"}`, 200, false, "f"},
		{"business failure", `{"code":-101,"message":"请登录"}`, 200, true, ""},
		{"HTTP failure", `{"code":0}`, 500, true, ""},
		{"empty result", `{}`, 200, true, ""},
		{"null code", `{"code":null}`, 200, true, ""},
		{"string code", `{"code":"0"}`, 200, true, ""},
		{"invalid JSON", `not JSON`, 200, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := postDanmu(sendTestRoom(tc.body, tc.status), "hello")
			if (err != nil) != tc.wantErr || reply != tc.reply {
				t.Fatalf("reply=%q err=%v", reply, err)
			}
		})
	}
}

func TestPostDanmuReadFailureIsNotSuccess(t *testing.T) {
	room := sendTestRoom("", 200)
	room.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"code":0}`), failingReader{}))}, nil
	})
	if _, err := postDanmu(room, "hello"); err == nil || !strings.Contains(err.Error(), "response interrupted") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestPostDanmuFormAndTimeout(t *testing.T) {
	room := sendTestRoom("", 200)
	room.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if _, ok := req.Context().Deadline(); !ok {
			t.Fatal("send request has no timeout")
		}
		if err := req.ParseMultipartForm(1024 * 1024); err != nil {
			t.Fatal(err)
		}
		for key, expected := range map[string]string{"msg": "测试 &= 😀", "csrf": "token", "csrf_token": "token", "roomid": "123"} {
			if actual := req.FormValue(key); actual != expected {
				t.Fatalf("%s=%q expected %q", key, actual, expected)
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})
	if _, err := postDanmu(room, "测试 &= 😀"); err != nil {
		t.Fatal(err)
	}
}

func TestPostDanmuMissingCredentialsDoNotSend(t *testing.T) {
	for _, guest := range []bool{true, false} {
		room := sendTestRoom("", 200)
		if guest {
			room.RoomUserInfo = nil
		} else {
			room.CSRF = ""
		}
		room.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("request sent without credentials")
			return nil, nil
		})
		if _, err := postDanmu(room, "hello"); err == nil {
			t.Fatal("expected login error")
		}
	}
}

func TestSendResultRetainsFailedDraft(t *testing.T) {
	room := sendTestRoom("", 200)
	m := InitialModel(room)
	m.textInput.SetValue("draft")
	m.sending = true
	updated, _ := m.Update(sendResultMsg{room: room, content: "draft", err: errors.New("blocked")})
	got := updated.(model)
	if got.textInput.Value() != "draft" || got.sending || got.sendStatus != "blocked" {
		t.Fatalf("failed send discarded draft or status: %+v", got)
	}
	updated, _ = got.Update(sendResultMsg{room: room, content: "draft", reply: "f"})
	got = updated.(model)
	if got.textInput.Value() != "draft" || !strings.Contains(got.sendStatus, "f") {
		t.Fatal("API hint was hidden")
	}
	updated, _ = got.Update(sendResultMsg{room: room, content: "draft"})
	if updated.(model).textInput.Value() != "" {
		t.Fatal("accepted draft not cleared")
	}
}

func TestSendResultDoesNotOverwriteNewDraftOrNewRoom(t *testing.T) {
	room := sendTestRoom("", 200)
	m := InitialModel(room)
	m.textInput.SetValue("new draft")
	updated, _ := m.Update(sendResultMsg{room: room, content: "old draft"})
	if updated.(model).textInput.Value() != "new draft" {
		t.Fatal("cleared a newer draft")
	}
	other := sendTestRoom("", 200)
	m.sending = true
	updated, _ = m.Update(sendResultMsg{room: other, content: "new draft"})
	if !updated.(model).sending || updated.(model).textInput.Value() != "new draft" {
		t.Fatal("previous room reply changed current state")
	}
}

func TestEnterRetainsDraftUntilResultAndPreventsDuplicateSend(t *testing.T) {
	m := InitialModel(sendTestRoom(`{"code":0}`, 200))
	m.state = inputView
	m.textInput.SetValue("draft")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	if cmd == nil || !got.sending || got.textInput.Value() != "draft" {
		t.Fatal("draft cleared before send result")
	}
	updated, duplicate := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.(model).sending || duplicate != nil {
		t.Fatal("duplicate send command emitted while request is in flight")
	}
}

func TestClosedAndInFlightSendCancellation(t *testing.T) {
	room := sendTestRoom("", 200)
	room.Close()
	room.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("closed room sent a request"); return nil, nil })
	if _, err := postDanmu(room, "draft"); err == nil {
		t.Fatal("closed room accepted a send")
	}

	room = sendTestRoom("", 200)
	started := make(chan struct{})
	room.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	finished := make(chan error, 1)
	go func() { _, err := postDanmu(room, "draft"); finished <- err }()
	<-started
	room.Close()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled send reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing room did not cancel send")
	}
}

func TestUserDanmuLengthAndCustomClientTimeout(t *testing.T) {
	room := sendTestRoom("", 200)
	m := InitialModel(room)
	if m.textInput.CharLimit != 30 {
		t.Fatalf("ignored account danmu length: %d", m.textInput.CharLimit)
	}
	if GetCustomHttpClient().Timeout <= 0 {
		t.Fatal("HTTP client has no timeout")
	}
}

func TestDanmuVariantsAndOptionalNameColor(t *testing.T) {
	for _, cmd := range []string{"DANMU_MSG", "DANMU_MSG:4:0:2:2:2:0"} {
		if !isDanmuCommand(cmd) {
			t.Fatal("danmu variant rejected")
		}
	}
	if isDanmuCommand("DANMU_MSG_OTHER") {
		t.Fatal("unrelated command accepted")
	}
	for _, user := range [][]interface{}{{float64(123), "name"}, {float64(123), "name", nil, nil, nil, nil, nil, nil}} {
		msg := &api.DanmuMessage{Info: []interface{}{
			[]interface{}{nil, nil, nil, float64(16777215), float64(1700000000000)},
			"hello", user, []interface{}{},
		}}
		danmu := processDanmuMsg(msg)
		if danmu == nil || danmu.content != "hello" || danmu.uName != "name" {
			t.Fatal("optional username color discarded message")
		}
	}
}
