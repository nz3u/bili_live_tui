package tui

import (
	"container/list"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	"github.com/shr-go/bili_live_tui/api"
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
	// statusSeq identifies the status that owns the pending expiry tick.
	statusSeq int
	width     int
	height    int
	roomID    uint64
	switching bool
	owner     *roomOwner
	memories  map[uint64]roomMemory
	settings  *settingsModel
	openURL   func(string) error
}

func InitialModel(room *api.LiveRoom) model {
	ti := textinput.New()
	ti.CharLimit = danmuLimit(room)
	ti.Placeholder = "Tab 输入 · Enter 发送 · Alt+N 切房 · F2 设置 · F6 打开直播间"

	return model{
		danmu:      list.New(),
		room:       room,
		roomID:     LiveConfig.RoomID,
		owner:      newRoomOwner(room),
		openURL:    openBrowser,
		memories:   make(map[uint64]roomMemory),
		viewport:   viewport.Model{},
		textInput:  ti,
		ready:      false,
		lockBottom: true,
		state:      contentView,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitRoom(m.room), pollTerminal())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if poll, ok := msg.(terminalPoll); ok {
		if poll.width > 0 && poll.height > 0 && (poll.width != m.width || poll.height != m.height) {
			next, cmd := m.Update(tea.WindowSizeMsg{Width: poll.width, Height: poll.height})
			return next, tea.Batch(cmd, pollTerminal())
		}
		return m, pollTerminal()
	}
	next, cmd := m.update(msg)
	result := next.(model)
	result.layoutInputs()
	return result, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.settings != nil {
			return m.settingsKey(msg)
		}
		switch msg.String() {
		case "f2":
			if m.sending || m.switching {
				return m, nil
			}
			m.settings = newSettings(LiveConfig)
			return m, nil
		case "ctrl+c":
			return m, tea.Quit
		case "alt+n":
			return m.nextRoom()
		case "f6":
			return m.openCurrentRoom()
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
			if m.state == inputView && !m.sending && !m.switching && strings.TrimSpace(m.textInput.Value()) != "" {
				m.sending = true
				m.sendStatus = "发送中"
				return m, sendDanmu(m.room, m.textInput.Value())
			}
			return m, nil
		}
	case browserOpened:
		// A delayed launcher result must not overwrite another room or operation.
		if msg.room != m.room || m.sending || m.switching || m.settings != nil {
			return m, nil
		}
		if msg.err != nil {
			cmd = m.showStatus("打开直播间失败：" + singleLine(msg.err.Error()))
		} else {
			cmd = m.showStatus("已请求浏览器打开直播间")
		}
		return m, cmd
	case statusExpiredMsg:
		m.expireStatus(msg)
		return m, nil
	case settingsMessage:
		if m.settings != msg.form || m.settings == nil {
			return m, nil
		}
		field := &m.settings.fields[msg.field]
		*field, cmd = field.Update(msg.message)
		return m, settingsCommand(m.settings, msg.field, cmd)
	case roomOpened:
		return m.finishSwitch(msg)
	case roomEvent:
		cmds = append(cmds, m.receive(msg))
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
		m.viewport.Width = max(1, msg.Width-2*focusMarginWidth)
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
	}

	if m.lockBottom {
		m.viewport.GotoBottom()
	}

	if m.settings != nil {
		return m, tea.Batch(cmds...)
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
	if m.width < 10 || m.height < 8 {
		return boundedView(m.inputLine(), m.width, m.height)
	}
	var s string
	content := m.viewport.View()
	if m.settings != nil {
		content = m.settings.view(m.viewport.Width, m.viewport.Height)
	}
	contentStr := fmt.Sprintf("%s\n%s\n%s", m.headerView(), content, m.footerView())
	textStr := m.inputLine()
	if m.state == contentView {
		s = lipgloss.JoinVertical(lipgloss.Left, focusedStyle.Render(contentStr), unFocusedStyle.Render(textStr))
	} else {
		s = lipgloss.JoinVertical(lipgloss.Left, unFocusedStyle.Render(contentStr), focusedStyle.Render(textStr))
	}
	return boundedView(s, m.width, m.height)
}

func boundedView(s string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	for i, line := range lines {
		lines[i] = truncate.StringWithTail(line, uint(width), "")
	}
	return strings.Join(lines, "\n")
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

	header = singleLine(header)
	if m.viewport.Width < 5 {
		return truncate.StringWithTail(header, uint(max(0, m.viewport.Width)), "")
	}
	header = truncate.StringWithTail(header, uint(m.viewport.Width-4), "…")
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
