package tui

import (
	"container/list"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
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
	danmu         *list.List
	room          *api.LiveRoom
	viewport      viewport.Model
	textInput     textinput.Model
	windowSize    tea.WindowSizeMsg
	pendingResize *tea.WindowSizeMsg
	terminalSize  *terminalSizeState
	ready         bool
	lockBottom    bool
	state         sessionState
	roomIndex     int
	roomRequest   uint64
	switching     bool
	sending       bool
	// sendHint replaces the removed danmu-area status bar: it is rendered in
	// the send box and expires on its own. sendHintSeq invalidates stale timers.
	sendHint    string
	sendHintSeq uint64
	settings    *settingsPanel
}

func InitialModel(room *api.LiveRoom) *model {
	ti := textinput.New()
	ti.CharLimit = danmuLength(room)

	return &model{
		danmu:        list.New(),
		terminalSize: &terminalSizeState{},
		room:         room,
		viewport:     viewport.Model{},
		textInput:    ti,
		ready:        false,
		lockBottom:   true,
		state:        contentView,
		roomIndex:    initialRoomIndex(room, LiveConfig),
		roomRequest:  initialRoomRequest(room, LiveConfig),
	}
}

type roomChangedMsg struct {
	source      *api.LiveRoom
	room        *api.LiveRoom
	index       int
	requestedID uint64
	err         error
	claimed     chan struct{}
}

func (m model) Close() { m.room.Close() }

func (m model) switchRoom() tea.Cmd {
	// Capture settings on the UI goroutine; a network command must not read
	// mutable LiveConfig while the settings panel applies a newer version.
	ids := configuredRooms(LiveConfig)
	if len(ids) < 2 {
		return nil
	}
	next := (roomIndexForRequest(m.room, ids, m.roomRequest) + 1) % len(ids)
	return m.switchToRoom(ids[next], next)
}

func (m model) switchToRoom(id uint64, index int) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.room.DoneChan:
			return nil
		default:
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-m.room.DoneChan:
				cancel()
			case <-ctx.Done():
			}
		}()
		room, err := live_room.AuthAndConnectContext(ctx, m.room.Client, id)
		if err != nil {
			logging.Errorf("switch room failed, err=%v", err)
			return roomChangedMsg{source: m.room, err: err}
		}
		claimed := make(chan struct{})
		// Commands may finish after Bubble Tea exits and discards their result.
		// Until Update claims this room, the old session owns its cleanup.
		go closeUnclaimedRoom(m.room.DoneChan, claimed, room)
		return roomChangedMsg{source: m.room, room: room, index: index, requestedID: id, claimed: claimed}
	}
}

