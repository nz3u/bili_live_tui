package live_room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/skip2/go-qrcode"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

var (
	QRCodeGenerateErr = errors.New("QRCode generate error")
	PollLoginError    = errors.New("poll login failed")
)

func QRCodeLogin(client *http.Client) (data *api.QRCodeLoginData, err error) {
	baseURL := "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	resp, err := boundedClient(client).Get(baseURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame+1))
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
	q.WriteFile(256, "login.png")
	qrStr := q.ToSmallString(false)
	data = &api.QRCodeLoginData{
		QRString: qrStr,
		QRKey:    respData.Data.QrcodeKey,
		Status:   api.QRLoginNotScan,
	}
	return
}

func PollLogin(client *http.Client, data *api.QRCodeLoginData) (cookie string, err error) {
	baseURL := "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"
	realURL := fmt.Sprintf("%s?qrcode_key=%s", baseURL, data.QRKey)
	resp, err := boundedClient(client).Get(realURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame+1))
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
	if client == nil {
		return
	}
	jar, _ := cookiejar.New(nil)
	elements := strings.Split(cookies, ";")
	var cookieSlice []*http.Cookie
	for _, element := range elements {
		element := strings.TrimSpace(element)
		nameValue := strings.SplitN(element, "=", 2)
		if len(nameValue) != 2 || strings.TrimSpace(nameValue[0]) == "" {
			continue
		}
		cookie := &http.Cookie{
			Name:   nameValue[0],
			Value:  nameValue[1],
			Path:   "/",
			Domain: ".bilibili.com",
		}
		if cookie.Valid() == nil {
			cookieSlice = append(cookieSlice, cookie)
		}
	}
	u, _ := url.Parse("https://bilibili.com")
	jar.SetCookies(u, cookieSlice)
	client.Jar = jar
}

func CheckCookieValid(client *http.Client, cookie string) bool {
	if client == nil || strings.TrimSpace(cookie) == "" {
		return false
	}
	parseCookieStr(client, cookie)
	return CheckAuth(client)
}

func CheckAuth(client *http.Client) bool {
	var data struct {
		Code *int `json:"code"`
	}
	return getJSON(context.Background(), client, "https://account.bilibili.com/site/getCoin", &data) == nil && data.Code != nil && *data.Code == 0
}

func GetUserInfo(client *http.Client) *api.UserInfo {
	var info api.UserInfo
	if err := getJSON(context.Background(), client, "https://api.bilibili.com/x/web-interface/nav", &info); err != nil || info.Code != 0 {
		return nil
	}
	return &info
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
