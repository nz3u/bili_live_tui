//go:build windows
// +build windows

package tui

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

const (
	// utf8ConsoleEnv marks the process that an earlier instance already
	// restarted inside a UTF-8 console, so the restart can never recurse.
	utf8ConsoleEnv = "BILI_TUI_UTF8_CONSOLE"
	// utf8ConsoleSkipEnv lets a user or a test opt out of the restart.
	utf8ConsoleSkipEnv = "BILI_TUI_NO_UTF8_RELAUNCH"
)

// NeedsUTF8ConsoleRelaunch reports whether the program must restart itself
// inside a UTF-8 console before it draws anything.
//
// go-runewidth picks its East Asian width table in its package init(), which
// runs before main(): it calls GetConsoleOutputCP and treats 932/936/949/950 as
// East Asian. That makes "ambiguous" glyphs — the box drawing and block
// characters the frames and the login QR code are built from — measure two
// cells while the Windows console draws them in one, so lipgloss pads and
// truncates against the wrong width and the code comes out skewed and
// unscannable.
//
// The deleted start.bat hid this by setting the console output encoding to
// UTF-8 *before* the executable started, so init() already saw code page 65001
// and chose the matching table. Launching the binary directly left the code
// page at 936, so init() chose the CJK table. Nothing inside main() can undo
// that choice, because the table is selected before main() runs.
//
// So the program does what start.bat did, built in: it switches the console to
// UTF-8 and starts a second copy of itself, whose init() sees the same code
// page start.bat used to provide. That second copy renders exactly like the old
// start.bat run.
func NeedsUTF8ConsoleRelaunch() bool {
	if os.Getenv(utf8ConsoleEnv) == "1" || os.Getenv(utf8ConsoleSkipEnv) == "1" {
		return false
	}
	// A redirected or piped stdout is not a console: there is no code page to
	// switch and restarting would lose the redirection.
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	return consoleOutputCodePage() != utf8CodePage
}

// RelaunchWithUTF8Console switches the console to UTF-8 and runs this same
// executable again inside it, returning the child's exit code. It is the
// built-in replacement for the old start.bat wrapper.
//
// The child inherits the console, so the user sees a single terminal, and it
// inherits stdin/stdout/stderr so Bubble Tea keeps owning that terminal.
func RelaunchWithUTF8Console() (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	// Set the code page *here*, in the parent, so the child is already attached
	// to a UTF-8 console when its own init() runs — the moment that decides the
	// width table.
	if ok, _, _ := procSetConsoleOutputCP.Call(uintptr(utf8CodePage)); ok == 0 {
		return 0, windows.GetLastError()
	}
	command := exec.Command(self, os.Args[1:]...)
	command.Env = append(os.Environ(), utf8ConsoleEnv+"=1")
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		// A non-zero exit from the UI is a normal outcome, not a relaunch
		// failure; hand the code back so the shell sees it too.
		if exitError, ok := err.(*exec.ExitError); ok {
			return exitError.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}
