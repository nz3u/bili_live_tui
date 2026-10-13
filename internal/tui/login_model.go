package tui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
	"net/http"
	"os"
	"time"
)

type loginStep uint8

const (
	loginStepConfirmLogin loginStep = iota
	loginStepWaitLogin
	loginStepLoginNeedRefresh
	loginStepLoginSuccess
	loginStepDone
)

type loginModel struct {
	step        loginStep
	client      *http.Client
	loginData   *api.QRCodeLoginData
	room        *api.LiveRoom
	cookies     string
	chooseLogin bool
	localCookie bool
	quit        bool
	errText     string
}

func newLoginModel(client *http.Client) loginModel {
	return loginModel{
		step:        loginStepConfirmLogin,
		client:      client,
		loginData:   nil,
		room:        nil,
		cookies:     "",
		chooseLogin: true,
		localCookie: false,
		quit:        false,
	}
}

type waitScanMsg struct{}

type TickMsg time.Time

type loginDataMsg struct {
	data *api.QRCodeLoginData
	err  error
}
type loginPollMsg struct {
	data    api.QRCodeLoginData
	cookies string
	err     error
}
type loginRoomMsg struct {
	room *api.LiveRoom
	err  error
}

func (m *loginModel) loadLoginData() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		data, err := live_room.QRCodeLogin(client)
		return loginDataMsg{data, err}
	}
}

