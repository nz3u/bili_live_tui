//go:build windows

package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/mattn/go-runewidth"
)

// Reading the screen buffer back proves what the user actually sees, not merely
// which code page was requested: a correct code page with a broken write path
// would still show mojibake.
var (
	procGetConsoleScreenBufferInfo  = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procReadConsoleOutputCharacterW = kernel32.NewProc("ReadConsoleOutputCharacterW")
	procSetConsoleCursorPosition    = kernel32.NewProc("SetConsoleCursorPosition")
)

type consoleCoord struct{ X, Y int16 }

type consoleSmallRect struct{ Left, Top, Right, Bottom int16 }

type consoleScreenBufferInfo struct {
	Size              consoleCoord
	CursorPosition    consoleCoord
	Attributes        uint16
	Window            consoleSmallRect
	MaximumWindowSize consoleCoord
}

func consoleCursorRow(handle uintptr) (int, error) {
	var info consoleScreenBufferInfo
	if ok, _, err := procGetConsoleScreenBufferInfo.Call(handle, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return 0, err
	}
	return int(info.CursorPosition.Y), nil
}

// consoleCursorCells writes s starting at column 0 of the given row and returns
// how many console cells the cursor actually advanced. This measures real
// rendering, which is the only honest ground truth for alignment: character
// counts and runewidth guesses both disagree with it.
func consoleCursorCells(handle uintptr, row int16, s string) (int, error) {
	origin := uint32(uint16(row)) << 16
	if ok, _, err := procSetConsoleCursorPosition.Call(handle, uintptr(origin)); ok == 0 {
		return 0, err
	}
	var before consoleScreenBufferInfo
	if ok, _, err := procGetConsoleScreenBufferInfo.Call(handle, uintptr(unsafe.Pointer(&before))); ok == 0 {
		return 0, err
	}
	if _, err := os.Stdout.WriteString(s); err != nil {
		return 0, err
	}
	var after consoleScreenBufferInfo
	if ok, _, err := procGetConsoleScreenBufferInfo.Call(handle, uintptr(unsafe.Pointer(&after))); ok == 0 {
		return 0, err
	}
	return int(after.CursorPosition.X) - int(before.CursorPosition.X), nil
}

// readConsoleRow returns the text currently displayed on the given screen row.
func readConsoleRow(handle uintptr, row, width int) (string, error) {
	buf := make([]uint16, width)
	var read uint32
	// COORD is two int16 packed into one uint32: x in the low word, y in the high.
	origin := uint32(uint16(row)) << 16
	ok, _, err := procReadConsoleOutputCharacterW.Call(handle,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(width), uintptr(origin), uintptr(unsafe.Pointer(&read)))
	if ok == 0 {
		return "", err
	}
	return syscall.UTF16ToString(buf[:read]), nil
}

type directDisplayReport struct {
	CodePage uint32
	Scraped  string
	// Misaligned lists glyphs whose computed cell width disagrees with what the
	// console actually draws. Any entry breaks lipgloss padding and truncation.
	Misaligned []string
	Sample     string
	Error      string
}

// Directly launched executables must display correctly with no start.bat wrapper.
// This covers both failure modes the wrapper used to hide:
//
//   - mojibake: Chinese text decoded through the wrong code page.
//   - misalignment: go-runewidth computing cell widths that disagree with the
//     console, which breaks every frame and the QR code.
func TestDirectExecutionDisplaysUTF8WithoutWrapper(t *testing.T) {
	if os.Getenv("BILI_TUI_DISPLAY_CHILD") == "1" {
		report := directDisplayReport{}
		defer func() {
			raw, _ := json.Marshal(report)
			_ = os.WriteFile(os.Getenv("BILI_TUI_DISPLAY_REPORT"), raw, 0600)
		}()
		console, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			report.Error = "open CONOUT$: " + err.Error()
			return
		}
		defer console.Close()
		os.Stdout = console
		// This is the whole fix under test: the binary configures itself.
		restore := SetupConsole()
		defer restore()
		report.CodePage = consoleOutputCodePage()

		// Glyphs the TUI frames and the login QR code are built from: box drawing,
		// block elements, and a CJK ideograph for contrast.
		glyphs := []string{"─", "│", "╭", "╮", "▀", "▄", "█", "中", "a"}
		for i, glyph := range glyphs {
			cells, err := consoleCursorCells(console.Fd(), int16(i), glyph)
			if err != nil {
				report.Error = "cells: " + err.Error()
				return
			}
			want := runewidth.RuneWidth([]rune(glyph)[0])
			if cells != want {
				report.Misaligned = append(report.Misaligned,
					glyph+" console="+itoa(cells)+" runewidth="+itoa(want))
			}
		}
		report.Sample = "console=" + itoa(len(glyphs)) + " glyphs checked"

		// Use a clean row below the glyph probes so the scrape sees only the text.
		row := int16(len(glyphs) + 1)
		if ok, _, err := procSetConsoleCursorPosition.Call(console.Fd(), uintptr(uint32(uint16(row))<<16)); ok == 0 {
			report.Error = "set cursor: " + err.Error()
			return
		}
		const text = "德云色 - 2693345 · 发送成功"
		if _, err := os.Stdout.WriteString(text); err != nil {
			report.Error = "write: " + err.Error()
			return
		}
		scraped, err := readConsoleRow(console.Fd(), int(row), 60)
		if err != nil {
			report.Error = "scrape: " + err.Error()
			return
		}
		report.Scraped = strings.TrimRight(scraped, " ")
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "display.json")
	log, err := os.Create(filepath.Join(directory, "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestDirectExecutionDisplaysUTF8WithoutWrapper$", "-test.timeout=10s")
	command.Dir = directory
	command.Env = append(os.Environ(), "BILI_TUI_DISPLAY_CHILD=1", "BILI_TUI_DISPLAY_REPORT="+reportPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010, HideWindow: true} // CREATE_NEW_CONSOLE
	command.Stdout, command.Stderr = log, log
	if err := command.Run(); err != nil {
		t.Fatalf("isolated direct-display test: %v", err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report directDisplayReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	const want = "德云色 - 2693345 · 发送成功"
	if report.CodePage != 65001 {
		t.Fatalf("console output code page is %d, want 65001", report.CodePage)
	}
	if report.Scraped != want {
		t.Fatalf("console showed %q, want %q; the exe still needs an encoding wrapper", report.Scraped, want)
	}
	if len(report.Misaligned) != 0 {
		t.Fatalf("computed cell widths disagree with the console (%s); frames and the QR code will be misaligned",
			strings.Join(report.Misaligned, "; "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
