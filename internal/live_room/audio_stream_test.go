package live_room

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestAudioStreamsPrefersFLVAndPreservesSignatures(t *testing.T) {
	client := offlineClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xlive/web-room/v2/index/getRoomPlayInfo" {
			t.Error(r.URL.Path)
		}
		for key, want := range map[string]string{"room_id": "7777", "qn": "80", "codec": "0", "protocol": "0,1", "format": "0,1,2", "platform": "web"} {
			if got := r.URL.Query().Get(key); got != want {
				t.Errorf("%s=%s want %s", key, got, want)
			}
		}
		fmt.Fprint(w, `{"code":0,"data":{"live_status":1,"playurl_info":{"playurl":{"stream":[{"format":[
		{"format_name":"fmp4","codec":[{"codec_name":"avc","base_url":"/hls/index.m3u8?","url_info":[{"host":"https://cdn.example","extra":"sign=a%2Fb&expires=123"}]}]},
		{"format_name":"flv","codec":[
		{"codec_name":"hevc","base_url":"/skip.flv?","url_info":[{"host":"https://cdn.example","extra":""}]},
		{"codec_name":"avc","drm":true,"base_url":"/drm.flv?","url_info":[{"host":"https://cdn.example","extra":""}]},
		{"codec_name":"avc","base_url":"/live.flv?","url_info":[
		{"host":"https://cdn.example","extra":"sign=a%2Fb&expires=123"},
		{"host":"https://cdn.example","extra":"sign=a%2Fb&expires=123"},
		{"host":"https://backup.example","extra":"sign=x"},
		{"host":"file://","extra":""}]}]}]}]}}}}`)
	})
	got, err := AudioStreams(context.Background(), client, 7777)
	want := []string{"https://cdn.example/live.flv?sign=a%2Fb&expires=123", "https://backup.example/live.flv?sign=x", "https://cdn.example/hls/index.m3u8?sign=a%2Fb&expires=123"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAudioStreamsErrors(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		offline    bool
	}{
		{"offline", `{"code":0,"data":{"live_status":0}}`, 200, true},
		{"round-play", `{"code":0,"data":{"live_status":2}}`, 200, true},
		{"api-error", `{"code":-400,"message":"bad room"}`, 200, false},
		{"no-stream", `{"code":0,"data":{"live_status":1}}`, 200, false},
		{"malformed", `{"code":`, 200, false},
		{"http", `{}`, 403, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := offlineClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); fmt.Fprint(w, tt.body) })
			streams, err := AudioStreams(context.Background(), client, 1)
			if err == nil || len(streams) != 0 || errors.Is(err, ErrNotLive) != tt.offline {
				t.Fatalf("%v %v", streams, err)
			}
		})
	}
}
func TestAudioStreamsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}
	_, err := AudioStreams(ctx, client, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
