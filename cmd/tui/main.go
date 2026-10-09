package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/internal/tui"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"os"
)

func main() {
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
