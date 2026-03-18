package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"discord-bot-plugins/internal/shared"
	"discord-bot-plugins/sdk/pluginapi"
)

const (
	personaStorageKey         = "persona_state"
	personaActionRefresh      = "persona:refresh"
	personaActionOpenUpsert   = "persona:open-upsert"
	personaActionOpenEdit     = "persona:open-edit-active"
	personaActionDeleteActive = "persona:delete-active"
	personaActionClearActive  = "persona:clear-active"
	personaActionUseSelect    = "persona:use-select"
	personaModalUpsert        = "persona:modal-upsert"
	personaModalEditActive    = "persona:modal-edit-active"
	personaFieldName          = "persona:field-name"
	personaFieldPrompt        = "persona:field-prompt"
	personaFieldEditPrompt    = "persona:field-edit-prompt"
	personaSelectOptionLimit  = 25
	personaPromptPreviewRunes = 700
	personaPromptOptionRunes  = 90
)

type legacyPersonaState struct {
	Personas map[string]string `json:"personas"`
	Active   string            `json:"active"`
}

type personaState struct {
	Scope    pluginapi.PersonaScope
	Personas map[string]pluginapi.PersonaEntry
	Active   string
}

type personaPlugin struct {
	pluginapi.BasePlugin
}

func (p *personaPlugin) Initialize(ctx context.Context, host *pluginapi.HostClient, request pluginapi.InitializeRequest) error {
	return migrateLegacyState(ctx, host)
}

func (p *personaPlugin) OnSlashCommand(ctx context.Context, host *pluginapi.HostClient, request pluginapi.SlashCommandRequest) (*pluginapi.InteractionResponse, error) {
	return personaPanelMessage(ctx, host, scopeFromLocation(request.Guild.ID, request.Channel), request.User, "")
}

