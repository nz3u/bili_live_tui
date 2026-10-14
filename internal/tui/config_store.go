package tui

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/shr-go/bili_live_tui/api"
)

func validateConfig(cfg api.BiliLiveConfig) error {
	if cfg.RoomID == 0 {
		return fmt.Errorf("room_id must be positive")
	}
	if len(cfg.RoomIDs) > 50 {
		return fmt.Errorf("room_ids may contain at most 50 rooms")
	}
	seen := make(map[uint64]bool)
	for _, id := range cfg.RoomIDs {
		if id == 0 {
			return fmt.Errorf("room_ids must contain only positive IDs")
		}
		if seen[id] {
			return fmt.Errorf("room_ids contains duplicate ID %d", id)
		}
		seen[id] = true
	}
	if cfg.ChatBuffer < 1 || cfg.ChatBuffer > 100000 {
		return fmt.Errorf("chat_buffer must be between 1 and 100000")
	}
	return nil
}

// configStore owns a snapshot, not LiveConfig. It never serializes credentials.
// Saves use optimistic conflict detection, not a cross-process filesystem lock.
type configStore struct {
	mu       sync.Mutex
	path     string
	baseline []byte
	info     os.FileInfo
	rename   func(string, string) error // os.Rename; injectable for failure tests
}

func openConfigStore(path string) (*configStore, api.BiliLiveConfig, error) {
	var cfg api.BiliLiveConfig
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, cfg, err
	}
	data, info, err := readConfigFile(absolute)
	if err != nil {
		return nil, cfg, err
	}
	if _, err = toml.Decode(string(data), &cfg); err != nil {
		return nil, cfg, fmt.Errorf("decode config: %w", err)
	}
	if err = validateConfig(cfg); err != nil {
		return nil, cfg, err
	}
	return &configStore{path: absolute, baseline: data, info: info, rename: os.Rename}, cfg, nil
}

func readConfigFile(path string) ([]byte, os.FileInfo, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("config must be a regular file (not a symlink)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(linkInfo, before) || !sameConfigInfo(before, after) {
		return nil, nil, fmt.Errorf("config changed while reading")
	}
	return data, after, nil
}

