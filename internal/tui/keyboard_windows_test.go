//go:build windows

package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/api"
	"golang.org/x/sys/windows"
)

type consoleKeyRecord struct {
	EventType                             uint16
	Padding                               uint16
	Down                                  int32
	Repeat, VirtualKey, ScanCode, Unicode uint16
	Control                               uint32
}

var consoleDLL = windows.NewLazySystemDLL("kernel32.dll")
var writeConsoleInput = consoleDLL.NewProc("WriteConsoleInputW")

func injectConsoleKeys(handle windows.Handle, keys ...consoleKeyRecord) tea.Cmd {
	return func() tea.Msg {
		for i := range keys {
			keys[i].EventType, keys[i].Down, keys[i].Repeat = 1, 1, 1
		}
		var written uint32
		ok, _, err := writeConsoleInput.Call(uintptr(handle), uintptr(unsafe.Pointer(&keys[0])), uintptr(len(keys)), uintptr(unsafe.Pointer(&written)))
		if ok == 0 || written != uint32(len(keys)) {
			return consoleProbeError{fmt.Errorf("WriteConsoleInputW wrote %d keys: %v", written, err)}
		}
		return nil
	}
}

type consoleProbeError struct{ err error }
type consoleProbeDeadline struct{}

type consoleProgramProbe struct {
	inner  model
	handle windows.Handle
	login  bool
	stage  int
	err    error
	// opened collects the pages Alt+F asked to open. It is a pointer because
	// Update has a value receiver, so the slice must outlive its copies.
	opened *[]string
}

func (m consoleProgramProbe) Init() tea.Cmd {
	return tea.Batch(injectConsoleKeys(m.handle, consoleKeyRecord{VirtualKey: 0x0d, Unicode: 0x0d}),
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return consoleProbeDeadline{} }))
}

func (m consoleProgramProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if err, ok := msg.(consoleProbeError); ok {
		m.err = err.err
		return m, tea.Quit
	}
	if _, ok := msg.(consoleProbeDeadline); ok {
		m.err = fmt.Errorf("console key timed out at stage %d", m.stage)
		return m, tea.Quit
	}
	updated, cmd := m.inner.Update(msg)
	m.inner = updated.(model)
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, cmd
	}
	if m.login {
		if key.String() != "enter" || !m.inner.textInput.Focused() {
			m.err = fmt.Errorf("first program did not receive Enter")
		}
		m.stage = 1
		return m, tea.Quit
	}
	switch m.stage {
	case 0:
		if key.String() != "enter" || !m.inner.textInput.Focused() {
			m.err = fmt.Errorf("second program lost initial Enter")
			return m, tea.Quit
		}
		m.stage++
		return m, injectConsoleKeys(m.handle, consoleKeyRecord{Unicode: '测'}, consoleKeyRecord{Unicode: '试'}, consoleKeyRecord{VirtualKey: 'O', Unicode: 'o'}, consoleKeyRecord{VirtualKey: 'K', Unicode: 'k'})
	case 1:
		if m.inner.textInput.Value() != "测试ok" {
			return m, cmd
		}
		m.stage++
		return m, injectConsoleKeys(m.handle, consoleKeyRecord{VirtualKey: 0x71}) // F2
	case 2:
		if key.String() != "f2" || m.inner.settings == nil {
			m.err = fmt.Errorf("console F2 did not open settings")
			return m, tea.Quit
		}
		m.stage++
		return m, injectConsoleKeys(m.handle, consoleKeyRecord{VirtualKey: 0x1b, Unicode: 0x1b})
	case 3:
		if key.String() != "esc" || m.inner.settings != nil || !m.inner.textInput.Focused() || m.inner.textInput.Value() != "测试ok" {
			m.err = fmt.Errorf("console Esc lost focus/draft")
			return m, tea.Quit
		}
		m.stage++
		// Inject ESC and 'f' in one call so they reach the decoder as one chunk:
		// the console reports Alt+F as the ESC-prefixed sequence.
		return m, injectConsoleKeys(m.handle,
			consoleKeyRecord{VirtualKey: 0x1b, Unicode: 0x1b},
			consoleKeyRecord{VirtualKey: 'F', Unicode: 'f'})
	case 4:
		// Alt+F must survive the real console decoder (conhost reports ESC 'f')
		// and must not disturb the draft or focus.
		if key.String() != "alt+f" || !m.inner.textInput.Focused() || m.inner.textInput.Value() != "测试ok" {
			m.err = fmt.Errorf("console Alt+F was not decoded or lost focus/draft")
			return m, tea.Quit
		}
		if len(*m.opened) != 1 || (*m.opened)[0] != roomPageURL(m.inner.room) {
			m.err = fmt.Errorf("console Alt+F opened %v, want %s", *m.opened, roomPageURL(m.inner.room))
			return m, tea.Quit
		}
		m.stage++
		return m, injectConsoleKeys(m.handle, consoleKeyRecord{VirtualKey: 'N', Unicode: 0x0e, Control: 0x0008})
	case 5:
		if key.String() != "ctrl+n" || !m.inner.switching || cmd == nil {
			m.err = fmt.Errorf("console Ctrl+N did not request a switch from input view")
		}
		m.stage++
		// Network commands are tested by the local TCP/HTTP integration test.
		return m, tea.Quit
	}
	return m, cmd
}

