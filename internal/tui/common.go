package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/internal/live_room"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"golang.org/x/term"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var (
	windowWidth  int
	windowHeight int
	LiveConfig   api.BiliLiveConfig
)

func init() {
	logging.InitLogConfig()
	windowWidth, windowHeight, _ = term.GetSize(int(os.Stdout.Fd()))
}

// LoadConfig is explicit so importing the UI does not require a config file
// in the working directory (including during offline regression tests).
func LoadConfig(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	snapshot, err := readConfigSnapshot(resolved)
	if err != nil {
		return err
	}
	cfg, err := decodeConfig(snapshot.raw)
	if err != nil {
		return err
	}
	LiveConfig, configFilePath = cfg, snapshot.path
	return nil
}

type userAgentTransport struct {
	ua atomic.Value // string; updated safely while requests are in flight
	rt http.RoundTripper
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if ua, ok := t.ua.Load().(string); ok {
		clone.Header.Set("User-Agent", ua)
	}
	return t.rt.RoundTrip(clone)
}

func GetCustomHttpClient() (client *http.Client) {
	transport := &userAgentTransport{rt: http.DefaultTransport}
	transport.ua.Store(effectiveUserAgent(LiveConfig.UserAgent))
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}
}

func effectiveUserAgent(ua string) string {
	if ua != "" {
		return ua
	}
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
}

func applyUserAgent(client *http.Client, ua string) {
	if client == nil {
		return
	}
	if transport, ok := client.Transport.(*userAgentTransport); ok {
		transport.ua.Store(effectiveUserAgent(ua))
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
	if _, err := RunProgram(p); err != nil {
		logging.Fatalf("PrepareEnterRoom ui error: %v", err)
		os.Exit(1)
	}
	if loginModel.quit {
		os.Exit(0)
	}
	return loginModel.room, nil
}
