package tui

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
)

const audioRetryDelay = 15 * time.Second

type audioEvent struct {
	room         *api.LiveRoom
	generation   uint64
	text         string
	playing      bool
	muted        bool
	muteRevision uint64
}

// Shared by all model values, including the initial model's deferred Close.
// Supervisors are serialized so a canceled room can never overlap the next one.
type audioOwner struct {
	mu           sync.Mutex
	playMu       sync.Mutex
	wg           sync.WaitGroup
	closed       bool
	started      bool
	generation   uint64
	muted        bool
	muteRevision uint64
	cancel       context.CancelFunc
	controls     chan struct{}
	events       chan audioEvent
	find         func() (string, error)
	resolve      func(context.Context, *http.Client, uint64) ([]string, error)
	launch       func(context.Context, string, string, string, bool) (audioSession, error)
	retryDelay   time.Duration
}

func newAudioOwner() *audioOwner {
	return &audioOwner{
		events: make(chan audioEvent, 8), find: findAudioPlayer,
		resolve: live_room.AudioStreams, launch: launchMPV, retryDelay: audioRetryDelay,
	}
}
func (o *audioOwner) start(room *api.LiveRoom, ua string) {
	o.mu.Lock()
	if o.started || o.closed {
		o.mu.Unlock()
		return
	}
	o.started = true
	o.mu.Unlock()
	o.setRoom(room, ua)
}
func (o *audioOwner) setRoom(room *api.LiveRoom, ua string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.started || o.closed {
		return
	}
	if o.cancel != nil {
		o.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.generation++
	generation := o.generation
	controls := make(chan struct{}, 1)
	o.controls = controls
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.playMu.Lock()
		defer o.playMu.Unlock()
		if ctx.Err() == nil {
			o.run(ctx, room, ua, generation, controls)
		}
	}()
}
func (o *audioOwner) setMuted(muted bool) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.muted = muted
	o.muteRevision++
	if o.controls != nil {
		select {
		case o.controls <- struct{}{}:
		default:
		}
	}
	return o.muteRevision
}
func (o *audioOwner) desiredMute() (bool, uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.muted, o.muteRevision
}
func (o *audioOwner) accepts(event audioEvent) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.closed && event.generation == o.generation
}
func (o *audioOwner) close() {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	if o.cancel != nil {
		o.cancel()
	}
	o.mu.Unlock()
	o.wg.Wait()
	close(o.events)
}
func (o *audioOwner) publish(event audioEvent) {
	select {
	case o.events <- event:
		return
	default:
	}
	// Prefer the latest state rather than blocking playback on a busy UI.
	select {
	case <-o.events:
	default:
	}
	select {
	case o.events <- event:
	default:
	}
}
func (o *audioOwner) run(ctx context.Context, room *api.LiveRoom, ua string, generation uint64, controls <-chan struct{}) {
	report := func(text string) { o.publish(audioEvent{room: room, generation: generation, text: text}) }
	for ctx.Err() == nil {
		player, err := o.find()
		if err != nil {
			report(err.Error())
		} else {
			report("连接中")
			streams, err := o.resolve(ctx, room.Client, room.RoomID)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, live_room.ErrNotLive) {
				report("未开播，等待开播")
			} else if err != nil {
				report("获取失败，稍后重试")
			} else {
				for _, address := range streams {
					if ctx.Err() != nil {
						return
					}
					report("连接中")
					// Start silently until the current mute preference has been applied.
					session, err := o.launch(ctx, player, address, ua, true)
					if err != nil {
						continue
					}
					if session.Ready(ctx) == nil {
						o.play(ctx, room, generation, session, controls)
					}
					session.Close()
					if ctx.Err() != nil {
						return
					}
				}
				report("播放中断，稍后重试")
			}
		}
		timer := time.NewTimer(o.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-controls:
			timer.Stop() // Also lets Alt+M retry after installing mpv.
		case <-timer.C:
		}
	}
}
func (o *audioOwner) play(ctx context.Context, room *api.LiveRoom, generation uint64, session audioSession, controls <-chan struct{}) {
	for {
		muted, muteRevision := o.desiredMute()
		if err := session.Mute(ctx, muted); err != nil {
			return
		}
		text := "播放中"
		if muted {
			text = "已静音"
		}
		o.publish(audioEvent{room: room, generation: generation, text: text, playing: true, muted: muted, muteRevision: muteRevision})
		select {
		case <-ctx.Done():
			return
		case <-session.Done():
			return
		case <-controls:
		}
	}
}
func waitAudio(owner *audioOwner) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-owner.events
		if !ok {
			return nil
		}
		return event
	}
}

type audioNoticeExpired struct{ seq uint64 }

// Audio notices have their own expiry token so they never clear a send/settings
// result. After one second only confirmed, unmuted playback keeps an icon.
func (m *model) showAudioNotice(text string) tea.Cmd {
	m.audioText = text
	m.audioNoticeSeq++
	seq := m.audioNoticeSeq
	return tea.Tick(statusTimeout, func(time.Time) tea.Msg { return audioNoticeExpired{seq: seq} })
}

func (m *model) resetAudioDisplay() {
	m.audioPlaying = false
	m.audioText = ""
	m.audioNoticeSeq++
}

func (m *model) receiveAudio(event audioEvent) tea.Cmd {
	if event.room != m.room || !m.audio.accepts(event) || event.playing && (event.muted != m.audioMuted || event.muteRevision != m.audioMuteRevision) {
		return nil
	}
	changed := !m.audioPlaying || m.audioAckRevision != event.muteRevision
	m.audioPlaying = event.playing
	if !event.playing {
		return m.showAudioNotice(event.text)
	}
	m.audioAckRevision = event.muteRevision
	if !changed {
		return nil
	} // Duplicate acknowledgments must not extend the notice.
	text := "播放中"
	if event.muted {
		text = "已静音"
	} else if event.muteRevision > 0 {
		text = "已取消静音"
	}
	return m.showAudioNotice(text)
}

func (m model) audioLabel() string {
	if m.audioText != "" {
		return "音频：" + m.audioText
	}
	if m.audioPlaying && !m.audioMuted && m.audioAckRevision == m.audioMuteRevision {
		return "▶"
	}
	return ""
}

func (m model) toggleAudioMute() (tea.Model, tea.Cmd) {
	m.audioMuted = !m.audioMuted
	m.audioMuteRevision = m.audio.setMuted(m.audioMuted)
	cmd := m.showAudioNotice("切换静音中")
	return m, cmd
}