func (p *personaPlugin) OnComponent(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest) (*pluginapi.InteractionResponse, error) {
	scope := scopeFromLocation(request.Guild.ID, request.Channel)
	state, err := loadPersonaState(ctx, host, scope)
	if err != nil {
		return nil, err
	}

	switch strings.TrimSpace(request.CustomID) {
	case personaActionRefresh:
		return personaPanelUpdate(state, request.User, "已刷新当前作用域的人设面板。"), nil
	case personaActionOpenUpsert:
		if !request.User.IsAdmin {
			return personaPanelUpdate(state, request.User, "你没有权限执行这个操作。"), nil
		}
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildPersonaUpsertModal(state),
		}, nil
	case personaActionOpenEdit:
		if !request.User.IsAdmin {
			return personaPanelUpdate(state, request.User, "你没有权限执行这个操作。"), nil
		}
		if strings.TrimSpace(state.Active) == "" {
			return personaPanelUpdate(state, request.User, "当前没有启用中的人设，无法编辑。"), nil
		}
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildPersonaEditModal(state),
		}, nil
	case personaActionDeleteActive:
		if !request.User.IsAdmin {
			return personaPanelUpdate(state, request.User, "你没有权限执行这个操作。"), nil
		}
		active := strings.TrimSpace(state.Active)
		if active == "" {
			return personaPanelUpdate(state, request.User, "当前没有启用中的人设，无法删除。"), nil
		}
		if err := host.PersonaDelete(ctx, state.Scope, active); err != nil {
			return nil, err
		}
		state, err = loadPersonaState(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		return personaPanelUpdate(state, request.User, fmt.Sprintf("已删除人设: %s", active)), nil
	case personaActionClearActive:
		if !request.User.IsAdmin {
			return personaPanelUpdate(state, request.User, "你没有权限执行这个操作。"), nil
		}
		if strings.TrimSpace(state.Active) == "" {
			return personaPanelUpdate(state, request.User, "当前没有启用中的人设。"), nil
		}
		if err := host.PersonaClearActive(ctx, state.Scope); err != nil {
			return nil, err
		}
		state, err = loadPersonaState(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		return personaPanelUpdate(state, request.User, "已清空当前启用人设。"), nil
	case personaActionUseSelect:
		if !request.User.IsAdmin {
			return personaPanelUpdate(state, request.User, "你没有权限执行这个操作。"), nil
		}
		if len(request.Values) == 0 {
			return personaPanelUpdate(state, request.User, "请选择一个人设。"), nil
		}
		name := normalizePersonaName(request.Values[0])
		if _, ok := state.Personas[name]; !ok {
			return personaPanelUpdate(state, request.User, "人设不存在。"), nil
		}
		if err := host.PersonaActivate(ctx, state.Scope, name); err != nil {
			return nil, err
		}
		state, err = loadPersonaState(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		return personaPanelUpdate(state, request.User, fmt.Sprintf("已切换到人设: %s", name)), nil
	default:
		return personaPanelUpdate(state, request.User, "未知的人设面板操作。"), nil
	}
}

func (p *personaPlugin) OnModal(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ModalRequest) (*pluginapi.InteractionResponse, error) {
	if !request.User.IsAdmin {
		return personaPanelMessage(ctx, host, scopeFromLocation(request.Guild.ID, request.Channel), request.User, "你没有权限执行这个操作。")
	}

	scope := scopeFromLocation(request.Guild.ID, request.Channel)
	state, err := loadPersonaState(ctx, host, scope)
	if err != nil {
		return nil, err
	}

	switch strings.TrimSpace(request.CustomID) {
	case personaModalUpsert:
		name := normalizePersonaName(request.Fields[personaFieldName])
		prompt := strings.TrimSpace(request.Fields[personaFieldPrompt])
		if name == "" || prompt == "" {
			return personaPanelMessage(ctx, host, scope, request.User, "人设名称和 Prompt 都不能为空。")
		}
		if err := host.PersonaUpsert(ctx, pluginapi.PersonaUpsertRequest{
			Scope:  scope,
			Name:   name,
			Prompt: prompt,
			Origin: "official_persona",
		}); err != nil {
			return nil, err
		}
		if err := host.PersonaActivate(ctx, scope, name); err != nil {
			return nil, err
		}
		return personaPanelMessage(ctx, host, scope, request.User, fmt.Sprintf("已保存并切换到人设: %s", name))
	case personaModalEditActive:
		active := strings.TrimSpace(state.Active)
		if active == "" {
			return personaPanelMessage(ctx, host, scope, request.User, "当前没有启用中的人设，无法编辑。")
		}
		prompt := strings.TrimSpace(request.Fields[personaFieldEditPrompt])
		if prompt == "" {
			return personaPanelMessage(ctx, host, scope, request.User, "当前人设 Prompt 不能为空。")
		}
		if err := host.PersonaUpsert(ctx, pluginapi.PersonaUpsertRequest{
			Scope:  scope,
			Name:   active,
			Prompt: prompt,
			Origin: "official_persona",
		}); err != nil {
			return nil, err
		}
		if err := host.PersonaActivate(ctx, scope, active); err != nil {
			return nil, err
		}
		return personaPanelMessage(ctx, host, scope, request.User, fmt.Sprintf("已更新当前人设: %s", active))
	default:
		return personaPanelMessage(ctx, host, scope, request.User, "未知的人设表单。")
	}
}

func personaPanelMessage(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, user pluginapi.UserInfo, notice string) (*pluginapi.InteractionResponse, error) {
	state, err := loadPersonaState(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeMessage,
		Message: buildPersonaPanel(state, user, notice, true),
	}, nil
}

func personaPanelUpdate(state personaState, user pluginapi.UserInfo, notice string) *pluginapi.InteractionResponse {
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeUpdate,
		Message: buildPersonaPanel(state, user, notice, false),
	}
}

