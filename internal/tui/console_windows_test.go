//go:build windows

package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

type codePageReport struct {
	Before, During, After    uint32
	InputBefore, InputDuring uint32
	RestoredTwiceSafe        bool
	// Width table state before and after SetupConsole. runewidth picks its East
	// Asian width table in its package init() from the console code page, before
	// main() can change it, so SetupConsole must also align the table.
	EastAsianBefore, EastAsianDuring bool
	BoxWidthDuring, BoxWidthAfter    int
	Error                            string
}

// The binary must render UTF-8 when launched directly, without the start.bat
// wrapper. SetupConsole switches the console output code page to UTF-8 and its
// restore must put the previous one back.
//
// The check must run in a real console: when `go test` captures stdout it is a
// pipe and SetupConsole is deliberately a no-op. A child process is started in
// its own hidden console to observe the real behaviour, matching the approach of
// TestWindowsConsoleKeyboardLifecycle.
func TestConsoleSetupAppliesUTF8OutputCodePage(t *testing.T) {
	if os.Getenv("BILI_TUI_CODEPAGE_CHILD") == "1" {
		report := codePageReport{}
		defer func() {
			raw, _ := json.Marshal(report)
			_ = os.WriteFile(os.Getenv("BILI_TUI_CODEPAGE_REPORT"), raw, 0600)
		}()
		// The runner redirects stdout to a log, but the child owns a real hidden
		// console. Attach stdout to CONOUT$ so it is a console — the same
		// situation as a directly launched executable, which is what SetupConsole
		// targets and why it is a no-op for a piped stdout.
		console, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			report.Error = err.Error()
			return
		}
		defer console.Close()
		previousStdout := os.Stdout
		os.Stdout = console
		defer func() { os.Stdout = previousStdout }()

		report.Before, report.InputBefore = consoleOutputCodePage(), consoleInputCodePage()
		report.EastAsianBefore = runewidth.EastAsianWidth
		restore := SetupConsole()
		report.During = consoleOutputCodePage()
		report.InputDuring = consoleInputCodePage()
		report.EastAsianDuring = runewidth.EastAsianWidth
		// The console draws these glyphs in one cell, so the width table must say
		// one cell too or lipgloss misaligns frames and the QR code.
		report.BoxWidthDuring = runewidth.RuneWidth('─')
		restore()
		report.After = consoleOutputCodePage()
		report.BoxWidthAfter = runewidth.RuneWidth('─')
		// A second restore must be harmless (no double-restore panic or state change).
		restore()
		report.RestoredTwiceSafe = consoleOutputCodePage() == report.After
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "codepage.json")
	log, err := os.Create(filepath.Join(directory, "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestConsoleSetupAppliesUTF8OutputCodePage$", "-test.timeout=10s")
	command.Dir = directory
	command.Env = append(os.Environ(), "BILI_TUI_CODEPAGE_CHILD=1", "BILI_TUI_CODEPAGE_REPORT="+reportPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010, HideWindow: true} // CREATE_NEW_CONSOLE
	command.Stdout, command.Stderr = log, log
	if err := command.Run(); err != nil {
		t.Fatalf("isolated console code page test: %v", err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report codePageReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	if report.During != 65001 {
		t.Fatalf("output code page is %d, want 65001 (UTF-8); the binary would show mojibake when started directly", report.During)
	}
	// The input code page must stay untouched: Bubble Tea decodes keypresses with
	// the system ANSI code page, so forcing input to UTF-8 would corrupt Chinese.
	if report.InputDuring != report.InputBefore {
		t.Fatalf("input code page changed (%d -> %d); Chinese input decoding would break", report.InputBefore, report.InputDuring)
	}
	if report.After != report.Before {
		t.Fatalf("console output code page not restored: before=%d after=%d", report.Before, report.After)
	}
	if !report.RestoredTwiceSafe {
		t.Fatal("a second restore changed the console output code page")
	}
	// go-runewidth picks its East Asian width table in its package init() from
	// the console code page, before main() can change it, so SetupConsole must
	// also align the table. The console draws box/block glyphs in one cell, so a
	// table that says two cells makes lipgloss misalign every frame and QR code.
	if report.BoxWidthDuring != 1 {
		t.Fatalf("box-drawing width is %d cells during setup, want 1; frames and the QR code would be misaligned", report.BoxWidthDuring)
	}
	if report.EastAsianBefore && report.EastAsianDuring {
		t.Fatal("East Asian width table was not aligned with the console")
	}
	if report.EastAsianBefore && report.BoxWidthAfter != 2 {
		t.Fatalf("width table not restored: box width after restore=%d, want 2", report.BoxWidthAfter)
	}
}
