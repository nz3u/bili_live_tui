package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/shr-go/bili_live_tui/api"
)

func configFixture(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0640); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigValidation(t *testing.T) {
	good := api.BiliLiveConfig{RoomID: 1, ChatBuffer: 100000, RoomIDs: []uint64{1, 2}, UserAgent: "Agent/1 中文"}
	if err := validateConfig(good); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*api.BiliLiveConfig)
	}{
		{"room zero", func(c *api.BiliLiveConfig) { c.RoomID = 0 }},
		{"list zero", func(c *api.BiliLiveConfig) { c.RoomIDs = []uint64{0} }},
		{"duplicate", func(c *api.BiliLiveConfig) { c.RoomIDs = []uint64{2, 2} }},
		{"too many", func(c *api.BiliLiveConfig) {
			c.RoomIDs = make([]uint64, 51)
			for i := range c.RoomIDs {
				c.RoomIDs[i] = uint64(i + 1)
			}
		}},
		{"buffer zero", func(c *api.BiliLiveConfig) { c.ChatBuffer = 0 }},
		{"buffer negative", func(c *api.BiliLiveConfig) { c.ChatBuffer = -1 }},
		{"buffer large", func(c *api.BiliLiveConfig) { c.ChatBuffer = 100001 }},
	}
	for _, control := range []string{"\n", "\r", "\t", "\x00", "\x7f", "\u0085", "\u2028", "\u2029"} {
		char := control
		cases = append(cases, struct {
			name   string
			change func(*api.BiliLiveConfig)
		}{"UA " + control, func(c *api.BiliLiveConfig) { c.UserAgent = "a" + char + "b" }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.change(&cfg)
			if validateConfig(cfg) == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	good.ChatBuffer = 1
	good.RoomIDs = make([]uint64, 50)
	for i := range good.RoomIDs {
		good.RoomIDs[i] = uint64(i + 1)
	}
	if err := validateConfig(good); err != nil {
		t.Fatal(err)
	}
}

func TestConfigPreservesUnknownAndMultiline(t *testing.T) {
	original := "# header\nroom_id = 1 # active\nroom_ids = [ # favorites\n  1, # first\n  2,\n] # end rooms\nchat_buffer = 5\nuser_agent = 'old#agent' # UA\nunknown = { room_id = 88, text = 'stay' } # unknown\ncookies = 'preexisting untouched'\nmultiline = '''\n[not_a_table]\nroom_id = 200\n'''\n[extra]\nroom_id = 99 # nested\nroom_ids = [77]\nchat_buffer = 8\n"
	path := configFixture(t, original)
	before, _ := os.Stat(path)
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RoomID = 3
	cfg.RoomIDs = []uint64{3, 4}
	cfg.ChatBuffer = 100
	cfg.UserAgent = "new#agent \"quoted\" \\ ok"
	cfg.ShowRoomTitle = true
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, preserved := range []string{"# header", "# active", "# favorites", "# first", "# end rooms", "# UA", "unknown = { room_id = 88, text = 'stay' } # unknown", "cookies = 'preexisting untouched'", "multiline = '''\n[not_a_table]\nroom_id = 200\n'''", "[extra]\nroom_id = 99 # nested\nroom_ids = [77]\nchat_buffer = 8\n"} {
		if !strings.Contains(string(data), preserved) {
			t.Errorf("lost %q in %s", preserved, data)
		}
	}
	var got api.BiliLiveConfig
	if _, err := toml.Decode(string(data), &got); err != nil {
		t.Fatal(err)
	}
	if got.RoomID != 3 || len(got.RoomIDs) != 2 || got.RoomIDs[1] != 4 || got.UserAgent != cfg.UserAgent || !got.ShowRoomTitle {
		t.Fatalf("wrong config: %+v", got)
	}
	after, _ := os.Stat(path)
	if before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("mode changed: %v -> %v", before.Mode(), after.Mode())
	}
	// A second save must use the new baseline, and nil clears an existing list.
	cfg.RoomIDs = nil
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	_, got, err = openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RoomIDs) != 0 {
		t.Fatal("nil list did not clear room_ids")
	}
}

func TestConfigQuotedKeysAndCRLF(t *testing.T) {
	path := configFixture(t, "\"room_id\" = 1 # id\r\n'chat_buffer' = 2\r\n\"unknown=key\" = \"unchanged\"\r\n[extra]\r\nroom_id = 40\r\n")
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RoomID = 2
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("\"room_id\" = 2 # id\r\n")) || !bytes.Contains(data, []byte("\"unknown=key\" = \"unchanged\"\r\n")) {
		t.Fatalf("unexpected result %s", data)
	}
	if strings.Contains(strings.ReplaceAll(string(data), "\r\n", ""), "\n") {
		t.Fatal("introduced LF into CRLF file")
	}
}