func buildPersonaPanel(state personaState, user pluginapi.UserInfo, notice string, ephemeral bool) *pluginapi.InteractionMessage {
	names := personaNames(state)
	active := strings.TrimSpace(state.Active)
	activePrompt := ""
	if persona, ok := state.Personas[active]; ok {
		activePrompt = strings.TrimSpace(persona.Prompt)
	}

	description := []string{
		"管理当前作用域的人设。保存后，核心会直接把启用中的人设 Prompt 注入聊天上下文。",
		"当前作用域: " + scopeLabel(state.Scope),
	}
	if !user.IsAdmin {
		description = append(description, "你当前只有查看权限。")
	}
	if strings.TrimSpace(notice) != "" {
		description = append(description, "提示: "+strings.TrimSpace(notice))
	}

	fields := []pluginapi.EmbedField{
		{
			Name:   "当前启用",
			Value:  firstNonEmpty(active, "未启用"),
			Inline: true,
		},
		{
			Name:   "已保存数量",
			Value:  fmt.Sprintf("%d", len(names)),
			Inline: true,
		},
		{
			Name:   "当前 Prompt 预览",
			Value:  personaPromptPreview(activePrompt),
			Inline: false,
		},
	}
	if len(names) > 0 {
		fields = append(fields, pluginapi.EmbedField{
			Name:   "人设列表",
			Value:  personaNameList(names, active),
			Inline: false,
		})
	}

	return &pluginapi.InteractionMessage{
		Content:   strings.TrimSpace(notice),
		Ephemeral: ephemeral,
		Embeds: []pluginapi.Embed{
			{
				Title:       "Persona Manager",
				Description: strings.Join(description, "\n"),
				Color:       0x2563EB,
				Fields:      fields,
				Footer:      "当前面板只操作当前服务器/频道/子区作用域。",
			},
		},
		Components: buildPersonaComponents(state, user.IsAdmin),
	}
}

func buildPersonaComponents(state personaState, isAdmin bool) []pluginapi.ActionRow {
	active := strings.TrimSpace(state.Active)
	rows := []pluginapi.ActionRow{
		{
			Buttons: []pluginapi.Button{
				{CustomID: personaActionOpenUpsert, Label: "新增/覆盖", Style: "success", Disabled: !isAdmin},
				{CustomID: personaActionOpenEdit, Label: "编辑当前", Style: "primary", Disabled: !isAdmin || active == ""},
				{CustomID: personaActionDeleteActive, Label: "删除当前", Style: "danger", Disabled: !isAdmin || active == ""},
				{CustomID: personaActionClearActive, Label: "清空启用", Style: "secondary", Disabled: !isAdmin || active == ""},
				{CustomID: personaActionRefresh, Label: "刷新", Style: "primary"},
			},
		},
	}

	names := personaNames(state)
	if len(names) == 0 {
		return rows
	}
	options := make([]pluginapi.SelectOption, 0, len(names))
	for index, name := range names {
		if index >= personaSelectOptionLimit {
			break
		}
		entry := state.Personas[name]
		options = append(options, pluginapi.SelectOption{
			Label:       shared.TruncateRunes(name, 100),
			Value:       name,
			Description: shared.TruncateRunes(shared.SingleLine(entry.Prompt), personaPromptOptionRunes),
			Default:     name == state.Active,
		})
	}
	rows = append(rows, pluginapi.ActionRow{
		SelectMenus: []pluginapi.SelectMenu{
			{
				CustomID:    personaActionUseSelect,
				Placeholder: "选择一个人设并切换",
				Options:     options,
				MinValues:   1,
				MaxValues:   1,
				Disabled:    !isAdmin,
			},
		},
	})
	return rows
}

func buildPersonaUpsertModal(state personaState) *pluginapi.ModalResponse {
	active := strings.TrimSpace(state.Active)
	activePrompt := ""
	if persona, ok := state.Personas[active]; ok {
		activePrompt = strings.TrimSpace(persona.Prompt)
	}
	return &pluginapi.ModalResponse{
		CustomID: personaModalUpsert,
		Title:    "新增或覆盖人设",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    personaFieldName,
				Label:       "人设名称",
				Style:       "short",
				Placeholder: "例如：maid / assistant / character",
				Value:       active,
				Required:    true,
				MinLength:   1,
				MaxLength:   64,
			},
			{
				CustomID:    personaFieldPrompt,
				Label:       "人设 Prompt",
				Style:       "paragraph",
				Placeholder: "输入完整人设提示词，保存后会自动切换到这个人设。",
				Value:       activePrompt,
				Required:    true,
				MinLength:   1,
				MaxLength:   4000,
			},
		},
	}
}