func sameConfigInfo(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

func (s *configStore) unchanged() error {
	data, info, err := readConfigFile(s.path)
	if err != nil {
		return fmt.Errorf("check config: %w", err)
	}
	if !sameConfigInfo(s.info, info) || !bytes.Equal(s.baseline, data) {
		return fmt.Errorf("config changed on disk; reload before saving")
	}
	return nil
}

func (s *configStore) save(cfg api.BiliLiveConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := s.unchanged(); err != nil {
		return err
	}
	data, err := rewriteConfig(s.baseline, cfg)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".config-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = f.Chmod(s.info.Mode().Perm()); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	// Capture the new baseline metadata before rename so success has no fallible
	// post-rename operation that could report failure after changing the file.
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = s.unchanged(); err != nil {
		return err
	}
	if err = s.rename(name, s.path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	s.baseline, s.info = data, info
	return nil
}

// configStatements finds logical TOML lines, respecting arrays, inline tables,
// comments and all four string forms. Input has already been TOML-validated.
func configStatements(text string) []string {
	var result []string
	start, depth := 0, 0
	var quote byte
	triple, comment := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if comment {
			if c != '\n' {
				continue
			}
			comment = false
		} else if quote != 0 {
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				if !triple {
					quote = 0
				} else if i+2 < len(text) && text[i+1] == quote && text[i+2] == quote {
					// TOML permits up to two quote characters immediately before the
					// closing triple delimiter.
					for i+1 < len(text) && text[i+1] == quote {
						i++
					}
					quote = 0
					triple = false
				}
			}
			continue
		} else {
			switch c {
			case '#':
				comment = true
			case '\'', '"':
				quote = c
				triple = i+2 < len(text) && text[i+1] == c && text[i+2] == c
				if triple {
					i += 2
				}
			case '[', '{':
				depth++
			case ']', '}':
				depth--
			}
		}
		if c == '\n' && depth == 0 && quote == 0 {
			result = append(result, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		result = append(result, text[start:])
	}
	return result
}

func rewriteConfig(original []byte, cfg api.BiliLiveConfig) ([]byte, error) {
	// Struct encoding is an explicit allowlist: cookies and unknown fields are
	// never obtained from runtime state or serialized here.
	if cfg.RoomIDs == nil {
		cfg.RoomIDs = []uint64{}
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(cfg); err != nil {
		return nil, err
	}
	values := make(map[string]string)
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(encoded.String()), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("unexpected encoded config")
		}
		key = strings.TrimSpace(key)
		order = append(order, key)
		values[key] = strings.TrimSpace(value)
	}
	newline := "\n"
	if bytes.Contains(original, []byte("\r\n")) {
		newline = "\r\n"
	}
	var out strings.Builder
	seen := make(map[string]bool)
	top := true
	for _, statement := range configStatements(string(original)) {
		trimmed := strings.TrimSpace(statement)
		if strings.HasPrefix(trimmed, "[") {
			top = false
		}
		if !top || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out.WriteString(statement)
			continue
		}
		// Try each '=' so quoted keys containing '=' remain opaque and intact.
		replaced := false
		for equal := 0; equal < len(statement); equal++ {
			if statement[equal] != '=' {
				continue
			}
			var keyMap map[string]interface{}
			if _, err := toml.Decode(statement[:equal]+"= 0", &keyMap); err != nil || len(keyMap) != 1 {
				continue
			}
			for key, val := range keyMap {
				value, known := values[key]
				if !known || val != int64(0) {
					continue
				}
				prefixEnd := equal + 1
				for prefixEnd < len(statement) && (statement[prefixEnd] == ' ' || statement[prefixEnd] == '\t') {
					prefixEnd++
				}
				out.WriteString(statement[:prefixEnd])
				out.WriteString(value)
				// Known values are numeric/bool/arrays.
				// Keep comment text, moving multiline-array comments to adjacent lines.
				comments := configValueComments(statement[equal+1:])
				for i, comment := range comments {
					if i == 0 {
						out.WriteByte(' ')
					} else {
						out.WriteString(newline)
					}
					out.WriteString(comment)
				}
				out.WriteString(newline)
				seen[key] = true
				replaced = true
			}
			if replaced {
				break
			}
		}
		if !replaced {
			out.WriteString(statement)
		}
	}
	var missing strings.Builder
	for _, key := range order {
		if !seen[key] {
			missing.WriteString(key + " = " + values[key] + newline)
		}
	}
	result := []byte(missing.String() + out.String())
	// Fail closed if unsupported syntax ever defeats the small lexical editor.
	var before, after map[string]interface{}
	if _, err := toml.Decode(string(original), &before); err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(result), &after); err != nil {
		return nil, fmt.Errorf("verify updated config: %w", err)
	}
	var want map[string]interface{}
	if _, err := toml.Decode(encoded.String(), &want); err != nil {
		return nil, err
	}
	for key := range values {
		if !reflect.DeepEqual(after[key], want[key]) {
			return nil, fmt.Errorf("could not update top-level key %s safely", key)
		}
		delete(before, key)
		delete(after, key)
	}
	if !sameConfigValue(reflect.ValueOf(before), reflect.ValueOf(after)) {
		return nil, fmt.Errorf("update would alter unknown config fields")
	}
	return result, nil
}

// TOML permits NaN in nested arrays and tables; DeepEqual alone considers
// even an unchanged NaN unequal to itself.
func sameConfigValue(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Interface:
		return sameConfigValue(a.Elem(), b.Elem())
	case reflect.Float64:
		return a.Float() == b.Float() || (math.IsNaN(a.Float()) && math.IsNaN(b.Float()))
	case reflect.Map:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return false
		}
		for _, key := range a.MapKeys() {
			if !sameConfigValue(a.MapIndex(key), b.MapIndex(key)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !sameConfigValue(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

func configValueComments(value string) []string {
	var comments []string
	var quote byte
	triple := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if quote != 0 {
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				if !triple {
					quote = 0
				} else if i+2 < len(value) && value[i+1] == quote && value[i+2] == quote {
					for i+1 < len(value) && value[i+1] == quote {
						i++
					}
					quote = 0
					triple = false
				}
			}
		} else if c == '\'' || c == '"' {
			quote = c
			triple = i+2 < len(value) && value[i+1] == c && value[i+2] == c
			if triple {
				i += 2
			}
		} else if c == '#' {
			end := strings.IndexByte(value[i:], '\n')
			if end < 0 {
				end = len(value) - i
			}
			comments = append(comments, strings.TrimSuffix(value[i:i+end], "\r"))
			i += end - 1
		}
	}
	return comments
}
