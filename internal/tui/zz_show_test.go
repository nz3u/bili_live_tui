package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/shr-go/bili_live_tui/api"
	"github.com/skip2/go-qrcode"
)

var aRE2 = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func TestZZShowQR(t *testing.T) {
	LiveConfig = defaultConfig()
	q, _ := qrcode.New("https://passport.bilibili.com/h5/appeal?qrcode_key=abcdef0123456789abcdef0123456789", qrcode.Low)
	windowWidth, windowHeight = 80, 24
	m := newLoginModel(nil)
	m.step = loginStepWaitLogin
	m.loginData = &api.QRCodeLoginData{QRString: q.ToSmallString(false), Status: api.QRLoginNotScan, QRImagePath: "login.png"}
	view := aRE2.ReplaceAllString(m.View(), "")
	fmt.Printf("--- 80x24 login view ---\n")
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		trimmed := strings.TrimRight(l, " ")
		if trimmed == "" { continue }
		fmt.Printf("%2d|%s\n", i, trimmed)
	}
	fmt.Printf("lines=%d\n", len(lines))
}
