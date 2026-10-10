//go:build !windows
// +build !windows

package tui

// SetupConsole is a no-op outside Windows: terminals there are UTF-8 already and
// the Windows console code page does not exist. It returns a no-op restore so
// callers can defer it unconditionally.
func SetupConsole() func() { return func() {} }
