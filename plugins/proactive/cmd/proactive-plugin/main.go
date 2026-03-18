package main

import (
	"context"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"discord-bot-plugins/internal/shared"
	"discord-bot-plugins/sdk/pluginapi"
)

const (
	proactiveStorageKey       = "proactive_state"
	proactiveActionEnable     = "proactive:enable"
	proactiveActionDisable    = "proactive:disable"
	proactiveActionEditChance = "proactive:edit-chance"
	proactiveActionRefresh    = "proactive:refresh"
	proactiveModalChance      = "proactive:modal-chance"
	proactiveFieldChance      = "proactive:field-chance"
)

type proactiveState struct {
	Enabled bool    `json:"enabled"`
	Chance  float64 `json:"chance"`
}

type proactivePlugin struct {
	pluginapi.BasePlugin
	mu   sync.Mutex
	rand *rand.Rand
}

func newProactivePlugin() *proactivePlugin {
	return &proactivePlugin{
		rand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (p *proactivePlugin) OnSlashCommand(ctx context.Context, host *pluginapi.HostClient, request pluginapi.SlashCommandRequest) (*pluginapi.InteractionResponse, error) {
	return proactivePanelMessage(ctx, host, request, "")
}

func (p *proactivePlugin) OnComponent(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest) (*pluginapi.InteractionResponse, error) {
	state, err := loadProactiveState(ctx, host)
	if err != nil {
		return nil, err
	}
	if !request.User.IsAdmin {
		return proactivePanelUpdate(ctx, host, request, state, "你没有权限执行这个操作。")
	}
	if strings.TrimSpace(request.Guild.ID) == "" {
		return proactivePanelUpdate(ctx, host, request, state, "主动回复管理只能在服务器频道中使用。")
	}

	switch strings.TrimSpace(request.CustomID) {
	case proactiveActionEnable:
		allowed, err := host.SpeechAllowed(ctx, request.Guild.ID, request.Channel.ID, request.Channel.ThreadID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return proactivePanelUpdate(ctx, host, request, state, "当前服务器/频道/子区还没有在 `/setup` 里放行，请先配置允许发言范围后再开启主动回复。")
		}
		state.Enabled = true
		if err := saveProactiveState(ctx, host, state); err != nil {
			return nil, err
		}
		return proactivePanelUpdate(ctx, host, request, state, "已开启主动回复。")
	case proactiveActionDisable:
		state.Enabled = false
		if err := saveProactiveState(ctx, host, state); err != nil {
			return nil, err
		}
		return proactivePanelUpdate(ctx, host, request, state, "已关闭主动回复。")
	case proactiveActionEditChance:
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildProactiveChanceModal(state.Chance),
		}, nil
	case proactiveActionRefresh:
		return proactivePanelUpdate(ctx, host, request, state, "已刷新主动回复面板。")
	default:
		return proactivePanelUpdate(ctx, host, request, state, "未知的主动回复操作。")
	}
}

func (p *proactivePlugin) OnModal(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ModalRequest) (*pluginapi.InteractionResponse, error) {
	state, err := loadProactiveState(ctx, host)
	if err != nil {
		return nil, err
	}
	if !request.User.IsAdmin {
		return proactivePanelMessage(ctx, host, requestToSlash(request), "你没有权限执行这个操作。")
	}
	if strings.TrimSpace(request.Guild.ID) == "" {
		return proactivePanelMessage(ctx, host, requestToSlash(request), "主动回复管理只能在服务器频道中使用。")
	}

	switch strings.TrimSpace(request.CustomID) {
	case proactiveModalChance:
		parsed, err := shared.ParsePercent(request.Fields[proactiveFieldChance])
		if err != nil || parsed < 0 || parsed > 100 {
			return proactivePanelMessage(ctx, host, requestToSlash(request), "主动回复概率必须是 0 到 100 之间的数字。")
		}
		state.Chance = parsed
		if err := saveProactiveState(ctx, host, state); err != nil {
			return nil, err
		}
		return proactivePanelMessage(ctx, host, requestToSlash(request), "已更新主动回复概率为 "+shared.FormatPercent(parsed)+"。")
	default:
		return proactivePanelMessage(ctx, host, requestToSlash(request), "未知的主动回复表单。")
	}
}

func (p *proactivePlugin) OnMessage(ctx context.Context, host *pluginapi.HostClient, request pluginapi.MessageEvent) error {
	if strings.TrimSpace(request.Message.Guild.ID) == "" {
		return nil
	}
	if request.Message.MentionedBot || request.Message.RepliedToBot {
		return nil
	}

	state, err := loadProactiveState(ctx, host)
	if err != nil {
		return err
	}
	if !state.Enabled || state.Chance <= 0 {
		return nil
	}

	p.mu.Lock()
	hit := p.rand.Float64()*100 < state.Chance
	p.mu.Unlock()
	if !hit {
		return nil
	}

	if err := host.ReplyToMessage(ctx, request.Message); err != nil {
		_ = host.Log(ctx, "WARN", "proactive reply failed: "+err.Error())
	}
	return nil
}

func proactivePanelMessage(ctx context.Context, host *pluginapi.HostClient, request pluginapi.SlashCommandRequest, notice string) (*pluginapi.InteractionResponse, error) {
	state, err := loadProactiveState(ctx, host)
	if err != nil {
		return nil, err
	}
	if !request.User.IsAdmin {
		return &pluginapi.InteractionResponse{
			Type: pluginapi.InteractionResponseTypeMessage,
			Message: &pluginapi.InteractionMessage{
				Content:   "你没有权限执行这个操作。",
				Ephemeral: true,
			},
		}, nil
	}
	if strings.TrimSpace(request.Guild.ID) == "" {
		return &pluginapi.InteractionResponse{
			Type: pluginapi.InteractionResponseTypeMessage,
			Message: &pluginapi.InteractionMessage{
				Content:   "主动回复管理只能在服务器频道中使用。",
				Ephemeral: true,
			},
		}, nil
	}
	message, err := buildProactivePanel(ctx, host, request.Guild.ID, request.Channel.ID, request.Channel.ThreadID, state, notice)
	if err != nil {
		return nil, err
	}
	message.Ephemeral = true
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeMessage,
		Message: message,
	}, nil
}

