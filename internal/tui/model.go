package tui

import (
	"container/list"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"golang.org/x/term"
)

type sessionState uint

const (
	focusMarginHeight              = 1
	focusMarginWidth               = 1
	contentView       sessionState = iota
	inputView
)

type medalInfo struct {
	level      uint8
	shipLevel  uint8
	name       string
	medalColor string
}

type danmuMsg struct {
	uid          uint64
	uName        string
	chatTime     time.Time
	content      string
	medal        *medalInfo
	nameColor    string
	contentColor string
}

type model struct {
	danmu      *list.List
	room       *api.LiveRoom
	viewport   viewport.Model
	textInput  textinput.Model
	ready      bool
	lockBottom bool
	state      sessionState
	sending    bool
	sendStatus string
	width      int
	height     int
}

func InitialModel(room *api.LiveRoom) model {
	ti := textinput.New()
	ti.CharLimit = danmuLimit(room)
	ti.Placeholder = "Tab 输入 · Enter 发送"

	return model{
		danmu:      list.New(),
		room:       room,
		viewport:   viewport.Model{},
		textInput:  ti,
		ready:      false,
		lockBottom: true,
		state:      contentView,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			if m.state == contentView {
				m.state = inputView
				cmd = m.textInput.Focus()
				cmds = append(cmds, cmd)
			} else if m.state == inputView {
				m.state = contentView
				m.textInput.Blur()
			}
		case "enter":
			if m.state == inputView && !m.sending && strings.TrimSpace(m.textInput.Value()) != "" {
				m.sending = true
				m.sendStatus = "发送中"
				return m, sendDanmu(m.room, m.textInput.Value())
			}
			return m, nil
		}
	case sendResult:
		if msg.room != m.room || !m.sending {
			return m, nil
		}
		m.sending = false
		if msg.err != nil {
			m.sendStatus = msg.err.Error()
		} else {
			m.sendStatus = "已发送"
			if m.textInput.Value() == msg.text {
				m.textInput.Reset()
			}
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		headerHeight := lipgloss.Height(m.headerView()) + focusMarginHeight
		footerHeight := lipgloss.Height(m.footerView()) + lipgloss.Height(m.textInput.View()) + 3*focusMarginHeight
		verticalMarginHeight := headerHeight + footerHeight
		verticalMarginWidth := 2 * focusMarginWidth

		if !m.ready {
			m.viewport = viewport.New(max(1, msg.Width-verticalMarginWidth), max(0, msg.Height-verticalMarginHeight))
			m.viewport.YPosition = headerHeight
			m.viewport.HighPerformanceRendering = false
			m.viewport.SetContent(m.renderDanmu())
			m.ready = true
		} else {
			m.viewport.Width = max(1, msg.Width-verticalMarginWidth)
			m.viewport.Height = max(0, msg.Height-verticalMarginHeight)
		}
		m.textInput.Width = max(1, msg.Width-verticalMarginWidth-3)
		m.viewport.SetContent(m.renderDanmu())
	case *danmuMsg:
		m.danmu.PushBack(msg)
		for m.danmu.Len() > LiveConfig.ChatBuffer {
			m.danmu.Remove(m.danmu.Front())
		}
		if m.ready {
			m.viewport.SetContent(m.renderDanmu())
		}
	}

	if m.lockBottom {
		m.viewport.GotoBottom()
	}

	// if focus isn't on contentView, only mouse can be capture by viewport
	if _, msgIsMouse := msg.(tea.MouseMsg); m.state == contentView || msgIsMouse {
		scrollPercent := m.viewport.ScrollPercent()
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		newScrollPercent := m.viewport.ScrollPercent()

		if scrollPercent != newScrollPercent {
			m.lockBottom = newScrollPercent == 1
		}
	}

	if _, msgIsMouse := msg.(tea.MouseMsg); m.state == inputView && !msgIsMouse && !m.sending {
		m.textInput, cmd = m.textInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if !m.ready {
		return "\nInitializing..."
	}
	var s string
	contentStr := fmt.Sprintf("%s\n%s\n%s", m.headerView(), m.viewport.View(), m.footerView())
	textStr := m.inputLine()
	if m.state == contentView {
		s = lipgloss.JoinVertical(lipgloss.Left, focusedStyle.Render(contentStr), unFocusedStyle.Render(textStr))
	} else {
		s = lipgloss.JoinVertical(lipgloss.Left, unFocusedStyle.Render(contentStr), focusedStyle.Render(textStr))
	}
	return s
}

func ReceiveMsg(program *tea.Program, room *api.LiveRoom) {
	for msg := range room.MessageChan {
		switch msg.Cmd {
		case "DANMU_MSG": // 普通弹幕消息
			if danmu := processDanmuMsg(msg); danmu != nil {
				program.Send(danmu)
			}
		case "INTERACT_WORD": // 普通进场消息

		case "ENTRY_EFFECT": // 特效进场消息 和上面的普通进场消息存在其一

		case "PREPARING": // 直播结束，这里断一下日志
			logging.Rotate()
		}
	}
}

func PoolWindowSize(program *tea.Program) {
	if runtime.GOOS != "windows" {
		return
	}
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	for range time.Tick(20 * time.Millisecond) {
		nowWidth, nowHeight, _ := term.GetSize(int(os.Stdout.Fd()))
		if width != nowWidth || height != nowHeight {
			width = nowWidth
			height = nowHeight
			windowSize := tea.WindowSizeMsg{
				Width:  width,
				Height: height,
			}
			program.Send(windowSize)
		}
	}
}

func (m model) headerView() string {
	b := lipgloss.RoundedBorder()
	b.Right = "├"
	roomID := m.room.ShortID
	if roomID == 0 {
		roomID = m.room.RoomID
	}

	if !LiveConfig.ShowRoomTitle && !LiveConfig.ShowRoomNumber {
		return ""
	}

	var header string
	// 热度好像已经没有了，先去掉了
	if LiveConfig.ShowRoomTitle {
		if LiveConfig.ShowRoomNumber {
			header = fmt.Sprintf("%s - %d", m.room.Title, roomID)
		} else {
			header = m.room.Title
		}
	} else {
		if LiveConfig.ShowRoomNumber {
			header = fmt.Sprintf("%d", roomID)
		}
	}

	title := lipgloss.NewStyle().BorderStyle(b).Padding(0, 1).
		Render(header)
	line := strings.Repeat("─", max(0, m.viewport.Width-lipgloss.Width(title)))
	return lipgloss.JoinHorizontal(lipgloss.Center, title, line)
}

func (m model) footerView() string {
	info := lipgloss.NewStyle().Render(fmt.Sprintf("%3.f%%", m.viewport.ScrollPercent()*100))
	line := strings.Repeat("─", max(0, m.viewport.Width-lipgloss.Width(info)))
	return lipgloss.JoinHorizontal(lipgloss.Center, line, info)
}

func (m model) renderDanmu() string {
	sb := strings.Builder{}
	viewportHeight := m.viewport.Height
	for n := m.danmu.Len(); n < viewportHeight; n++ {
		sb.WriteRune('\n')
	}
	for danmuElem := m.danmu.Front(); danmuElem != nil; danmuElem = danmuElem.Next() {
		danmu, ok := danmuElem.Value.(*danmuMsg)
		if ok {
			if danmu.medal != nil {
				sb.WriteString(medalStyle(danmu.medal))
			}
			sb.WriteString(fmt.Sprintln(nameStyle(danmu.uName, danmu.nameColor),
				contentStyle(danmu.content, danmu.contentColor)))
		}
	}
	return sb.String()
}
