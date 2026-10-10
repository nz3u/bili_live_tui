package tui

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/shr-go/bili_live_tui/api"
)

// roomPageURL is the web page matching the room currently subscribed to. The
// short ID is what the header shows and what live.bilibili.com serves, so it is
// preferred; the physical room ID is the fallback when no short ID is known.
func roomPageURL(room *api.LiveRoom) string {
	id := room.ShortID
	if id == 0 {
		id = room.RoomID
	}
	return fmt.Sprintf("https://live.bilibili.com/%d", id)
}

// openRoomPage is a variable so tests can observe the request without launching
// a real browser.
var openRoomPage = func(room *api.LiveRoom) error {
	return systemOpenURL(roomPageURL(room))
}

// systemOpenURL opens a URL in the user's default browser. On Windows rundll32
// uses the shell protocol handler directly: unlike `cmd /c start` it neither
// flashes a console window nor lets a command interpreter reparse the URL.
func systemOpenURL(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
