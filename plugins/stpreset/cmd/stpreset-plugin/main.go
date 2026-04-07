package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"discord-bot-plugins/internal/shared"
	"discord-bot-plugins/sdk/pluginapi"
)

const (
	stPresetStoragePrefix = "stpreset:"
	stPresetHistoryKeep   = 48

	stPresetActionEnable            = "stpreset:enable"
	stPresetActionDisable           = "stpreset:disable"
	stPresetActionImportHelp        = "stpreset:import-help"
	stPresetActionEditContent       = "stpreset:edit-content"
	stPresetActionClear             = "stpreset:clear"
	stPresetActionRefresh           = "stpreset:refresh"
	stPresetActionSelectPrompt      = "stpreset:select-prompt"
	stPresetActionPromptToggle      = "stpreset:prompt-toggle"
	stPresetActionPromptEditMeta    = "stpreset:prompt-edit-meta"
	stPresetActionPromptEditContent = "stpreset:prompt-edit-content"
	stPresetActionPromptMoveUp      = "stpreset:prompt-up"
	stPresetActionPromptMoveDown    = "stpreset:prompt-down"
	stPresetActionSelectRegex       = "stpreset:select-regex"
	stPresetActionRegexGlobal       = "stpreset:regex-global"
	stPresetActionRegexToggle       = "stpreset:regex-toggle"
	stPresetActionRegexAdd          = "stpreset:regex-add"
	stPresetActionRegexImport       = "stpreset:regex-import"
	stPresetActionRegexDelete       = "stpreset:regex-delete"

	stPresetModalContent       = "stpreset:modal-content"
	stPresetModalPromptMeta    = "stpreset:modal-prompt-meta"
	stPresetModalPromptContent = "stpreset:modal-prompt-content"
	stPresetModalRegexAdd      = "stpreset:modal-regex-add"
	stPresetModalRegexImport   = "stpreset:modal-regex-import"

	stPresetFieldCharName         = "stpreset:field-char-name"
	stPresetFieldCharDescription  = "stpreset:field-char-description"
	stPresetFieldCharPersonality  = "stpreset:field-char-personality"
	stPresetFieldScenario         = "stpreset:field-scenario"
	stPresetFieldPersonaDesc      = "stpreset:field-persona-description"
	stPresetFieldDialogueExamples = "stpreset:field-dialogue-examples"
	stPresetFieldPromptName       = "stpreset:field-prompt-name"
	stPresetFieldPromptRole       = "stpreset:field-prompt-role"
	stPresetFieldPromptTriggers   = "stpreset:field-prompt-triggers"
	stPresetFieldPromptPosition   = "stpreset:field-prompt-position"
	stPresetFieldPromptContent    = "stpreset:field-prompt-content"
	stPresetFieldRegexName        = "stpreset:field-regex-name"
	stPresetFieldRegexOrder       = "stpreset:field-regex-order"
	stPresetFieldRegexPattern     = "stpreset:field-regex-pattern"
	stPresetFieldRegexReplace     = "stpreset:field-regex-replace"
	stPresetFieldRegexImport      = "stpreset:field-regex-import"
	stPresetFieldValueLimit       = 4000
	stPresetSourcePreviewRunes    = 180
	stPresetContentPreviewRunes   = 900
	stPresetDialoguePreviewRunes  = 360
	stPresetPresetSummaryRunes    = 500
	stPresetPresetValidationRunes = 220
	stPresetComponentPreviewRunes = 128
	stPresetPromptPreviewRunes    = 700
	stPresetRegexPreviewRunes     = 700
	stPresetPageSize              = 23
	stPresetPagePrevValue         = "__page_prev__"
	stPresetPageNextValue         = "__page_next__"
)

type presetState struct {
	Enabled            bool                      `json:"enabled"`
	PresetName         string                    `json:"preset_name,omitempty"`
	PresetJSON         string                    `json:"preset_json,omitempty"`
	PresetPath         string                    `json:"preset_path,omitempty"`
	CharName           string                    `json:"char_name,omitempty"`
	CharDescription    string                    `json:"char_description,omitempty"`
	CharPersonality    string                    `json:"char_personality,omitempty"`
	Scenario           string                    `json:"scenario,omitempty"`
	PersonaDescription string                    `json:"persona_description,omitempty"`
	DialogueExamples   string                    `json:"dialogue_examples,omitempty"`
	PromptOverrides    map[string]promptOverride `json:"prompt_overrides,omitempty"`
	PromptOrder        []string                  `json:"prompt_order,omitempty"`
	SelectedPrompt     string                    `json:"selected_prompt,omitempty"`
	PromptPage         int                       `json:"prompt_page,omitempty"`
	RegexEnabled       *bool                     `json:"regex_enabled,omitempty"`
	RegexDisabled      []string                  `json:"regex_disabled,omitempty"`
	CustomRegex        []customRegexRule         `json:"custom_regex,omitempty"`
	SelectedRegex      string                    `json:"selected_regex,omitempty"`
	RegexPage          int                       `json:"regex_page,omitempty"`
	UpdatedAt          string                    `json:"updated_at,omitempty"`
}

type presetPlugin struct {
	pluginapi.BasePlugin
	mu     sync.Mutex
	rand   *rand.Rand
	loaded map[string]cachedPreset
}

func newPresetPlugin() *presetPlugin {
	return &presetPlugin{
		rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
		loaded: map[string]cachedPreset{},
	}
}

