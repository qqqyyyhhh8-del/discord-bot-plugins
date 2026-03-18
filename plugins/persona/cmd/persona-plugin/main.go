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

type personaState struct {
	Personas map[string]string `json:"personas"`
	Active   string            `json:"active"`
}

type personaPlugin struct {
	pluginapi.BasePlugin
}

func (p *personaPlugin) OnSlashCommand(ctx context.Context, host *pluginapi.HostClient, request pluginapi.SlashCommandRequest) (*pluginapi.InteractionResponse, error) {
	return personaPanelMessage(ctx, host, request.User, "")
}

func (p *personaPlugin) OnComponent(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest) (*pluginapi.InteractionResponse, error) {
	state, err := loadPersonaState(ctx, host)
	if err != nil {
		return nil, err
	}

	switch strings.TrimSpace(request.CustomID) {
	case personaActionRefresh:
		return personaPanelUpdate(state, request.User, "已刷新人设面板。"), nil
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
		delete(state.Personas, active)
		state.Active = ""
		if err := savePersonaState(ctx, host, state); err != nil {
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
		state.Active = ""
		if err := savePersonaState(ctx, host, state); err != nil {
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
		name := strings.TrimSpace(request.Values[0])
		if _, ok := state.Personas[name]; !ok {
			return personaPanelUpdate(state, request.User, "人设不存在。"), nil
		}
		state.Active = name
		if err := savePersonaState(ctx, host, state); err != nil {
			return nil, err
		}
		return personaPanelUpdate(state, request.User, fmt.Sprintf("已切换到人设: %s", name)), nil
	default:
		return personaPanelUpdate(state, request.User, "未知的人设面板操作。"), nil
	}
}

func (p *personaPlugin) OnModal(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ModalRequest) (*pluginapi.InteractionResponse, error) {
	if !request.User.IsAdmin {
		return personaPanelMessage(ctx, host, request.User, "你没有权限执行这个操作。")
	}

	state, err := loadPersonaState(ctx, host)
	if err != nil {
		return nil, err
	}

	switch strings.TrimSpace(request.CustomID) {
	case personaModalUpsert:
		name := normalizePersonaName(request.Fields[personaFieldName])
		prompt := strings.TrimSpace(request.Fields[personaFieldPrompt])
		if name == "" || prompt == "" {
			return personaPanelMessage(ctx, host, request.User, "人设名称和 Prompt 都不能为空。")
		}
		state.Personas[name] = prompt
		state.Active = name
		if err := savePersonaState(ctx, host, state); err != nil {
			return nil, err
		}
		return personaPanelMessage(ctx, host, request.User, fmt.Sprintf("已保存并切换到人设: %s", name))
	case personaModalEditActive:
		active := strings.TrimSpace(state.Active)
		if active == "" {
			return personaPanelMessage(ctx, host, request.User, "当前没有启用中的人设，无法编辑。")
		}
		prompt := strings.TrimSpace(request.Fields[personaFieldEditPrompt])
		if prompt == "" {
			return personaPanelMessage(ctx, host, request.User, "当前人设 Prompt 不能为空。")
		}
		state.Personas[active] = prompt
		if err := savePersonaState(ctx, host, state); err != nil {
			return nil, err
		}
		return personaPanelMessage(ctx, host, request.User, fmt.Sprintf("已更新当前人设: %s", active))
	default:
		return personaPanelMessage(ctx, host, request.User, "未知的人设表单。")
	}
}

func (p *personaPlugin) OnPromptBuild(ctx context.Context, host *pluginapi.HostClient, request pluginapi.PromptBuildRequest) (*pluginapi.PromptBuildResponse, error) {
	state, err := loadPersonaState(ctx, host)
	if err != nil {
		return nil, err
	}
	active := strings.TrimSpace(state.Active)
	if active == "" {
		return nil, nil
	}
	prompt := strings.TrimSpace(state.Personas[active])
	if prompt == "" {
		return nil, nil
	}
	return &pluginapi.PromptBuildResponse{
		Blocks: []pluginapi.PromptBlock{
			{
				Role:    "system",
				Content: "当前人设 Prompt:\n" + prompt,
			},
		},
	}, nil
}

func personaPanelMessage(ctx context.Context, host *pluginapi.HostClient, user pluginapi.UserInfo, notice string) (*pluginapi.InteractionResponse, error) {
	state, err := loadPersonaState(ctx, host)
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
	activePrompt := strings.TrimSpace(state.Personas[active])

	description := []string{
		"管理当前启用的人设，并把人设 Prompt 注入聊天上下文。",
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
				Footer:      "保存后会自动切换到对应人设。",
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
		options = append(options, pluginapi.SelectOption{
			Label:       shared.TruncateRunes(name, 100),
			Value:       name,
			Description: shared.TruncateRunes(shared.SingleLine(state.Personas[name]), personaPromptOptionRunes),
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
				Value:       strings.TrimSpace(state.Personas[active]),
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
	return &pluginapi.ModalResponse{
		CustomID: personaModalEditActive,
		Title:    title,
		Fields: []pluginapi.ModalField{
			{
				CustomID:    personaFieldEditPrompt,
				Label:       "当前人设 Prompt",
				Style:       "paragraph",
				Placeholder: "修改当前启用人设的 Prompt 内容",
				Value:       strings.TrimSpace(state.Personas[active]),
				Required:    true,
				MinLength:   1,
				MaxLength:   4000,
			},
		},
	}
}

func loadPersonaState(ctx context.Context, host *pluginapi.HostClient) (personaState, error) {
	state := personaState{Personas: map[string]string{}}
	found, err := host.StorageGet(ctx, personaStorageKey, &state)
	if err != nil {
		return personaState{}, err
	}
	if !found || state.Personas == nil {
		state.Personas = map[string]string{}
	}
	state.Active = normalizePersonaName(state.Active)
	normalized := make(map[string]string, len(state.Personas))
	for name, prompt := range state.Personas {
		name = normalizePersonaName(name)
		prompt = strings.TrimSpace(prompt)
		if name == "" || prompt == "" {
			continue
		}
		normalized[name] = prompt
	}
	state.Personas = normalized
	if _, ok := state.Personas[state.Active]; !ok {
		state.Active = ""
	}
	return state, nil
}

func savePersonaState(ctx context.Context, host *pluginapi.HostClient, state personaState) error {
	if state.Personas == nil {
		state.Personas = map[string]string{}
	}
	return host.StorageSet(ctx, personaStorageKey, state)
}

func personaNames(state personaState) []string {
	names := make([]string, 0, len(state.Personas))
	for name := range state.Personas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
