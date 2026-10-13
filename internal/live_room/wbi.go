package live_room

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The mutex protects both the shared cache and compatibility WbiKeys methods.
var wbiMu sync.Mutex
var wbiKeys WbiKeys

type WbiKeys struct {
	Img, Sub, Mixin string
	lastUpdateTime  time.Time
}

func signAndGenerateURL(s string) (string, error) {
	return signURL(context.Background(), http.DefaultClient, s)
}
func signURL(ctx context.Context, client *http.Client, s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	// Fetch outside the cache lock: cancellation never waits for another network request.
	wbiMu.Lock()
	keys := wbiKeys
	wbiMu.Unlock()
	if time.Since(keys.lastUpdateTime) >= time.Hour {
		if err = keys.fetch(ctx, client); err != nil {
			return "", err
		}
		wbiMu.Lock()
		wbiKeys = keys
		wbiMu.Unlock()
	}
	keys.sign(u)
	return u.String(), nil
}
func Sign(u *url.URL) error { return wbiKeys.Sign(u) }
func Update() error         { return wbiKeys.Update() }
func Get() (WbiKeys, error) {
	wbiMu.Lock()
	defer wbiMu.Unlock()
	if err := wbiKeys.update(false); err != nil {
		return WbiKeys{}, err
	}
	return wbiKeys, nil
}
func (wk *WbiKeys) Sign(u *url.URL) error {
	wbiMu.Lock()
	defer wbiMu.Unlock()
	if err := wk.update(false); err != nil {
		return err
	}
	wk.sign(u)
	return nil
}
func (wk *WbiKeys) sign(u *url.URL) {
	values := removeUnwantedChars(u.Query(), '!', '\'', '(', ')', '*')
	values.Del("w_rid")
	values.Set("wts", strconv.FormatInt(time.Now().Unix(), 10))
	hash := md5.Sum([]byte(values.Encode() + wk.Mixin))
	values.Set("w_rid", hex.EncodeToString(hash[:]))
	u.RawQuery = values.Encode()
}
func (wk *WbiKeys) Update() error { wbiMu.Lock(); defer wbiMu.Unlock(); return wk.update(true) }
func (wk *WbiKeys) update(force bool) error {
	if !force && time.Since(wk.lastUpdateTime) < time.Hour {
		return nil
	}
	return wk.fetch(context.Background(), http.DefaultClient)
}
func (wk *WbiKeys) fetch(ctx context.Context, client *http.Client) error {
	var nav Nav
	if err := getJSON(ctx, client, "https://api.bilibili.com/x/web-interface/nav", &nav); err != nil {
		return err
	}
	if nav.Code != 0 && nav.Code != -101 {
		return fmt.Errorf("WBI code %d", nav.Code)
	}
	key := func(s string) string {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		base := path.Base(u.Path)
		return strings.TrimSuffix(base, path.Ext(base))
	}
	img, sub := key(nav.Data.WbiImg.ImgUrl), key(nav.Data.WbiImg.SubUrl)
	if len(img) != 32 || len(sub) != 32 {
		return fmt.Errorf("invalid WBI keys")
	}
	if _, err := hex.DecodeString(img + sub); err != nil {
		return fmt.Errorf("invalid WBI keys: %w", err)
	}
	wk.Img = img
	wk.Sub = sub
	wk.mixin()
	wk.lastUpdateTime = time.Now()
	return nil
}
func (wk *WbiKeys) mixin() {
	wbi := wk.Img + wk.Sub
	if len(wbi) < 64 {
		wk.Mixin = ""
		return
	}
	var out [32]byte
	for i := range out {
		out[i] = wbi[mixinKeyEncTab[i]]
	}
	wk.Mixin = string(out[:])
}

var mixinKeyEncTab = [...]int{46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13}

func removeUnwantedChars(v url.Values, chars ...byte) url.Values {
	out := make(url.Values, len(v))
	for k, values := range v {
		for _, s := range values {
			for _, c := range chars {
				s = strings.ReplaceAll(s, string(c), "")
			}
			out[k] = append(out[k], s)
		}
	}
	return out
}

type Nav struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		WbiImg struct {
			ImgUrl string `json:"img_url"`
			SubUrl string `json:"sub_url"`
		} `json:"wbi_img"`
	} `json:"data"`
}
