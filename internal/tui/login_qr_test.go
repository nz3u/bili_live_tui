package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/skip2/go-qrcode"
)

var ansiSeqRE = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func stripANSI(s string) string { return ansiSeqRE.ReplaceAllString(s, "") }

// qrRowOf returns the QR content of a rendered line, and whether the line is a QR
// row at all (only block glyphs and spaces once the dialog border is removed).
func qrRowOf(line string) (string, bool) {
	trimmed := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│"))
	if trimmed == "" {
		return "", false
	}
	for _, r := range trimmed {
		switch r {
		case ' ', '█', '▀', '▄':
		default:
			return "", false
		}
	}
	return trimmed, true
}

// qrRowsOf collects the ordered QR rows a view actually renders.
func qrRowsOf(view string) []string {
	var rows []string
	for _, line := range strings.Split(stripANSI(view), "\n") {
		if row, ok := qrRowOf(line); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func loginQRFix(t *testing.T) (string, []string) {
	t.Helper()
	q, err := qrcode.New("https://passport.bilibili.com/h5/appeal?qrcode_key=abcdef0123456789abcdef0123456789", qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	qr := q.ToSmallString(false)
	return qr, strings.Split(strings.TrimRight(qr, "\n"), "\n")
}

func waitLoginFor(qr, imagePath string, width, height int) *loginModel {
	windowWidth, windowHeight = width, height
	m := newLoginModel(nil)
	m.step = loginStepWaitLogin
	m.loginData = &api.QRCodeLoginData{QRString: qr, Status: api.QRLoginNotScan, QRImagePath: imagePath}
	return &m
}

// The login QR must render complete and undistorted, or not at all — never
// truncated. Two real bugs made it unscannable:
//
//  1. The instruction line was concatenated with the absolute path of the saved
//     login.png. lipgloss.JoinVertical stretches every line to the widest line, so
//     a long path widened the QR block past the window; Place then became a no-op
//     and Bubble Tea's renderer truncated each QR row at the window edge, removing
//     modules from both sides of the code.
//  2. ToSmallString ends with a newline, adding a blank row that pushed the dialog
//     one row too tall for a standard 24-row console, scrolling the bottom of the
//     code off screen.
func TestLoginQRCodeRendersCompleteOrPrompts(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig = defaultConfig()

	qr, src := loginQRFix(t)
	srcWidth := lipgloss.Width(src[0])

	paths := []string{"", "login.png", `C:\Users\Tominysun\Downloads\bililive-windows-amd64\login.png`}
	sizes := [][2]int{
		{120, 40}, {100, 30}, {80, 24}, {80, 30}, {60, 24}, {45, 24},
		{43, 30}, {42, 30}, {30, 20}, {20, 10},
	}
	for _, size := range sizes {
		for _, path := range paths {
			width, height := size[0], size[1]
			view := stripANSI(waitLoginFor(qr, path, width, height).View())
			lines := strings.Split(view, "\n")
			rows := qrRowsOf(view)

			// The dialog needs a border column on each side of the QR, and enough
			// rows for the instructions plus the code plus the border.
			canFit := srcWidth+2 <= width && height >= loginSmallestHeight
			switch {
			case canFit:
				if len(rows) != len(src) {
					t.Errorf("%dx%d path=%d: rendered %d of %d QR rows", width, height, len([]rune(path)), len(rows), len(src))
					continue
				}
				for i := range src {
					if rows[i] != src[i] {
						t.Errorf("%dx%d path=%d: QR row %d differs from the source code", width, height, len([]rune(path)), i)
						break
					}
				}
			default:
				// Truncation is worse than no code: it looks scannable but is not.
				if len(rows) != 0 {
					t.Errorf("%dx%d is too small for the QR, but a truncated code was drawn", width, height)
				}
				if !strings.Contains(view, "放大窗口") {
					t.Errorf("%dx%d cannot show the QR but does not tell the user to enlarge the window", width, height)
				}
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > width {
					t.Errorf("%dx%d: line %d is %d cells wide, exceeding the terminal", width, height, i, w)
					break
				}
			}
			if len(lines) > height {
				t.Errorf("%dx%d: view is %d rows tall, exceeding the terminal", width, height, len(lines))
			}
		}
	}
}

// A standard 80x24 console must show the whole code without scrolling.
func TestLoginQRFitsStandard80x24Console(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig = defaultConfig()

	qr, src := loginQRFix(t)
	// Even with a long saved path, the code itself must stay complete.
	view := stripANSI(waitLoginFor(qr, `C:\Users\Tominysun\Downloads\bililive-v1.2.7-windows-amd64\login.png`, 80, 24).View())
	rows := qrRowsOf(view)
	if len(rows) != len(src) {
		t.Fatalf("80x24 renders %d of %d QR rows; the code must be complete", len(rows), len(src))
	}
	for i := range src {
		if rows[i] != src[i] {
			t.Fatalf("QR row %d differs at 80x24", i)
		}
	}
	if lines := strings.Split(view, "\n"); len(lines) > 24 {
		t.Fatalf("login view is %d rows, taller than a standard console", len(lines))
	}
}

// The QR block must stay rectangular: a single odd-width row means the code is
// skewed and will not scan.
func TestLoginQRRowsHaveUniformWidth(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig = defaultConfig()

	qr, src := loginQRFix(t)
	width := lipgloss.Width(src[0])
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}} {
		rows := qrRowsOf(waitLoginFor(qr, "login.png", size[0], size[1]).View())
		if len(rows) == 0 {
			t.Fatalf("%dx%d rendered no QR rows", size[0], size[1])
		}
		for i, row := range rows {
			if got := lipgloss.Width(row); got != width {
				t.Fatalf("%dx%d QR row %d is %d cells, want %d", size[0], size[1], i, got, width)
			}
		}
	}
}

// Resizing must be honored, because the QR needs a minimum width and the dialog
// is centered with Place(), which silently no-ops when the content is wider.
func TestLoginViewTracksTerminalResize(t *testing.T) {
	previous := LiveConfig
	defer func() { LiveConfig = previous }()
	LiveConfig = defaultConfig()
	windowWidth, windowHeight = 120, 40

	m := newLoginModel(nil)
	m.step = loginStepWaitLogin
	qr, _ := loginQRFix(t)
	m.loginData = &api.QRCodeLoginData{QRString: qr, Status: api.QRLoginNotScan}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(*loginModel)
	if windowWidth != 80 || windowHeight != 24 {
		t.Fatalf("login model ignored the resize: %dx%d", windowWidth, windowHeight)
	}
	if lines := strings.Split(stripANSI(got.View()), "\n"); len(lines) > 24 {
		t.Fatalf("resized login view is %d rows, taller than the 24-row window", len(lines))
	}
	// A window too narrow for the code must say so rather than draw it truncated.
	updated, _ = got.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	if rows := qrRowsOf(updated.(*loginModel).View()); len(rows) != 0 {
		t.Fatalf("narrow window drew %d truncated QR rows", len(rows))
	}
}