func TestConfigExternalChange(t *testing.T) {
	path := configFixture(t, "room_id = 1\nchat_buffer = 2\n")
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("room_id = 9\nchat_buffer = 2\n")
	// Even a same-size change whose timestamp is restored must be detected.
	info, _ := os.Stat(path)
	if err := os.WriteFile(path, changed, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := store.save(cfg); err == nil {
		t.Fatal("expected conflict")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, changed) {
		t.Fatal("external edit overwritten")
	}
}

func TestConfigFailedSavePreservesOriginal(t *testing.T) {
	original := "room_id = 1\nchat_buffer = 2\n# original\n"
	path := configFixture(t, original)
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RoomID = 2
	store.rename = func(_, _ string) error { return errors.New("injected rename failure") }
	if err := store.save(cfg); err == nil {
		t.Fatal("expected failure")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatal("original damaged")
	}
	if string(store.baseline) != original {
		t.Fatal("baseline changed on failure")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*.tmp"))
	if len(files) != 0 {
		t.Fatalf("temporary files leaked: %v", files)
	}
	store.rename = os.Rename
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ChatBuffer = 0
	saved, _ := os.ReadFile(path)
	if err := store.save(cfg); err == nil {
		t.Fatal("expected validation failure")
	}
	got, _ = os.ReadFile(path)
	if !bytes.Equal(saved, got) {
		t.Fatal("invalid save changed file")
	}
}

func TestConfigOpenErrors(t *testing.T) {
	for _, text := range []string{"not toml!", "room_id = 0\nchat_buffer = 2", "room_id = 1\nchat_buffer = 0", "room_id = 1\nroom_ids = [1,1]\nchat_buffer = 2"} {
		if _, _, err := openConfigStore(configFixture(t, text)); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	if _, _, err := openConfigStore(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, _, err := openConfigStore(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestConfigUnusualLegalTOML(t *testing.T) {
	unknown := `"key=with#punctuation" = "opaque # = [ ]"
"" = "empty key"
dotted . "room_id" = 91
nan_value = nan
numbers = [nan, +nan, -nan, +inf, -inf, 0xCA_FE, 0o755, 0b1010]
inline = { nested = { value = nan }, room_id = 90 }
when = 1979-05-27T07:32:00Z
local = 07:32:00
basic = """
[not.a.table]
room_id = 42
ending with two quotes"""""
literal = '''
# not a comment
room_id = 43
ending with two quotes'''''
continued = """first\
   second"""
array = [
  "# [ =", # keep array layout
  { item = nan },
]
`
	tail := `[[servers]]
room_id = 55
value = nan
[servers.nested]
chat_buffer = 77
[[servers]]
room_id = 66
value = "no final newline"`
	path := configFixture(t, unknown+"\"room_id\" = 1 # active\nchat_buffer = 2\nuser_agent = '''old''' # keep UA comment\n"+tail)
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RoomID = 7
	cfg.UserAgent = "new # agent"
	cfg.ShowShipLevel, cfg.ShowMedalName, cfg.ShowMedalLevel = true, true, true
	cfg.ColorMode, cfg.ShowRoomTitle, cfg.ShowRoomNumber = true, true, true
	if err := store.save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), unknown) || !strings.HasSuffix(string(data), tail) {
		t.Fatalf("unknown TOML not preserved verbatim:\n%s", data)
	}
	_, got, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RoomID != cfg.RoomID || got.UserAgent != cfg.UserAgent || !got.ShowShipLevel || !got.ShowMedalName || !got.ShowMedalLevel || !got.ColorMode || !got.ShowRoomTitle || !got.ShowRoomNumber {
		t.Fatalf("wrong saved values: %+v", got)
	}
	if err := store.save(got); err != nil {
		t.Fatalf("second save: %v", err)
	}
}

func TestConfigExternalReplacementAndRemoval(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove=%v", remove), func(t *testing.T) {
			original := "room_id = 1\nchat_buffer = 2\n"
			path := configFixture(t, original)
			store, cfg, err := openConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(filepath.Dir(path), "replacement.toml")
			if !remove {
				if err := os.WriteFile(replacement, []byte(original), 0640); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, store.info.ModTime(), store.info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if !remove {
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			}
			cfg.RoomID = 2
			if err := store.save(cfg); err == nil {
				t.Fatal("expected external file conflict")
			}
			if string(store.baseline) != original {
				t.Fatal("baseline changed on conflict")
			}
			if remove {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("missing file recreated")
				}
			} else {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != original {
					t.Fatal("replacement changed")
				}
			}
		})
	}
}

func TestConfigModeChangeConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions test")
	}
	path := configFixture(t, "room_id = 1\nchat_buffer = 2\n")
	store, cfg, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.save(cfg); err == nil {
		t.Fatal("permission change not detected")
	}
}
