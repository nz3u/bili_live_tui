package main

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/internal/tui"
	"github.com/shr-go/bili_live_tui/pkg/logging"
)

func main() {
	// Runs before anything can draw, and before go-runewidth's package init()
	// would have read the console code page: on a CJK console that init() makes
	// the QR code's block glyphs measure two cells while the console draws one,
	// and the code comes out unscannable. The deleted start.bat avoided this by
	// setting UTF-8 before the executable started, so the program now does the
	// same thing itself — switch the console to UTF-8 and run a second copy in
	// it. That copy's init() sees the code page start.bat used to provide.
	if tui.NeedsUTF8ConsoleRelaunch() {
		code, err := tui.RelaunchWithUTF8Console()
		if err != nil {
			logging.Errorf("relaunch in UTF-8 console failed, err=%v", err)
		} else {
			os.Exit(code)
		}
	}

	// Configure the console for UTF-8 in-process so the binary renders correctly
	// when launched directly (double-click, Explorer, or a plain shell). This
	// replaces the start.bat wrapper that only existed to set the output
	// encoding before the program started.
	restoreConsole := tui.SetupConsole()
	defer restoreConsole()

	if err := tui.LoadConfig("config.toml"); err != nil {
		logging.Fatalf("load config error, err=%v", err)
	}
	defer logging.Cleanup()
	logging.Infof("tui start")
	client := tui.GetCustomHttpClient()
	room, err := tui.PrepareEnterRoom(client)
	if err != nil || room == nil {
		logging.Fatalf("Connect server error, err=%v", err)
	}
	defer room.Close()
	m := tui.InitialModel(room)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	finalModel, err := tui.RunProgram(p)
	if closer, ok := finalModel.(interface{ Close() }); ok {
		closer.Close()
	}
	if err != nil {
		logging.Fatalf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}
