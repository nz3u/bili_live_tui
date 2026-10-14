package tui

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/shr-go/bili_live_tui/internal/live_room"
)

// Explicitly opt-in: normal tests never contact Bilibili or play sound.
func TestMPVLiveAudio(t *testing.T) {
	if os.Getenv("BILILIVE_AUDIO_LIVE_TEST") != "1" {
		t.Skip("opt-in real stream test")
	}
	player := os.Getenv("BILILIVE_MPV")
	if player == "" {
		var err error
		player, err = findAudioPlayer()
		if err != nil {
			t.Fatal(err)
		}
	}
	room := uint64(7777)
	if value := os.Getenv("BILILIVE_TEST_ROOM"); value != "" {
		var err error
		room, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	streams, err := live_room.AudioStreams(ctx, GetCustomHttpClient(), room)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("room %d: %d signed stream candidates (URLs redacted)", room, len(streams))
	for i, address := range streams {
		session, err := launchMPV(ctx, player, address, audioUserAgent(), true)
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Ready(ctx); err != nil {
			session.Close()
			t.Logf("candidate %d unavailable: %v", i, err)
			continue
		}
		defer session.Close()
		s := session.(*mpvSession)
		get := func(property string) json.RawMessage {
			data, err := s.request(ctx, "get_property", property)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
		t.Logf("decoded audio: %s; output: %s", get("audio-params"), get("audio-out-params"))
		if data, err := s.request(ctx, "get_property", "video-params"); err == nil && string(data) != "null" {
			t.Fatalf("video is enabled: %s", data)
		}
		var first, second float64
		if err := json.Unmarshal(get("time-pos"), &first); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if err := json.Unmarshal(get("time-pos"), &second); err != nil {
			t.Fatal(err)
		}
		if second <= first {
			t.Fatal("muted playback did not advance")
		}
		for _, muted := range []bool{false, true} {
			if err := s.Mute(ctx, muted); err != nil {
				t.Fatal(err)
			}
			var actual bool
			if err := json.Unmarshal(get("mute"), &actual); err != nil {
				t.Fatal(err)
			}
			if actual != muted {
				t.Fatalf("mute=%v, want %v", actual, muted)
			}
		}
		t.Logf("mute/unmute verified; time advances %.3f -> %.3f; no video decoded", first, second)
		return
	}
	t.Fatal("no stream candidate could decode audio")
}
