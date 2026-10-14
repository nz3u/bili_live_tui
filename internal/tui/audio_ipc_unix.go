//go:build !windows

package tui

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"time"
)

func dialAudioIPC(ctx context.Context, endpoint string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
}

func startAudioProcess(ctx context.Context, player string, args []string) (*audioProcess, error) {
	if !filepath.IsAbs(player) {
		return nil, fmt.Errorf("mpv executable path must be absolute")
	}
	cmd := exec.CommandContext(ctx, player, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &audioProcess{pid: cmd.Process.Pid, wait: cmd.Wait, stop: func() { _ = cmd.Process.Kill() }}, nil
}