func (p *presetPlugin) OnSlashCommand(ctx context.Context, host *pluginapi.HostClient, request pluginapi.SlashCommandRequest) (*pluginapi.InteractionResponse, error) {
	scope := scopeFromLocation(request.Guild.ID, request.Channel)
	if strings.TrimSpace(scope.GuildID) == "" {
		return ephemeralText("酒馆预设面板只能在服务器频道或子区中使用。"), nil
	}

	notice := ""
	if attachment := presetImportAttachment(request.Options); attachment != nil {
		if !request.User.IsAdmin {
			notice = "你没有权限导入预设文件。"
		} else {
			state, err := loadPresetState(ctx, host, scope)
			if err != nil {
				return nil, err
			}
			imported, err := p.importPresetAttachment(ctx, attachment)
			if err != nil {
				notice = "预设导入失败: " + shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes)
			} else {
				state.PresetName = imported.Name
				state.PresetJSON = imported.JSON
				state.PresetPath = ""
				if strings.TrimSpace(state.CharName) == "" {
					state.CharName = inferCharName(imported.Preset, imported.Name)
				}
				state.UpdatedAt = time.Now().Format(time.RFC3339)
				if err := savePresetState(ctx, host, scope, state); err != nil {
					return nil, err
				}
				notice = fmt.Sprintf("已导入预设文件 `%s`。", imported.Name)
			}
		}
	}

	message, err := p.buildPanel(ctx, host, scope, request.User, notice)
	if err != nil {
		return nil, err
	}
	message.Ephemeral = true
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeMessage,
		Message: message,
	}, nil
}

