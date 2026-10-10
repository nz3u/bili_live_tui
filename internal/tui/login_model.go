package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"net/http"
	"os"
	"time"
)

// loginDialogPadding is the horizontal space the dialog border consumes, so a
// dialog is never allowed to occupy the entire window width.
const loginDialogPadding = 2

// loginSmallestHeight is the shortest window the QR dialog is designed for: the
// instructions plus the 21-row QR block plus the dialog border. A standard
// 80x24 console must show the whole code, otherwise the bottom rows scroll away
// and the code cannot be scanned.
const loginSmallestHeight = 24

// fitWidth clamps a requested dialog width to what the window can actually show.
//
// This matters for the QR code: lipgloss.Place is a no-op when the content is
// wider than the target, and Bubble Tea's renderer then truncates every line to
// the window width. A QR wider than the window therefore loses modules off both
// sides and stops being scannable.
func fitWidth(requested int) int {
	if available := windowWidth - loginDialogPadding; requested > available {
		requested = available
	}
	if requested < 1 {
		requested = 1
	}
	return requested
}

// wrapToWidth wraps text to at most width cells and pads each line to that width,
// keeping a block rectangular. It is CJK aware because lipgloss measures cells.
func wrapToWidth(text string, width int) string {
	return lipgloss.NewStyle().Width(max(1, width)).Align(lipgloss.Center).Render(text)
}

// qrDisplayWidth is the cell width of the QR text block.
func qrDisplayWidth(qr string) int {
	width := 0
	for _, line := range strings.Split(qr, "\n") {
		if w := lipgloss.Width(line); w > width {
			width = w
		}
	}
	return width
}

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

func (m *loginModel) loadLoginData() tea.Msg {
	loginData, err := live_room.QRCodeLogin(m.client)
	if err != nil {
		logging.Fatalf("loadLoginData failed, err=%v", err)
	}
	m.loginData = loginData
	return waitScanMsg{}
}

func tickEvery() tea.Cmd {
	return tea.Every(time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

func (m *loginModel) pollLoginStatus() tea.Msg {
	cookies, err := live_room.PollLogin(m.client, m.loginData)
	if err != nil {
		logging.Fatalf("pollLoginStatus failed, err=%v", err)
	}
	switch m.loginData.Status {
	case api.QRLoginExpired:
		m.step = loginStepLoginNeedRefresh
	case api.QRLoginSuccess:
		m.step = loginStepLoginSuccess
		m.cookies = cookies
	}
	return m.step
}

func (m *loginModel) enterRoom() tea.Msg {
	if m.chooseLogin && !m.localCookie {
		if !live_room.CheckCookieValid(m.client, m.cookies) {
			logging.Fatalf("PrepareEnterRoom cookies check failed, program exit")
		}
		os.WriteFile("COOKIE.DAT", []byte(m.cookies), 0o660)
	}

	roomID := LiveConfig.RoomID
	if roomID == 0 && len(LiveConfig.RoomIDs) > 0 {
		roomID = LiveConfig.RoomIDs[0]
	}
	if room, err := live_room.AuthAndConnect(m.client, roomID); err != nil {
		logging.Fatalf("AuthAndConnect failed, err=%v", err)
	} else {
		m.room = room
	}
	m.step = loginStepDone
	return m.step
}

func (m *loginModel) Init() tea.Cmd {
	if m.step == loginStepLoginSuccess {
		return m.enterRoom
	}
	return nil
}

func (m *loginModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Track the live window size. The login view centers dialogs with
		// Place(), which silently becomes a no-op when the content is wider than
		// the window, and the renderer then truncates the frame (which cut the
		// QR code apart).
		windowWidth, windowHeight = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
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
					return m, m.loadLoginData
				} else {
					m.step = loginStepLoginSuccess
					return m, m.enterRoom
				}
			}
		case loginStepLoginNeedRefresh:
			if msg.String() == "enter" || msg.String() == " " {
				m.step = loginStepWaitLogin
				m.loginData = nil
				return m, m.loadLoginData
			}
		}
		return m, nil
	case waitScanMsg:
		return m, tickEvery()
	case TickMsg:
		return m, m.pollLoginStatus
	case loginStep:
		if msg == loginStepWaitLogin {
			return m, tickEvery()
		} else if msg == loginStepLoginSuccess {
			return m, m.enterRoom
		} else if msg == loginStepDone {
			return m, tea.Quit
		}
	}
	return m, nil
}

