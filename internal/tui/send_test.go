package tui

import (
	"errors"
	"io"
	"net/http"
	"reflect"
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

// Update returns a tea.Batch of several commands (the hint timer plus the send
// or switch work), and batchMsg is unexported in this Bubble Tea version, so walk
// the returned message reflectively to find the scheduled hint expiry.
func findHintExpiry(msg tea.Msg) (hintExpiredMsg, bool) {
	if expiry, ok := msg.(hintExpiredMsg); ok {
		return expiry, true
	}
	if msg == nil {
		return hintExpiredMsg{}, false
	}
	value := reflect.ValueOf(msg)
	if value.Kind() != reflect.Slice {
		return hintExpiredMsg{}, false
	}
	for i := 0; i < value.Len(); i++ {
		next, ok := value.Index(i).Interface().(tea.Cmd)
		if !ok {
			continue
		}
		if expiry, ok := findHintExpiry(next()); ok {
			return expiry, true
		}
	}
	return hintExpiredMsg{}, false
}

func hintExpiryFromCmd(t *testing.T, cmd tea.Cmd) hintExpiredMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command returned")
	}
	expiry, ok := findHintExpiry(cmd())
	if !ok {
		t.Fatal("no hint expiry was scheduled")
	}
	return expiry
}

func TestSendResultRetainsFailedDraft(t *testing.T) {
	room := sendTestRoom("", 200)
	m := InitialModel(room)
	m.textInput.SetValue("draft")
	m.sending = true
	updated, _ := m.Update(sendResultMsg{room: room, content: "draft", err: errors.New("blocked")})
	got := updated.(model)
	if got.textInput.Value() != "draft" || got.sending || !strings.Contains(got.sendHint, "blocked") {
		t.Fatalf("failed send discarded draft or hint: %+v", got)
	}
	updated, _ = got.Update(sendResultMsg{room: room, content: "draft", reply: "f"})
	got = updated.(model)
	if got.textInput.Value() != "draft" || !strings.Contains(got.sendHint, "f") {
		t.Fatal("API hint was hidden")
	}
	updated, _ = got.Update(sendResultMsg{room: room, content: "draft"})
	got = updated.(model)
	if got.textInput.Value() != "" || got.sendHint != "发送成功" {
		t.Fatalf("accepted draft not cleared or success not hinted: %q %q", got.textInput.Value(), got.sendHint)
	}
}

// A failure restores the rejected draft so it can be fixed and resent.
func TestFailedSendRestoresDraftIntoEmptyBox(t *testing.T) {
	room := sendTestRoom("", 200)
	m := InitialModel(room)
	m.state = inputView
	m.sending = true
	updated, _ := m.Update(sendResultMsg{room: room, content: "被拒绝的草稿", err: errors.New("发送失败 (1003212): 内容被过滤")})
	got := updated.(model)
	if got.textInput.Value() != "被拒绝的草稿" {
		t.Fatalf("failed draft was not restored: %q", got.textInput.Value())
	}
	if !strings.Contains(got.sendHint, "发送失败") || got.sending {
		t.Fatalf("failure was not reported in the send box: %+v", got)
	}
}

// User input outranks the feedback: a draft typed while the send was in flight is
// never replaced, and the hint must not disturb typing.
func TestNewInputWinsOverRestoredDraftAndHint(t *testing.T) {
	room := sendTestRoom("", 200)
	m := InitialModel(room)
	m.state = inputView
	m.textInput.Focus()
	m.sending = true
	// The user starts typing before the failure arrives.
	m.textInput.SetValue("用户新输入")
	updated, _ := m.Update(sendResultMsg{room: room, content: "旧草稿", err: errors.New("发送失败")})
	got := updated.(model)
	if got.textInput.Value() != "用户新输入" {
		t.Fatalf("restored draft replaced newer user input: %q", got.textInput.Value())
	}
	// Continued typing still works while the failure hint is displayed.
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("！")})
	got = updated.(model)
	if got.textInput.Value() != "用户新输入！" {
		t.Fatalf("hint blocked typing: %q", got.textInput.Value())
	}
	if got.textInput.Placeholder != blurredInputPlaceholder && got.textInput.Placeholder != focusedInputPlaceholder {
		t.Fatalf("hint replaced the input placeholder while a draft exists: %q", got.textInput.Placeholder)
	}
}

// Success and failure hints both expire on their own after one second.
func TestSendHintExpiresAfterOneSecond(t *testing.T) {
	room := sendTestRoom("", 200)
	for _, tc := range []struct {
		name string
		msg  sendResultMsg
	}{
		{"success", sendResultMsg{room: room, content: "draft"}},
		{"failure", sendResultMsg{room: room, content: "draft", err: errors.New("blocked")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := InitialModel(room)
			m.textInput.SetValue("draft")
			m.sending = true
			updated, cmd := m.Update(tc.msg)
			got := updated.(model)
			if got.sendHint == "" || cmd == nil {
				t.Fatalf("%s hint missing or never scheduled to expire: %+v", tc.name, got)
			}
			// The scheduled command must include the expiry timer, and applying
			// it must clear the hint.
			expiry := hintExpiryFromCmd(t, cmd)
			updated, _ = got.Update(expiry)
			if updated.(model).sendHint != "" {
				t.Fatalf("%s hint outlived its one-second window", tc.name)
			}
		})
	}
}

// A stale timer must not clear a newer hint, and another room's timer must not
// touch this room's hint.
func TestStaleHintTimerDoesNotClearNewerHint(t *testing.T) {
	room := sendTestRoom("", 200)
	m := InitialModel(room)
	m.sending = true
	updated, first := m.Update(sendResultMsg{room: room, content: "draft"})
	got := updated.(model)
	stale := hintExpiryFromCmd(t, first)

	// A newer hint arrives before the old timer fires.
	got.sending = true
	got.textInput.SetValue("draft")
	updated, _ = got.Update(sendResultMsg{room: room, content: "draft", err: errors.New("newer failure")})
	got = updated.(model)

	updated, _ = got.Update(stale)
	if updated.(model).sendHint != "newer failure" {
		t.Fatalf("stale timer cleared the newer hint: %q", updated.(model).sendHint)
	}
	// A timer from another room is ignored entirely.
	other := sendTestRoom("", 200)
	updated, _ = got.Update(hintExpiredMsg{room: other, seq: got.sendHintSeq})
	if updated.(model).sendHint == "" {
		t.Fatal("timer from another room cleared the hint")
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
