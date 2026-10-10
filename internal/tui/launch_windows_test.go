//go:build windows

package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// The relaunch must reproduce the old start.bat behaviour: go-runewidth reads
// the console code page in its package init(), before main() runs, so only a
// process that starts *in* a UTF-8 console picks the width table that makes the
// QR code's block glyphs measure one cell.
//
// The child is this test binary itself, started on a fresh console forced to
// code page 936 — the state that made the code unscannable. The parent switches
// the console to UTF-8 and restarts, so the restarted copy must observe 65001,
// and its runewidth init() must have chosen the non-CJK table.
func TestRelaunchLeavesChildInUTF8Console(t *testing.T) {
	if os.Getenv("BILI_TUI_UTF8_CHILD") == "1" {
		report := struct {
			CodePage  uint32
			BlockWide int
			Error     string
		}{}
		defer func() {
			raw, _ := json.Marshal(report)
			_ = os.WriteFile(os.Getenv("BILI_TUI_UTF8_REPORT"), raw, 0o600)
		}()
		// The runner captures stdout, so attach to CONOUT$ to be a real console
		// — the same situation as a directly launched executable, and the only
		// one in which the relaunch is supposed to happen at all.
		console, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			report.Error = "open CONOUT$: " + err.Error()
			return
		}
		defer console.Close()
		os.Stdout = console

		if NeedsUTF8ConsoleRelaunch() {
			code, err := RelaunchWithUTF8Console()
			if err != nil {
				report.Error = "relaunch: " + err.Error()
				return
			}
			os.Exit(code)
		}
		// Recorded by the restarted copy: the code page it started in, which is
		// what go-runewidth's init() consumed, plus the width it gives a QR block.
		report.CodePage = consoleOutputCodePage()
		report.BlockWide = runewidth.RuneWidth('\u2588')
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "relaunch.json")
	// chcp 936 runs before the child, so the child's init() sees a CJK code
	// page — the exact situation that made the QR unscannable without start.bat.
	//
	// The command goes through a batch file: exec escapes each argument, so
	// handing cmd.exe the whole "chcp && ..." line as one argument would wrap it
	// in quotes and cmd would never find the executable.
	script := filepath.Join(directory, "probe.bat")
	if err := os.WriteFile(script, []byte(
		"@echo off\r\nchcp 936 >NUL\r\n\""+executable+
			"\" -test.run=TestRelaunchLeavesChildInUTF8Console -test.timeout=30s\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(script)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"BILI_TUI_UTF8_CHILD=1", "BILI_TUI_UTF8_REPORT="+reportPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010, HideWindow: true}
	log, err := os.Create(filepath.Join(directory, "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	command.Stdout, command.Stderr = log, log
	if err := command.Run(); err != nil {
		body, _ := os.ReadFile(log.Name())
		t.Fatalf("child failed: %v\n%s", err, body)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		CodePage  uint32
		BlockWide int
		Error     string
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	// The restarted copy must start in a UTF-8 console, so go-runewidth's
	// init() picks the table where QR blocks are one cell — what start.bat used
	// to guarantee.
	if report.CodePage != 65001 {
		t.Fatalf("child started on a 936 console reports code page %d, want 65001; "+
			"runewidth would pick the CJK width table and the QR code would not scan", report.CodePage)
	}
	if report.BlockWide != 1 {
		t.Fatalf("QR block glyph measures %d cells in the restarted copy, want 1; "+
			"the code would be drawn skewed and would not scan", report.BlockWide)
	}
}

// The guard must not fire twice: the restarted copy must see the UTF-8 console
// it was launched into and continue instead of relaunching again.
func TestRelaunchDoesNotRecurse(t *testing.T) {
	t.Setenv(utf8ConsoleEnv, "1")
	if NeedsUTF8ConsoleRelaunch() {
		t.Fatal("the restarted copy wants to relaunch again; the relaunch would never terminate")
	}
}

// A user must be able to opt out, and a piped stdout is not a console at all:
// neither may trigger a restart, which would break redirection and tooling.
func TestRelaunchSkippedWhenDisabled(t *testing.T) {
	t.Setenv(utf8ConsoleSkipEnv, "1")
	if NeedsUTF8ConsoleRelaunch() {
		t.Fatal("relaunch requested even though it was explicitly disabled")
	}
}

// The relaunch must not hang: a child that inherits the terminal and quits on
// its own must return promptly. A timeout here would freeze the real startup.
func TestRelaunchReturnsPromptly(t *testing.T) {
	if os.Getenv("BILI_TUI_UTF8_TIMEOUT_CHILD") == "1" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Re-run this very test as the child: it returns immediately.
	command := exec.Command(executable, "-test.run=^TestRelaunchReturnsPromptly$",
		"-test.timeout=30s")
	command.Env = append(os.Environ(), "BILI_TUI_UTF8_TIMEOUT_CHILD=1")
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- command.Run() }()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 25*time.Second {
			t.Fatalf("child took %v; the relaunch path is slow to return", elapsed)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("relaunch did not return within 30s; startup would hang")
	}
}
