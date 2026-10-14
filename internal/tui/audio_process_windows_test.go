//go:build windows

package tui

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Run in separate processes: forced exit deliberately bypasses every defer.
func TestAudioWindowsProcessHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] == "audio-child" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("BILILIVE_AUDIO_WINDOWS_HELPER") != "parent" {
		return
	}
	var pid int
	var stop func()
	if player := os.Getenv("BILILIVE_AUDIO_WINDOWS_TEST_MPV"); player != "" {
		session, err := launchMPV(context.Background(), player, os.Getenv("BILILIVE_AUDIO_WINDOWS_WAV"), audioUserAgent(), true)
		if err != nil {
			t.Fatal(err)
		}
		stop = session.Close
		defer stop()
		if err := session.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
		pid = session.(*mpvSession).processID
	} else {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		process, err := startAudioProcess(context.Background(), executable, []string{"-test.run=^TestAudioWindowsProcessHelper$", "--", "audio-child"})
		if err != nil {
			t.Fatal(err)
		}
		stop = func() { process.stop(); _ = process.wait() }
		defer process.stop()
		pid = process.pid
	}
	if err := os.WriteFile(os.Getenv("BILILIVE_AUDIO_WINDOWS_PIDFILE"), []byte(strconv.Itoa(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	var exit [1]byte
	_, _ = os.Stdin.Read(exit[:])
	stop()
}

func testWindowsAudioParentExit(t *testing.T, player string, forced bool) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	parent := exec.CommandContext(ctx, executable, "-test.run=^TestAudioWindowsProcessHelper$")
	parent.Env = append(os.Environ(), "BILILIVE_AUDIO_WINDOWS_HELPER=parent", "BILILIVE_AUDIO_WINDOWS_PIDFILE="+pidFile, "BILILIVE_AUDIO_WINDOWS_TEST_MPV="+player)
	if player != "" {
		wave := filepath.Join(dir, "silence.wav")
		if err := os.WriteFile(wave, audioTestWAV(), 0600); err != nil {
			t.Fatal(err)
		}
		parent.Env = append(parent.Env, "BILILIVE_AUDIO_WINDOWS_WAV="+wave)
	}
	// Retain helper errors without pipe readers that could depend on the child.
	log, err := os.Create(filepath.Join(dir, "helper.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	parent.Stdout, parent.Stderr = log, log
	stdin, err := parent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		cancel()
		if !waited {
			_ = parent.Wait()
		}
	}()
	var pid int
	deadline := time.NewTimer(17 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for pid == 0 {
		select {
		case <-deadline.C:
			cancel()
			_ = parent.Wait()
			waited = true
			data, _ := os.ReadFile(filepath.Join(dir, "helper.log"))
			t.Fatalf("helper startup timed out: %s", data)
		case <-tick.C:
			if data, err := os.ReadFile(pidFile); err == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
		}
	}
	// Hold the exact process handle so PID reuse cannot cause a false result.
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if forced {
		if err := parent.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := stdin.Write([]byte{'q'}); err != nil {
			t.Fatal(err)
		}
	}
	err = parent.Wait()
	waited = true
	if !forced && err != nil {
		t.Fatal(err)
	}
	if forced && err == nil {
		t.Fatal("force-killed helper exited normally")
	}
	state, err := windows.WaitForSingleObject(child, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("audio child %d survived parent exit (wait=%d, err=%v)", pid, state, err)
	}
	t.Logf("child %d terminated after parent exit; forced=%v; real mpv=%v", pid, forced, player != "")
}

func TestAudioWindowsJobKillsPlayerOnParentExit(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%v", forced), func(t *testing.T) { testWindowsAudioParentExit(t, "", forced) })
	}
}

func TestAudioWindowsRealMPVParentExit(t *testing.T) {
	player := os.Getenv("BILILIVE_MPV")
	if player == "" {
		t.Skip("set BILILIVE_MPV to verify real mpv with offline silent WAV")
	}
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%v", forced), func(t *testing.T) { testWindowsAudioParentExit(t, player, forced) })
	}
}

func audioTestWAV() []byte {
	const size = 60 * 8000 * 2 // 60 seconds, 8 kHz mono PCM16, all zero (silent).
	data := make([]byte, 44+size)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+size))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(size))
	return data
}
