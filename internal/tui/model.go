package tui

import (
	"container/list"
	"context"
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
	"github.com/shr-go/bili_live_tui/internal/live_room"
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
	windowSize tea.WindowSizeMsg
	ready      bool
	lockBottom bool
	state      sessionState
	roomIndex  int
	switching  bool
	sending    bool
	sendStatus string
}

func InitialModel(room *api.LiveRoom) *model {
	ti := textinput.New()
	ti.CharLimit = danmuLength(room)

	return &model{
		danmu:      list.New(),
		room:       room,
		viewport:   viewport.Model{},
		textInput:  ti,
		ready:      false,
		lockBottom: true,
		state:      contentView,
	}
}

type roomChangedMsg struct {
	source  *api.LiveRoom
	room    *api.LiveRoom
	index   int
	err     error
	claimed chan struct{}
}

func (m model) Close() { m.room.Close() }

func (m model) switchRoom() tea.Cmd {
	return func() tea.Msg {
		if len(LiveConfig.RoomIDs) < 2 {
			return nil
		}
		select {
		case <-m.room.DoneChan:
			return nil
		default:
		}
		next := (m.roomIndex + 1) % len(LiveConfig.RoomIDs)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-m.room.DoneChan:
				cancel()
			case <-ctx.Done():
			}
		}()
		room, err := live_room.AuthAndConnectContext(ctx, m.room.Client, LiveConfig.RoomIDs[next])
		if err != nil {
			logging.Errorf("switch room failed, err=%v", err)
			return roomChangedMsg{source: m.room, err: err}
		}
		claimed := make(chan struct{})
		// Commands may finish after Bubble Tea exits and discards their result.
		// Until Update claims this room, the old session owns its cleanup.
		go closeUnclaimedRoom(m.room.DoneChan, claimed, room)
		return roomChangedMsg{source: m.room, room: room, index: next, claimed: claimed}
	}
}

