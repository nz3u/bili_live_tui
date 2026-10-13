package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
)

func TestLoginResultsAppliedInUpdate(t *testing.T) {
	m := newLoginModel(nil)
	m.step = loginStepWaitLogin
	data := &api.QRCodeLoginData{Status: api.QRLoginNotScan}
	_, cmd := m.Update(loginDataMsg{data: data})
	if m.loginData != data || cmd == nil {
		t.Fatal("QR result not applied")
	}
	_, cmd = m.Update(loginPollMsg{data: api.QRCodeLoginData{Status: api.QRLoginExpired}})
	if m.step != loginStepLoginNeedRefresh || cmd != nil {
		t.Fatal("expired QR still polling")
	}
	_, cmd = m.Update(loginRoomMsg{err: errors.New("offline")})
	if m.errText != "offline" || cmd != nil {
		t.Fatal("failure not shown")
	}
	if m.View() == "" {
		t.Fatal("missing retry view")
	}
}

func TestLoginResize(t *testing.T) {
	m := newLoginModel(nil)
	oldW, oldH := windowWidth, windowHeight
	defer func() { windowWidth, windowHeight = oldW, oldH }()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if windowWidth != 100 || windowHeight != 40 {
		t.Fatal("size not updated")
	}
}
