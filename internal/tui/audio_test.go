package tui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
)

type fakeAudioSession struct {
	done  chan struct{}
	mutes chan bool
	once  sync.Once
	ready func(context.Context) error
}

func newFakeAudioSession() *fakeAudioSession {
	return &fakeAudioSession{done: make(chan struct{}), mutes: make(chan bool, 16)}
}
func (s *fakeAudioSession) Ready(ctx context.Context) error {
	if s.ready != nil {
		return s.ready(ctx)
	}
	return ctx.Err()
}
func (s *fakeAudioSession) Mute(ctx context.Context, value bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.mutes <- value:
		return nil
	}
}
func (s *fakeAudioSession) Done() <-chan struct{} { return s.done }
func (s *fakeAudioSession) Close()                { s.once.Do(func() { close(s.done) }) }

func fakeAudioOwner(t *testing.T) *audioOwner {
	t.Helper()
	o := newAudioOwner()
	o.find = func(string, string) (string, error) { return "fake-player", nil }
	o.resolve = func(ctx context.Context, client *http.Client, id uint64) ([]string, error) {
		return []string{"https://cdn.example/live"}, nil
	}
	t.Cleanup(o.close)
	return o
}
func awaitAudioEvent(t *testing.T, o *audioOwner, test func(audioEvent) bool) audioEvent {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-o.events:
			if test(event) {
				return event
			}
		case <-timer.C:
			t.Fatal("audio event timed out")
			return audioEvent{}
		}
	}
}
func awaitMute(t *testing.T, s *fakeAudioSession, want bool) {
	t.Helper()
	select {
	case got := <-s.mutes:
		if got != want {
			t.Fatalf("mute %v want %v", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mute timed out")
	}
}

func TestAudioAutoStartSwitchMuteAndClose(t *testing.T) {
	o := fakeAudioOwner(t)
	first, second := newFakeAudioSession(), newFakeAudioSession()
	var launches atomic.Int32
	o.launch = func(ctx context.Context, player, address, ua string, muted bool) (audioSession, error) {
		if !muted {
			t.Error("player must start silently")
		}
		if launches.Add(1) == 1 {
			return first, nil
		}
		select {
		case <-first.done:
		default:
			t.Error("old audio overlaps new room")
		}
		return second, nil
	}
	r1, r2 := fakeRoom(1), fakeRoom(2)
	o.start(r1, "ua")
	awaitMute(t, first, false)
	awaitAudioEvent(t, o, func(e audioEvent) bool { return e.room == r1 && e.playing && !e.muted })
	o.setMuted(true)
	awaitMute(t, first, true)
	o.setRoom(r2, "ua")
	awaitMute(t, second, true)
	awaitAudioEvent(t, o, func(e audioEvent) bool { return e.room == r2 && e.playing && e.muted })
	o.setMuted(false)
	awaitMute(t, second, false)
	o.close()
	select {
	case <-second.done:
	default:
		t.Fatal("player leaked on exit")
	}
	o.setRoom(r1, "ua")
	if launches.Load() != 2 {
		t.Fatal("launched after exit")
	}
}
func TestAudioMuteDuringStartup(t *testing.T) {
	o := fakeAudioOwner(t)
	s := newFakeAudioSession()
	entered, release := make(chan struct{}), make(chan struct{})
	s.ready = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	o.launch = func(context.Context, string, string, string, bool) (audioSession, error) { return s, nil }
	o.start(fakeRoom(1), "ua")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("startup timed out")
	}
	o.setMuted(true)
	close(release)
	awaitMute(t, s, true)
}
func TestAudioOfflineAutomaticRetry(t *testing.T) {
	o := fakeAudioOwner(t)
	o.retryDelay = time.Millisecond
	var requests atomic.Int32
	o.resolve = func(context.Context, *http.Client, uint64) ([]string, error) {
		if requests.Add(1) == 1 {
			return nil, live_room.ErrNotLive
		}
		return []string{"https://cdn.example/live"}, nil
	}
	s := newFakeAudioSession()
	o.launch = func(context.Context, string, string, string, bool) (audioSession, error) { return s, nil }
	o.start(fakeRoom(1), "ua")
	awaitAudioEvent(t, o, func(e audioEvent) bool { return strings.Contains(e.text, "未开播") })
	awaitMute(t, s, false)
	if requests.Load() < 2 {
		t.Fatal("did not retry")
	}
}
func TestAudioFallbackOnBadCDN(t *testing.T) {
	o := fakeAudioOwner(t)
	o.resolve = func(context.Context, *http.Client, uint64) ([]string, error) { return []string{"bad", "good"}, nil }
	bad, good := newFakeAudioSession(), newFakeAudioSession()
	bad.ready = func(context.Context) error { return errors.New("bad CDN") }
	o.launch = func(ctx context.Context, player, address, ua string, muted bool) (audioSession, error) {
		if address == "bad" {
			return bad, nil
		}
		select {
		case <-bad.done:
		default:
			t.Error("bad player not cleaned up")
		}
		return good, nil
	}
	o.start(fakeRoom(1), "ua")
	awaitMute(t, good, false)
}
func TestAudioCloseCancelsLookupWithoutLateLaunch(t *testing.T) {
	o := fakeAudioOwner(t)
	entered := make(chan struct{})
	o.resolve = func(ctx context.Context, _ *http.Client, _ uint64) ([]string, error) {
		close(entered)
		<-ctx.Done()
		return []string{"https://cdn.example/late"}, nil
	}
	var launches atomic.Int32
	o.launch = func(context.Context, string, string, string, bool) (audioSession, error) {
		launches.Add(1)
		return newFakeAudioSession(), nil
	}
	o.start(fakeRoom(1), "ua")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("lookup timed out")
	}
	done := make(chan struct{})
	go func() { o.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("close blocked")
	}
	if launches.Load() != 0 {
		t.Fatal("late player started")
	}
}

