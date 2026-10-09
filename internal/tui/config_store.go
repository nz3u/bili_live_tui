package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/shr-go/bili_live_tui/api"
)

const maxConfigSize = 1024 * 1024

var configFilePath = "config.toml"

var knownConfigKeys = map[string]bool{
	"room_id": true, "room_ids": true, "room_switch_key": true, "settings_key": true,
	"chat_buffer": true, "user_agent": true, "show_room_title": true, "show_room_number": true,
	"color_mode": true, "show_ship_level": true, "show_medal_name": true, "show_medal_level": true,
}

type configSnapshot struct {
	path   string
	raw    []byte
	mode   os.FileMode
	exists bool
}

func defaultConfig() api.BiliLiveConfig {
	return api.BiliLiveConfig{
		RoomSwitchKey: "ctrl+n", SettingsKey: "f2", ChatBuffer: 200,
		ShowRoomTitle: true, ShowRoomNumber: true, ColorMode: true,
		ShowShipLevel: true, ShowMedalName: true, ShowMedalLevel: true,
	}
}

func cloneConfig(cfg api.BiliLiveConfig) api.BiliLiveConfig {
	cfg.RoomIDs = append([]uint64(nil), cfg.RoomIDs...)
	return cfg
}

func roomSwitchKey(cfg api.BiliLiveConfig) string {
	if key := strings.ToLower(strings.TrimSpace(cfg.RoomSwitchKey)); key != "" {
		return key
	}
	return "ctrl+n"
}

func settingsKey(cfg api.BiliLiveConfig) string {
	if key := strings.ToLower(strings.TrimSpace(cfg.SettingsKey)); key != "" {
		return key
	}
	return "f2"
}

func validateShortcut(key string) error {
	switch key {
	case "ctrl+c", "ctrl+i", "ctrl+j", "ctrl+m", "ctrl+h", "ctrl+s":
		return fmt.Errorf("快捷键 %s 与退出、输入或保存操作冲突", key)
	}
	if len(key) == 6 && strings.HasPrefix(key, "ctrl+") && key[5] >= 'a' && key[5] <= 'z' {
		return nil
	}
	if strings.HasPrefix(key, "f") {
		if n, err := strconv.Atoi(strings.TrimPrefix(key, "f")); err == nil && n >= 1 && n <= 12 && key == fmt.Sprintf("f%d", n) {
			return nil
		}
	}
	return fmt.Errorf("快捷键请使用 ctrl+字母 或 f1 至 f12（不能占用 Enter/Tab/Esc）")
}

func normalizeConfig(cfg api.BiliLiveConfig) (api.BiliLiveConfig, error) {
	cfg = cloneConfig(cfg)
	cfg.RoomSwitchKey, cfg.SettingsKey = roomSwitchKey(cfg), settingsKey(cfg)
	if cfg.RoomID == 0 && len(cfg.RoomIDs) > 0 {
		cfg.RoomID = cfg.RoomIDs[0]
	}
	if cfg.RoomID == 0 || cfg.RoomID > 1<<63-1 {
		return cfg, fmt.Errorf("默认直播间必须是有效的正整数 ID")
	}
	if len(cfg.RoomIDs) > 1000 {
		return cfg, fmt.Errorf("直播间列表最多包含 1000 个房间")
	}
	seen := make(map[uint64]bool)
	for _, id := range cfg.RoomIDs {
		if id == 0 || id > 1<<63-1 {
			return cfg, fmt.Errorf("直播间列表中的 ID 必须是有效正整数")
		}
		if seen[id] {
			return cfg, fmt.Errorf("直播间列表包含重复 ID: %d", id)
		}
		seen[id] = true
	}
	if cfg.ChatBuffer < 1 || cfg.ChatBuffer > 100000 {
		return cfg, fmt.Errorf("弹幕缓存条数必须在 1 至 100000 之间")
	}
	if err := validateShortcut(cfg.RoomSwitchKey); err != nil {
		return cfg, fmt.Errorf("切房快捷键: %w", err)
	}
	if err := validateShortcut(cfg.SettingsKey); err != nil {
		return cfg, fmt.Errorf("设置快捷键: %w", err)
	}
	if cfg.RoomSwitchKey == cfg.SettingsKey || cfg.RoomSwitchKey == "f2" || cfg.SettingsKey == "f6" {
		return cfg, fmt.Errorf("设置与切房快捷键不能冲突（F2 固定用于设置，F6 固定用于切房）")
	}
	if len(cfg.UserAgent) > 4096 || strings.IndexFunc(cfg.UserAgent, unicode.IsControl) >= 0 {
		return cfg, fmt.Errorf("User-Agent 不能包含控制字符（包括换行和 Tab），长度不能超过 4096 字节")
	}
	return cfg, nil
}