func (p *presetPlugin) OnComponent(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest) (*pluginapi.InteractionResponse, error) {
	scope := scopeFromLocation(request.Guild.ID, request.Channel)
	if strings.TrimSpace(scope.GuildID) == "" {
		return ephemeralText("酒馆预设面板只能在服务器频道或子区中使用。"), nil
	}

	state, err := loadPresetState(ctx, host, scope)
	if err != nil {
		return nil, err
	}

	action := strings.TrimSpace(request.CustomID)
	if presetActionRequiresAdmin(action) && !request.User.IsAdmin {
		return p.panelUpdate(ctx, host, scope, request.User, state, "你没有权限执行这个操作。")
	}

	switch action {
	case stPresetActionEnable:
		if !state.hasPresetSource() {
			return p.panelUpdate(ctx, host, scope, request.User, state, "请先使用 `/preset` 上传预设 JSON 文件。")
		}
		source, preset, err := p.loadPresetForState(state)
		if err != nil {
			return p.panelUpdate(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		state.Enabled = true
		state.CharName = firstNonEmpty(state.CharName, inferCharName(preset, source))
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, "已启用当前作用域的酒馆预设。")
	case stPresetActionDisable:
		state.Enabled = false
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, "已关闭当前作用域的酒馆预设。")
	case stPresetActionImportHelp:
		return p.panelUpdate(ctx, host, scope, request.User, state, "请重新执行 `/preset`，并在 `preset_file` 选项里上传 SillyTavern 预设 JSON 文件。")
	case stPresetActionEditContent:
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildPresetContentModal(state),
		}, nil
	case stPresetActionClear:
		if err := deletePresetState(ctx, host, scope); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, presetState{}, "已清空当前作用域的酒馆预设配置。")
	case stPresetActionRefresh:
		return p.panelUpdate(ctx, host, scope, request.User, state, "已刷新酒馆预设面板。")
	case stPresetActionSelectPrompt:
		if len(request.Values) == 0 {
			return p.panelUpdate(ctx, host, scope, request.User, state, "请选择一个预设条目。")
		}
		switch strings.TrimSpace(request.Values[0]) {
		case stPresetPagePrevValue:
			state.PromptPage--
		case stPresetPageNextValue:
			state.PromptPage++
		case "__empty__":
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可选条目。")
		default:
			state.SelectedPrompt = strings.TrimSpace(request.Values[0])
		}
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, "已切换当前条目选择。")
	case stPresetActionSelectRegex:
		if len(request.Values) == 0 {
			return p.panelUpdate(ctx, host, scope, request.User, state, "请选择一条正则规则。")
		}
		switch strings.TrimSpace(request.Values[0]) {
		case stPresetPagePrevValue:
			state.RegexPage--
		case stPresetPageNextValue:
			state.RegexPage++
		case "__empty__":
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可选正则。")
		default:
			state.SelectedRegex = strings.TrimSpace(request.Values[0])
		}
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, "已切换当前正则选择。")
	case stPresetActionPromptToggle:
		_, preset, err := p.loadPresetForState(state)
		if err != nil {
			return p.panelUpdate(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedPrompt(view, &state)
		if !ok {
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可切换的条目。")
		}
		override := state.PromptOverrides[item.Identifier]
		next := !item.Enabled
		override.Enabled = &next
		state.PromptOverrides[item.Identifier] = override
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, fmt.Sprintf("已将条目 `%s` 设置为%s。", item.Identifier, promptEnabledText(next)))
	case stPresetActionPromptMoveUp, stPresetActionPromptMoveDown:
		_, preset, err := p.loadPresetForState(state)
		if err != nil {
			return p.panelUpdate(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedPrompt(view, &state)
		if !ok {
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可移动的条目。")
		}
		delta := -1
		if action == stPresetActionPromptMoveDown {
			delta = 1
		}
		state.PromptOrder = movePromptIdentifier(state.PromptOrder, item.Identifier, delta, preset.orderedPromptEntries())
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, fmt.Sprintf("已调整条目 `%s` 的渲染位置。", item.Identifier))
	case stPresetActionPromptEditMeta:
		_, preset, err := p.loadPresetForState(state)
		if err != nil {
			return p.panelUpdate(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedPrompt(view, &state)
		if !ok {
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可编辑的条目。")
		}
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildPromptMetaModal(item),
		}, nil
	case stPresetActionPromptEditContent:
		_, preset, err := p.loadPresetForState(state)
		if err != nil {
			return p.panelUpdate(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedPrompt(view, &state)
		if !ok {
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可编辑的条目内容。")
		}
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildPromptContentModal(item),
		}, nil
	case stPresetActionRegexGlobal:
		enabled := !state.regexEngineEnabled()
		state.RegexEnabled = &enabled
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, fmt.Sprintf("已将正则总开关设置为%s。", boolIcon(enabled)))
	case stPresetActionRegexToggle:
		_, preset, err := p.loadPresetForState(state)
		if err != nil && state.hasPresetSource() {
			return p.panelUpdate(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedRegex(view, &state)
		if !ok {
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可切换的正则规则。")
		}
		if item.Custom {
			for index := range state.CustomRegex {
				if strings.TrimSpace(state.CustomRegex[index].ID) != item.ID {
					continue
				}
				state.CustomRegex[index].Enabled = !state.CustomRegex[index].Enabled
				break
			}
		} else {
			state.RegexDisabled = regexDisabledToggle(state.RegexDisabled, item.ID, !item.Enabled)
		}
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, "已更新当前正则规则状态。")
	case stPresetActionRegexAdd:
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildRegexAddModal(),
		}, nil
	case stPresetActionRegexImport:
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildRegexImportModal(),
		}, nil
	case stPresetActionRegexDelete:
		_, preset, err := p.loadPresetForState(state)
		if err != nil && state.hasPresetSource() {
			return p.panelUpdate(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedRegex(view, &state)
		if !ok {
			return p.panelUpdate(ctx, host, scope, request.User, state, "当前没有可删除的正则规则。")
		}
		if item.Custom {
			filtered := make([]customRegexRule, 0, len(state.CustomRegex))
			for _, rule := range state.CustomRegex {
				if strings.TrimSpace(rule.ID) == item.ID {
					continue
				}
				filtered = append(filtered, rule)
			}
			state.CustomRegex = filtered
		} else {
			state.RegexDisabled = regexDisabledToggle(state.RegexDisabled, item.ID, false)
		}
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelUpdate(ctx, host, scope, request.User, state, "已删除或屏蔽当前正则规则。")
	default:
		return p.panelUpdate(ctx, host, scope, request.User, state, "未知的酒馆预设操作。")
	}
}

func (p *presetPlugin) OnModal(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ModalRequest) (*pluginapi.InteractionResponse, error) {
	scope := scopeFromLocation(request.Guild.ID, request.Channel)
	if strings.TrimSpace(scope.GuildID) == "" {
		return ephemeralText("酒馆预设面板只能在服务器频道或子区中使用。"), nil
	}
	if !request.User.IsAdmin {
		message, err := p.buildPanel(ctx, host, scope, request.User, "你没有权限执行这个操作。")
		if err != nil {
			return nil, err
		}
		message.Ephemeral = true
		return &pluginapi.InteractionResponse{
			Type:    pluginapi.InteractionResponseTypeMessage,
			Message: message,
		}, nil
	}

	state, err := loadPresetState(ctx, host, scope)
	if err != nil {
		return nil, err
	}

	switch strings.TrimSpace(request.CustomID) {
	case stPresetModalContent:
		state.CharName = strings.TrimSpace(request.Fields[stPresetFieldCharName])
		state.CharDescription = strings.TrimSpace(request.Fields[stPresetFieldCharDescription])
		state.CharPersonality = strings.TrimSpace(request.Fields[stPresetFieldCharPersonality])
		state.Scenario = strings.TrimSpace(request.Fields[stPresetFieldScenario])
		state.PersonaDescription = strings.TrimSpace(request.Fields[stPresetFieldPersonaDesc])
		state.DialogueExamples = strings.TrimSpace(request.Fields[stPresetFieldDialogueExamples])
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelMessage(ctx, host, scope, request.User, state, "已更新当前作用域的预设内容参数。")
	case stPresetModalPromptMeta:
		_, preset, err := p.loadPresetForState(state)
		if err != nil {
			return p.panelMessage(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedPrompt(view, &state)
		if !ok {
			return p.panelMessage(ctx, host, scope, request.User, state, "当前没有可编辑的条目。")
		}
		override := state.PromptOverrides[item.Identifier]
		name := strings.TrimSpace(request.Fields[stPresetFieldPromptName])
		role := strings.TrimSpace(request.Fields[stPresetFieldPromptRole])
		override.Name = name
		override.NameSet = name != ""
		override.Role = role
		override.RoleSet = role != ""
		override.Triggers = parseTriggersInput(request.Fields[stPresetFieldPromptTriggers])
		override.TriggersSet = true
		state.PromptOverrides[item.Identifier] = override

		positionInput := strings.TrimSpace(request.Fields[stPresetFieldPromptPosition])
		if positionInput != "" {
			position, err := strconv.Atoi(positionInput)
			if err != nil || position <= 0 {
				return p.panelMessage(ctx, host, scope, request.User, state, "条目位置必须是大于 0 的整数。")
			}
			state.PromptOrder = movePromptIdentifierTo(state.PromptOrder, item.Identifier, position-1, preset.orderedPromptEntries())
		}
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelMessage(ctx, host, scope, request.User, state, fmt.Sprintf("已更新条目 `%s` 的名称、触发器与位置。", item.Identifier))
	case stPresetModalPromptContent:
		_, preset, err := p.loadPresetForState(state)
		if err != nil {
			return p.panelMessage(ctx, host, scope, request.User, state, "预设读取失败: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		view := buildPresetEditorView(state, preset)
		item, ok := selectedPrompt(view, &state)
		if !ok {
			return p.panelMessage(ctx, host, scope, request.User, state, "当前没有可编辑内容的条目。")
		}
		override := state.PromptOverrides[item.Identifier]
		override.Content = request.Fields[stPresetFieldPromptContent]
		override.ContentSet = true
		state.PromptOverrides[item.Identifier] = override
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelMessage(ctx, host, scope, request.User, state, fmt.Sprintf("已更新条目 `%s` 的内容。", item.Identifier))
	case stPresetModalRegexAdd:
		pattern := strings.TrimSpace(request.Fields[stPresetFieldRegexPattern])
		replacement := request.Fields[stPresetFieldRegexReplace]
		if pattern == "" {
			return p.panelMessage(ctx, host, scope, request.User, state, "正则 pattern 不能为空。")
		}
		if _, err := compilePresetRegex(pattern); err != nil {
			return p.panelMessage(ctx, host, scope, request.User, state, "正则 pattern 非法: "+shared.TruncateRunes(err.Error(), stPresetPresetValidationRunes))
		}
		order := 0
		orderInput := strings.TrimSpace(request.Fields[stPresetFieldRegexOrder])
		if orderInput != "" {
			parsed, err := strconv.Atoi(orderInput)
			if err != nil {
				return p.panelMessage(ctx, host, scope, request.User, state, "正则 order 必须是整数。")
			}
			order = parsed
		}
		name := strings.TrimSpace(request.Fields[stPresetFieldRegexName])
		rule := customRegexRule{
			ID:          makeRegexID("custom", name, len(state.CustomRegex), order, pattern, replacement),
			Name:        name,
			Order:       order,
			PatternSpec: pattern,
			Replacement: replacement,
			Enabled:     true,
		}
		state.CustomRegex = append(state.CustomRegex, rule)
		state.SelectedRegex = rule.ID
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelMessage(ctx, host, scope, request.User, state, "已新增自定义正则规则。")
	case stPresetModalRegexImport:
		items, err := parseRegexImportBlock(request.Fields[stPresetFieldRegexImport])
		if err != nil {
			return p.panelMessage(ctx, host, scope, request.User, state, err.Error())
		}
		state.CustomRegex = append(state.CustomRegex, items...)
		if len(items) > 0 {
			state.SelectedRegex = items[len(items)-1].ID
		}
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := savePresetState(ctx, host, scope, state); err != nil {
			return nil, err
		}
		return p.panelMessage(ctx, host, scope, request.User, state, fmt.Sprintf("已导入 %d 条正则规则。", len(items)))
	default:
		return p.panelMessage(ctx, host, scope, request.User, state, "未知的酒馆预设表单。")
	}
}

func (p *presetPlugin) OnMessage(ctx context.Context, host *pluginapi.HostClient, request pluginapi.MessageEvent) error {
	scope := scopeFromLocation(request.Message.Guild.ID, request.Message.Channel)
	if strings.TrimSpace(scope.GuildID) == "" {
		return nil
	}
	state, err := loadPresetState(ctx, host, scope)
	if err != nil {
		return err
	}
	if !state.Enabled {
		return nil
	}

	channelID := conversationChannelID(request.Message.Channel)
	if channelID == "" {
		return nil
	}
	if err := host.MemoryAppend(ctx, channelID, memoryMessageFromContext(request.Message, "user")); err != nil {
		return err
	}
	return host.MemoryTrimHistory(ctx, channelID, stPresetHistoryKeep)
}

func (p *presetPlugin) OnReplyCommitted(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ReplyCommittedRequest) error {
	scope := scopeFromLocation(request.TriggerMessage.Guild.ID, request.TriggerMessage.Channel)
	if strings.TrimSpace(scope.GuildID) == "" {
		return nil
	}
	state, err := loadPresetState(ctx, host, scope)
	if err != nil {
		return err
	}
	if !state.Enabled {
		return nil
	}

	channelID := conversationChannelID(request.ReplyMessage.Channel)
	if channelID == "" {
		return nil
	}
	if err := host.MemoryAppend(ctx, channelID, memoryMessageFromContext(request.ReplyMessage, "assistant")); err != nil {
		return err
	}
	return host.MemoryTrimHistory(ctx, channelID, stPresetHistoryKeep)
}

func (p *presetPlugin) OnContextBuild(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ContextBuildRequest) (*pluginapi.ContextBuildResponse, error) {
	scope := scopeFromLocation(request.CurrentMessage.Guild.ID, request.CurrentMessage.Channel)
	if strings.TrimSpace(scope.GuildID) == "" {
		return nil, nil
	}
	state, err := loadPresetState(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	if !state.Enabled || !state.hasPresetSource() {
		return nil, nil
	}

	source, preset, err := p.loadPresetForState(state)
	if err != nil {
		return nil, err
	}
	if state.CharName == "" {
		state.CharName = inferCharName(preset, source)
	}

	channelID := conversationChannelID(request.CurrentMessage.Channel)
	memories := []pluginapi.MemoryMessage{}
	if channelID != "" {
		stored, err := host.MemoryGet(ctx, channelID)
		if err != nil {
			return nil, err
		}
		memories = append(memories, stored.Messages...)
	}

	blocks, err := p.renderPromptBlocks(state, preset, request.CurrentMessage, memories)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("rendered SillyTavern preset produced no prompt blocks")
	}

	return &pluginapi.ContextBuildResponse{
		Override:             true,
		ReplaceSystemPrompt:  true,
		ReplacePersonaPrompt: true,
		PromptBlocks:         blocks,
	}, nil
}

func (p *presetPlugin) panelMessage(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, user pluginapi.UserInfo, state presetState, notice string) (*pluginapi.InteractionResponse, error) {
	message, err := p.buildPanel(ctx, host, scope, user, notice)
	if err != nil {
		return nil, err
	}
	message.Ephemeral = true
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeMessage,
		Message: message,
	}, nil
}

func (p *presetPlugin) panelUpdate(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, user pluginapi.UserInfo, state presetState, notice string) (*pluginapi.InteractionResponse, error) {
	message, err := p.buildPanel(ctx, host, scope, user, notice)
	if err != nil {
		return nil, err
	}
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeUpdate,
		Message: message,
	}, nil
}

func (p *presetPlugin) buildPanel(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, user pluginapi.UserInfo, notice string) (*pluginapi.InteractionMessage, error) {
	state, err := loadPresetState(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	state = normalizePresetState(state)

	presetStatus := "未设置"
	presetPreview := "当前还没有导入预设文件。"
	view := presetEditorView{}
	if state.hasPresetSource() {
		source, preset, loadErr := p.loadPresetForState(state)
		if loadErr != nil {
			presetStatus = "读取失败"
			presetPreview = shared.TruncateRunes(loadErr.Error(), stPresetPresetSummaryRunes)
		} else {
			presetStatus = "可用"
			presetPreview = buildPresetSummary(preset, source)
			view = buildPresetEditorView(state, preset)
			if state.CharName == "" {
				state.CharName = inferCharName(preset, source)
			}
		}
	}

	descriptionLines := []string{
		"使用 SillyTavern 风格的 JSON 预设接管当前作用域的提示词拼装。",
		"启用后将替换宿主原生的 system prompt 与 persona prompt，并按预设顺序组装上下文。",
		"导入方式：重新执行 `/preset`，并在 `preset_file` 选项里上传预设 JSON 文件。",
	}
	if !user.IsAdmin {
		descriptionLines = append(descriptionLines, "你当前只有查看权限。")
	}
	if strings.TrimSpace(notice) != "" {
		descriptionLines = append(descriptionLines, "提示: "+strings.TrimSpace(notice))
	}

	roleCard := buildRoleCardPreview(state)
	dialoguePreview := "未设置"
	if value := strings.TrimSpace(state.DialogueExamples); value != "" {
		dialoguePreview = "```text\n" + shared.TruncateRunes(value, stPresetDialoguePreviewRunes) + "\n```"
	}

	selectedPromptText := "当前没有可选条目。"
	if item, ok := selectedPrompt(view, &state); ok {
		triggerText := "无"
		if len(item.Triggers) > 0 {
			triggerText = strings.Join(item.Triggers, ", ")
		}
		selectedPromptText = strings.Join([]string{
			fmt.Sprintf("名称: %s", valueOrFallback(item.Name, item.Identifier)),
			fmt.Sprintf("标识: %s", item.Identifier),
			fmt.Sprintf("角色: %s", valueOrFallback(item.Role, "system")),
			fmt.Sprintf("状态: %s", promptEnabledText(item.Enabled)),
			fmt.Sprintf("位置: %d", item.Position),
			fmt.Sprintf("触发器: %s", triggerText),
			previewPromptContent(item.Content),
		}, "\n")
	}

	selectedRegexText := "当前没有可选正则。"
	if item, ok := selectedRegex(view, &state); ok {
		selectedRegexText = strings.Join([]string{
			fmt.Sprintf("名称: %s", valueOrFallback(item.Name, item.ID)),
			fmt.Sprintf("来源: %s", item.Source),
			fmt.Sprintf("Order: %d", item.Order),
			fmt.Sprintf("状态: %s", promptEnabledText(item.Enabled)),
			previewRegexBody(item.PatternSpec, item.Replacement),
		}, "\n")
	}

	return &pluginapi.InteractionMessage{
		Content: strings.TrimSpace(notice),
		Embeds: []pluginapi.Embed{
			{
				Title:       "酒馆预设控制台",
				Description: strings.Join(descriptionLines, "\n"),
				Color:       presetPanelColor(state.Enabled, presetStatus == "可用"),
				Fields: []pluginapi.EmbedField{
					{Name: "当前作用域", Value: scopeLabel(scope), Inline: false},
					{Name: "当前状态", Value: presetEnabledLabel(state.Enabled), Inline: true},
					{Name: "预设校验", Value: presetStatus, Inline: true},
					{Name: "预设来源", Value: presetSourcePreview(state), Inline: false},
					{Name: "预设摘要", Value: presetPreview, Inline: false},
					{Name: "角色名", Value: valueOrFallback(state.CharName, "未设置"), Inline: true},
					{Name: "角色卡参数", Value: roleCard, Inline: false},
					{Name: "对话示例", Value: dialoguePreview, Inline: false},
				},
				Footer: "当前作用域内的消息会被持续写入插件自己的上下文历史，供预设里的 chatHistory 宏位使用。",
			},
			{
				Title:       "条目调试",
				Description: "从当前有效 `prompt_order` 中选择条目，调整名称、触发器、内容与渲染位置。",
				Color:       0x3B82F6,
				Fields: []pluginapi.EmbedField{
					{Name: "条目总数", Value: fmt.Sprintf("%d", len(view.Prompts)), Inline: true},
					{Name: "当前页", Value: fmt.Sprintf("%d", maxInt(state.PromptPage+1, 1)), Inline: true},
					{Name: "当前选中条目", Value: selectedPromptText, Inline: false},
				},
			},
			{
				Title:       "正则调试",
				Description: "支持正则总开关、单条开关、删除、添加与批量导入。",
				Color:       0x111827,
				Fields: []pluginapi.EmbedField{
					{Name: "正则总开关", Value: boolIcon(state.regexEngineEnabled()), Inline: true},
					{Name: "规则总数", Value: fmt.Sprintf("%d", len(view.Regexes)), Inline: true},
					{Name: "当前选中正则", Value: selectedRegexText, Inline: false},
				},
			},
		},
		Components: []pluginapi.ActionRow{
			{
				Buttons: []pluginapi.Button{
					{CustomID: stPresetActionEnable, Label: "启用", Style: "success", Disabled: !user.IsAdmin || state.Enabled || !state.hasPresetSource()},
					{CustomID: stPresetActionDisable, Label: "停用", Style: "danger", Disabled: !user.IsAdmin || !state.Enabled},
					{CustomID: stPresetActionImportHelp, Label: "导入说明", Style: "secondary", Disabled: false},
					{CustomID: stPresetActionEditContent, Label: "编辑内容", Style: "primary", Disabled: !user.IsAdmin},
					{CustomID: stPresetActionClear, Label: "清空", Style: "danger", Disabled: !user.IsAdmin},
				},
			},
			{
				SelectMenus: []pluginapi.SelectMenu{
					buildPromptSelectMenu(view, &state),
				},
			},
			{
				Buttons: []pluginapi.Button{
					{CustomID: stPresetActionPromptToggle, Label: "条目开关", Style: "secondary", Disabled: !user.IsAdmin || len(view.Prompts) == 0},
					{CustomID: stPresetActionPromptEditMeta, Label: "改名称/触发器", Style: "primary", Disabled: !user.IsAdmin || len(view.Prompts) == 0},
					{CustomID: stPresetActionPromptEditContent, Label: "改内容", Style: "primary", Disabled: !user.IsAdmin || len(view.Prompts) == 0},
					{CustomID: stPresetActionPromptMoveUp, Label: "上移", Style: "secondary", Disabled: !user.IsAdmin || len(view.Prompts) == 0},
					{CustomID: stPresetActionPromptMoveDown, Label: "下移", Style: "secondary", Disabled: !user.IsAdmin || len(view.Prompts) == 0},
				},
			},
			{
				SelectMenus: []pluginapi.SelectMenu{
					buildRegexSelectMenu(view, &state),
				},
			},
			{
				Buttons: []pluginapi.Button{
					{CustomID: stPresetActionRegexGlobal, Label: "正则总开关", Style: "secondary", Disabled: !user.IsAdmin},
					{CustomID: stPresetActionRegexToggle, Label: "单条开关", Style: "secondary", Disabled: !user.IsAdmin || len(view.Regexes) == 0},
					{CustomID: stPresetActionRegexAdd, Label: "添加正则", Style: "primary", Disabled: !user.IsAdmin},
					{CustomID: stPresetActionRegexImport, Label: "导入正则", Style: "primary", Disabled: !user.IsAdmin},
					{CustomID: stPresetActionRegexDelete, Label: "删除正则", Style: "danger", Disabled: !user.IsAdmin || len(view.Regexes) == 0},
				},
			},
		},
	}, nil
}

func buildPresetContentModal(state presetState) *pluginapi.ModalResponse {
	return &pluginapi.ModalResponse{
		CustomID: stPresetModalContent,
		Title:    "编辑预设内容",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    stPresetFieldCharName,
				Label:       "角色名 {{char}}",
				Style:       "short",
				Placeholder: "留空则回退为导入文件名",
				Value:       limitFieldValue(state.CharName),
				MaxLength:   256,
			},
			{
				CustomID:    stPresetFieldCharDescription,
				Label:       "角色描述 charDescription",
				Style:       "paragraph",
				Placeholder: "角色设定、外观、背景等",
				Value:       limitFieldValue(state.CharDescription),
				MaxLength:   stPresetFieldValueLimit,
			},
			{
				CustomID:    stPresetFieldCharPersonality,
				Label:       "角色性格 charPersonality",
				Style:       "paragraph",
				Placeholder: "性格、说话习惯、表达方式等",
				Value:       limitFieldValue(state.CharPersonality),
				MaxLength:   stPresetFieldValueLimit,
			},
			{
				CustomID:    stPresetFieldScenario,
				Label:       "场景 scenario",
				Style:       "paragraph",
				Placeholder: "当前故事背景、关系、场面设定等",
				Value:       limitFieldValue(state.Scenario),
				MaxLength:   stPresetFieldValueLimit,
			},
			{
				CustomID:    stPresetFieldPersonaDesc,
				Label:       "用户人设 personaDescription",
				Style:       "paragraph",
				Placeholder: "当前用户/读者的人设补充",
				Value:       limitFieldValue(state.PersonaDescription),
				MaxLength:   stPresetFieldValueLimit,
			},
			{
				CustomID:    stPresetFieldDialogueExamples,
				Label:       "对话示例 dialogueExamples",
				Style:       "paragraph",
				Placeholder: "可选，给预设中的 dialogueExamples 使用",
				Value:       limitFieldValue(state.DialogueExamples),
				MaxLength:   stPresetFieldValueLimit,
			},
		},
	}
}

func buildPromptMetaModal(item promptEditorItem) *pluginapi.ModalResponse {
	return &pluginapi.ModalResponse{
		CustomID: stPresetModalPromptMeta,
		Title:    "编辑预设条目",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    stPresetFieldPromptName,
				Label:       "条目名称",
				Style:       "short",
				Placeholder: "留空则回退为原始名称",
				Value:       limitFieldValue(item.Name),
				MaxLength:   256,
			},
			{
				CustomID:    stPresetFieldPromptRole,
				Label:       "条目角色",
				Style:       "short",
				Placeholder: "例如 system / user / assistant",
				Value:       limitFieldValue(item.Role),
				MaxLength:   32,
			},
			{
				CustomID:    stPresetFieldPromptTriggers,
				Label:       "触发器",
				Style:       "paragraph",
				Placeholder: "逗号、分号或换行分隔；留空则清空触发器限制",
				Value:       limitFieldValue(strings.Join(item.Triggers, "\n")),
				MaxLength:   stPresetFieldValueLimit,
			},
			{
				CustomID:    stPresetFieldPromptPosition,
				Label:       "渲染位置",
				Style:       "short",
				Placeholder: "输入大于 0 的整数，例如 1",
				Value:       strconv.Itoa(item.Position),
				MaxLength:   16,
			},
		},
	}
}

func buildPromptContentModal(item promptEditorItem) *pluginapi.ModalResponse {
	return &pluginapi.ModalResponse{
		CustomID: stPresetModalPromptContent,
		Title:    "编辑条目内容",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    stPresetFieldPromptContent,
				Label:       "条目内容",
				Style:       "paragraph",
				Placeholder: "可直接编辑 Prompt 内容；支持宏和 <regex> 标签",
				Value:       limitFieldValue(item.Content),
				MaxLength:   stPresetFieldValueLimit,
			},
		},
	}
}

