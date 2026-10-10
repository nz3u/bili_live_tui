package live_room

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/skip2/go-qrcode"
)

// login.png must land beside the executable rather than in whatever directory
// the process happened to start in. The old code wrote a bare relative path and
// discarded the error, so a directly launched binary (previously start.bat
// pinned the working directory) could leave the advertised file missing or in an
// unexpected place.
func TestWriteLoginQRImageUsesGivenDirectory(t *testing.T) {
	q, err := qrcode.New("https://example.invalid/login", qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := writeLoginQRImage(q, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("image written to %q, want directory %q", path, dir)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("returned path %q is not absolute; the UI cannot tell the user where the file is", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("advertised image does not exist: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("image is empty")
	}
	// A real PNG, so the file is actually scannable.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 8 || string(raw[1:4]) != "PNG" {
		t.Fatalf("file is not a PNG: %q", raw[:min(8, len(raw))])
	}
}

// A directory that cannot be written to must surface an error so the login
// screen can say the fallback file is unavailable, instead of silently
// promising a file that does not exist.
func TestWriteLoginQRImageReportsFailure(t *testing.T) {
	q, err := qrcode.New("https://example.invalid/login", qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	if _, err := writeLoginQRImage(q, missing); err == nil {
		t.Fatal("expected an error for an unwritable directory")
	}
}

func TestQRImageDirFallsBackToWorkingDirectory(t *testing.T) {
	// The default resolver must always return a usable directory.
	if dir := qrImageDir(); dir == "" {
		t.Fatal("qrImageDir returned an empty directory")
	}
}
