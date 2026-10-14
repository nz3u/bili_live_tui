package live_room

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

var ErrNotLive = errors.New("直播间未开播")

// AudioStreams returns signed playback URLs, preferring AVC/FLV for latency.
// These are multiplexed live streams, not a guaranteed audio-only CDN resource.
// The player must disable video decoding. URLs must not be cached or logged.
func AudioStreams(ctx context.Context, client *http.Client, id uint64) ([]string, error) {
	query := url.Values{
		"room_id": {fmt.Sprint(id)}, "protocol": {"0,1"},
		"format": {"0,1,2"}, "codec": {"0"}, "qn": {"80"},
		"platform": {"web"}, "ptype": {"8"},
	}
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			LiveStatus  int `json:"live_status"`
			PlayURLInfo struct {
				PlayURL struct {
					Stream []struct {
						Format []struct {
							Name  string `json:"format_name"`
							Codec []struct {
								Name    string `json:"codec_name"`
								BaseURL string `json:"base_url"`
								DRM     bool   `json:"drm"`
								URLInfo []struct {
									Host  string `json:"host"`
									Extra string `json:"extra"`
								} `json:"url_info"`
							} `json:"codec"`
						} `json:"format"`
					} `json:"stream"`
				} `json:"playurl"`
			} `json:"playurl_info"`
		} `json:"data"`
	}
	err := getJSON(ctx, client, "https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo?"+query.Encode(), &response)
	if err != nil {
		return nil, err
	}
	if response.Code != 0 {
		return nil, fmt.Errorf("播放接口错误 %d：%s", response.Code, response.Message)
	}
	if response.Data.LiveStatus != 1 {
		return nil, ErrNotLive
	}
	var flv, hls []string
	seen := make(map[string]bool)
	for _, stream := range response.Data.PlayURLInfo.PlayURL.Stream {
		for _, format := range stream.Format {
			if format.Name != "flv" && format.Name != "ts" && format.Name != "fmp4" {
				continue
			}
			for _, codec := range format.Codec {
				if codec.Name != "avc" || codec.DRM || codec.BaseURL == "" {
					continue
				}
				for _, info := range codec.URLInfo {
					address := info.Host + codec.BaseURL + info.Extra
					u, err := url.Parse(address)
					if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || seen[address] {
						continue
					}
					seen[address] = true
					if format.Name == "flv" {
						flv = append(flv, address)
					} else {
						hls = append(hls, address)
					}
				}
			}
		}
	}
	streams := append(flv, hls...)
	if len(streams) == 0 {
		return nil, errors.New("未找到可播放的音轨流")
	}
	return streams, nil
}
