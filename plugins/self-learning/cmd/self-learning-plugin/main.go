package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"discord-bot-plugins/internal/shared"
	"discord-bot-plugins/sdk/pluginapi"
)

const (
	configCollection        = "memory_config"
	profileCollection       = "memory_scope_state"
	legacyProfileCollection = "memory_profile"
	eventCollection         = "memory_event"
	episodeCollection       = "memory_episode"
	cardCollection          = "memory_card"
	recentCollection        = "memory_recent"
	recentEventCollection   = "memory_recent_events"

	memoryActionRefresh        = "memory:refresh"
	memoryActionToggle         = "memory:toggle"
	memoryActionEditTargets    = "memory:edit-targets"
	memoryActionClearTargets   = "memory:clear-targets"
	memoryActionForceLearn     = "memory:force-learn"
	memoryActionRotateWebToken = "memory:rotate-web-token"
	memoryModalTargets         = "memory:modal-targets"
	memoryFieldTargets         = "memory:field-targets"

	autoPersonaName      = "self-learning:auto"
	defaultBatchSize     = 60
	maxBatchSize         = 120
	maxRecentMessages    = 18
	maxRecentEvents      = 24
	maxHighlights        = 8
	maxRelations         = 8
	maxTopAffinities     = 6
	maxEpisodeRecords    = 48
	maxCardRecords       = 96
	maxPanelPreviewRunes = 900
)

type learningConfig struct {
	Enabled       bool     `json:"enabled"`
	TargetUserIDs []string `json:"target_user_ids,omitempty"`
	BatchSize     int      `json:"batch_size,omitempty"`
}

