package live_room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"github.com/skip2/go-qrcode"
)

var (
	QRCodeGenerateErr = errors.New("QRCode generate error")
	PollLoginError    = errors.New("poll login failed")
)

// loginImageName is the QR image the login screen tells the user to scan.
const loginImageName = "login.png"

// qrImageDir resolves where login.png is written. The login screen promises a
// file in the software directory, so the directory of the running executable is
// preferred. Writing to the current working directory (the previous behaviour)
// only worked because start.bat pinned the working directory to the release
// folder; a directly launched binary may start anywhere, which made the
// advertised file appear in the wrong place or not at all.
//
// It is a variable so tests can point it at a temporary directory.
var qrImageDir = func() string {
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			return dir
		}
	}
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return "."
}

// writeLoginQRImage stores the QR code beside the executable and reports the
// absolute path. A failure is returned so the UI can say the file is missing
// instead of silently promising a file nobody can find.
func writeLoginQRImage(q *qrcode.QRCode, dir string) (string, error) {
	path := filepath.Join(dir, loginImageName)
	if err := q.WriteFile(256, path); err != nil {
		return "", err
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute, nil
	}
	return path, nil
}

func QRCodeLogin(client *http.Client) (data *api.QRCodeLoginData, err error) {
	baseURL := "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	resp, err := client.Get(baseURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	respData := new(api.QRCodeGenerateResp)
	if err = json.Unmarshal(body, respData); err != nil || respData.Code != 0 {
		err = QRCodeGenerateErr
		return
	}
	q, err := qrcode.New(respData.Data.Url, qrcode.Low)
	if err != nil {
		return
	}
	data = &api.QRCodeLoginData{
		QRString: q.ToSmallString(false),
		QRKey:    respData.Data.QrcodeKey,
		Status:   api.QRLoginNotScan,
	}
	// The on-screen QR code is the primary path; the file is a fallback for
	// terminals that cannot draw it, so a write failure must not abort login.
	path, imageErr := writeLoginQRImage(q, qrImageDir())
	if imageErr != nil {
		logging.Warnf("write login qrcode image failed, err=%v", imageErr)
		return
	}
	data.QRImagePath = path
	return
}

func PollLogin(client *http.Client, data *api.QRCodeLoginData) (cookie string, err error) {
	baseURL := "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"
	realURL := fmt.Sprintf("%s?qrcode_key=%s", baseURL, data.QRKey)
	resp, err := client.Get(realURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	var pollLogin api.PollLoginResp
	err = json.Unmarshal(body, &pollLogin)
	if err != nil || pollLogin.Code != 0 {
		err = PollLoginError
		return
	}
	data.Status = pollLogin.Data.Code
	if data.Status == api.QRLoginSuccess {
		sb := strings.Builder{}
		cookies := resp.Cookies()
		for n, oneCookie := range cookies {
			sb.WriteString(oneCookie.Name)
			sb.WriteRune('=')
			sb.WriteString(oneCookie.Value)
			if n+1 != len(cookies) {
				sb.WriteString("; ")
			}
		}
		cookie = sb.String()
	}
	return
}

func parseCookieStr(client *http.Client, cookies string) {
	jar, _ := cookiejar.New(nil)
	elements := strings.Split(cookies, ";")
	var cookieSlice []*http.Cookie
	for _, element := range elements {
		element := strings.TrimSpace(element)
		name, value, ok := strings.Cut(element, "=")
		if !ok || name == "" {
			continue
		}
		cookie := &http.Cookie{
			Name:   name,
			Value:  value,
			Path:   "/",
			Domain: ".bilibili.com",
		}
		cookieSlice = append(cookieSlice, cookie)
	}
	u, _ := url.Parse("https://bilibili.com")
	jar.SetCookies(u, cookieSlice)
	client.Jar = jar
}

func CheckCookieValid(client *http.Client, cookie string) bool {
	if cookie == "" {
		return false
	}
	parseCookieStr(client, cookie)
	return CheckAuth(client)
}

func CheckAuth(client *http.Client) bool {
	baseURL := "https://account.bilibili.com/site/getCoin"
	resp, err := client.Get(baseURL)
	if err != nil {
		return false
	}

	defer resp.Body.Close()
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	var data map[string]interface{}
	if err = json.Unmarshal(respBody, &data); err != nil {
		return false
	}
	code, ok := data["code"].(float64)
	return ok && code == 0
}

func GetUserInfo(client *http.Client) *api.UserInfo {
	info, _ := getUserInfo(client)
	return info
}

func getUserInfo(client *http.Client) (*api.UserInfo, error) {
	return getUserInfoContext(context.Background(), client)
}

func getUserInfoContext(ctx context.Context, client *http.Client) (*api.UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.bilibili.com/x/web-interface/nav", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取登录状态失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取登录状态 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var status struct {
		Code *int `json:"code"`
	}
	var info api.UserInfo
	if err := json.Unmarshal(body, &status); err != nil || status.Code == nil {
		return nil, errors.New("登录状态接口返回无效结果")
	}
	if *status.Code == -101 {
		return nil, nil // Only a definite 'not logged in' response is a guest.
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	if info.Code != 0 || info.Data.Mid == 0 {
		return nil, fmt.Errorf("获取登录状态失败 (%d): %s", info.Code, info.Message)
	}
	return &info, nil
}

func getCSRF(client *http.Client) string {
	if client == nil || client.Jar == nil {
		return ""
	}
	u, _ := url.Parse("https://api.live.bilibili.com")
	cookies := client.Jar.Cookies(u)
	for _, cookie := range cookies {
		if cookie.Name == "bili_jct" {
			return cookie.Value
		}
	}
	return ""
}
