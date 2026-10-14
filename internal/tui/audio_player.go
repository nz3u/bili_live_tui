package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type audioSession interface {
	Ready(context.Context) error
	Mute(context.Context, bool) error
	Done() <-chan struct{}
	Close()
}

func audioConfigDir() string {
	if loadedConfig != nil {
		return filepath.Dir(loadedConfig.path)
	}
	return "."
}

func findAudioPlayer() (string, error) {
	return configuredAudioPlayer(LiveConfig.MPVPath, audioConfigDir())
}

// Resolve only the explicitly configured location, never PATH or siblings.
// Relative paths are relative to the config file, not the working directory.
func configuredAudioPlayer(configured, baseDir string) (string, error) {
	path := strings.TrimSpace(configured)
	if path == "" {
		return "", errors.New("请在设置中填写 mpv_path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("mpv_path 路径无效")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", errors.New("未找到 mpv，请检查 mpv_path")
	}
	if info.IsDir() {
		name := "mpv"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path = filepath.Join(path, name)
		info, err = os.Stat(path)
		if err != nil {
			return "", errors.New("目录内未找到 mpv，请检查 mpv_path")
		}
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("mpv_path 须为播放器目录或可执行文件")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return "", errors.New("mpv_path 文件没有执行权限")
	}
	return path, nil
}

type audioProcess struct {
	pid  int
	wait func() error
	stop func()
}

type mpvSession struct {
	cancel    context.CancelFunc
	done      chan struct{}
	endpoint  string
	processID int
}

func mpvArgs(endpoint, address, ua string, muted bool) []string {
	mute := "no"
	if muted {
		mute = "yes"
	}
	return []string{
		"--no-config", "--load-scripts=no", "--ytdl=no", "--vid=no", "--sid=no",
		"--force-window=no", "--terminal=no", "--input-terminal=no", "--idle=no",
		"--audio-display=no", "--keep-open=no", "--volume=100",
		"--mute=" + mute,
		"--network-timeout=10", "--cache=yes", "--cache-secs=2",
		"--demuxer-readahead-secs=1", "--input-ipc-server=" + endpoint,
		"--referrer=https://live.bilibili.com/", "--user-agent=" + ua,
		"--", address,
	}
}

func launchMPV(ctx context.Context, player, address, ua string, muted bool) (audioSession, error) {
	dir, err := os.MkdirTemp("", "bililive-audio-")
	if err != nil {
		return nil, err
	}
	endpoint := filepath.Join(dir, "ipc")
	if runtime.GOOS == "windows" {
		endpoint = `\\.\pipe\` + filepath.Base(dir)
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &mpvSession{cancel: cancel, done: make(chan struct{}), endpoint: endpoint}
	args := mpvArgs(endpoint, address, ua, muted)
	// Portable Windows builds may not ship a CA store. Use an explicit bundle
	// beside mpv when supplied, never disable TLS certificate verification.
	caFile := filepath.Join(filepath.Dir(player), "cacert.pem")
	if info, err := os.Stat(caFile); err == nil && !info.IsDir() {
		args = append([]string{"--tls-ca-file=" + caFile}, args...)
	}
	// Never inherit terminal input/output: this must not steal keys or corrupt the TUI.
	process, err := startAudioProcess(ctx, player, args)
	if err != nil {
		cancel()
		os.RemoveAll(dir) // dir was created exclusively by MkdirTemp above.
		return nil, err
	}
	s.processID = process.pid
	go func() {
		select {
		case <-ctx.Done():
			process.stop()
		case <-s.done:
		}
	}()
	go func() {
		_ = process.wait()
		process.stop()
		cancel()
		os.RemoveAll(dir)
		close(s.done)
	}()
	return s, nil
}

func (s *mpvSession) Done() <-chan struct{} { return s.done }
func (s *mpvSession) Close()                { s.cancel(); <-s.done }

// Each request has its own connection and deadline, so a dead player can never
// hang shutdown or the UI. Event lines can precede the matching JSON response.
func (s *mpvSession) request(ctx context.Context, command ...interface{}) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := dialAudioIPC(ctx, s.endpoint)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()
	if err = json.NewEncoder(conn).Encode(map[string]interface{}{"command": command, "request_id": 1}); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var response struct {
			ID    int             `json:"request_id"`
			Error string          `json:"error"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != 1 {
			continue
		}
		if response.Error != "success" {
			return nil, fmt.Errorf("mpv: %s", response.Error)
		}
		return response.Data, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.ErrUnexpectedEOF
}

func (s *mpvSession) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		data, err := s.request(ctx, "get_property", "audio-params")
		if err == nil && len(data) > 0 && string(data) != "null" {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-s.done:
			timer.Stop()
			return errors.New("mpv 未能播放音轨")
		case <-timer.C:
		}
	}
}
func (s *mpvSession) Mute(ctx context.Context, muted bool) error {
	_, err := s.request(ctx, "set_property", "mute", muted)
	return err
}