func buildRegexAddModal() *pluginapi.ModalResponse {
	return &pluginapi.ModalResponse{
		CustomID: stPresetModalRegexAdd,
		Title:    "添加正则规则",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    stPresetFieldRegexName,
				Label:       "规则名称",
				Style:       "short",
				Placeholder: "可选，用于面板显示",
				MaxLength:   256,
			},
			{
				CustomID:    stPresetFieldRegexOrder,
				Label:       "Order",
				Style:       "short",
				Placeholder: "整数，默认 0",
				Value:       "0",
				MaxLength:   16,
			},
			{
				CustomID:    stPresetFieldRegexPattern,
				Label:       "Pattern",
				Style:       "paragraph",
				Placeholder: "例如 /User: /gs 或普通 Go 正则",
				Required:    true,
				MaxLength:   stPresetFieldValueLimit,
			},
			{
				CustomID:    stPresetFieldRegexReplace,
				Label:       "Replacement",
				Style:       "paragraph",
				Placeholder: "替换内容",
				MaxLength:   stPresetFieldValueLimit,
			},
		},
	}
}

func buildRegexImportModal() *pluginapi.ModalResponse {
	return &pluginapi.ModalResponse{
		CustomID: stPresetModalRegexImport,
		Title:    "导入正则规则",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    stPresetFieldRegexImport,
				Label:       "导入内容",
				Style:       "paragraph",
				Placeholder: "支持粘贴 <regex order=N>...</regex>，或每行 `/pattern/flags => replacement`",
				Required:    true,
				MaxLength:   stPresetFieldValueLimit,
			},
		},
	}
}