func decodeConfig(raw []byte) (api.BiliLiveConfig, error) {
	cfg := defaultConfig()
	metadata, err := toml.Decode(string(raw), &cfg)
	if err != nil {
		return cfg, fmt.Errorf("配置文件格式错误: %w", err)
	}
	seen := make(map[string]bool)
	for _, key := range metadata.Keys() {
		if len(key) != 1 {
			continue
		}
		canonical := strings.ToLower(key[0])
		if !knownConfigKeys[canonical] {
			continue
		}
		if seen[canonical] {
			return cfg, fmt.Errorf("配置项 %s 存在大小写重复，请只保留一个", canonical)
		}
		seen[canonical] = true
	}
	return normalizeConfig(cfg)
}

func readConfigSnapshot(path string) (configSnapshot, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return configSnapshot{}, err
	}
	snapshot := configSnapshot{path: absolute, mode: 0644}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if !info.Mode().IsRegular() {
		return snapshot, fmt.Errorf("配置路径不是普通文件（符号链接请直接指定目标文件）")
	}
	if info.Size() > maxConfigSize {
		return snapshot, fmt.Errorf("配置文件超过 1 MiB，无法安全编辑")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return snapshot, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return snapshot, err
	}
	if len(raw) > maxConfigSize {
		return snapshot, fmt.Errorf("配置文件超过 1 MiB，无法安全编辑")
	}
	snapshot.raw, snapshot.mode, snapshot.exists = raw, info.Mode().Perm(), true
	return snapshot, nil
}

func encodeConfig(raw []byte, cfg api.BiliLiveConfig) ([]byte, error) {
	// Keep unknown options/tables instead of silently deleting future or local settings.
	values := make(map[string]interface{})
	if len(raw) > 0 {
		if _, err := toml.Decode(string(raw), &values); err != nil {
			return nil, err
		}
	}
	// Struct decoding is case-insensitive. Remove old aliases before writing
	// canonical keys, otherwise the next load can randomly choose either value.
	for key := range values {
		if canonical := strings.ToLower(key); canonical != key && knownConfigKeys[canonical] {
			delete(values, key)
		}
	}
	values["room_id"], values["room_ids"] = cfg.RoomID, cfg.RoomIDs
	values["room_switch_key"], values["settings_key"] = cfg.RoomSwitchKey, cfg.SettingsKey
	values["chat_buffer"], values["user_agent"] = cfg.ChatBuffer, cfg.UserAgent
	values["show_room_title"], values["show_room_number"] = cfg.ShowRoomTitle, cfg.ShowRoomNumber
	values["color_mode"], values["show_ship_level"] = cfg.ColorMode, cfg.ShowShipLevel
	values["show_medal_name"], values["show_medal_level"] = cfg.ShowMedalName, cfg.ShowMedalLevel
	var out bytes.Buffer
	out.WriteString("# 由 TUI 设置面板保存；修改前的原文件（含注释）保存在同名 .bak 文件。\n# room_id 为默认房间；room_ids 为按顺序切换的列表。F2 设置，F6 切房。\n")
	if err := toml.NewEncoder(&out).Encode(values); err != nil {
		return nil, err
	}
	if out.Len() > maxConfigSize {
		return nil, fmt.Errorf("保存后的配置文件超过 1 MiB")
	}
	return out.Bytes(), nil
}