func TestAltMMuteIsGlobalAndPreservesDraft(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	m.textInput.SetValue("草稿")
	m.sendStatus = "发送中"
	m.sending = true
	for _, state := range []sessionState{contentView, inputView} {
		m.state = state
		for _, settings := range []bool{false, true} {
			m.settings = nil
			if settings {
				m.settings = newSettings(LiveConfig)
			}
			before := m.audioMuted
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
			m = next.(model)
			if m.audioMuted == before || m.textInput.Value() != "草稿" || m.sendStatus != "发送中" || !m.sending {
				t.Fatal("Alt+M changed unrelated state")
			}
		}
	}
}
func TestStaleAudioEventsDoNotChangeCurrentRoom(t *testing.T) {
	roomConfig(t)
	r := fakeRoom(1)
	m := InitialModel(r)
	defer m.Close()
	m.audio.generation = 2
	m.audioMuted = true
	m.audioText = "当前状态"
	for _, event := range []audioEvent{
		{room: fakeRoom(2), generation: 2, text: "旧房间"},
		{room: r, generation: 1, text: "旧连接"},
		{room: r, generation: 2, text: "旧静音结果", playing: true, muted: false},
	} {
		next, cmd := m.Update(event)
		m = next.(model)
		if m.audioText != "当前状态" || cmd == nil {
			t.Fatal("stale result applied or no next subscription")
		}
	}
}
func TestAudioStatusKeepsInputRowBoundsAndDraftTail(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	m.textInput.CharLimit = 0
	m.textInput.Focus()
	m.textInput.SetValue(strings.Repeat("字", 40) + "TAIL")
	m.textInput.CursorEnd()
	m.audioText = "未找到 mpv，请安装或放在程序旁"
	m.sendStatus = "发送失败"
	for _, width := range []int{40, 80, 120} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(model)
		if !strings.Contains(m.inputLine(), "TAIL") {
			t.Fatal("audio status hides draft tail")
		}
	}
}
func TestUIAudioChangesOnlyAfterSuccessfulRoomSwitch(t *testing.T) {
	roomConfig(t)
	firstRoom, secondRoom := fakeRoom(1), fakeRoom(2)
	m := InitialModel(firstRoom)
	m.audio = fakeAudioOwner(t)
	defer m.Close()
	first, second := newFakeAudioSession(), newFakeAudioSession()
	var launches atomic.Int32
	m.audio.launch = func(context.Context, string, string, string, bool) (audioSession, error) {
		if launches.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}
	m.Init()
	awaitMute(t, first, false)
	m.owner.open = func(*http.Client, uint64) (*api.LiveRoom, error) { return nil, errors.New("offline") }
	next, cmd := m.nextRoom()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	select {
	case <-first.done:
		t.Fatal("failed switch stopped current audio")
	default:
	}
	if launches.Load() != 1 {
		t.Fatal("failed switch restarted audio")
	}
	next, _ = m.toggleAudioMute()
	m = next.(model)
	awaitMute(t, first, true)
	m.owner.open = func(*http.Client, uint64) (*api.LiveRoom, error) { return secondRoom, nil }
	next, cmd = m.nextRoom()
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	awaitMute(t, second, true)
	if m.room != secondRoom || !m.audioMuted {
		t.Fatal("switched audio lost room or mute state")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("previous room still playing")
	}
}