func loadPresetState(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (presetState, error) {
	state := presetState{}
	_, err := host.StorageGet(ctx, scopeStorageKey(scope), &state)
	if err != nil {
		return presetState{}, err
	}
	return normalizePresetState(state), nil
}

func savePresetState(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, state presetState) error {
	state = normalizePresetState(state)
	state.UpdatedAt = firstNonEmpty(state.UpdatedAt, time.Now().Format(time.RFC3339))
	return host.StorageSet(ctx, scopeStorageKey(scope), state)
}

func deletePresetState(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) error {
	return host.StorageDelete(ctx, scopeStorageKey(scope))
}

func scopeStorageKey(scope pluginapi.PersonaScope) string {
	switch strings.TrimSpace(scope.Type) {
	case pluginapi.PersonaScopeThread:
		return stPresetStoragePrefix + "thread:" + strings.TrimSpace(scope.GuildID) + ":" + strings.TrimSpace(scope.ChannelID) + ":" + strings.TrimSpace(scope.ThreadID)
	case pluginapi.PersonaScopeChannel:
		return stPresetStoragePrefix + "channel:" + strings.TrimSpace(scope.GuildID) + ":" + strings.TrimSpace(scope.ChannelID)
	case pluginapi.PersonaScopeGuild:
		return stPresetStoragePrefix + "guild:" + strings.TrimSpace(scope.GuildID)
	default:
		return stPresetStoragePrefix + "global"
	}
}

func scopeFromLocation(guildID string, channel pluginapi.ChannelInfo) pluginapi.PersonaScope {
	guildID = strings.TrimSpace(guildID)
	channelID := strings.TrimSpace(channel.ID)
	threadID := strings.TrimSpace(channel.ThreadID)
	switch {
	case threadID != "":
		return pluginapi.PersonaScope{
			Type:      pluginapi.PersonaScopeThread,
			GuildID:   guildID,
			ChannelID: channelID,
			ThreadID:  threadID,
		}
	case channelID != "":
		return pluginapi.PersonaScope{
			Type:      pluginapi.PersonaScopeChannel,
			GuildID:   guildID,
			ChannelID: channelID,
		}
	case guildID != "":
		return pluginapi.PersonaScope{
			Type:    pluginapi.PersonaScopeGuild,
			GuildID: guildID,
		}
	default:
		return pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGlobal}
	}
}

