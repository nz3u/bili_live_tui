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

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/shr-go/bili_live_tui/api"
)

type alignLine struct {
	Index    int
	Computed int
	Rendered int
	Preview  string
}

type alignReport struct {
	CodePage uint32
	Lines    []alignLine
	Drifting []string
	Error    string
}

// The frame, the QR code and the send box are built from box-drawing, block and
// separator glyphs. lipgloss sizes and pads them with go-runewidth, so any glyph
// whose computed width disagrees with what the Windows console actually draws
// shifts every following cell. A directly launched binary used to hit exactly
// that: runewidth picks its East Asian table in its package init() from the
// console code page — before main() can change it — and start.bat was what made
// the code page 65001 by then. Without the wrapper a 936 console made runewidth
// call box/block glyphs 2 cells wide while the console drew 1, misaligning the
// frames and the login QR code.
//
// This renders the real login and chat views in an isolated console and compares
// the width lipgloss computed for each line with the cells the console really
// advanced.
func TestRenderedViewsMatchComputedWidths(t *testing.T) {
	if os.Getenv("BILI_TUI_ALIGN_CHILD") == "1" {
		report := alignReport{}
		defer func() {
			raw, _ := json.Marshal(report)
			_ = os.WriteFile(os.Getenv("BILI_TUI_ALIGN_REPORT"), raw, 0600)
		}()
		console, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			report.Error = "open CONOUT$: " + err.Error()
			return
		}
		defer console.Close()
		os.Stdout = console
		// This is the fix under test: the binary configures itself.
		restore := SetupConsole()
		defer restore()
		report.CodePage = consoleOutputCodePage()

		// Every user-facing glyph that lipgloss measures must agree with the
		// console, otherwise the line containing it drifts. U+00B7 middle dot is
		// deliberately absent: the console draws it two cells wide while
		// go-runewidth reports one, so the UI uses U+2022 bullets instead (see
		// TestUIStringsAvoidDriftingGlyphs).
		glyphs := []string{
			"─", "│", "╭", "╮", "╰", "╯", "├", "┌", "┐", "└", "┘",
			"▀", "▄", "█", "•", "中", "全", "、", "a", " ", "|", "/",
		}
		for _, glyph := range glyphs {
			if !widthsAgree(console.Fd(), glyph) {
				report.Drifting = append(report.Drifting, glyph)
			}
		}

		// Render the real views at a fixed size so Place() padding is stable.
		windowWidth, windowHeight = 100, 30
		LiveConfig = defaultConfig()

		login := newLoginModel(nil)
		login.step = loginStepWaitLogin
		login.loginData = &api.QRCodeLoginData{
			QRString:    strings.Repeat("▀▄█", 20),
			Status:      api.QRLoginNotScan,
			QRImagePath: "/tmp/login.png",
		}

		room := uiTestRoom()
		room.RoomID, room.ShortID, room.Title = 7777, 2693345, "德云色"
		chat := InitialModel(room)
		updated, _ := chat.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		chatView := updated.(model).View()

		row, index := int16(0), 0
		for _, view := range []string{login.View(), chatView} {
			for _, line := range strings.Split(view, "\n") {
				if line == "" {
					continue
				}
				rendered, err := consoleCursorCells(console.Fd(), row, line)
				if err != nil {
					report.Error = "cells: " + err.Error()
					return
				}
				preview := line
				if runes := []rune(preview); len(runes) > 20 {
					preview = string(runes[:20])
				}
				report.Lines = append(report.Lines, alignLine{
					Index: index, Computed: lipgloss.Width(line), Rendered: rendered, Preview: preview,
				})
				index++
				if row++; row > 25 {
					row = 0
				}
			}
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "align.json")
	log, err := os.Create(filepath.Join(directory, "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestRenderedViewsMatchComputedWidths$", "-test.timeout=15s")
	command.Dir = directory
	command.Env = append(os.Environ(), "BILI_TUI_ALIGN_CHILD=1", "BILI_TUI_ALIGN_REPORT="+reportPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010, HideWindow: true} // CREATE_NEW_CONSOLE
	command.Stdout, command.Stderr = log, log
	if err := command.Run(); err != nil {
		t.Fatalf("isolated alignment test: %v", err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report alignReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	if len(report.Drifting) != 0 {
		t.Fatalf("these glyphs are measured differently than the console draws them: %s",
			strings.Join(report.Drifting, " "))
	}
	if len(report.Lines) == 0 {
		t.Fatal("no view lines were measured")
	}
	mismatched := 0
	for _, line := range report.Lines {
		if line.Computed == line.Rendered {
			continue
		}
		if mismatched++; mismatched <= 5 {
			t.Errorf("line %d (%q): lipgloss computed %d cells, console drew %d",
				line.Index, line.Preview, line.Computed, line.Rendered)
		}
	}
	if mismatched != 0 {
		t.Fatalf("%d/%d rendered lines drifted from their computed width", mismatched, len(report.Lines))
	}
}

// widthsAgree reports whether runewidth's cell width for a glyph matches the
// cells the console advances when drawing it.
func widthsAgree(handle uintptr, glyph string) bool {
	cells, err := consoleCursorCells(handle, 0, glyph)
	if err != nil {
		return false
	}
	return cells == runewidth.RuneWidth([]rune(glyph)[0])
}