func snapshotUnchanged(snapshot configSnapshot) error {
	current, err := readConfigSnapshot(snapshot.path)
	if err != nil {
		return err
	}
	if current.exists != snapshot.exists || current.mode != snapshot.mode || !bytes.Equal(current.raw, snapshot.raw) {
		return fmt.Errorf("配置文件已被外部修改；请关闭并重新打开设置，避免覆盖其他修改")
	}
	return nil
}

func atomicWriteConfig(path string, raw []byte, mode os.FileMode, beforeReplace ...func() error) (err error) {
	if info, statErr := os.Lstat(path); statErr == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("保存目标不是普通文件: %s", path)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { file.Close(); os.Remove(name) }()
	if err = file.Chmod(mode); err != nil {
		return err
	}
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// Check after preparing/syncing the replacement, immediately before rename.
	// This detects external edits during IO too. Without cooperation from other
	// editors, portable filesystems still cannot provide an atomic compare-swap.
	for _, check := range beforeReplace {
		if err = check(); err != nil {
			return err
		}
	}
	// Rename replaces the old file; never remove the original before replacement.
	return os.Rename(name, path)
}

func saveConfig(snapshot configSnapshot, cfg api.BiliLiveConfig) (configSnapshot, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return snapshot, err
	}
	if err = snapshotUnchanged(snapshot); err != nil {
		return snapshot, err
	}
	raw, err := encodeConfig(snapshot.raw, cfg)
	if err != nil {
		return snapshot, err
	}
	if snapshot.exists {
		if err = atomicWriteConfig(snapshot.path+".bak", snapshot.raw, snapshot.mode); err != nil {
			return snapshot, fmt.Errorf("备份配置失败，原配置未修改: %w", err)
		}
	}
	if err = snapshotUnchanged(snapshot); err != nil {
		return snapshot, err
	}
	if err = atomicWriteConfig(snapshot.path, raw, snapshot.mode, func() error { return snapshotUnchanged(snapshot) }); err != nil {
		return snapshot, fmt.Errorf("保存配置失败: %w", err)
	}
	snapshot.raw, snapshot.exists = raw, true
	// Windows reports permissions as 0666/0444 rather than the requested Unix
	// mode, especially for newly created files. Remember the actual mode so a
	// second save does not mistake our own first save for an external chmod.
	if info, statErr := os.Stat(snapshot.path); statErr == nil {
		snapshot.mode = info.Mode().Perm()
	}
	return snapshot, nil
}

// Retain the user's list order. A default not in that list is prepended so it
// remains reachable; if the active room was removed, next switch starts at 0.
func configuredRooms(cfg api.BiliLiveConfig) []uint64 {
	ids := append([]uint64(nil), cfg.RoomIDs...)
	found := false
	for _, id := range ids {
		if id == cfg.RoomID {
			found = true
		}
	}
	if cfg.RoomID != 0 && !found {
		ids = append([]uint64{cfg.RoomID}, ids...)
	}
	seen := make(map[uint64]bool)
	result := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	return result
}

// Prefer the exact requested ID, not an old index or the first physical-room
// alias. This also works if settings reorder the list during network setup.
func roomIndexForRequest(room *api.LiveRoom, ids []uint64, requested uint64) int {
	if requested != 0 && (requested == room.RoomID || requested == room.ShortID) {
		for i, id := range ids {
			if id == requested {
				return i
			}
		}
	}
	return roomIndexInList(room, ids)
}

func initialRoomRequest(room *api.LiveRoom, cfg api.BiliLiveConfig) uint64 {
	if cfg.RoomID != 0 && (cfg.RoomID == room.RoomID || cfg.RoomID == room.ShortID) {
		return cfg.RoomID
	}
	return room.RoomID
}

func initialRoomIndex(room *api.LiveRoom, cfg api.BiliLiveConfig) int {
	return roomIndexForRequest(room, configuredRooms(cfg), initialRoomRequest(room, cfg))
}

func roomIndexInList(room *api.LiveRoom, ids []uint64) int {
	for index, id := range ids {
		if id == room.RoomID || (room.ShortID != 0 && id == room.ShortID) {
			return index
		}
	}
	return -1
}