func scopeLabel(scope pluginapi.PersonaScope) string {
	switch strings.TrimSpace(scope.Type) {
	case pluginapi.PersonaScopeThread:
		return fmt.Sprintf("子区 `%s` / 频道 `%s` / 服务器 `%s`", strings.TrimSpace(scope.ThreadID), strings.TrimSpace(scope.ChannelID), strings.TrimSpace(scope.GuildID))
	case pluginapi.PersonaScopeChannel:
		return fmt.Sprintf("频道 `%s` / 服务器 `%s`", strings.TrimSpace(scope.ChannelID), strings.TrimSpace(scope.GuildID))
	case pluginapi.PersonaScopeGuild:
		return fmt.Sprintf("服务器 `%s`", strings.TrimSpace(scope.GuildID))
	default:
		return "全局"
	}
}

func conversationChannelID(channel pluginapi.ChannelInfo) string {
	if threadID := strings.TrimSpace(channel.ThreadID); threadID != "" {
		return threadID
	}
	return strings.TrimSpace(channel.ID)
}

func memoryMessageFromContext(message pluginapi.MessageContext, role string) pluginapi.MemoryMessage {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "user"
	}
	return pluginapi.MemoryMessage{
		Role:    role,
		Guild:   message.Guild,
		Content: strings.TrimSpace(message.Content),
		Time:    strings.TrimSpace(message.Time),
		Author:  message.Author,
		ReplyTo: message.ReplyTo,
		Images:  append([]pluginapi.ImageReference(nil), message.Images...),
	}
}