func buildPersonaEditModal(state personaState) *pluginapi.ModalResponse {
	active := strings.TrimSpace(state.Active)
	title := "编辑当前人设"
	if active != "" {
		title = shared.TruncateRunes("编辑当前人设: "+active, 45)
	}
	activePrompt := ""
	if persona, ok := state.Personas[active]; ok {
		activePrompt = strings.TrimSpace(persona.Prompt)
	}
	return &pluginapi.ModalResponse{
		CustomID: personaModalEditActive,
		Title:    title,
		Fields: []pluginapi.ModalField{
			{
				CustomID:    personaFieldEditPrompt,
				Label:       "当前人设 Prompt",
				Style:       "paragraph",
				Placeholder: "修改当前启用人设的 Prompt 内容",
				Value:       activePrompt,
				Required:    true,
				MinLength:   1,
				MaxLength:   4000,
			},
		},
	}
}

func loadPersonaState(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (personaState, error) {
	response, err := host.PersonaList(ctx, scope)
	if err != nil {
		return personaState{}, err
	}
	state := personaState{
		Scope:    scope,
		Personas: map[string]pluginapi.PersonaEntry{},
		Active:   normalizePersonaName(response.Active),
	}
	for _, persona := range response.Personas {
		name := normalizePersonaName(persona.Name)
		prompt := strings.TrimSpace(persona.Prompt)
		if name == "" || prompt == "" {
			continue
		}
		persona.Name = name
		persona.Prompt = prompt
		persona.Origin = strings.TrimSpace(persona.Origin)
		persona.UpdatedAt = strings.TrimSpace(persona.UpdatedAt)
		state.Personas[name] = persona
	}
	if _, ok := state.Personas[state.Active]; !ok {
		state.Active = ""
	}
	return state, nil
}

func migrateLegacyState(ctx context.Context, host *pluginapi.HostClient) error {
	var legacy legacyPersonaState
	found, err := host.StorageGet(ctx, personaStorageKey, &legacy)
	if err != nil || !found {
		return err
	}
	globalScope := pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGlobal}
	for name, prompt := range legacy.Personas {
		name = normalizePersonaName(name)
		prompt = strings.TrimSpace(prompt)
		if name == "" || prompt == "" {
			continue
		}
		if err := host.PersonaUpsert(ctx, pluginapi.PersonaUpsertRequest{
			Scope:  globalScope,
			Name:   name,
			Prompt: prompt,
			Origin: "official_persona_legacy",
		}); err != nil {
			return err
		}
	}
	if active := normalizePersonaName(legacy.Active); active != "" {
		if err := host.PersonaActivate(ctx, globalScope, active); err != nil {
			return err
		}
	}
	return host.StorageDelete(ctx, personaStorageKey)
}

func personaNames(state personaState) []string {
	names := make([]string, 0, len(state.Personas))
	for name := range state.Personas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

func normalizePersonaName(value string) string {
	return strings.TrimSpace(value)
}

func personaPromptPreview(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "当前没有启用中的人设 Prompt。"
	}
	return "```text\n" + shared.TruncateRunes(prompt, personaPromptPreviewRunes) + "\n```"
}

func personaNameList(names []string, active string) string {
	items := make([]string, 0, len(names))
	for _, name := range names {
		label := name
		if name == active {
			label += " (当前)"
		}
		items = append(items, "- "+label)
	}
	return strings.Join(items, "\n")
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

func main() {
	manifest, err := pluginapi.ReadManifest("plugin.json")
	if err != nil {
		log.Fatal(err)
	}
	if err := pluginapi.Serve(manifest, &personaPlugin{}); err != nil {
		log.Fatal(err)
	}
}