func proactivePanelUpdate(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest, state proactiveState, notice string) (*pluginapi.InteractionResponse, error) {
	message, err := buildProactivePanel(ctx, host, request.Guild.ID, request.Channel.ID, request.Channel.ThreadID, state, notice)
	if err != nil {
		return &pluginapi.InteractionResponse{
			Type: pluginapi.InteractionResponseTypeUpdate,
			Message: &pluginapi.InteractionMessage{
				Content: strings.TrimSpace(notice),
			},
		}, nil
	}
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeUpdate,
		Message: message,
	}, nil
}

func buildProactivePanel(ctx context.Context, host *pluginapi.HostClient, guildID, channelID, threadID string, state proactiveState, notice string) (*pluginapi.InteractionMessage, error) {
	speechAllowed := false
	if host != nil {
		allowed, err := host.SpeechAllowed(ctx, guildID, channelID, threadID)
		if err != nil {
			return nil, err
		}
		speechAllowed = allowed
	}
	locationLabel := "当前服务器/频道不在允许发言范围内"
	if speechAllowed {
		locationLabel = "当前服务器/频道在允许发言范围内"
	}

	description := "控制机器人在没有被 @ 或没有直接回复机器人的情况下，是否按概率主动回复普通群聊消息。"
	if !speechAllowed {
		description += "\n\n当前交互位置还没有在 `/setup` 里放行。开启主动回复时会被拦截。"
	}
	if strings.TrimSpace(notice) != "" {
		description += "\n\n提示: " + strings.TrimSpace(notice)
	}

	return &pluginapi.InteractionMessage{
		Content: strings.TrimSpace(notice),
		Embeds: []pluginapi.Embed{
			{
				Title:       "Proactive Reply Control",
				Description: description,
				Color:       proactivePanelColor(state.Enabled, speechAllowed),
				Fields: []pluginapi.EmbedField{
					{Name: "当前状态", Value: proactiveEnabledLabel(state.Enabled), Inline: true},
					{Name: "当前概率", Value: shared.FormatPercent(state.Chance), Inline: true},
					{Name: "当前交互位置", Value: locationLabel, Inline: false},
					{Name: "说明", Value: "主动回复只在群聊里生效，且仍然遵守 `/setup` 配置的服务器、频道、子区白名单。", Inline: false},
				},
				Footer: "命中概率后才会回复；未命中时不会打断当前聊天。",
			},
		},
		Components: []pluginapi.ActionRow{
			{
				Buttons: []pluginapi.Button{
					{CustomID: proactiveActionEnable, Label: "开启", Style: "success", Disabled: state.Enabled},
					{CustomID: proactiveActionDisable, Label: "关闭", Style: "danger", Disabled: !state.Enabled},
					{CustomID: proactiveActionEditChance, Label: "编辑概率", Style: "primary"},
					{CustomID: proactiveActionRefresh, Label: "刷新", Style: "secondary"},
				},
			},
		},
	}, nil
}

func buildProactiveChanceModal(chance float64) *pluginapi.ModalResponse {
	return &pluginapi.ModalResponse{
		CustomID: proactiveModalChance,
		Title:    "编辑主动回复概率",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    proactiveFieldChance,
				Label:       "概率百分比",
				Style:       "short",
				Placeholder: "输入 0 到 100，例如 5 或 12.5",
				Value:       shared.FormatPercent(chance),
				Required:    true,
				MaxLength:   16,
			},
		},
	}
}

func loadProactiveState(ctx context.Context, host *pluginapi.HostClient) (proactiveState, error) {
	state := proactiveState{}
	_, err := host.StorageGet(ctx, proactiveStorageKey, &state)
	if err != nil {
		return proactiveState{}, err
	}
	if state.Chance < 0 {
		state.Chance = 0
	}
	if state.Chance > 100 {
		state.Chance = 100
	}
	return state, nil
}

func saveProactiveState(ctx context.Context, host *pluginapi.HostClient, state proactiveState) error {
	return host.StorageSet(ctx, proactiveStorageKey, state)
}

func proactiveEnabledLabel(enabled bool) string {
	if enabled {
		return "已开启"
	}
	return "已关闭"
}

func proactivePanelColor(enabled, speechAllowed bool) int {
	switch {
	case enabled && speechAllowed:
		return 0x10B981
	case enabled && !speechAllowed:
		return 0xF59E0B
	case !speechAllowed:
		return 0xDC2626
	default:
		return 0x6B7280
	}
}

func requestToSlash(request pluginapi.ModalRequest) pluginapi.SlashCommandRequest {
	return pluginapi.SlashCommandRequest{
		Guild:   request.Guild,
		Channel: request.Channel,
		User:    request.User,
	}
}

func main() {
	manifest, err := pluginapi.ReadManifest("plugin.json")
	if err != nil {
		log.Fatal(err)
	}
	if err := pluginapi.Serve(manifest, newProactivePlugin()); err != nil {
		log.Fatal(err)
	}
}