// restoreFailedDraft puts a rejected draft back into the send box. User input has
// the highest priority: a draft the user typed while the request was in flight
// (or a box already holding other text) is never replaced. A failed draft
// normally survives the request anyway; this also covers a box emptied meanwhile.
func (m *model) restoreFailedDraft(content string) {
	if content == "" || m.textInput.Value() != "" {
		return
	}
	m.textInput.SetValue(content)
	m.textInput.CursorEnd()
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
	return tea.Batch(waitForDanmu(m.room), waitForWindowResize(m.room, m.terminalSize))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if handled, command := m.handleKey(msg); handled {
			return m, command
		}
	case windowSizePollMsg:
		if msg.room != m.room {
			break
		}
		// The renderer handles WindowSizeMsg before calling model.Update.
		// Recursively updating only the model leaves its clipping width stale.
		m.pendingResize = &msg.size
		return m, func() tea.Msg {
			select {
			case <-msg.room.DoneChan:
				return nil
			default:
				return msg.size
			}
		}
	case tea.WindowSizeMsg:
		// This is also the renderer's size, even if a delayed event arrived.
		// The active poll observes it and repairs any stale/invalid resize.
		m.terminalSize.store(msg)
		if msg.Width <= 0 || msg.Height <= 0 {
			break
		}
		m.windowSize = msg
		if m.pendingResize != nil && *m.pendingResize == msg {
			m.pendingResize = nil
			// Start the next poll only after this native event has been handled.
			// Otherwise rapid resizes can race the forwarded event.
			cmds = append(cmds, waitForWindowResize(m.room, m.terminalSize))
		}
		m.resizeLayout(msg)
		if m.settings != nil {
			m.settings.resize(msg)
		}
	case receivedDanmuMsg:
		if msg.room != m.room {
			break
		}
		cmds = append(cmds, waitForDanmu(m.room))
		m.danmu.PushBack(msg.danmu)
		m.trimHistory()
		if m.ready {
			m.viewport.SetContent(m.renderDanmu())
		}
	case sendResultMsg:
		if msg.room != m.room {
			break // A late reply from the previous room must not alter this draft.
		}
		m.sending = false
		switch {
		case msg.err != nil:
			// Failure: restore the failed draft so it can be fixed and resent,
			// but never clobber anything the user typed in the meantime.
			m.restoreFailedDraft(msg.content)
			cmds = append(cmds, m.showHint(msg.err.Error(), sendResultHintDuration))
		case msg.reply != "":
			// Some API responses have code=0 but still carry a filtering hint.
			// Keep the draft instead of claiming delivery.
			logging.Warnf("send danmu API reply: %s", msg.reply)
			cmds = append(cmds, m.showHint("发送接口回复: "+msg.reply, sendResultHintDuration))
		default:
			cmds = append(cmds, m.showHint("发送成功", sendResultHintDuration))
			if m.textInput.Value() == msg.content {
				m.textInput.Reset()
			}
		}
		// The branches above may have changed the draft (restored on failure,
		// cleared on success) after showHint refreshed the placeholder, so the
		// box must be refreshed again to reflect the final draft.
		m.refreshInputPlaceholder()
		m.layoutInputWidth()
	case hintExpiredMsg:
		if msg.room != m.room || msg.seq != m.sendHintSeq {
			break // Superseded by a newer hint or a different room.
		}
		m.clearHint()
	case settingsSavedMsg:
		if m.settings == nil || msg.panel != m.settings {
			break
		}
		m.settings.saving = false
		if msg.err != nil {
			m.settings.status = msg.err.Error()
			break
		}
		LiveConfig = cloneConfig(msg.config)
		applyUserAgent(m.room.Client, LiveConfig.UserAgent)
		m.roomIndex = roomIndexForRequest(m.room, configuredRooms(LiveConfig), m.roomRequest)
		m.trimHistory()
		m.resizeLayout(m.windowSize)
		m.settings.snapshot = msg.snapshot
		m.settings.setValues(LiveConfig)
		m.settings.resize(m.windowSize)
		m.settings.status = "保存成功，当前配置已应用；原文件已备份。"
		if msg.enterDefault {
			cmds = append(cmds, m.closeSettings())
			if m.room.RoomID != LiveConfig.RoomID && m.room.ShortID != LiveConfig.RoomID {
				m.switching = true
				cmds = append(cmds, m.showHint("正在进入默认直播间…", hintSticky))
				cmds = append(cmds, m.switchToRoom(LiveConfig.RoomID, 0))
			} else {
				cmds = append(cmds, m.showHint("配置已保存，当前已在默认直播间", noticeHintDuration))
			}
		} else {
			cmds = append(cmds, m.settings.focus(m.settings.cursor))
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
			cmds = append(cmds, m.showHint("切换失败: "+msg.err.Error(), noticeHintDuration))
			break
		}
		close(msg.claimed)
		m.room.Close()
		m.room = msg.room
		m.pendingResize = nil
		m.sending = false
		m.clearHint()
		m.textInput.CharLimit = danmuLength(msg.room)
		m.roomRequest = msg.requestedID
		m.roomIndex = roomIndexForRequest(msg.room, configuredRooms(LiveConfig), msg.requestedID)
		m.danmu.Init()
		m.refreshInputPlaceholder()
		cmds = append(cmds, waitForDanmu(m.room), waitForWindowResize(m.room, m.terminalSize))
		m.resizeLayout(m.windowSize)
	}

	if m.lockBottom {
		m.viewport.GotoBottom()
	}

	if m.settings != nil {
		command, _ := m.settings.update(msg, m.switching)
		cmds = append(cmds, command)
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

	if _, msgIsMouse := msg.(tea.MouseMsg); m.state == inputView && !msgIsMouse {
		m.textInput, cmd = m.textInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// View renders the chat history, the room header and the send box. The danmu
// area has no status bar row any more: notices share the send box (see hint.go).
func (m model) View() string {
	if m.settings != nil {
		return m.settings.view()
	}
	if !m.ready {
		return "\nInitializing..."
	}
	inputStr := m.inputRow()
	// header + input rows, plus one row per focus-margin border line, plus at
	// least one viewport row.
	minimumHeight := lipgloss.Height(m.headerView()) + lipgloss.Height(inputStr) + 4*focusMarginHeight + 1
	if m.windowSize.Width < 6 || m.windowSize.Height < minimumHeight {
		return lipgloss.NewStyle().MaxWidth(max(1, m.windowSize.Width)).MaxHeight(1).Render("Resize terminal")
	}
	var s string
	contentStr := fmt.Sprintf("%s\n%s", m.headerView(), m.viewport.View())
	if m.state == contentView {
		s = lipgloss.JoinVertical(lipgloss.Left, focusedStyle.Render(contentStr), unFocusedStyle.Render(inputStr))
	} else {
		s = lipgloss.JoinVertical(lipgloss.Left, unFocusedStyle.Render(contentStr), focusedStyle.Render(inputStr))
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

// Shared by the model and its active poll so a delayed native resize cannot
// leave the renderer at a stale size while the poll waits on an old snapshot.
type terminalSizeState struct{ size atomic.Uint64 }

func (s *terminalSizeState) store(size tea.WindowSizeMsg) {
	s.size.Store(uint64(uint32(size.Width))<<32 | uint64(uint32(size.Height)))
}

func (s *terminalSizeState) load() tea.WindowSizeMsg {
	size := s.size.Load()
	return tea.WindowSizeMsg{Width: int(uint32(size >> 32)), Height: int(uint32(size))}
}

func waitForWindowResize(room *api.LiveRoom, size *terminalSizeState) tea.Cmd {
	if runtime.GOOS != "windows" {
		return nil
	}
	return func() tea.Msg {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		return pollWindowResize(room, size.load, ticker.C, func() (int, int, error) {
			return term.GetSize(int(os.Stdout.Fd()))
		})
	}
}

func pollWindowResize(room *api.LiveRoom, lastSize func() tea.WindowSizeMsg, ticks <-chan time.Time, readSize func() (int, int, error)) tea.Msg {
	for {
		select {
		case <-room.DoneChan:
			return nil
		case <-ticks:
			width, height, err := readSize()
			previous := lastSize()
			if err == nil && width > 0 && height > 0 && (width != previous.Width || height != previous.Height) {
				return windowSizePollMsg{room: room, size: tea.WindowSizeMsg{Width: width, Height: height}}
			}
		}
	}
}

// headerView renders the room line. It also carries the scroll percentage that
// used to live in the removed danmu-area status bar, so no extra row is spent on
// it: the header already ends in filler that was otherwise unused.
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

	width := max(1, m.viewport.Width)
	title := lipgloss.NewStyle().BorderStyle(b).Padding(0, 1).MaxWidth(width).Render(header)
	scroll := fmt.Sprintf("%3.f%%", m.viewport.ScrollPercent()*100)
	// Only show the percentage when it fits beside the title; a cramped header
	// must never push the frame wider than the terminal.
	if lipgloss.Width(title)+lipgloss.Width(scroll) > width {
		scroll = ""
	}
	line := strings.Repeat("─", max(0, width-lipgloss.Width(title)-lipgloss.Width(scroll)))
	return lipgloss.JoinHorizontal(lipgloss.Center, title, line, scroll)
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
