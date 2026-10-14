package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func expireAudio(t *testing.T, m model) model {
	t.Helper()
	next, _ := m.Update(audioNoticeExpired{seq: m.audioNoticeSeq})
	return next.(model)
}

func TestAudioStablePlaybackAndMuteUseMinimalPrompt(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	m.width = 80
	m.textInput.SetValue("保留草稿")
	next, cmd := m.Update(audioEvent{room: m.room, playing: true, text: "播放中"})
	m = next.(model)
	if cmd == nil || !strings.Contains(m.inputPrompt(78), "音频：播放中") {
		t.Fatal("full start notice missing")
	}
	m = expireAudio(t, m)
	if got := m.inputPrompt(78); got != "▶ > " {
		t.Fatalf("stable playback prompt %q", got)
	}
	for _, muted := range []bool{true, false} {
		next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
		m = next.(model)
		if cmd == nil {
			t.Fatal("toggle has no expiry")
		}
		next, cmd = m.Update(audioEvent{room: m.room, playing: true, muted: muted, muteRevision: m.audioMuteRevision})
		m = next.(model)
		want := "已取消静音"
		if muted {
			want = "已静音"
		}
		if m.audioText != want || cmd == nil || !strings.Contains(m.inputPrompt(78), want) {
			t.Fatal("full mute notice missing")
		}
		m = expireAudio(t, m)
		wantPrompt := "▶ > "
		if muted {
			wantPrompt = "> "
		}
		if got := m.inputPrompt(78); got != wantPrompt {
			t.Fatalf("mute %v prompt %q", muted, got)
		}
		if m.textInput.Value() != "保留草稿" {
			t.Fatal("notice changed draft")
		}
	}
}

func TestAudioErrorsExpireWithoutIcon(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	m.audioPlaying = true
	for _, text := range []string{"未找到 mpv，请安装或放在程序旁", "连接中", "未开播，等待开播", "获取失败，稍后重试"} {
		next, cmd := m.Update(audioEvent{room: m.room, text: text})
		m = next.(model)
		if m.audioText != text || cmd == nil || m.audioPlaying {
			t.Fatal("error state missing")
		}
		m = expireAudio(t, m)
		if got := m.inputPrompt(78); got != "> " {
			t.Fatalf("error left a persistent label: %q", got)
		}
	}
}

func TestAudioExpiryDoesNotClearNewNoticeOrSettingsStatus(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	m.showAudioNotice("旧提示")
	old := m.audioNoticeSeq
	m.showAudioNotice("已静音")
	m.showStatus("设置已保存")
	next, _ := m.Update(audioNoticeExpired{seq: old})
	m = next.(model)
	if m.audioText != "已静音" {
		t.Fatal("stale tick cleared new audio notice")
	}
	m = expireAudio(t, m)
	if m.sendStatus != "设置已保存" {
		t.Fatal("audio expiry cleared settings result")
	}
	m.showAudioNotice("连接中")
	next, _ = m.Update(statusExpiredMsg{text: "设置已保存", seq: m.statusSeq})
	m = next.(model)
	if m.audioText != "连接中" {
		t.Fatal("settings expiry cleared audio result")
	}
}

func TestAudioIconWaitsForUnmuteAcknowledgment(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	next, _ := m.toggleAudioMute()
	m = next.(model)
	next, _ = m.Update(audioEvent{room: m.room, playing: true, muted: true, muteRevision: m.audioMuteRevision})
	m = expireAudio(t, next.(model))
	next, _ = m.toggleAudioMute()
	m = expireAudio(t, next.(model))
	if m.audioLabel() != "" {
		t.Fatal("unconfirmed unmute displays playback icon")
	}
	next, _ = m.Update(audioEvent{room: m.room, playing: true, muteRevision: m.audioMuteRevision})
	m = expireAudio(t, next.(model))
	if m.audioLabel() != "▶" {
		t.Fatal("confirmed unmute has no playback icon")
	}
}

func TestAudioAndSavedNoticeBothRemainVisible(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	m.width = 80
	for _, text := range []string{"未找到 mpv，请安装或放在程序旁", "已静音", "已取消静音"} {
		m.showAudioNotice(text)
		m.showStatus("设置已保存")
		prompt := m.inputPrompt(78)
		if !strings.Contains(prompt, "设置已保存") || !strings.Contains(prompt, "音频：") {
			t.Fatalf("notice hidden: %q", prompt)
		}
		if text != "未找到 mpv，请安装或放在程序旁" && !strings.Contains(prompt, text) {
			t.Fatalf("mute notice truncated: %q", prompt)
		}
	}
}

func TestAudioNoticeTickUsesOneSecond(t *testing.T) {
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	start := time.Now()
	cmd := m.showAudioNotice("已静音")
	msg := cmd().(audioNoticeExpired)
	if elapsed := time.Since(start); elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatalf("notice duration %s", elapsed)
	}
	if msg.seq != m.audioNoticeSeq || statusTimeout != time.Second {
		t.Fatal("audio does not share settings timeout")
	}
}

func TestAudioNoticeInSettingsAndDuplicateAckDoesNotExtendIt(t *testing.T) {
	roomConfig(t)
	m := InitialModel(fakeRoom(1))
	defer m.Close()
	m.width = 100
	m.settings = newSettings(LiveConfig)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	m = next.(model)
	event := audioEvent{room: m.room, playing: true, muted: true, muteRevision: m.audioMuteRevision}
	next, _ = m.Update(event)
	m = next.(model)
	if !strings.Contains(m.inputLine(), "已静音") {
		t.Fatal("settings hides toggle notice")
	}
	seq := m.audioNoticeSeq
	next, _ = m.Update(event)
	m = next.(model)
	if m.audioNoticeSeq != seq {
		t.Fatal("duplicate ack extended one-second notice")
	}
	m = expireAudio(t, m)
	if strings.Contains(m.inputLine(), "音频") || strings.Contains(m.inputLine(), "▶") {
		t.Fatal("steady mute occupies settings row")
	}
}