func (m consoleProgramProbe) View() string { return m.inner.View() }

type consoleTestReport struct {
	Stages []int    `json:"stages"`
	Modes  []uint32 `json:"modes"`
	Error  string   `json:"error,omitempty"`
}

// Run in an isolated, hidden Windows console: real CONIN$/VT/raw input and two
// consecutive Programs. Never inject into the user's active terminal window.
func TestWindowsConsoleKeyboardLifecycle(t *testing.T) {
	if os.Getenv("BILI_TUI_CONSOLE_CHILD") == "1" {
		report := consoleTestReport{}
		defer func() {
			raw, _ := json.Marshal(report)
			_ = os.WriteFile(os.Getenv("BILI_TUI_CONSOLE_REPORT"), raw, 0600)
		}()
		input, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err != nil {
			report.Error = err.Error()
			return
		}
		defer input.Close()
		output, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			report.Error = err.Error()
			return
		}
		defer output.Close()
		oldInput, oldOutput := os.Stdin, os.Stdout
		os.Stdin, os.Stdout = input, output
		defer func() { os.Stdin, os.Stdout = oldInput, oldOutput }()
		LiveConfig = defaultConfig()
		LiveConfig.RoomID, LiveConfig.RoomIDs = 1, []uint64{1, 2}
		handle := windows.Handle(input.Fd())
		var original uint32
		if err := windows.GetConsoleMode(handle, &original); err != nil {
			report.Error = err.Error()
			return
		}
		report.Modes = append(report.Modes, original)
		// Never launch a real browser from a test; record the requested page.
		opened := new([]string)
		previousOpen := openRoomPage
		openRoomPage = func(room *api.LiveRoom) error {
			*opened = append(*opened, roomPageURL(room))
			return nil
		}
		defer func() { openRoomPage = previousOpen }()
		for _, login := range []bool{true, false} {
			room := uiTestRoom()
			room.RoomID = 1
			final, err := RunProgram(tea.NewProgram(consoleProgramProbe{inner: *InitialModel(room), handle: handle, login: login, opened: opened}, tea.WithAltScreen()))
			room.Close()
			if err != nil {
				report.Error = err.Error()
				return
			}
			probe := final.(consoleProgramProbe)
			report.Stages = append(report.Stages, probe.stage)
			if probe.err != nil {
				report.Error = probe.err.Error()
				return
			}
			var mode uint32
			if err := windows.GetConsoleMode(handle, &mode); err != nil {
				report.Error = err.Error()
				return
			}
			report.Modes = append(report.Modes, mode)
			if mode != original {
				report.Error = fmt.Sprintf("console mode leaked: before=%x after=%x", original, mode)
				return
			}
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "console.json")
	log, err := os.Create(filepath.Join(directory, "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	// Match the encoding the shipped binary now sets for itself: only
	// OutputEncoding changes; do not force InputEncoding.
	script := fmt.Sprintf("& {[Console]::OutputEncoding = [Text.UTF8Encoding]::UTF8}; & '%s' '-test.run=^TestWindowsConsoleKeyboardLifecycle$' '-test.timeout=10s'", strings.ReplaceAll(executable, "'", "''"))
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script)
	command.Dir = directory
	command.Env = append(os.Environ(), "BILI_TUI_CONSOLE_CHILD=1", "BILI_TUI_CONSOLE_REPORT="+reportPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010, HideWindow: true} // CREATE_NEW_CONSOLE
	command.Stdout, command.Stderr = log, log
	if err := command.Run(); err != nil {
		t.Fatalf("isolated Windows console test: %v", err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report consoleTestReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	if len(report.Stages) != 2 || report.Stages[0] != 1 || report.Stages[1] != 6 || len(report.Modes) != 3 {
		t.Fatalf("incomplete console keyboard lifecycle: %+v", report)
	}
}
