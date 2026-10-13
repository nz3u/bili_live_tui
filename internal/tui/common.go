package tui

import (
	"fmt"
	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"golang.org/x/term"
	"net/http"
	"os"
	"time"
)

var (
	windowWidth  int
	windowHeight int
	LiveConfig   api.BiliLiveConfig
)

// LoadConfig is explicit so importing the UI never reads files or exits tests.
func LoadConfig(path string) error {
	windowWidth, windowHeight, _ = term.GetSize(int(os.Stdout.Fd()))
	var cfg api.BiliLiveConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.RoomID == 0 || cfg.ChatBuffer <= 0 {
		return fmt.Errorf("room_id and chat_buffer must be positive")
	}
	LiveConfig = cfg
	return nil
}

type userAgentTransport struct {
	ua string
	rt http.RoundTripper
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("User-Agent", t.ua)
	return t.rt.RoundTrip(clone)
}

func GetCustomHttpClient() (client *http.Client) {
	ua := LiveConfig.UserAgent
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
	}
	transport := &userAgentTransport{
		ua: ua,
		rt: http.DefaultTransport,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}
}

func PrepareEnterRoom(client *http.Client) (room *api.LiveRoom, err error) {
	loginModel := newLoginModel(client)
	if cookieBytes, err := os.ReadFile("COOKIE.DAT"); err == nil {
		cookies := string(cookieBytes)
		if live_room.CheckCookieValid(client, cookies) {
			loginModel.step = loginStepLoginSuccess
			loginModel.localCookie = true
		}
	}
	p := tea.NewProgram(&loginModel, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if err := p.Start(); err != nil {
		logging.Fatalf("PrepareEnterRoom ui error: %v", err)
		os.Exit(1)
	}
	if loginModel.quit {
		os.Exit(0)
	}
	return loginModel.room, nil
}
