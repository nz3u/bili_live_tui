package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shr-go/bili_live_tui/api"
)

type settingsField struct {
	key, label      string
	input           textinput.Model
	isBool, enabled bool
}

type settingsPanel struct {
	fields         []settingsField
	cursor, offset int
	width, height  int
	status         string
	saving         bool
	snapshot       configSnapshot
}

type settingsSavedMsg struct {
	panel        *settingsPanel
	config       api.BiliLiveConfig
	snapshot     configSnapshot
	enterDefault bool
	err          error
}

func newSettingsPanel(cfg api.BiliLiveConfig, snapshot configSnapshot) *settingsPanel {
	p := &settingsPanel{snapshot: snapshot}
	p.setValues(cfg)
	return p
}

func (p *settingsPanel) setValues(cfg api.BiliLiveConfig) {
	ids := make([]string, len(cfg.RoomIDs))
	for i, id := range cfg.RoomIDs {
		ids[i] = strconv.FormatUint(id, 10)
	}
	text := func(key, label, value string, limit int) settingsField {
		input := textinput.New()
		input.Prompt, input.CharLimit = "  ", limit
		input.SetValue(value)
		return settingsField{key: key, label: label, input: input}
	}
	boolean := func(key, label string, value bool) settingsField {
		return settingsField{key: key, label: label, isBool: true, enabled: value}
	}
	p.fields = []settingsField{
		text("room_id", "默认直播间 ID（空值使用列表首项）", strconv.FormatUint(cfg.RoomID, 10), 20),
		text("room_ids", "直播间列表（逗号分隔；空值只保留默认房间）", strings.Join(ids, ", "), 21000),
		text("room_switch_key", "切房快捷键（另可使用 F6）", roomSwitchKey(cfg), 30),
		text("settings_key", "设置快捷键（另可使用 F2）", settingsKey(cfg), 30),
		text("chat_buffer", "弹幕缓存条数（1–100000）", strconv.Itoa(cfg.ChatBuffer), 6),
		boolean("show_room_title", "显示直播间标题", cfg.ShowRoomTitle),
		boolean("show_room_number", "显示直播间编号", cfg.ShowRoomNumber),
		boolean("color_mode", "彩色弹幕", cfg.ColorMode),
		boolean("show_ship_level", "显示舰长等级", cfg.ShowShipLevel),
		boolean("show_medal_name", "显示勋章名称", cfg.ShowMedalName),
		boolean("show_medal_level", "显示勋章等级", cfg.ShowMedalLevel),
		text("user_agent", "User-Agent（留空使用内置浏览器标识）", cfg.UserAgent, 4096),
	}
}