func buildRoleCardPreview(state presetState) string {
	lines := []string{
		"角色描述: " + previewInlineField(state.CharDescription),
		"角色性格: " + previewInlineField(state.CharPersonality),
		"场景: " + previewInlineField(state.Scenario),
		"用户人设: " + previewInlineField(state.PersonaDescription),
	}
	return "```text\n" + shared.TruncateRunes(strings.Join(lines, "\n"), stPresetContentPreviewRunes) + "\n```"
}

func previewInlineField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "未设置"
	}
	return shared.TruncateRunes(shared.SingleLine(value), stPresetComponentPreviewRunes)
}

func presetSourcePreview(state presetState) string {
	switch {
	case strings.TrimSpace(state.PresetName) != "":
		return "```text\n" + shared.TruncateRunes(strings.TrimSpace(state.PresetName), stPresetSourcePreviewRunes) + "\n```"
	case strings.TrimSpace(state.PresetPath) != "":
		return "```text\n" + shared.TruncateRunes(strings.TrimSpace(state.PresetPath), stPresetSourcePreviewRunes) + "\n```"
	default:
		return "未导入"
	}
}

func presetEnabledLabel(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "已关闭"
}

func presetPanelColor(enabled, valid bool) int {
	switch {
	case enabled && valid:
		return 0x10B981
	case enabled:
		return 0xF59E0B
	case valid:
		return 0x3B82F6
	default:
		return 0x6B7280
	}
}

