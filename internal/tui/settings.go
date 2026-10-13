package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	"github.com/shr-go/bili_live_tui/api"
)

var settingLabels = []string{"默认直播间（下次启动）", "直播间列表（逗号分隔）", "弹幕缓存条数", "显示房间标题", "显示房间号", "彩色显示", "显示舰长等级", "显示勋章名称", "显示勋章等级", "User-Agent（下次连接）"}

type settingsModel struct {
	fields    []textinput.Model
	selected  int
	errorText string
}

func newSettings(cfg api.BiliLiveConfig) *settingsModel {
	ids := make([]string, len(cfg.RoomIDs))
	for i, id := range cfg.RoomIDs {
		ids[i] = strconv.FormatUint(id, 10)
	}
	values := []string{strconv.FormatUint(cfg.RoomID, 10), strings.Join(ids, ", "), strconv.Itoa(cfg.ChatBuffer), strconv.FormatBool(cfg.ShowRoomTitle), strconv.FormatBool(cfg.ShowRoomNumber), strconv.FormatBool(cfg.ColorMode), strconv.FormatBool(cfg.ShowShipLevel), strconv.FormatBool(cfg.ShowMedalName), strconv.FormatBool(cfg.ShowMedalLevel), cfg.UserAgent}
	s := &settingsModel{fields: make([]textinput.Model, len(values))}
	for i, value := range values {
		s.fields[i] = textinput.New()
		s.fields[i].Prompt = ""
		s.fields[i].SetValue(value)
		s.fields[i].CharLimit = 4096
	}
	s.fields[0].Focus()
	return s
}
func (s *settingsModel) config() (api.BiliLiveConfig, error) {
	cfg := LiveConfig
	id, err := strconv.ParseUint(strings.TrimSpace(s.fields[0].Value()), 10, 64)
	if err != nil || id == 0 {
		return cfg, fmt.Errorf("默认直播间须为正整数")
	}
	cfg.RoomID = id
	cfg.RoomIDs = nil
	raw := strings.TrimSpace(s.fields[1].Value())
	if raw != "" {
		raw = strings.ReplaceAll(raw, "，", ",")
		for _, part := range strings.Split(raw, ",") {
			id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64)
			if err != nil || id == 0 {
				return cfg, fmt.Errorf("直播间列表须为逗号分隔的正整数")
			}
			cfg.RoomIDs = append(cfg.RoomIDs, id)
		}
	}
	cfg.ChatBuffer, err = strconv.Atoi(strings.TrimSpace(s.fields[2].Value()))
	if err != nil {
		return cfg, fmt.Errorf("缓存条数须为整数")
	}
	flags := []*bool{&cfg.ShowRoomTitle, &cfg.ShowRoomNumber, &cfg.ColorMode, &cfg.ShowShipLevel, &cfg.ShowMedalName, &cfg.ShowMedalLevel}
	for i, flag := range flags {
		*flag = s.fields[i+3].Value() == "true"
	}
	cfg.UserAgent = s.fields[9].Value()
	return cfg, validateConfig(cfg)
}
func isBooleanSetting(index int) bool { return index >= 3 && index <= 8 }
func (s *settingsModel) update(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "tab", "down", "shift+tab", "up":
		s.fields[s.selected].Blur()
		step := 1
		if key.String() == "up" || key.String() == "shift+tab" {
			step = -1
		}
		s.selected = (s.selected + step + len(s.fields)) % len(s.fields)
		return s.fields[s.selected].Focus()
	case "enter", " ", "left", "right":
		if isBooleanSetting(s.selected) {
			s.fields[s.selected].SetValue(strconv.FormatBool(s.fields[s.selected].Value() != "true"))
			return nil
		}
	}
	if isBooleanSetting(s.selected) {
		return nil
	}
	var cmd tea.Cmd
	s.fields[s.selected], cmd = s.fields[s.selected].Update(key)
	return cmd
}
func (s *settingsModel) view(width, height int) string {
	if height <= 0 {
		return ""
	}
	width = max(1, width)
	lines := make([]string, 0, height)
	// Scroll the form inside the existing viewport, never grow the main layout.
	start := 0
	if s.selected >= height {
		start = s.selected - height + 1
	}
	for i := start; i < len(s.fields) && len(lines) < height; i++ {
		prefix := "  "
		if i == s.selected {
			prefix = "> "
		}
		label := settingLabels[i] + ": "
		ti := s.fields[i]
		// Narrow terminals prioritize the selected value over the label.
		label = truncate.StringWithTail(label, uint(max(0, width/2-2)), "…")
		ti.Width = max(1, width-lipgloss.Width(prefix+label)-1)
		value := ti.View()
		if isBooleanSetting(i) {
			value = "[ ]"
			if ti.Value() == "true" {
				value = "[x]"
			}
		}
		lines = append(lines, truncate.StringWithTail(prefix+label+value, uint(width), ""))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
func (m model) settingsKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "f2":
		m.settings = nil
		return m, nil
	case "ctrl+s":
		cfg, err := m.settings.config()
		if err == nil {
			if loadedConfig == nil {
				err = fmt.Errorf("未加载配置文件")
			} else {
				err = loadedConfig.save(cfg)
			}
		}
		if err != nil {
			m.settings.errorText = singleLine(err.Error())
			return m, nil
		}
		LiveConfig = cfg
		m.settings = nil
		m.sendStatus = "设置已保存"
		valid := make(map[uint64]bool)
		for _, id := range configuredRooms(cfg) {
			valid[id] = true
		}
		for id, memory := range m.memories {
			if !valid[id] && id != m.roomID {
				delete(m.memories, id)
				continue
			}
			for memory.messages.Len() > cfg.ChatBuffer {
				memory.messages.Remove(memory.messages.Front())
			}
		}
		for m.danmu.Len() > cfg.ChatBuffer {
			m.danmu.Remove(m.danmu.Front())
		}
		return m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	default:
		return m, m.settings.update(key)
	}
}