func parseRoomIDs(value string) ([]uint64, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '，' || r == ';' || unicode.IsSpace(r) })
	ids := make([]uint64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseUint(part, 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("直播间列表包含无效 ID: %s", part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (p *settingsPanel) values() (api.BiliLiveConfig, error) {
	cfg := defaultConfig()
	for _, field := range p.fields {
		value := strings.TrimSpace(field.input.Value())
		switch field.key {
		case "room_id":
			if value == "" {
				cfg.RoomID = 0
				continue
			}
			id, err := strconv.ParseUint(value, 10, 64)
			if err != nil || id == 0 {
				return cfg, fmt.Errorf("默认直播间 ID 必须是正整数")
			}
			cfg.RoomID = id
		case "room_ids":
			ids, err := parseRoomIDs(value)
			if err != nil {
				return cfg, err
			}
			cfg.RoomIDs = ids
		case "room_switch_key":
			cfg.RoomSwitchKey = value
		case "settings_key":
			cfg.SettingsKey = value
		case "chat_buffer":
			n, err := strconv.Atoi(value)
			if err != nil {
				return cfg, fmt.Errorf("弹幕缓存条数必须是整数")
			}
			cfg.ChatBuffer = n
		case "show_room_title":
			cfg.ShowRoomTitle = field.enabled
		case "show_room_number":
			cfg.ShowRoomNumber = field.enabled
		case "color_mode":
			cfg.ColorMode = field.enabled
		case "show_ship_level":
			cfg.ShowShipLevel = field.enabled
		case "show_medal_name":
			cfg.ShowMedalName = field.enabled
		case "show_medal_level":
			cfg.ShowMedalLevel = field.enabled
		case "user_agent":
			cfg.UserAgent = field.input.Value()
		}
	}
	return normalizeConfig(cfg)
}

func (p *settingsPanel) resize(size tea.WindowSizeMsg) {
	p.width, p.height = max(1, size.Width), max(1, size.Height)
	for i := range p.fields {
		p.fields[i].input.Width = max(1, p.width-3)
		p.fields[i].input, _ = p.fields[i].input.Update(size)
	}
}

func (p *settingsPanel) focus(index int) tea.Cmd {
	for i := range p.fields {
		p.fields[i].input.Blur()
	}
	p.cursor = (index + len(p.fields) + 3) % (len(p.fields) + 3)
	if p.cursor < len(p.fields) && !p.fields[p.cursor].isBool {
		return p.fields[p.cursor].input.Focus()
	}
	return nil
}

func (p *settingsPanel) save(enterDefault bool) tea.Cmd {
	if p.saving {
		return nil
	}
	cfg, err := p.values()
	if err != nil {
		p.status = err.Error()
		return nil
	}
	p.saving, p.status = true, "正在保存…"
	snapshot := p.snapshot
	return func() tea.Msg {
		saved, err := saveConfig(snapshot, cfg)
		return settingsSavedMsg{panel: p, config: cfg, snapshot: saved, enterDefault: enterDefault, err: err}
	}
}

// update returns a command and whether the panel should close. Esc discards
// edits; while saving, closing is disabled so the result retains its owner.
func (p *settingsPanel) update(msg tea.Msg, switching bool) (tea.Cmd, bool) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if p.saving {
			return nil, false
		}
		switch key.String() {
		case "esc":
			return nil, true
		case "ctrl+s":
			return p.save(false), false
		case "tab", "down":
			return p.focus(p.cursor + 1), false
		case "shift+tab", "up":
			return p.focus(p.cursor - 1), false
		case "enter", "ctrl+j", " ", "left", "right":
			if p.cursor >= len(p.fields) {
				if key.String() == "left" {
					return p.focus(p.cursor - 1), false
				}
				if key.String() == "right" {
					return p.focus(p.cursor + 1), false
				}
				switch p.cursor - len(p.fields) {
				case 0:
					return p.save(false), false
				case 1:
					if switching {
						p.status = "正在切房，请稍后再保存并进入默认房间"
						return nil, false
					}
					return p.save(true), false
				case 2:
					return nil, true
				}
			}
			if p.fields[p.cursor].isBool {
				p.fields[p.cursor].enabled = !p.fields[p.cursor].enabled
				p.status = ""
				return nil, false
			}
			if key.String() == "enter" || key.String() == "ctrl+j" {
				return p.focus(p.cursor + 1), false
			}
		}
	}
	if p.cursor < len(p.fields) && !p.fields[p.cursor].isBool {
		var cmd tea.Cmd
		p.fields[p.cursor].input, cmd = p.fields[p.cursor].input.Update(msg)
		return cmd, false
	}
	return nil, false
}

func fitSettingsLine(text string, width int) string {
	return lipgloss.NewStyle().MaxWidth(max(1, width)).Render(strings.Join(strings.Fields(text), " "))
}

func (p *settingsPanel) view() string {
	if p.width < 20 || p.height < 7 {
		return fitSettingsLine("Settings: resize; Esc back", p.width)
	}
	rows := make([]string, 0, len(p.fields)*2+3)
	selectedStart, selectedEnd := 0, 0
	for i, field := range p.fields {
		prefix := "  "
		if i == p.cursor {
			prefix = "> "
			selectedStart = len(rows)
		}
		if field.isBool {
			value := "关"
			if field.enabled {
				value = "开"
			}
			rows = append(rows, fitSettingsLine(fmt.Sprintf("%s%s [%s]", prefix, field.label, value), p.width))
		} else {
			rows = append(rows, fitSettingsLine(prefix+field.label, p.width))
			// Keep input spaces/cursor intact; only the rendered width is clipped.
			rows = append(rows, lipgloss.NewStyle().MaxWidth(p.width).Render(field.input.View()))
		}
		if i == p.cursor {
			selectedEnd = len(rows)
		}
	}
	for i, label := range []string{"[ 保存 ]", "[ 保存并进入默认房间 ]", "[ 取消 / 关闭 ]"} {
		prefix := "  "
		if p.cursor == len(p.fields)+i {
			prefix = "> "
			selectedStart, selectedEnd = len(rows), len(rows)+1
		}
		rows = append(rows, fitSettingsLine(prefix+label, p.width))
	}
	bodyHeight := max(1, p.height-5)
	if p.offset > selectedStart {
		p.offset = selectedStart
	}
	if selectedEnd > p.offset+bodyHeight {
		p.offset = selectedEnd - bodyHeight
	}
	p.offset = min(max(0, p.offset), max(0, len(rows)-bodyHeight))
	end := min(len(rows), p.offset+bodyHeight)
	body := append([]string(nil), rows[p.offset:end]...)
	for len(body) < bodyHeight {
		body = append(body, "")
	}
	status := p.status
	if status == "" {
		status = "默认房间用于下次启动；也可选择保存并进入默认房间。"
	}
	lines := []string{
		fitSettingsLine("设置 · Tab/↑↓ 选择 · 空格/Enter 切换选项", p.width),
		fitSettingsLine("配置: "+p.snapshot.path, p.width),
	}
	lines = append(lines, body...)
	lines = append(lines,
		fitSettingsLine("Ctrl+S 保存 | Esc/F2 关闭（未保存的修改会丢弃）", p.width),
		fitSettingsLine(status, p.width),
		fitSettingsLine("列表/外观/快捷键立即生效；原文件含注释备份为 .bak", p.width))
	return strings.Join(lines, "\n")
}

func (m *model) openSettings() tea.Cmd {
	snapshot, err := readConfigSnapshot(configFilePath)
	if err != nil {
		m.sendStatus = "读取设置失败: " + err.Error()
		return nil
	}
	cfg := cloneConfig(LiveConfig)
	if snapshot.exists {
		cfg, err = decodeConfig(snapshot.raw)
		if err != nil {
			m.sendStatus = "读取设置失败: " + err.Error()
			return nil
		}
	}
	m.textInput.Blur()
	m.settings = newSettingsPanel(cfg, snapshot)
	m.settings.resize(m.windowSize)
	return m.settings.focus(0)
}

func (m *model) closeSettings() tea.Cmd {
	m.settings = nil
	if m.state == inputView {
		return m.textInput.Focus()
	}
	m.textInput.Blur()
	return nil
}
