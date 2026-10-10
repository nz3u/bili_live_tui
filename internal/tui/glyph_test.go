package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// driftingGlyphs are characters the Windows console draws with a different cell
// width than go-runewidth reports, which shifts every cell after them. U+00B7
// middle dot is the known case: conhost advances 2 cells while runewidth says 1,
// so the shortcut hint that used three of them measured 3 cells narrower than the
// console drew.
var driftingGlyphs = map[string]string{
	"·": "U+00B7 middle dot (console draws 2 cells, runewidth says 1); use U+2022 • instead",
}

// Lipgloss measures view strings with go-runewidth, so a drifting glyph in any
// user-facing literal silently breaks alignment on Windows no matter how the
// console is configured. Scan the package sources so a new literal cannot
// reintroduce one.
func TestUIStringsAvoidDriftingGlyphs(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		// Only the shipped UI code, not tests (tests legitimately name the
		// drifting glyph to document and detect it).
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for lineNo, line := range strings.Split(string(raw), "\n") {
			// Comments document the quirk; only flag string literals.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for glyph, why := range driftingGlyphs {
				if strings.Contains(line, glyph) {
					t.Errorf("%s:%d uses a width-drifting glyph: %s", name, lineNo+1, why)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files were scanned")
	}
}