func buildPresetSummary(preset stPreset, path string) string {
	ordered := preset.orderedPrompts()
	names := make([]string, 0, len(ordered))
	for _, item := range ordered {
		name := firstNonEmpty(item.Name, item.Identifier)
		if name == "" {
			continue
		}
		names = append(names, name)
		if len(names) >= 8 {
			break
		}
	}
	summary := []string{
		"文件: " + filepath.Base(path),
		fmt.Sprintf("启用条目: %d", len(ordered)),
	}
	if len(names) > 0 {
		summary = append(summary, "前几个条目: "+strings.Join(names, " / "))
	}
	return "```text\n" + shared.TruncateRunes(strings.Join(summary, "\n"), stPresetPresetSummaryRunes) + "\n```"
}

func inferCharName(preset stPreset, path string) string {
	if value := strings.TrimSpace(preset.DefaultCharName); value != "" {
		return value
	}
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(path)), filepath.Ext(strings.TrimSpace(path)))
	return strings.TrimSpace(base)
}

func valueOrFallback(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func limitFieldValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= stPresetFieldValueLimit {
		return value
	}
	return string(runes[:stPresetFieldValueLimit])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func presetActionRequiresAdmin(action string) bool {
	switch strings.TrimSpace(action) {
	case stPresetActionEnable,
		stPresetActionDisable,
		stPresetActionEditContent,
		stPresetActionClear,
		stPresetActionPromptToggle,
		stPresetActionPromptEditMeta,
		stPresetActionPromptEditContent,
		stPresetActionPromptMoveUp,
		stPresetActionPromptMoveDown,
		stPresetActionRegexGlobal,
		stPresetActionRegexToggle,
		stPresetActionRegexAdd,
		stPresetActionRegexImport,
		stPresetActionRegexDelete:
		return true
	default:
		return false
	}
}

func ephemeralText(content string) *pluginapi.InteractionResponse {
	return &pluginapi.InteractionResponse{
		Type: pluginapi.InteractionResponseTypeMessage,
		Message: &pluginapi.InteractionMessage{
			Content:   strings.TrimSpace(content),
			Ephemeral: true,
		},
	}
}

func main() {
	manifest, err := pluginapi.ReadManifest("plugin.json")
	if err != nil {
		log.Fatal(err)
	}
	if err := pluginapi.Serve(manifest, newPresetPlugin()); err != nil {
		log.Fatal(err)
	}
}