// placeDialog centers a bordered dialog in the window, clamped to the real
// window size so it can never be clipped or wrap.
func (m *loginModel) placeDialog(text string, requestedWidth int) string {
	width := fitWidth(requestedWidth)
	body := lipgloss.NewStyle().Width(width).Align(lipgloss.Center).Render(text)
	return lipgloss.Place(windowWidth, windowHeight,
		lipgloss.Center, lipgloss.Center,
		dialogBoxStyle.Render(body),
		lipgloss.WithWhitespaceForeground(subtle),
	)
}

// loginInstructions is the shortest QR login hint: one line at the QR's width.
// The block is centered together with the QR code, and every extra wrapped line
// pushes the code down out of a standard 24-row console. The saved image path is
// added only when the window has room for the extra lines (see waitLoginView).
const loginInstructions = "扫描二维码完成登录"

// waitLoginView renders the QR code with its instructions.
//
// The instructions and the QR are padded to the same width on purpose.
// lipgloss.JoinVertical stretches every line to the widest line, so an
// instruction line longer than the QR (an absolute file path easily is) used to
// widen the whole block; Place then became a no-op and the renderer truncated
// each QR row at the window edge, cutting modules off both sides and making the
// code unscannable. Wrapping the text to the QR width prevents that.
func (m *loginModel) waitLoginView() string {
	// ToSmallString ends with a newline; left in place it adds a blank row to the
	// block and pushes the dialog one row too tall for a 24-row console.
	qr := strings.TrimRight(m.loginData.QRString, "\n")
	qrWidth := qrDisplayWidth(qr)
	if qrWidth < 1 {
		qrWidth = 1
	}

	// The QR itself must fit inside the window, otherwise truncation is
	// unavoidable and the code cannot be scanned. Resizing re-renders this view,
	// so the code appears as soon as the window is wide enough.
	if maxQR := fitWidth(qrWidth); qrWidth > maxQR {
		return m.placeDialog(fmt.Sprintf(
			"二维码需要 %d 列，当前窗口只有 %d 列。\n请放大窗口，或扫描已保存的 login.png。",
			qrWidth, max(1, windowWidth)), maxQR)
	}

	instructions := loginInstructions
	// Prefer the real saved path, but only when the window is tall enough for the
	// extra wrapped lines; the QR must never be pushed out of view.
	if m.loginData.QRImagePath != "" && windowHeight >= loginSmallestHeight+2 {
		instructions = "扫描二维码完成登录；二维码无法显示时请扫描 " + m.loginData.QRImagePath
	}
	body := lipgloss.JoinVertical(lipgloss.Center, wrapToWidth(instructions, qrWidth), qr)
	view := lipgloss.Place(windowWidth, windowHeight,
		lipgloss.Center, lipgloss.Center,
		dialogBoxStyle.Copy().Padding(0, 0).Render(body),
		lipgloss.WithWhitespaceForeground(subtle),
	)

	// A very short window cannot show the whole code. Say so explicitly instead
	// of letting the bottom rows scroll out of sight.
	if lipgloss.Height(view) > windowHeight {
		return m.placeDialog(fmt.Sprintf(
			"当前窗口只有 %d 行，显示二维码需要 %d 行。\n请放大窗口，或扫描已保存的 login.png。",
			max(1, windowHeight), lipgloss.Height(view)), max(1, windowWidth))
	}
	return view
}

func (m *loginModel) View() string {
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

		question := lipgloss.NewStyle().Width(fitWidth(50)).Align(lipgloss.Center).
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
			if m.loginData.Status == api.QRLoginNotConfirm {
				return m.placeDialog("请在手机上点击确定完成登录", 50)
			}
			return m.waitLoginView()
		}
	case loginStepLoginNeedRefresh:
		question := lipgloss.NewStyle().Width(fitWidth(50)).Align(lipgloss.Center).
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
		return m.placeDialog(str, 50)
	}
	return ""
}
