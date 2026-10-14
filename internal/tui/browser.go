package tui

import (
	"fmt"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
)

type browserOpened struct {
	room *api.LiveRoom
	err  error
}

// Only construct URLs from the active room ID, never from editable text.
func roomURL(room *api.LiveRoom) string {
	if room == nil {
		return ""
	}
	id := room.RoomID
	if id == 0 {
		id = room.ShortID
	}
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("https://live.bilibili.com/%d", id)
}

func browserCommand(goos, url string) (*exec.Cmd, error) {
	switch goos {
	case "windows":
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url), nil
	case "darwin":
		return exec.Command("open", url), nil
	case "linux":
		return exec.Command("xdg-open", url), nil
	default:
		return nil, fmt.Errorf("不支持的系统：%s", goos)
	}
}

func openBrowser(url string) error {
	cmd, err := browserCommand(runtime.GOOS, url)
	if err != nil {
		return err
	}
	// Run inside a tea.Cmd: do not block the UI, and reap the launcher process.
	return cmd.Run()
}

func (m model) openCurrentRoom() (tea.Model, tea.Cmd) {
	url := roomURL(m.room)
	if url == "" {
		cmd := m.showStatus("当前没有可打开的直播间")
		return m, cmd
	}
	room, open := m.room, m.openURL
	return m, func() tea.Msg {
		return browserOpened{room: room, err: open(url)}
	}
}
