//go:build !windows
// +build !windows

package tui

import "errors"

// NeedsUTF8ConsoleRelaunch is false outside Windows: terminals there are UTF-8
// already and there is no console code page for go-runewidth to misread.
func NeedsUTF8ConsoleRelaunch() bool { return false }

// RelaunchWithUTF8Console is never needed outside Windows.
func RelaunchWithUTF8Console() (int, error) {
	return 0, errors.New("UTF-8 console relaunch is Windows-only")
}