type relationEdge struct {
	LeftUserID  string `json:"left_user_id"`
	RightUserID string `json:"right_user_id"`
	Type        string `json:"type"`
	Strength    int    `json:"strength,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
}

type learningProfile struct {
	Scope               pluginapi.PersonaScope `json:"scope"`
	PersonaName         string                 `json:"persona_name,omitempty"`
	PersonaPrompt       string                 `json:"persona_prompt,omitempty"`
	SceneSummary        string                 `json:"scene_summary,omitempty"`
	Summary             string                 `json:"summary,omitempty"`
	StyleSummary        string                 `json:"style_summary,omitempty"`
	SlangSummary        string                 `json:"slang_summary,omitempty"`
	RelationshipSummary string                 `json:"relationship_summary,omitempty"`
	IntentSummary       string                 `json:"intent_summary,omitempty"`
	MoodSummary         string                 `json:"mood_summary,omitempty"`
	Highlights          []string               `json:"highlights,omitempty"`
	Relations           []relationEdge         `json:"relations,omitempty"`
	Affinity            map[string]int         `json:"affinity,omitempty"`
	LastLearnedAt       string                 `json:"last_learned_at,omitempty"`
	ProcessedCursor     string                 `json:"processed_cursor,omitempty"`
	LastError           string                 `json:"last_error,omitempty"`
	EventCount          int                    `json:"event_count,omitempty"`
	EpisodeCount        int                    `json:"episode_count,omitempty"`
	CardCount           int                    `json:"card_count,omitempty"`
}

type conversationEvent struct {
	MessageID    string                     `json:"message_id,omitempty"`
	Role         string                     `json:"role"`
	Time         string                     `json:"time,omitempty"`
	Author       pluginapi.UserInfo         `json:"author"`
	Content      string                     `json:"content,omitempty"`
	ReplyTo      *pluginapi.ReplyInfo       `json:"reply_to,omitempty"`
	Images       []pluginapi.ImageReference `json:"images,omitempty"`
	MentionedBot bool                       `json:"mentioned_bot,omitempty"`
	RepliedToBot bool                       `json:"replied_to_bot,omitempty"`
}

type recentBuffer struct {
	Messages []pluginapi.PromptConversationMessage `json:"messages,omitempty"`
}

type recentEventBuffer struct {
	Events []conversationEvent `json:"events,omitempty"`
}

type learnResult struct {
	PersonaPrompt       string            `json:"persona_prompt"`
	SceneSummary        string            `json:"scene_summary"`
	Summary             string            `json:"summary"`
	StyleSummary        string            `json:"style_summary"`
	SlangSummary        string            `json:"slang_summary"`
	RelationshipSummary string            `json:"relationship_summary"`
	IntentSummary       string            `json:"intent_summary"`
	MoodSummary         string            `json:"mood_summary"`
	Highlights          []string          `json:"highlights"`
	Relations           []relationEdge    `json:"relations"`
	Affinity            map[string]int    `json:"affinity"`
	Cards               []learnCardResult `json:"cards"`
}

type panelState struct {
	Scope   pluginapi.PersonaScope
	Config  learningConfig
	Profile learningProfile
	Recent  recentBuffer
}

type selfLearningPlugin struct {
	pluginapi.BasePlugin
	web    *memoryWebRuntime
	webErr string
}

func (p *selfLearningPlugin) Initialize(ctx context.Context, host *pluginapi.HostClient, request pluginapi.InitializeRequest) error {
	settings, err := ensureWebSettings(ctx, host)
	if err != nil {
		p.webErr = err.Error()
		return nil
	}
	webRuntime, err := startMemoryWebRuntime(host, settings)
	if err != nil {
		p.webErr = err.Error()
		_ = host.Log(ctx, "WARN", "self-learning web ui disabled: "+err.Error())
		return nil
	}
	p.web = webRuntime
	p.webErr = ""
	_ = host.Log(ctx, "INFO", "self-learning web ui listening on "+webRuntime.ListenAddr())
	return nil
}

func (p *selfLearningPlugin) Shutdown(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ShutdownRequest) error {
	if p.web == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := p.web.Close(shutdownCtx)
	p.web = nil
	return err
}

func (p *selfLearningPlugin) currentWebStatus() memoryWebStatus {
	if p.web != nil {
		status := p.web.Snapshot()
		if status.Error == "" {
			status.Error = strings.TrimSpace(p.webErr)
		}
		return status
	}
	return memoryWebStatus{
		Running: false,
		Error:   firstNonEmpty(p.webErr, "Web 控制台尚未启动。"),
	}
}

func (p *selfLearningPlugin) OnSlashCommand(ctx context.Context, host *pluginapi.HostClient, request pluginapi.SlashCommandRequest) (*pluginapi.InteractionResponse, error) {
	return memoryPanelMessage(ctx, host, scopeFromLocation(request.Guild.ID, request.Channel), request.User, p.currentWebStatus(), "")
}

func (p *selfLearningPlugin) OnComponent(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest) (*pluginapi.InteractionResponse, error) {
	scope := scopeFromLocation(request.Guild.ID, request.Channel)
	state, err := loadPanelState(ctx, host, scope)
	if err != nil {
		return nil, err
	}

	switch strings.TrimSpace(request.CustomID) {
	case memoryActionRefresh:
		return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "已刷新自学习面板。"), nil
	case memoryActionToggle:
		if !request.User.IsAdmin {
			return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "你没有权限执行这个操作。"), nil
		}
		state.Config.Enabled = !state.Config.Enabled
		if err := saveConfig(ctx, host, scope, state.Config); err != nil {
			return nil, err
		}
		state, err = loadPanelState(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		if state.Config.Enabled {
			return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "已启用当前作用域的自学习。"), nil
		}
		return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "已停用当前作用域的自学习。"), nil
	case memoryActionEditTargets:
		if !request.User.IsAdmin {
			return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "你没有权限执行这个操作。"), nil
		}
		return &pluginapi.InteractionResponse{
			Type:  pluginapi.InteractionResponseTypeModal,
			Modal: buildTargetsModal(state),
		}, nil
	case memoryActionClearTargets:
		if !request.User.IsAdmin {
			return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "你没有权限执行这个操作。"), nil
		}
		state.Config.TargetUserIDs = nil
		if err := saveConfig(ctx, host, scope, state.Config); err != nil {
			return nil, err
		}
		state, err = loadPanelState(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "已清空目标用户。"), nil
	case memoryActionForceLearn:
		if !request.User.IsAdmin {
			return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "你没有权限执行这个操作。"), nil
		}
		updated, changed, err := learnScope(ctx, host, scope, true)
		if err != nil {
			return memoryPanelMessage(ctx, host, scope, request.User, p.currentWebStatus(), "强制学习失败: "+err.Error())
		}
		if !changed {
			return memoryPanelMessage(ctx, host, scope, request.User, p.currentWebStatus(), "当前没有可学习的新消息。")
		}
		state, err = loadPanelState(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		return memoryPanelUpdate(withProfile(state, updated), request.User, p.currentWebStatus(), "已完成一次强制学习。"), nil
	case memoryActionRotateWebToken:
		if !request.User.IsAdmin {
			return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "你没有权限执行这个操作。"), nil
		}
		settings, err := loadWebSettings(ctx, host)
		if err != nil {
			return nil, err
		}
		settings.AccessToken, err = generateAccessToken()
		if err != nil {
			return nil, err
		}
		if err := saveWebSettings(ctx, host, settings); err != nil {
			return nil, err
		}
		if p.web != nil {
			p.web.UpdateToken(settings.AccessToken)
		}
		return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "已重新生成 Web 控制台访问令牌。"), nil
	default:
		return memoryPanelUpdate(state, request.User, p.currentWebStatus(), "未知的自学习面板操作。"), nil
	}
}

func (p *selfLearningPlugin) OnModal(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ModalRequest) (*pluginapi.InteractionResponse, error) {
	scope := scopeFromLocation(request.Guild.ID, request.Channel)
	if !request.User.IsAdmin {
		return memoryPanelMessage(ctx, host, scope, request.User, p.currentWebStatus(), "你没有权限执行这个操作。")
	}

	switch strings.TrimSpace(request.CustomID) {
	case memoryModalTargets:
		config, err := loadConfig(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		config.TargetUserIDs = parseUserIDs(request.Fields[memoryFieldTargets])
		config.Enabled = true
		if err := saveConfig(ctx, host, scope, config); err != nil {
			return nil, err
		}
		return memoryPanelMessage(ctx, host, scope, request.User, p.currentWebStatus(), "已更新目标用户，并自动启用当前作用域自学习。")
	default:
		return memoryPanelMessage(ctx, host, scope, request.User, p.currentWebStatus(), "未知的自学习表单。")
	}
}

func (p *selfLearningPlugin) OnMessage(ctx context.Context, host *pluginapi.HostClient, request pluginapi.MessageEvent) error {
	scope := scopeFromMessage(request.Message)
	config, err := loadConfig(ctx, host, scope)
	if err != nil || !config.Enabled {
		return err
	}
	event := conversationEvent{
		MessageID:    strings.TrimSpace(request.Message.MessageID),
		Role:         "user",
		Time:         strings.TrimSpace(request.Message.Time),
		Author:       request.Message.Author,
		Content:      strings.TrimSpace(request.Message.Content),
		ReplyTo:      request.Message.ReplyTo,
		Images:       append([]pluginapi.ImageReference(nil), request.Message.Images...),
		MentionedBot: request.Message.MentionedBot,
		RepliedToBot: request.Message.RepliedToBot,
	}
	if err := appendEvent(ctx, host, scope, event); err != nil {
		return err
	}
	if err := appendRecentEvent(ctx, host, scope, event); err != nil {
		return err
	}
	return appendRecentMessage(ctx, host, scope, pluginapi.PromptConversationMessage{
		Role:    "user",
		Content: renderEventForPrompt(event),
	})
}

func (p *selfLearningPlugin) OnReplyCommitted(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ReplyCommittedRequest) error {
	scope := scopeFromMessage(request.TriggerMessage)
	config, err := loadConfig(ctx, host, scope)
	if err != nil || !config.Enabled {
		return err
	}
	event := conversationEvent{
		MessageID:    strings.TrimSpace(request.ReplyMessage.MessageID),
		Role:         "assistant",
		Time:         strings.TrimSpace(request.ReplyMessage.Time),
		Author:       request.ReplyMessage.Author,
		Content:      strings.TrimSpace(request.ReplyMessage.Content),
		Images:       append([]pluginapi.ImageReference(nil), request.ReplyMessage.Images...),
		MentionedBot: false,
		RepliedToBot: true,
	}
	if strings.TrimSpace(request.TriggerMessage.MessageID) != "" || strings.TrimSpace(request.TriggerMessage.Content) != "" {
		event.ReplyTo = &pluginapi.ReplyInfo{
			MessageID: strings.TrimSpace(request.TriggerMessage.MessageID),
			Role:      "user",
			Content:   strings.TrimSpace(request.TriggerMessage.Content),
			Time:      strings.TrimSpace(request.TriggerMessage.Time),
			Author:    request.TriggerMessage.Author,
		}
	}
	if err := appendEvent(ctx, host, scope, event); err != nil {
		return err
	}
	if err := appendRecentEvent(ctx, host, scope, event); err != nil {
		return err
	}
	return appendRecentMessage(ctx, host, scope, pluginapi.PromptConversationMessage{
		Role:    "assistant",
		Content: renderEventForPrompt(event),
	})
}

func (p *selfLearningPlugin) OnContextBuild(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ContextBuildRequest) (*pluginapi.ContextBuildResponse, error) {
	scope := scopeFromMessage(request.CurrentMessage)
	config, err := loadConfig(ctx, host, scope)
	if err != nil || !config.Enabled {
		return nil, err
	}

	profile, err := loadProfile(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	return buildMemoryV2Context(ctx, host, scope, config, profile, request)
}

func (p *selfLearningPlugin) OnInterval(ctx context.Context, host *pluginapi.HostClient, request pluginapi.IntervalRequest) error {
	configs, err := listConfiguredScopes(ctx, host)
	if err != nil {
		return err
	}
	for _, scope := range configs {
		if _, _, err := learnScope(ctx, host, scope, false); err != nil {
			_ = host.Log(ctx, "WARN", fmt.Sprintf("learn scope %s failed: %v", scopeKey(scope), err))
		}
	}
	return nil
}

func memoryPanelMessage(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, user pluginapi.UserInfo, web memoryWebStatus, notice string) (*pluginapi.InteractionResponse, error) {
	state, err := loadPanelState(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeMessage,
		Message: buildMemoryPanel(state, user, web, notice, true),
	}, nil
}

func memoryPanelUpdate(state panelState, user pluginapi.UserInfo, web memoryWebStatus, notice string) *pluginapi.InteractionResponse {
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeUpdate,
		Message: buildMemoryPanel(state, user, web, notice, false),
	}
}

func buildMemoryPanel(state panelState, user pluginapi.UserInfo, web memoryWebStatus, notice string, ephemeral bool) *pluginapi.InteractionMessage {
	description := []string{
		"接管当前作用域的上下文、记忆和自动人格演化。",
		"作用域: " + scopeLabel(state.Scope),
	}
	if strings.TrimSpace(notice) != "" {
		description = append(description, "提示: "+strings.TrimSpace(notice))
	}
	if !user.IsAdmin {
		description = append(description, "你当前只有查看权限。")
	}

	fields := []pluginapi.EmbedField{
		{
			Name:   "状态",
			Value:  boolLabel(state.Config.Enabled),
			Inline: true,
		},
		{
			Name:   "批处理大小",
			Value:  fmt.Sprintf("%d", normalizeConfig(state.Config).BatchSize),
			Inline: true,
		},
		{
			Name:   "目标用户",
			Value:  targetListLabel(state.Config.TargetUserIDs),
			Inline: false,
		},
		{
			Name:   "最后学习",
			Value:  firstNonEmpty(state.Profile.LastLearnedAt, "尚未学习"),
			Inline: true,
		},
		{
			Name:   "已处理事件",
			Value:  fmt.Sprintf("%d", state.Profile.EventCount),
			Inline: true,
		},
		{
			Name:   "Episodes / Cards",
			Value:  fmt.Sprintf("%d / %d", state.Profile.EpisodeCount, state.Profile.CardCount),
			Inline: true,
		},
		{
			Name:   "当前场景摘要",
			Value:  previewBlock(state.Profile.SceneSummary, "当前还没有场景摘要。"),
			Inline: false,
		},
		{
			Name:   "自动人格 Prompt",
			Value:  previewBlock(state.Profile.PersonaPrompt, "当前还没有生成自动人格 Prompt。"),
			Inline: false,
		},
		{
			Name:   "学习摘要",
			Value:  previewBlock(joinNonEmpty("\n\n", state.Profile.Summary, state.Profile.StyleSummary, state.Profile.SlangSummary), "当前还没有学习摘要。"),
			Inline: false,
		},
		{
			Name:   "关系图谱",
			Value:  previewBlock(relationsPreview(state.Profile), "当前还没有关系图谱数据。"),
			Inline: false,
		},
		{
			Name:   "好感度概览",
			Value:  previewBlock(affinityPreview(state.Profile.Affinity), "当前还没有好感度数据。"),
			Inline: false,
		},
		{
			Name:   "Web 控制台",
			Value:  previewBlock(webPanelPreview(web, user.IsAdmin), "Web 控制台不可用。"),
			Inline: false,
		},
	}

	return &pluginapi.InteractionMessage{
		Content:   strings.TrimSpace(notice),
		Ephemeral: ephemeral,
		Embeds: []pluginapi.Embed{
			{
				Title:       "Self Learning Memory",
				Description: strings.Join(description, "\n"),
				Color:       memoryPanelColor(state.Config.Enabled),
				Fields:      fields,
				Footer:      "面板操作的是当前服务器/频道/子区作用域。",
			},
		},
		Components: buildMemoryPanelComponents(state, user.IsAdmin),
	}
}

func buildMemoryPanelComponents(state panelState, isAdmin bool) []pluginapi.ActionRow {
	toggleLabel := "启用学习"
	toggleStyle := "success"
	if state.Config.Enabled {
		toggleLabel = "停用学习"
		toggleStyle = "danger"
	}
	return []pluginapi.ActionRow{
		{
			Buttons: []pluginapi.Button{
				{CustomID: memoryActionToggle, Label: toggleLabel, Style: toggleStyle, Disabled: !isAdmin},
				{CustomID: memoryActionEditTargets, Label: "目标用户", Style: "primary", Disabled: !isAdmin},
				{CustomID: memoryActionClearTargets, Label: "清空目标", Style: "secondary", Disabled: !isAdmin || len(state.Config.TargetUserIDs) == 0},
				{CustomID: memoryActionForceLearn, Label: "强制学习", Style: "primary", Disabled: !isAdmin || !state.Config.Enabled},
				{CustomID: memoryActionRefresh, Label: "刷新", Style: "secondary"},
			},
		},
		{
			Buttons: []pluginapi.Button{
				{CustomID: memoryActionRotateWebToken, Label: "重置 Web 令牌", Style: "secondary", Disabled: !isAdmin},
			},
		},
	}
}

func buildTargetsModal(state panelState) *pluginapi.ModalResponse {
	return &pluginapi.ModalResponse{
		CustomID: memoryModalTargets,
		Title:    "设置目标用户",
		Fields: []pluginapi.ModalField{
			{
				CustomID:    memoryFieldTargets,
				Label:       "用户 ID 或提及",
				Style:       "paragraph",
				Placeholder: "每行一个用户 ID 或 <@123> 提及，留空表示只学习社区整体关系与黑话。",
				Value:       strings.Join(state.Config.TargetUserIDs, "\n"),
				Required:    false,
				MaxLength:   2000,
			},
		},
	}
}

func learnScope(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, force bool) (learningProfile, bool, error) {
	config, err := loadConfig(ctx, host, scope)
	if err != nil {
		return learningProfile{}, false, err
	}
	if !config.Enabled {
		return learningProfile{}, false, nil
	}
	profile, err := loadProfile(ctx, host, scope)
	if err != nil {
		return learningProfile{}, false, err
	}
	fail := func(err error) (learningProfile, bool, error) {
		_ = saveProfileError(ctx, host, scope, err)
		return profile, false, err
	}

	events, cursor, err := listEventsBatch(ctx, host, scope, profile.ProcessedCursor, config.BatchSize)
	if err != nil {
		return fail(err)
	}
	if len(events) == 0 {
		return profile, false, nil
	}

	content, err := host.Chat(ctx, buildLearningMessages(config, profile, events, force))
	if err != nil {
		return fail(err)
	}
	result, err := parseLearnResult(content)
	if err != nil {
		return fail(err)
	}

	profile = mergeProfile(scope, profile, result, cursor, len(events))
	profile, err = saveLearningArtifacts(ctx, host, scope, profile, result, events)
	if err != nil {
		return fail(err)
	}
	if err := saveProfile(ctx, host, scope, profile); err != nil {
		return fail(err)
	}
	if strings.TrimSpace(profile.PersonaPrompt) != "" {
		if err := syncAutoPersona(ctx, host, scope, profile); err != nil {
			return fail(err)
		}
	}
	_ = syncMemoryWorldBook(ctx, host, scope, profile)
	return profile, true, nil
}

func buildLearningMessages(config learningConfig, profile learningProfile, events []conversationEvent, force bool) []pluginapi.ChatMessage {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, renderEventForLearning(event))
	}
	targets := "无"
	if len(config.TargetUserIDs) > 0 {
		targets = strings.Join(config.TargetUserIDs, ", ")
	}
	prefix := []string{
		"目标用户: " + targets,
		fmt.Sprintf("批处理事件数: %d", len(events)),
	}
	if force {
		prefix = append(prefix, "当前是手动强制学习。")
	}
	if profile.Summary != "" {
		prefix = append(prefix, "上一次长期摘要:\n"+profile.Summary)
	}
	if profile.SceneSummary != "" {
		prefix = append(prefix, "上一次场景摘要:\n"+profile.SceneSummary)
	}
	if profile.PersonaPrompt != "" {
		prefix = append(prefix, "上一次自动人格 Prompt:\n"+profile.PersonaPrompt)
	}
	prefix = append(prefix, "需要分析的新对话批次:\n"+strings.Join(lines, "\n\n"))

	return []pluginapi.ChatMessage{
		{
			Role: "system",
			Content: strings.Join([]string{
				"你是 Discord 自学习记忆分析器。",
				"请根据聊天记录持续学习社区黑话、目标用户风格、关系图谱、情绪状态、对话意图和好感度，并整理成可检索的结构化卡片。",
				"输出必须是严格 JSON，不要使用 Markdown 代码块，不要输出解释。",
				"关系类型请保守，优先使用这些标签：高频互动、问答互动、观点认同、好友、闺蜜、同事、同学、师生、亲属、情侣、暧昧、竞争、敌对、崇拜、粉丝。",
				"好感度 affinity 的范围是 0 到 100。",
				"cards 字段用于生成结构化记忆卡片，每张卡片必须包含 kind, subject_id, subject_name, title, content, confidence, ttl_days, evidence, related_user_ids。",
				"kind 只允许使用 fact, preference, style, slang, relationship, topic。",
				"scene_summary 用一段短摘要概括这一批次当前发生了什么。",
				"JSON 字段必须包含 persona_prompt, scene_summary, summary, style_summary, slang_summary, relationship_summary, intent_summary, mood_summary, highlights, relations, affinity, cards。",
			}, "\n"),
		},
		{
			Role:    "user",
			Content: strings.Join(prefix, "\n\n"),
		},
	}
}

func parseLearnResult(content string) (learnResult, error) {
	content = strings.TrimSpace(content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return learnResult{}, fmt.Errorf("invalid learning response: missing json object")
	}
	var result learnResult
	if err := json.Unmarshal([]byte(content[start:end+1]), &result); err != nil {
		return learnResult{}, err
	}
	result.PersonaPrompt = strings.TrimSpace(result.PersonaPrompt)
	result.SceneSummary = strings.TrimSpace(result.SceneSummary)
	result.Summary = strings.TrimSpace(result.Summary)
	result.StyleSummary = strings.TrimSpace(result.StyleSummary)
	result.SlangSummary = strings.TrimSpace(result.SlangSummary)
	result.RelationshipSummary = strings.TrimSpace(result.RelationshipSummary)
	result.IntentSummary = strings.TrimSpace(result.IntentSummary)
	result.MoodSummary = strings.TrimSpace(result.MoodSummary)
	result.Highlights = trimStrings(result.Highlights, maxHighlights)
	if len(result.Relations) > maxRelations {
		result.Relations = result.Relations[:maxRelations]
	}
	result.Affinity = normalizeAffinity(result.Affinity)
	if len(result.Cards) > 10 {
		result.Cards = result.Cards[:10]
	}
	return result, nil
}

func mergeProfile(scope pluginapi.PersonaScope, profile learningProfile, result learnResult, cursor string, eventCount int) learningProfile {
	profile.Scope = scope
	profile.PersonaName = autoPersonaName
	profile.PersonaPrompt = firstNonEmpty(result.PersonaPrompt, profile.PersonaPrompt)
	profile.SceneSummary = firstNonEmpty(result.SceneSummary, profile.SceneSummary)
	profile.Summary = firstNonEmpty(result.Summary, profile.Summary)
	profile.StyleSummary = firstNonEmpty(result.StyleSummary, profile.StyleSummary)
	profile.SlangSummary = firstNonEmpty(result.SlangSummary, profile.SlangSummary)
	profile.RelationshipSummary = firstNonEmpty(result.RelationshipSummary, profile.RelationshipSummary)
	profile.IntentSummary = firstNonEmpty(result.IntentSummary, profile.IntentSummary)
	profile.MoodSummary = firstNonEmpty(result.MoodSummary, profile.MoodSummary)
	if len(result.Highlights) > 0 {
		profile.Highlights = append([]string(nil), result.Highlights...)
	}
	if len(result.Relations) > 0 {
		profile.Relations = append([]relationEdge(nil), result.Relations...)
	}
	if len(result.Affinity) > 0 {
		profile.Affinity = normalizeAffinity(result.Affinity)
	}
	profile.LastLearnedAt = time.Now().Format(time.RFC3339)
	profile.ProcessedCursor = strings.TrimSpace(cursor)
	profile.LastError = ""
	profile.EventCount += eventCount
	return normalizeProfile(profile)
}

func syncAutoPersona(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, profile learningProfile) error {
	if err := host.PersonaUpsert(ctx, pluginapi.PersonaUpsertRequest{
		Scope:  scope,
		Name:   autoPersonaName,
		Prompt: profile.PersonaPrompt,
		Origin: "official_self_learning",
	}); err != nil {
		return err
	}
	return host.PersonaActivate(ctx, scope, autoPersonaName)
}

func loadPanelState(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (panelState, error) {
	config, err := loadConfig(ctx, host, scope)
	if err != nil {
		return panelState{}, err
	}
	profile, err := loadProfile(ctx, host, scope)
	if err != nil {
		return panelState{}, err
	}
	recent, err := loadRecent(ctx, host, scope)
	if err != nil {
		return panelState{}, err
	}
	return panelState{
		Scope:   scope,
		Config:  config,
		Profile: profile,
		Recent:  recent,
	}, nil
}

func loadConfig(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (learningConfig, error) {
	var config learningConfig
	found, _, err := host.RecordsGet(ctx, configCollection, scopeKey(scope), &config)
	if err != nil {
		return learningConfig{}, err
	}
	if !found {
		return normalizeConfig(learningConfig{}), nil
	}
	return normalizeConfig(config), nil
}

func saveConfig(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, config learningConfig) error {
	return host.RecordsPut(ctx, configCollection, scopeKey(scope), normalizeConfig(config))
}

func loadProfile(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (learningProfile, error) {
	var profile learningProfile
	found, _, err := host.RecordsGet(ctx, profileCollection, scopeKey(scope), &profile)
	if err != nil {
		return learningProfile{}, err
	}
	if !found {
		found, _, err = host.RecordsGet(ctx, legacyProfileCollection, scopeKey(scope), &profile)
		if err != nil {
			return learningProfile{}, err
		}
	}
	if !found {
		return normalizeProfile(learningProfile{Scope: scope, PersonaName: autoPersonaName}), nil
	}
	if profile.Scope.Type == "" {
		profile.Scope = scope
	}
	return normalizeProfile(profile), nil
}

func saveProfile(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, profile learningProfile) error {
	profile.Scope = scope
	return host.RecordsPut(ctx, profileCollection, scopeKey(scope), normalizeProfile(profile))
}

func appendEvent(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, event conversationEvent) error {
	key := eventKey(scope, firstNonEmpty(event.Time, time.Now().Format(time.RFC3339Nano)), firstNonEmpty(event.MessageID, fmt.Sprintf("generated-%d", time.Now().UnixNano())))
	return host.RecordsPut(ctx, eventCollection, key, event)
}

func listEventsBatch(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, cursor string, limit int) ([]conversationEvent, string, error) {
	limit = normalizeBatchSize(limit)
	response, err := host.RecordsList(ctx, pluginapi.RecordsListRequest{
		Collection: eventCollection,
		Prefix:     scopeKey(scope) + "|",
		Limit:      limit,
		Cursor:     strings.TrimSpace(cursor),
	})
	if err != nil {
		return nil, "", err
	}
	events := make([]conversationEvent, 0, len(response.Items))
	lastKey := strings.TrimSpace(cursor)
	for _, item := range response.Items {
		lastKey = strings.TrimSpace(item.Key)
		var event conversationEvent
		if err := json.Unmarshal(item.Value, &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events, lastKey, nil
}

func loadRecent(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (recentBuffer, error) {
	var buffer recentBuffer
	found, _, err := host.RecordsGet(ctx, recentCollection, scopeKey(scope), &buffer)
	if err != nil {
		return recentBuffer{}, err
	}
	if !found {
		return recentBuffer{}, nil
	}
	if len(buffer.Messages) > maxRecentMessages {
		buffer.Messages = buffer.Messages[len(buffer.Messages)-maxRecentMessages:]
	}
	return buffer, nil
}

func loadRecentEvents(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (recentEventBuffer, error) {
	var buffer recentEventBuffer
	found, _, err := host.RecordsGet(ctx, recentEventCollection, scopeKey(scope), &buffer)
	if err != nil {
		return recentEventBuffer{}, err
	}
	if !found {
		return recentEventBuffer{}, nil
	}
	if len(buffer.Events) > maxRecentEvents {
		buffer.Events = buffer.Events[len(buffer.Events)-maxRecentEvents:]
	}
	return buffer, nil
}

func appendRecentMessage(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, message pluginapi.PromptConversationMessage) error {
	buffer, err := loadRecent(ctx, host, scope)
	if err != nil {
		return err
	}
	if strings.TrimSpace(message.Content) == "" {
		return nil
	}
	buffer.Messages = append(buffer.Messages, pluginapi.PromptConversationMessage{
		Role:    firstNonEmpty(message.Role, "user"),
		Content: strings.TrimSpace(message.Content),
	})
	if len(buffer.Messages) > maxRecentMessages {
		buffer.Messages = buffer.Messages[len(buffer.Messages)-maxRecentMessages:]
	}
	return host.RecordsPut(ctx, recentCollection, scopeKey(scope), buffer)
}

func appendRecentEvent(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, event conversationEvent) error {
	buffer, err := loadRecentEvents(ctx, host, scope)
	if err != nil {
		return err
	}
	if strings.TrimSpace(event.Content) == "" && len(event.Images) == 0 {
		return nil
	}
	buffer.Events = append(buffer.Events, event)
	if len(buffer.Events) > maxRecentEvents {
		buffer.Events = buffer.Events[len(buffer.Events)-maxRecentEvents:]
	}
	return host.RecordsPut(ctx, recentEventCollection, scopeKey(scope), buffer)
}

func listConfiguredScopes(ctx context.Context, host *pluginapi.HostClient) ([]pluginapi.PersonaScope, error) {
	response, err := host.RecordsList(ctx, pluginapi.RecordsListRequest{
		Collection: configCollection,
		Limit:      200,
	})
	if err != nil {
		return nil, err
	}
	scopes := make([]pluginapi.PersonaScope, 0, len(response.Items))
	for _, item := range response.Items {
		var config learningConfig
		if err := json.Unmarshal(item.Value, &config); err != nil {
			continue
		}
		config = normalizeConfig(config)
		if !config.Enabled {
			continue
		}
		scopes = append(scopes, parseScopeKey(item.Key))
	}
	return scopes, nil
}

func scopeFromMessage(message pluginapi.MessageContext) pluginapi.PersonaScope {
	return scopeFromLocation(message.Guild.ID, message.Channel)
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

func scopeKey(scope pluginapi.PersonaScope) string {
	switch strings.TrimSpace(scope.Type) {
	case pluginapi.PersonaScopeThread:
		return fmt.Sprintf("thread|%s|%s|%s", strings.TrimSpace(scope.GuildID), strings.TrimSpace(scope.ChannelID), strings.TrimSpace(scope.ThreadID))
	case pluginapi.PersonaScopeChannel:
		return fmt.Sprintf("channel|%s|%s", strings.TrimSpace(scope.GuildID), strings.TrimSpace(scope.ChannelID))
	case pluginapi.PersonaScopeGuild:
		return fmt.Sprintf("guild|%s", strings.TrimSpace(scope.GuildID))
	default:
		return "global"
	}
}

func parseScopeKey(key string) pluginapi.PersonaScope {
	parts := strings.Split(strings.TrimSpace(key), "|")
	switch {
	case len(parts) == 4 && parts[0] == "thread":
		return pluginapi.PersonaScope{
			Type:      pluginapi.PersonaScopeThread,
			GuildID:   parts[1],
			ChannelID: parts[2],
			ThreadID:  parts[3],
		}
	case len(parts) == 3 && parts[0] == "channel":
		return pluginapi.PersonaScope{
			Type:      pluginapi.PersonaScopeChannel,
			GuildID:   parts[1],
			ChannelID: parts[2],
		}
	case len(parts) == 2 && parts[0] == "guild":
		return pluginapi.PersonaScope{
			Type:    pluginapi.PersonaScopeGuild,
			GuildID: parts[1],
		}
	default:
		return pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGlobal}
	}
}

func eventKey(scope pluginapi.PersonaScope, timeValue, messageID string) string {
	return fmt.Sprintf("%s|%s|%s", scopeKey(scope), strings.TrimSpace(timeValue), strings.TrimSpace(messageID))
}

func normalizeConfig(config learningConfig) learningConfig {
	config.TargetUserIDs = trimStrings(config.TargetUserIDs, 64)
	config.BatchSize = normalizeBatchSize(config.BatchSize)
	return config
}

func normalizeProfile(profile learningProfile) learningProfile {
	profile.PersonaName = firstNonEmpty(profile.PersonaName, autoPersonaName)
	profile.PersonaPrompt = strings.TrimSpace(profile.PersonaPrompt)
	profile.SceneSummary = strings.TrimSpace(profile.SceneSummary)
	profile.Summary = strings.TrimSpace(profile.Summary)
	profile.StyleSummary = strings.TrimSpace(profile.StyleSummary)
	profile.SlangSummary = strings.TrimSpace(profile.SlangSummary)
	profile.RelationshipSummary = strings.TrimSpace(profile.RelationshipSummary)
	profile.IntentSummary = strings.TrimSpace(profile.IntentSummary)
	profile.MoodSummary = strings.TrimSpace(profile.MoodSummary)
	profile.Highlights = trimStrings(profile.Highlights, maxHighlights)
	profile.Affinity = normalizeAffinity(profile.Affinity)
	if len(profile.Relations) > maxRelations {
		profile.Relations = profile.Relations[:maxRelations]
	}
	return profile
}

func normalizeBatchSize(value int) int {
	switch {
	case value <= 0:
		return defaultBatchSize
	case value > maxBatchSize:
		return maxBatchSize
	default:
		return value
	}
}

func normalizeAffinity(values map[string]int) map[string]int {
	if len(values) == 0 {
		return map[string]int{}
	}
	normalized := make(map[string]int, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		switch {
		case value < 0:
			value = 0
		case value > 100:
			value = 100
		}
		normalized[key] = value
	}
	return normalized
}

func parseUserIDs(input string) []string {
	fields := strings.FieldsFunc(strings.TrimSpace(input), func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	})
	ids := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		field = strings.TrimPrefix(field, "<@")
		field = strings.TrimPrefix(field, "!")
		field = strings.TrimSuffix(field, ">")
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		ids = append(ids, field)
	}
	sort.Strings(ids)
	return ids
}

func renderEventForPrompt(event conversationEvent) string {
	return renderEvent(event, 260)
}

func renderEventForLearning(event conversationEvent) string {
	return renderEvent(event, 340)
}

func renderEvent(event conversationEvent, contentLimit int) string {
	lines := []string{
		"时间: " + firstNonEmpty(event.Time, "unknown"),
		"角色: " + firstNonEmpty(event.Role, "user"),
		"发送者: " + authorLabel(event.Author),
		"内容: " + firstNonEmpty(shared.TruncateRunes(strings.TrimSpace(event.Content), contentLimit), "[空消息]"),
	}
	if event.ReplyTo != nil {
		lines = append(lines, "回复对象: "+authorLabel(event.ReplyTo.Author))
		lines = append(lines, "被回复内容: "+firstNonEmpty(shared.TruncateRunes(strings.TrimSpace(event.ReplyTo.Content), 180), "[空消息]"))
	}
	if len(event.Images) > 0 {
		lines = append(lines, fmt.Sprintf("图片输入: %d 个", len(event.Images)))
	}
	if event.MentionedBot {
		lines = append(lines, "提及 Bot: 是")
	}
	if event.RepliedToBot {
		lines = append(lines, "回复 Bot: 是")
	}
	return strings.Join(lines, "\n")
}

func authorLabel(user pluginapi.UserInfo) string {
	display := firstNonEmpty(strings.TrimSpace(user.DisplayName), strings.TrimSpace(user.Nick), strings.TrimSpace(user.GlobalName), strings.TrimSpace(user.Username), strings.TrimSpace(user.ID))
	if strings.TrimSpace(user.ID) == "" {
		return display
	}
	return fmt.Sprintf("%s (ID: %s)", display, strings.TrimSpace(user.ID))
}

func targetListLabel(ids []string) string {
	if len(ids) == 0 {
		return "未设置，当前仅学习社区整体关系与黑话。"
	}
	items := make([]string, 0, len(ids))
	for _, id := range ids {
		items = append(items, "- `"+strings.TrimSpace(id)+"`")
	}
	return strings.Join(items, "\n")
}

func previewBlock(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return "```text\n" + shared.TruncateRunes(value, maxPanelPreviewRunes) + "\n```"
}

func relationsPreview(profile learningProfile) string {
	lines := make([]string, 0, len(profile.Relations)+1)
	if profile.RelationshipSummary != "" {
		lines = append(lines, shared.TruncateRunes(profile.RelationshipSummary, 220))
	}
	for index, relation := range profile.Relations {
		if index >= maxRelations {
			break
		}
		line := fmt.Sprintf("- %s <-> %s : %s (%d)", relation.LeftUserID, relation.RightUserID, firstNonEmpty(relation.Type, "未标注"), relation.Strength)
		if evidence := strings.TrimSpace(relation.Evidence); evidence != "" {
			line += " | " + shared.TruncateRunes(shared.SingleLine(evidence), 80)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func affinityPreview(values map[string]int) string {
	if len(values) == 0 {
		return ""
	}
	type item struct {
		UserID string
		Score  int
	}
	items := make([]item, 0, len(values))
	for userID, score := range values {
		items = append(items, item{UserID: userID, Score: score})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return items[i].UserID < items[j].UserID
		}
		return items[i].Score > items[j].Score
	})
	if len(items) > maxTopAffinities {
		items = items[:maxTopAffinities]
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- `%s`: %d/100", item.UserID, item.Score))
	}
	return strings.Join(lines, "\n")
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

func withProfile(state panelState, profile learningProfile) panelState {
	state.Profile = profile
	return state
}

func memoryPanelColor(enabled bool) int {
	if enabled {
		return 0x16A34A
	}
	return 0x475569
}

func boolLabel(value bool) string {
	if value {
		return "已启用"
	}
	return "未启用"
}

func joinNonEmpty(sep string, values ...string) string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		filtered = append(filtered, value)
	}
	return strings.Join(filtered, sep)
}

func prefixedSection(title, value string) string {
	title = strings.TrimSpace(title)
	value = strings.TrimSpace(value)
	if title == "" || value == "" {
		return ""
	}
	return title + ":\n" + value
}

func trimStrings(values []string, limit int) []string {
	filtered := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		filtered = append(filtered, value)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
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
	if err := pluginapi.Serve(manifest, &selfLearningPlugin{}); err != nil {
		log.Fatal(err)
	}
}