func tickEvery() tea.Cmd {
	return tea.Every(time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

func (m *loginModel) pollLoginStatus() tea.Cmd {
	client, data := m.client, *m.loginData
	return func() tea.Msg {
		cookies, err := live_room.PollLogin(client, &data)
		return loginPollMsg{data, cookies, err}
	}
}

func (m *loginModel) enterRoom() tea.Cmd {
	client, cookies, saveCookie, roomID := m.client, m.cookies, m.chooseLogin && !m.localCookie, LiveConfig.RoomID
	return func() tea.Msg {
		if saveCookie {
			if !live_room.CheckCookieValid(client, cookies) {
				return loginRoomMsg{err: fmt.Errorf("登录失效，请重启后重新登录")}
			}
			if err := os.WriteFile("COOKIE.DAT", []byte(cookies), 0o600); err != nil {
				return loginRoomMsg{err: fmt.Errorf("保存登录信息失败: %w", err)}
			}
		}
		room, err := live_room.AuthAndConnect(client, roomID)
		return loginRoomMsg{room, err}
	}
}

func (m *loginModel) Init() tea.Cmd {
	if m.step == loginStepLoginSuccess {
		return tea.Batch(m.enterRoom(), pollTerminal())
	}
	return pollTerminal()
}

func (m *loginModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case terminalPoll:
		if msg.width > 0 && msg.height > 0 {
			windowWidth, windowHeight = msg.width, msg.height
		}
		return m, pollTerminal()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
		if m.errText != "" && msg.String() == "enter" {
			m.errText = ""
			if m.step == loginStepLoginSuccess {
				return m, m.enterRoom()
			}
			m.step = loginStepWaitLogin
			return m, m.loadLoginData()
		}
		switch m.step {
		case loginStepConfirmLogin:
			switch msg.String() {
			case "tab":
				m.chooseLogin = !m.chooseLogin
			case "left":
				m.chooseLogin = true
			case "right":
				m.chooseLogin = false
			case "enter", " ":
				if m.chooseLogin {
					m.step = loginStepWaitLogin
					return m, m.loadLoginData()
				} else {
					m.step = loginStepLoginSuccess
					return m, m.enterRoom()
				}
			}
		case loginStepLoginNeedRefresh:
			if msg.String() == "enter" || msg.String() == " " {
				m.step = loginStepWaitLogin
				m.loginData = nil
				return m, m.loadLoginData()
			}
		}
		return m, nil
	case loginDataMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		m.loginData = msg.data
		return m, tickEvery()
	case TickMsg:
		if m.loginData == nil || m.step != loginStepWaitLogin {
			return m, nil
		}
		return m, m.pollLoginStatus()
	case loginPollMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		m.loginData = &msg.data
		switch msg.data.Status {
		case api.QRLoginExpired:
			m.step = loginStepLoginNeedRefresh
		case api.QRLoginSuccess:
			m.step, m.cookies = loginStepLoginSuccess, msg.cookies
			return m, m.enterRoom()
		default:
			return m, tickEvery()
		}
	case loginRoomMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		m.room, m.step = msg.room, loginStepDone
		return m, tea.Quit
	case tea.WindowSizeMsg:
		windowWidth, windowHeight = msg.Width, msg.Height
	case loginStep:
		if msg == loginStepWaitLogin {
			return m, tickEvery()
		} else if msg == loginStepLoginSuccess {
			return m, m.enterRoom()
		} else if msg == loginStepDone {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *loginModel) View() string {
	if m.errText != "" {
		return lipgloss.Place(windowWidth, windowHeight, lipgloss.Center, lipgloss.Center,
			dialogBoxStyle.Render(singleLine(m.errText)+" · Enter 重试 · Ctrl+C 退出"))
	}
	switch m.step {
	case loginStepConfirmLogin:
		var loginButton, cancelButton string
		if m.chooseLogin {
			loginButton = activeButtonStyle.Render("扫码登录")
			cancelButton = buttonStyle.Render("取消")
		} else {
			loginButton = buttonStyle.Render("扫码登录")
			cancelButton = activeButtonStyle.Render("取消")
		}

		question := lipgloss.NewStyle().Width(50).Align(lipgloss.Center).
			Render("扫码登陆后才能发送弹幕哦！")
		buttons := lipgloss.JoinHorizontal(lipgloss.Top, loginButton, "  ", cancelButton)
		ui := lipgloss.JoinVertical(lipgloss.Center, question, buttons)
		dialog := lipgloss.Place(windowWidth, windowHeight,
			lipgloss.Center, lipgloss.Center,
			dialogBoxStyle.Render(ui),
			lipgloss.WithWhitespaceForeground(subtle),
		)
		return dialog
	case loginStepWaitLogin:
		if m.loginData != nil {
			tips := "扫描下方的二维码或软件目录内的login.png完成登录"
			if m.loginData.Status == api.QRLoginNotConfirm {
				tips = "请在手机上点击确定完成登录"
			}
			ui := lipgloss.JoinVertical(lipgloss.Center, tips, m.loginData.QRString)
			dialogBoxStyleCopy := dialogBoxStyle.Copy().Padding(0, 0)
			return lipgloss.Place(windowWidth, windowHeight,
				lipgloss.Center, lipgloss.Center,
				dialogBoxStyleCopy.Render(ui),
				lipgloss.WithWhitespaceForeground(subtle),
			)
		}
	case loginStepLoginNeedRefresh:
		question := lipgloss.NewStyle().Width(50).Align(lipgloss.Center).
			Render("二维码已过期，请刷新后再试")
		confirmButton := activeButtonStyle.Render("刷新")
		ui := lipgloss.JoinVertical(lipgloss.Center, question, confirmButton)
		return lipgloss.Place(windowWidth, windowHeight,
			lipgloss.Center, lipgloss.Center,
			dialogBoxStyle.Render(ui),
			lipgloss.WithWhitespaceForeground(subtle),
		)
	case loginStepLoginSuccess:
		str := "登陆成功，正在连接服务器"
		if !m.chooseLogin {
			str = "以游客身份登录，正在连接服务器"
		}
		text := lipgloss.NewStyle().Width(50).Align(lipgloss.Center).Render(str)
		dialog := lipgloss.Place(windowWidth, windowHeight,
			lipgloss.Center, lipgloss.Center,
			dialogBoxStyle.Render(text),
			lipgloss.WithWhitespaceForeground(subtle),
		)
		return dialog
	}
	return ""
}