func TestMissingAudioPlayerIsNonfatal(t *testing.T) {
	o := fakeAudioOwner(t)
	o.find = func(string, string) (string, error) { return "", errors.New("未找到 mpv") }
	var lookups atomic.Int32
	o.resolve = func(context.Context, *http.Client, uint64) ([]string, error) { lookups.Add(1); return nil, nil }
	o.start(fakeRoom(1), "ua")
	awaitAudioEvent(t, o, func(e audioEvent) bool { return strings.Contains(e.text, "未找到 mpv") })
	if lookups.Load() != 0 {
		t.Fatal("fetched streams without a player")
	}
}

func TestAudioMuteAcknowledgmentRejectsABA(t *testing.T) {
	roomConfig(t)
	r := fakeRoom(1)
	m := InitialModel(r)
	defer m.Close()
	m.audioText = "播放中"
	old := audioEvent{room: r, playing: true, text: "旧的播放中"}
	for i := 0; i < 2; i++ {
		next, _ := m.toggleAudioMute()
		m = next.(model)
	}
	next, _ := m.Update(old)
	m = next.(model)
	if m.audioText != "切换静音中" {
		t.Fatal("old unmuted event acknowledged a newer toggle")
	}
	old.muteRevision = m.audioMuteRevision
	old.text = "播放中"
	next, _ = m.Update(old)
	m = next.(model)
	if m.audioText != "已取消静音" {
		t.Fatal("current acknowledgment rejected")
	}
}

func TestAudioReconnectStatusBeforeFallbackReady(t *testing.T) {
	o := fakeAudioOwner(t)
	o.resolve = func(context.Context, *http.Client, uint64) ([]string, error) { return []string{"first", "second"}, nil }
	first, second := newFakeAudioSession(), newFakeAudioSession()
	second.ready = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	o.launch = func(ctx context.Context, player, address, ua string, muted bool) (audioSession, error) {
		if address == "first" {
			return first, nil
		}
		return second, nil
	}
	o.start(fakeRoom(1), "ua")
	awaitAudioEvent(t, o, func(e audioEvent) bool { return e.playing })
	first.Close()
	awaitAudioEvent(t, o, func(e audioEvent) bool { return !e.playing && e.text == "连接中" })
}

func TestAudioPlayerConfiguredDirectoryOrFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "播放器 with spaces")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := "mpv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	player := filepath.Join(dir, name)
	if err := os.WriteFile(player, []byte("stub"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, configured := range []string{dir, player, filepath.Base(dir), filepath.Join(filepath.Base(dir), name)} {
		got, err := configuredAudioPlayer(configured, base)
		if err != nil || got != player {
			t.Fatalf("configured %q: %q %v", configured, got, err)
		}
	}
}

func TestAudioPlayerNeverFallsBackToPATHOrSiblings(t *testing.T) {
	base := t.TempDir()
	name := "mpv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(base, name), []byte("stub"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", base)
	for _, configured := range []string{"", "   ", "missing-player", "./missing-dir"} {
		if got, err := configuredAudioPlayer(configured, base); err == nil || got != "" {
			t.Fatalf("implicit fallback for %q: %s", configured, got)
		}
	}
	// A bare filename is a relative config path, not a PATH lookup.
	if got, err := configuredAudioPlayer(name, t.TempDir()); err == nil || got != "" {
		t.Fatalf("bare name searched PATH: %s", got)
	}
}

func TestAudioPlayerRejectsEmptyDirectoryAndNonRegularTarget(t *testing.T) {
	base := t.TempDir()
	if _, err := configuredAudioPlayer(base, base); err == nil {
		t.Fatal("empty directory accepted")
	}
	name := "mpv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.Mkdir(filepath.Join(base, name), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredAudioPlayer(base, base); err == nil {
		t.Fatal("directory named mpv accepted as executable")
	}
}

func TestAudioPlayerRejectsNonExecutableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix execute permissions")
	}
	base := t.TempDir()
	player := filepath.Join(base, "mpv")
	if err := os.WriteFile(player, []byte("stub"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredAudioPlayer(player, base); err == nil {
		t.Fatal("non-executable file accepted")
	}
}

func TestAudioProcessRejectsBareNameWithoutPATHLookup(t *testing.T) {
	if process, err := startAudioProcess(context.Background(), "mpv", nil); err == nil || process != nil {
		t.Fatal("bare player name accepted")
	}
}

func TestMPVArgumentsDisableVideoAndSeparateURL(t *testing.T) {
	url := "https://cdn.example/live?x=1&sign=a%2Fb"
	args := mpvArgs("endpoint", url, "ua", true)
	for _, flag := range []string{"--vid=no", "--sid=no", "--terminal=no", "--input-terminal=no", "--mute=yes", "--no-config", "--load-scripts=no"} {
		found := false
		for _, arg := range args {
			if arg == flag {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s", flag)
		}
	}
	if !reflect.DeepEqual(args[len(args)-2:], []string{"--", url}) {
		t.Fatal("URL not passed as a separate argument")
	}
}
