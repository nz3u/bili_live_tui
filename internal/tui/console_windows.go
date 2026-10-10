//go:build windows
// +build windows

package tui

import (
	"os"

	"github.com/mattn/go-runewidth"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// utf8CodePage is the Windows console code page for UTF-8.
const utf8CodePage = 65001

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
	procGetConsoleCP       = kernel32.NewProc("GetConsoleCP")
)

// SetupConsole prepares a directly launched binary (double-click, Explorer, or a
// plain shell) so it renders correctly without the start.bat wrapper, whose only
// job was to set the PowerShell output encoding before exec. It does two things:
//
//  1. Switches the console *output* code page to UTF-8. Go writes raw UTF-8
//     bytes, so a CJK code page such as 936 would decode them as GBK and show
//     mojibake.
//  2. Pins go-runewidth's East Asian width table so its cell widths match what
//     the Windows console actually draws. runewidth decides this in its package
//     init() from the console output code page, which runs before main(); on a
//     936 console it reports 2 cells for "ambiguous" glyphs (box drawing
//     ─│╭╮, blocks █▀▄) that the console draws in 1 cell. lipgloss then pads and
//     truncates against the wrong width, so frames and the QR code come out
//     misaligned. The old start.bat ran with the code page already at 65001, so
//     runewidth picked the matching table — that is exactly why it looked right.
//
// The input code page is deliberately left alone: Bubble Tea decodes keypresses
// with go-localereader, which reads bytes using the system ANSI code page, so
// forcing input to UTF-8 would corrupt Chinese input.
//
// The returned function restores the previous code page and width table. Only the
// width alignment runs when stdout is not a console (redirected or piped), where
// UTF-8 bytes are already written as-is and no console state may be touched.
func SetupConsole() func() {
	restoreWidth := alignRuneWidthWithConsole()
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return restoreWidth
	}
	previous, _, _ := procGetConsoleOutputCP.Call()
	if ok, _, _ := procSetConsoleOutputCP.Call(uintptr(utf8CodePage)); ok == 0 {
		return restoreWidth
	}
	return func() {
		restoreWidth()
		if previous != 0 && previous != utf8CodePage {
			procSetConsoleOutputCP.Call(previous)
		}
	}
}

// alignRuneWidthWithConsole makes go-runewidth's East Asian width table agree
// with the Windows console. Windows consoles draw Unicode "ambiguous" width
// glyphs — box drawing, block elements, and similar — in a single cell, while
// runewidth treats them as 2 cells whenever the console code page looks CJK
// (932/936/949/950/51932). CJK ideographs are unaffected: they are in the
// unconditional double-width table, so they stay 2 cells either way.
//
// This is only applied on Windows. On Unix a CJK locale genuinely means the
// terminal treats those glyphs as double width, which is the convention
// runewidth implements there.
func alignRuneWidthWithConsole() func() {
	if os.Getenv("RUNEWIDTH_EASTASIAN") != "" {
		// The user configured the width table explicitly; leave it untouched.
		return func() {}
	}
	previousEastAsian, previousFlag := runewidth.DefaultCondition.EastAsianWidth, runewidth.EastAsianWidth
	runewidth.EastAsianWidth = false
	runewidth.DefaultCondition.EastAsianWidth = false
	return func() {
		runewidth.EastAsianWidth = previousFlag
		runewidth.DefaultCondition.EastAsianWidth = previousEastAsian
	}
}

// consoleOutputCodePage reports the console output code page. It exists so the
// encoding setup can be verified from tests and from the isolated console tests.
func consoleOutputCodePage() uint32 {
	cp, _, _ := procGetConsoleOutputCP.Call()
	return uint32(cp)
}

// consoleInputCodePage reports the console input code page. SetupConsole must
// leave it alone, so tests assert it stays untouched.
func consoleInputCodePage() uint32 {
	cp, _, _ := procGetConsoleCP.Call()
	return uint32(cp)
}