func (m model) sendDanmu(needSend string) tea.Cmd {
	room := m.room
	return func() tea.Msg {
		reply, err := postDanmu(room, needSend)
		if err != nil {
			logging.Errorf("Send Danmu failed, err=%v", err)
		}
		return sendResultMsg{room: room, content: needSend, reply: reply, err: err}
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitForDanmu(m.room), waitForWindowResize(m.room, m.windowSize))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := LiveConfig.RoomSwitchKey
		if key == "" {
			key = "ctrl+n"
		}
		if msg.String() == key && m.state == contentView {
			if !m.switching && len(LiveConfig.RoomIDs) >= 2 {
				m.switching = true
				cmds = append(cmds, m.switchRoom())
			}
			break
		}
		switch msg.String() {
		case "ctrl+c":
			m.room.Close()
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
			if m.state == inputView && !m.sending {
				needSend := m.textInput.Value()
				if len(needSend) > 0 {
					m.sending = true
					m.sendStatus = "正在发送…"
					cmd = m.sendDanmu(needSend)
					cmds = append(cmds, cmd)
				}
			}
		}
	case windowSizePollMsg:
		if msg.room != m.room {
			break
		}
		m.windowSize = msg.size
		updated, cmd := m.Update(msg.size)
		return updated, tea.Batch(cmd, waitForWindowResize(m.room, m.windowSize))
	case tea.WindowSizeMsg:
		m.windowSize = msg
		headerHeight := lipgloss.Height(m.headerView()) + focusMarginHeight
		footerHeight := lipgloss.Height(m.footerView()) + lipgloss.Height(m.textInput.View()) + 3*focusMarginHeight
		verticalMarginHeight := headerHeight + footerHeight
		verticalMarginWidth := 2 * focusMarginWidth

		if !m.ready {
			m.viewport = viewport.New(msg.Width-verticalMarginWidth, msg.Height-verticalMarginHeight)
			m.viewport.YPosition = headerHeight
			m.viewport.HighPerformanceRendering = false
			m.viewport.SetContent(m.renderDanmu())
			m.ready = true
		} else {
			m.viewport.Width = msg.Width - verticalMarginWidth
			m.viewport.Height = msg.Height - verticalMarginHeight
		}
		textWieth := msg.Width - verticalMarginWidth - 3
		m.textInput.Placeholder = lipgloss.NewStyle().Width(textWieth).Render("Press Enter to Send")
		m.textInput.Width = textWieth
	case receivedDanmuMsg:
		if msg.room != m.room {
			break
		}
		cmds = append(cmds, waitForDanmu(m.room))
		m.danmu.PushBack(msg.danmu)
		for m.danmu.Len() > LiveConfig.ChatBuffer {
			m.danmu.Remove(m.danmu.Front())
		}
		if m.ready {
			m.viewport.SetContent(m.renderDanmu())
		}
	case sendResultMsg:
		if msg.room != m.room {
			break // A late reply from the previous room must not alter this draft.
		}
		m.sending = false
		if msg.err != nil {
			m.sendStatus = msg.err.Error()
		} else if msg.reply != "" {
			// Some API responses have code=0 but still carry a filtering hint.
			// Display it and retain the draft instead of claiming delivery.
			m.sendStatus = "发送接口回复: " + msg.reply
			logging.Warnf("send danmu API reply: %s", msg.reply)
		} else {
			m.sendStatus = "发送请求已接受"
			if m.textInput.Value() == msg.content {
				m.textInput.Reset()
			}
		}
	case roomChangedMsg:
		closed := false
		select {
		case <-m.room.DoneChan:
			closed = true
		default:
		}
		if msg.source != m.room || closed {
			if msg.claimed != nil {
				close(msg.claimed)
			}
			if msg.room != nil {
				msg.room.Close()
			}
			break
		}
		m.switching = false
		if msg.err != nil {
			m.sendStatus = "切换失败: " + msg.err.Error()
			break
		}
		close(msg.claimed)
		m.room.Close()
		m.room = msg.room
		m.sending = false
		m.sendStatus = ""
		m.textInput.CharLimit = danmuLength(msg.room)
		m.roomIndex = msg.index
		m.danmu.Init()
		cmds = append(cmds, waitForDanmu(m.room), waitForWindowResize(m.room, m.windowSize))
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

	if _, msgIsMouse := msg.(tea.MouseMsg); m.state == inputView && !msgIsMouse {
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
	textStr := m.textInput.View()
	if m.state == contentView {
		s = lipgloss.JoinVertical(lipgloss.Left, focusedStyle.Render(contentStr), unFocusedStyle.Render(textStr))
	} else {
		s = lipgloss.JoinVertical(lipgloss.Left, unFocusedStyle.Render(contentStr), focusedStyle.Render(textStr))
	}
	return s
}

type receivedDanmuMsg struct {
	room  *api.LiveRoom
	danmu *danmuMsg
}

// Subscribe through a Bubble Tea command instead of calling Program.Send from
// a goroutine: this version of Bubble Tea can block Send forever during exit.
func waitForDanmu(room *api.LiveRoom) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case <-room.DoneChan:
				return nil
			case msg := <-room.MessageChan:
				if msg == nil {
					return nil
				}
				if isDanmuCommand(msg.Cmd) {
					if danmu := processDanmuMsg(msg); danmu != nil {
						return receivedDanmuMsg{room: room, danmu: danmu}
					}
				} else if msg.Cmd == "PREPARING" {
					logging.Rotate()
				}
			}
		}
	}
}

func closeUnclaimedRoom(sourceDone, claimed <-chan struct{}, room *api.LiveRoom) {
	select {
	case <-claimed:
	case <-sourceDone:
		select {
		case <-claimed:
		default:
			room.Close()
		}
	}
}

type windowSizePollMsg struct {
	room *api.LiveRoom
	size tea.WindowSizeMsg
}

func waitForWindowResize(room *api.LiveRoom, previous tea.WindowSizeMsg) tea.Cmd {
	if runtime.GOOS != "windows" {
		return nil
	}
	return func() tea.Msg {
		width, height := previous.Width, previous.Height
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-room.DoneChan:
				return nil
			case <-ticker.C:
				nowWidth, nowHeight, err := term.GetSize(int(os.Stdout.Fd()))
				if err == nil && (width != nowWidth || height != nowHeight) {
					return windowSizePollMsg{room: room, size: tea.WindowSizeMsg{Width: nowWidth, Height: nowHeight}}
				}
			}
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
	status := lipgloss.NewStyle().MaxWidth(max(0, m.viewport.Width-lipgloss.Width(info))).Render(strings.Join(strings.Fields(m.sendStatus), " "))
	line := strings.Repeat("─", max(0, m.viewport.Width-lipgloss.Width(info)-lipgloss.Width(status)))
	return lipgloss.JoinHorizontal(lipgloss.Center, status, line, info)
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
