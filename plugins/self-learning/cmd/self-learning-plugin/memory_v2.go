package main

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"discord-bot-plugins/internal/shared"
	"discord-bot-plugins/sdk/pluginapi"
)

const (
	maxPromptRecentMessages = 8
	maxPromptCards          = 6
	maxPromptEpisodes       = 3
	maxPromptEvidence       = 4
	maxPromptCardRunes      = 260
	maxPromptEpisodeRunes   = 360
	maxPromptSceneRunes     = 520
	cardListPageSize        = 160
	episodeListPageSize     = 120
	contextEmbedTimeout     = 1200 * time.Millisecond
	contextRerankTimeout    = 900 * time.Millisecond
	contextStepReserve      = 700 * time.Millisecond
)

type learnCardResult struct {
	Kind           string   `json:"kind"`
	SubjectID      string   `json:"subject_id,omitempty"`
	SubjectName    string   `json:"subject_name,omitempty"`
	Title          string   `json:"title"`
	Content        string   `json:"content"`
	Confidence     float64  `json:"confidence,omitempty"`
	TTLDays        int      `json:"ttl_days,omitempty"`
	Evidence       []string `json:"evidence,omitempty"`
	RelatedUserIDs []string `json:"related_user_ids,omitempty"`
}

type memoryCardRecord struct {
	Scope           pluginapi.PersonaScope `json:"scope"`
	Kind            string                 `json:"kind"`
	SubjectID       string                 `json:"subject_id,omitempty"`
	SubjectName     string                 `json:"subject_name,omitempty"`
	Title           string                 `json:"title"`
	Content         string                 `json:"content"`
	Confidence      float64                `json:"confidence,omitempty"`
	Evidence        []string               `json:"evidence,omitempty"`
	RelatedUserIDs  []string               `json:"related_user_ids,omitempty"`
	UpdatedAt       string                 `json:"updated_at,omitempty"`
	ExpiresAt       string                 `json:"expires_at,omitempty"`
	SourceEpisode   string                 `json:"source_episode,omitempty"`
	EmbeddingBase64 string                 `json:"embedding_base64,omitempty"`
}

type memoryEpisodeRecord struct {
	ID                  string                 `json:"id"`
	Scope               pluginapi.PersonaScope `json:"scope"`
	StartedAt           string                 `json:"started_at,omitempty"`
	EndedAt             string                 `json:"ended_at,omitempty"`
	SceneSummary        string                 `json:"scene_summary,omitempty"`
	Summary             string                 `json:"summary,omitempty"`
	StyleSummary        string                 `json:"style_summary,omitempty"`
	SlangSummary        string                 `json:"slang_summary,omitempty"`
	RelationshipSummary string                 `json:"relationship_summary,omitempty"`
	IntentSummary       string                 `json:"intent_summary,omitempty"`
	MoodSummary         string                 `json:"mood_summary,omitempty"`
	Highlights          []string               `json:"highlights,omitempty"`
	ParticipantIDs      []string               `json:"participant_ids,omitempty"`
	EvidenceMessageIDs  []string               `json:"evidence_message_ids,omitempty"`
	UpdatedAt           string                 `json:"updated_at,omitempty"`
	EmbeddingBase64     string                 `json:"embedding_base64,omitempty"`
}

type scoredCard struct {
	Key   string
	Score float64
	Card  memoryCardRecord
}

type scoredEpisode struct {
	Key     string
	Score   float64
	Episode memoryEpisodeRecord
}

type contextBudgetPreview struct {
	SceneSummary int `json:"scene_summary"`
	Cards        int `json:"cards"`
	Episodes     int `json:"episodes"`
	Evidence     int `json:"evidence"`
	Recent       int `json:"recent"`
}

func memoryBudgetPreset() contextBudgetPreview {
	return contextBudgetPreview{
		SceneSummary: maxPromptSceneRunes,
		Cards:        maxPromptCards * maxPromptCardRunes,
		Episodes:     maxPromptEpisodes * maxPromptEpisodeRunes,
		Evidence:     maxPromptEvidence * 180,
		Recent:       maxPromptRecentMessages * 220,
	}
}

func buildMemoryV2Context(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, config learningConfig, profile learningProfile, request pluginapi.ContextBuildRequest) (*pluginapi.ContextBuildResponse, error) {
	recentEvents, err := loadRecentEvents(ctx, host, scope)
	if err != nil {
		return nil, err
	}

	queryText := buildRetrievalQuery(request.CurrentMessage)
	queryVector, _ := embedWithinBudget(ctx, host, queryText, contextEmbedTimeout)
	retrievalScopes := scopeHierarchy(scope)
	actorIDs := relevantActorIDs(request.CurrentMessage, config)

	cards, err := selectRelevantCards(ctx, host, retrievalScopes, actorIDs, queryText, queryVector)
	if err != nil {
		return nil, err
	}
	episodes, err := selectRelevantEpisodes(ctx, host, retrievalScopes, actorIDs, queryText, queryVector)
	if err != nil {
		return nil, err
	}

	recent := buildRecentPromptMessages(recentEvents.Events, request.CurrentMessage)
	evidence := buildEvidenceLines(cards, episodes)
	blocks := buildMemoryPromptBlocks(config, profile, request.CurrentMessage, cards, episodes)
	personaPrompt := strings.TrimSpace(request.CurrentPersonaPrompt)
	if personaPrompt == "" {
		personaPrompt = strings.TrimSpace(profile.PersonaPrompt)
	}

	return &pluginapi.ContextBuildResponse{
		Override:      true,
		SystemPrompt:  strings.TrimSpace(request.CurrentSystemPrompt),
		PersonaPrompt: personaPrompt,
		Summary:       buildSceneSummary(config, profile, request.CurrentMessage),
		Retrieved:     evidence,
		Recent:        recent,
		PromptBlocks:  blocks,
	}, nil
}

func buildRetrievalQuery(message pluginapi.MessageContext) string {
	lines := []string{
		"当前消息:",
		strings.TrimSpace(message.Content),
		"发送者: " + authorLabel(message.Author),
	}
	if message.ReplyTo != nil {
		lines = append(lines,
			"回复对象: "+authorLabel(message.ReplyTo.Author),
			"被回复内容: "+strings.TrimSpace(message.ReplyTo.Content),
		)
	}
	if len(message.Images) > 0 {
		lines = append(lines, fmt.Sprintf("附带图片/表情: %d", len(message.Images)))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func scopeHierarchy(scope pluginapi.PersonaScope) []pluginapi.PersonaScope {
	values := []pluginapi.PersonaScope{scope}
	switch strings.TrimSpace(scope.Type) {
	case pluginapi.PersonaScopeThread:
		values = append(values,
			pluginapi.PersonaScope{Type: pluginapi.PersonaScopeChannel, GuildID: scope.GuildID, ChannelID: scope.ChannelID},
			pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGuild, GuildID: scope.GuildID},
			pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGlobal},
		)
	case pluginapi.PersonaScopeChannel:
		values = append(values,
			pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGuild, GuildID: scope.GuildID},
			pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGlobal},
		)
	case pluginapi.PersonaScopeGuild:
		values = append(values, pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGlobal})
	default:
		values = []pluginapi.PersonaScope{{Type: pluginapi.PersonaScopeGlobal}}
	}
	deduped := make([]pluginapi.PersonaScope, 0, len(values))
	seen := map[string]struct{}{}
	for _, item := range values {
		key := scopeKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, item)
	}
	return deduped
}

func relevantActorIDs(message pluginapi.MessageContext, config learningConfig) map[string]struct{} {
	actors := map[string]struct{}{}
	if userID := strings.TrimSpace(message.Author.ID); userID != "" {
		actors[userID] = struct{}{}
	}
	if message.ReplyTo != nil {
		if userID := strings.TrimSpace(message.ReplyTo.Author.ID); userID != "" {
			actors[userID] = struct{}{}
		}
	}
	for _, userID := range config.TargetUserIDs {
		userID = strings.TrimSpace(userID)
		if userID != "" {
			actors[userID] = struct{}{}
		}
	}
	return actors
}

func selectRelevantCards(ctx context.Context, host *pluginapi.HostClient, scopes []pluginapi.PersonaScope, actorIDs map[string]struct{}, queryText string, queryVector []float64) ([]memoryCardRecord, error) {
	candidates := make([]scoredCard, 0, 32)
	scopeWeights := scopeWeightMap(scopes)
	for _, scope := range scopes {
		cards, err := listMemoryCards(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		for _, item := range cards {
			if isExpiredAt(item.ExpiresAt) {
				continue
			}
			score := scoreCard(item, actorIDs, queryText, queryVector, scopeWeights[scopeKey(scope)])
			if score <= 0 {
				continue
			}
			candidates = append(candidates, scoredCard{
				Key:   cardStorageKey(scope, item),
				Score: score,
				Card:  item,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Card.Title < candidates[j].Card.Title
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > maxPromptCards*2 {
		candidates = candidates[:maxPromptCards*2]
	}
	candidates = rerankCardCandidates(ctx, host, queryText, candidates)
	if len(candidates) > maxPromptCards {
		candidates = candidates[:maxPromptCards]
	}

	selected := make([]memoryCardRecord, 0, len(candidates))
	for _, item := range candidates {
		selected = append(selected, item.Card)
	}
	return selected, nil
}

func selectRelevantEpisodes(ctx context.Context, host *pluginapi.HostClient, scopes []pluginapi.PersonaScope, actorIDs map[string]struct{}, queryText string, queryVector []float64) ([]memoryEpisodeRecord, error) {
	candidates := make([]scoredEpisode, 0, 24)
	scopeWeights := scopeWeightMap(scopes)
	for _, scope := range scopes {
		episodes, err := listMemoryEpisodes(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		for _, item := range episodes {
			score := scoreEpisode(item, actorIDs, queryText, queryVector, scopeWeights[scopeKey(scope)])
			if score <= 0 {
				continue
			}
			candidates = append(candidates, scoredEpisode{
				Key:     episodeStorageKey(scope, item.ID),
				Score:   score,
				Episode: item,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Episode.EndedAt > candidates[j].Episode.EndedAt
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > maxPromptEpisodes {
		candidates = candidates[:maxPromptEpisodes]
	}

	selected := make([]memoryEpisodeRecord, 0, len(candidates))
	for _, item := range candidates {
		selected = append(selected, item.Episode)
	}
	return selected, nil
}

func buildRecentPromptMessages(events []conversationEvent, current pluginapi.MessageContext) []pluginapi.PromptConversationMessage {
	trimmed := make([]conversationEvent, 0, len(events)+1)
	trimmed = append(trimmed, events...)
	if !hasEventMessage(trimmed, strings.TrimSpace(current.MessageID)) {
		trimmed = append(trimmed, conversationEvent{
			MessageID:    strings.TrimSpace(current.MessageID),
			Role:         "user",
			Time:         strings.TrimSpace(current.Time),
			Author:       current.Author,
			Content:      strings.TrimSpace(current.Content),
			ReplyTo:      current.ReplyTo,
			Images:       append([]pluginapi.ImageReference(nil), current.Images...),
			MentionedBot: current.MentionedBot,
			RepliedToBot: current.RepliedToBot,
		})
	}
	if len(trimmed) > maxPromptRecentMessages {
		trimmed = trimmed[len(trimmed)-maxPromptRecentMessages:]
	}
	messages := make([]pluginapi.PromptConversationMessage, 0, len(trimmed))
	for _, event := range trimmed {
		messages = append(messages, pluginapi.PromptConversationMessage{
			Role:    firstNonEmpty(strings.TrimSpace(event.Role), "user"),
			Content: renderEvent(event, 220),
		})
	}
	return messages
}

func hasEventMessage(events []conversationEvent, messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}
	for _, event := range events {
		if strings.TrimSpace(event.MessageID) == messageID {
			return true
		}
	}
	return false
}

func buildMemoryPromptBlocks(config learningConfig, profile learningProfile, current pluginapi.MessageContext, cards []memoryCardRecord, episodes []memoryEpisodeRecord) []pluginapi.PromptBlock {
	blocks := make([]pluginapi.PromptBlock, 0, 4)

	sceneLines := []string{
		"Memory V2 场景状态",
		"当前作用域: " + scopeLabel(scopeFromMessage(current)),
	}
	if len(config.TargetUserIDs) > 0 {
		sceneLines = append(sceneLines, "模仿目标用户: "+strings.Join(config.TargetUserIDs, ", "))
	}
	if affinity := profile.Affinity[strings.TrimSpace(current.Author.ID)]; affinity != 0 {
		sceneLines = append(sceneLines, fmt.Sprintf("当前对发言者的好感度: %d/100", affinity))
	}
	if profile.MoodSummary != "" {
		sceneLines = append(sceneLines, "当前情绪: "+shared.SingleLine(shared.TruncateRunes(profile.MoodSummary, 120)))
	}
	if profile.SceneSummary != "" {
		sceneLines = append(sceneLines, "近期场景: "+shared.SingleLine(shared.TruncateRunes(profile.SceneSummary, 180)))
	}
	blocks = append(blocks, pluginapi.PromptBlock{
		Role:    "system",
		Content: shared.TruncateRunes(strings.Join(sceneLines, "\n"), maxPromptSceneRunes),
	})

	styleParts := trimStrings([]string{
		"profile.style: " + profile.StyleSummary,
		"profile.slang: " + profile.SlangSummary,
		"profile.intent: " + profile.IntentSummary,
		"profile.relationship: " + profile.RelationshipSummary,
	}, 4)
	if len(styleParts) > 0 {
		blocks = append(blocks, pluginapi.PromptBlock{
			Role:    "system",
			Content: "表达与社区状态:\n" + shared.TruncateRunes(strings.Join(styleParts, "\n\n"), maxPromptSceneRunes),
		})
	}

	if len(cards) > 0 {
		lines := make([]string, 0, len(cards)+1)
		lines = append(lines, "结构化记忆卡片:")
		for index, card := range cards {
			lines = append(lines, fmt.Sprintf("%d. [%s] %s\n%s", index+1, firstNonEmpty(card.Kind, "fact"), firstNonEmpty(card.Title, "未命名卡片"), shared.TruncateRunes(card.Content, maxPromptCardRunes)))
		}
		blocks = append(blocks, pluginapi.PromptBlock{
			Role:    "system",
			Content: strings.Join(lines, "\n\n"),
		})
	}

	if len(episodes) > 0 {
		lines := make([]string, 0, len(episodes)+1)
		lines = append(lines, "相关历史片段:")
		for index, episode := range episodes {
			lines = append(lines, fmt.Sprintf("%d. %s\n%s", index+1, firstNonEmpty(episode.SceneSummary, episode.EndedAt), shared.TruncateRunes(joinNonEmpty("\n", episode.Summary, strings.Join(episode.Highlights, "；")), maxPromptEpisodeRunes)))
		}
		blocks = append(blocks, pluginapi.PromptBlock{
			Role:    "system",
			Content: strings.Join(lines, "\n\n"),
		})
	}

	return blocks
}

func buildSceneSummary(config learningConfig, profile learningProfile, current pluginapi.MessageContext) string {
	lines := []string{}
	if len(config.TargetUserIDs) > 0 {
		lines = append(lines, "目标用户: "+strings.Join(config.TargetUserIDs, ", "))
	}
	if profile.SceneSummary != "" {
		lines = append(lines, profile.SceneSummary)
	}
	if affinity := profile.Affinity[strings.TrimSpace(current.Author.ID)]; affinity != 0 {
		lines = append(lines, fmt.Sprintf("当前对发言者好感度 %d/100", affinity))
	}
	if profile.MoodSummary != "" {
		lines = append(lines, "情绪: "+shared.SingleLine(shared.TruncateRunes(profile.MoodSummary, 80)))
	}
	return shared.TruncateRunes(strings.Join(lines, "\n"), 360)
}

func buildEvidenceLines(cards []memoryCardRecord, episodes []memoryEpisodeRecord) []string {
	lines := make([]string, 0, maxPromptEvidence)
	for _, card := range cards {
		if len(lines) >= maxPromptEvidence {
			break
		}
		snippet := joinNonEmpty(" | ",
			firstNonEmpty(card.Title, "卡片"),
			shared.SingleLine(shared.TruncateRunes(card.Content, 120)),
			firstEvidence(card.Evidence),
		)
		if strings.TrimSpace(snippet) != "" {
			lines = append(lines, snippet)
		}
	}
	for _, episode := range episodes {
		if len(lines) >= maxPromptEvidence {
			break
		}
		snippet := joinNonEmpty(" | ",
			firstNonEmpty(episode.SceneSummary, episode.EndedAt),
			shared.SingleLine(shared.TruncateRunes(episode.Summary, 120)),
		)
		if strings.TrimSpace(snippet) != "" {
			lines = append(lines, snippet)
		}
	}
	return lines
}

func firstEvidence(values []string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return shared.SingleLine(shared.TruncateRunes(value, 80))
		}
	}
	return ""
}

func saveLearningArtifacts(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, profile learningProfile, result learnResult, events []conversationEvent) (learningProfile, error) {
	episodeID, err := upsertEpisodeRecord(ctx, host, scope, result, events)
	if err != nil {
		return profile, err
	}
	if err := upsertMemoryCards(ctx, host, scope, episodeID, result.Cards); err != nil {
		return profile, err
	}
	profile.SceneSummary = firstNonEmpty(result.SceneSummary, profile.SceneSummary)
	episodeCount, err := pruneEpisodeRecords(ctx, host, scope, maxEpisodeRecords)
	if err != nil {
		return profile, err
	}
	cardCount, err := pruneCardRecords(ctx, host, scope, maxCardRecords)
	if err != nil {
		return profile, err
	}
	profile.EpisodeCount = episodeCount
	profile.CardCount = cardCount
	return profile, nil
}

func saveProfileError(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, err error) error {
	if err == nil {
		return nil
	}
	profile, loadErr := loadProfile(ctx, host, scope)
	if loadErr != nil {
		return loadErr
	}
	profile.LastError = strings.TrimSpace(err.Error())
	return saveProfile(ctx, host, scope, profile)
}

func syncMemoryWorldBook(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, profile learningProfile) error {
	guildID := strings.TrimSpace(scope.GuildID)
	if guildID == "" {
		return nil
	}
	content := joinNonEmpty("\n\n",
		prefixedSection("社区黑话与语境", profile.SlangSummary),
		prefixedSection("表达风格", profile.StyleSummary),
		prefixedSection("关系观察", profile.RelationshipSummary),
	)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return host.UpsertWorldBook(ctx, pluginapi.UpsertWorldBookRequest{
		Key:     "self-learning:community:" + guildID,
		Title:   "Self Learning Community Memory",
		Content: content,
		GuildID: guildID,
		Source:  "official_self_learning",
	})
}

func upsertEpisodeRecord(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, result learnResult, events []conversationEvent) (string, error) {
	if len(events) == 0 {
		return "", nil
	}
	startedAt := firstNonEmpty(strings.TrimSpace(events[0].Time), time.Now().Format(time.RFC3339))
	endedAt := firstNonEmpty(strings.TrimSpace(events[len(events)-1].Time), startedAt)
	episodeID := buildEpisodeID(events)
	record := memoryEpisodeRecord{
		ID:                  episodeID,
		Scope:               scope,
		StartedAt:           startedAt,
		EndedAt:             endedAt,
		SceneSummary:        strings.TrimSpace(firstNonEmpty(result.SceneSummary, result.Summary)),
		Summary:             strings.TrimSpace(result.Summary),
		StyleSummary:        strings.TrimSpace(result.StyleSummary),
		SlangSummary:        strings.TrimSpace(result.SlangSummary),
		RelationshipSummary: strings.TrimSpace(result.RelationshipSummary),
		IntentSummary:       strings.TrimSpace(result.IntentSummary),
		MoodSummary:         strings.TrimSpace(result.MoodSummary),
		Highlights:          trimStrings(result.Highlights, maxHighlights),
		ParticipantIDs:      collectParticipantIDs(events),
		EvidenceMessageIDs:  collectEvidenceMessageIDs(events),
		UpdatedAt:           time.Now().Format(time.RFC3339),
	}
	if embedding, err := host.Embed(ctx, episodeEmbeddingInput(record)); err == nil {
		record.EmbeddingBase64 = encodeEmbeddingVector(embedding)
	}
	return episodeID, host.RecordsPut(ctx, episodeCollection, episodeStorageKey(scope, episodeID), record)
}

func upsertMemoryCards(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, episodeID string, cards []learnCardResult) error {
	for _, item := range cards {
		record, key := normalizeLearnCard(scope, episodeID, item)
		if key == "" {
			continue
		}
		if embedding, err := host.Embed(ctx, cardEmbeddingInput(record)); err == nil {
			record.EmbeddingBase64 = encodeEmbeddingVector(embedding)
		}
		if err := host.RecordsPut(ctx, cardCollection, key, record); err != nil {
			return err
		}
	}
	return nil
}

func normalizeLearnCard(scope pluginapi.PersonaScope, episodeID string, item learnCardResult) (memoryCardRecord, string) {
	record := memoryCardRecord{
		Scope:          scope,
		Kind:           normalizeCardKind(item.Kind),
		SubjectID:      strings.TrimSpace(item.SubjectID),
		SubjectName:    strings.TrimSpace(item.SubjectName),
		Title:          strings.TrimSpace(item.Title),
		Content:        strings.TrimSpace(item.Content),
		Confidence:     clampConfidence(item.Confidence),
		Evidence:       trimStrings(item.Evidence, 4),
		RelatedUserIDs: trimStrings(item.RelatedUserIDs, 8),
		UpdatedAt:      time.Now().Format(time.RFC3339),
		SourceEpisode:  strings.TrimSpace(episodeID),
	}
	if record.Title == "" || record.Content == "" {
		return memoryCardRecord{}, ""
	}
	if record.Confidence == 0 {
		record.Confidence = 0.68
	}
	if ttlDays := normalizeTTLDays(item.TTLDays, record.Kind); ttlDays > 0 {
		record.ExpiresAt = time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour).Format(time.RFC3339)
	}
	return record, cardStorageKey(scope, record)
}

func listMemoryEpisodes(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) ([]memoryEpisodeRecord, error) {
	response, err := host.RecordsList(ctx, pluginapi.RecordsListRequest{
		Collection: episodeCollection,
		Prefix:     scopeKey(scope) + "|",
		Limit:      episodeListPageSize,
	})
	if err != nil {
		return nil, err
	}
	items := make([]memoryEpisodeRecord, 0, len(response.Items))
	for _, entry := range response.Items {
		var record memoryEpisodeRecord
		if err := json.Unmarshal(entry.Value, &record); err != nil {
			continue
		}
		items = append(items, normalizeEpisodeRecord(record))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].EndedAt > items[j].EndedAt
	})
	return items, nil
}

func listMemoryCards(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) ([]memoryCardRecord, error) {
	response, err := host.RecordsList(ctx, pluginapi.RecordsListRequest{
		Collection: cardCollection,
		Prefix:     scopeKey(scope) + "|",
		Limit:      cardListPageSize,
	})
	if err != nil {
		return nil, err
	}
	items := make([]memoryCardRecord, 0, len(response.Items))
	for _, entry := range response.Items {
		var record memoryCardRecord
		if err := json.Unmarshal(entry.Value, &record); err != nil {
			continue
		}
		items = append(items, normalizeCardRecord(record))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt == items[j].UpdatedAt {
			return items[i].Title < items[j].Title
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	return items, nil
}

func pruneEpisodeRecords(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, keep int) (int, error) {
	episodes, err := listMemoryEpisodes(ctx, host, scope)
	if err != nil {
		return 0, err
	}
	if len(episodes) <= keep {
		return len(episodes), nil
	}
	for _, item := range episodes[keep:] {
		if err := host.RecordsDelete(ctx, episodeCollection, episodeStorageKey(scope, item.ID)); err != nil {
			return 0, err
		}
	}
	return keep, nil
}

func pruneCardRecords(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, keep int) (int, error) {
	cards, err := listMemoryCards(ctx, host, scope)
	if err != nil {
		return 0, err
	}
	active := make([]memoryCardRecord, 0, len(cards))
	for _, item := range cards {
		if isExpiredAt(item.ExpiresAt) {
			if err := host.RecordsDelete(ctx, cardCollection, cardStorageKey(scope, item)); err != nil {
				return 0, err
			}
			continue
		}
		active = append(active, item)
	}
	if len(active) <= keep {
		return len(active), nil
	}
	for _, item := range active[keep:] {
		if err := host.RecordsDelete(ctx, cardCollection, cardStorageKey(scope, item)); err != nil {
			return 0, err
		}
	}
	return keep, nil
}

func buildEpisodeID(events []conversationEvent) string {
	hasher := sha1.New()
	for _, event := range events {
		_, _ = hasher.Write([]byte(firstNonEmpty(event.MessageID, event.Time, event.Content)))
		_, _ = hasher.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hasher.Sum(nil))[:16]
}

func collectParticipantIDs(events []conversationEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if userID := strings.TrimSpace(event.Author.ID); userID != "" {
			ids = append(ids, userID)
		}
		if event.ReplyTo != nil {
			if userID := strings.TrimSpace(event.ReplyTo.Author.ID); userID != "" {
				ids = append(ids, userID)
			}
		}
	}
	return trimStrings(ids, 16)
}

func collectEvidenceMessageIDs(events []conversationEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if messageID := strings.TrimSpace(event.MessageID); messageID != "" {
			ids = append(ids, messageID)
		}
	}
	return trimStrings(ids, 16)
}

func episodeEmbeddingInput(record memoryEpisodeRecord) string {
	return joinNonEmpty("\n",
		record.SceneSummary,
		record.Summary,
		record.StyleSummary,
		record.SlangSummary,
		record.RelationshipSummary,
		record.IntentSummary,
		record.MoodSummary,
		strings.Join(record.Highlights, "\n"),
	)
}

func cardEmbeddingInput(record memoryCardRecord) string {
	return joinNonEmpty("\n", record.Kind, record.Title, record.Content, strings.Join(record.Evidence, "\n"))
}

func scopeWeightMap(scopes []pluginapi.PersonaScope) map[string]float64 {
	weights := map[string]float64{}
	for index, scope := range scopes {
		weight := 3.6 - float64(index)*0.9
		if weight < 0.6 {
			weight = 0.6
		}
		weights[scopeKey(scope)] = weight
	}
	return weights
}

func scoreCard(card memoryCardRecord, actorIDs map[string]struct{}, queryText string, queryVector []float64, scopeWeight float64) float64 {
	score := scopeWeight
	if _, ok := actorIDs[strings.TrimSpace(card.SubjectID)]; ok {
		score += 2.8
	}
	for _, related := range card.RelatedUserIDs {
		if _, ok := actorIDs[strings.TrimSpace(related)]; ok {
			score += 1.4
		}
	}
	score += cardConfidenceWeight(card.Confidence)
	score += recencyWeight(card.UpdatedAt, 14*24*time.Hour)
	score += lexicalScore(queryText, card.Title+" "+card.Content) * 2.2
	score += cosineScore(queryVector, decodeEmbeddingVector(card.EmbeddingBase64)) * 3.1
	score += cardKindWeight(card.Kind)
	return score
}

func scoreEpisode(episode memoryEpisodeRecord, actorIDs map[string]struct{}, queryText string, queryVector []float64, scopeWeight float64) float64 {
	score := scopeWeight + recencyWeight(episode.EndedAt, 10*24*time.Hour)
	for _, participant := range episode.ParticipantIDs {
		if _, ok := actorIDs[strings.TrimSpace(participant)]; ok {
			score += 1.4
		}
	}
	score += lexicalScore(queryText, joinNonEmpty(" ", episode.SceneSummary, episode.Summary, strings.Join(episode.Highlights, " "))) * 1.8
	score += cosineScore(queryVector, decodeEmbeddingVector(episode.EmbeddingBase64)) * 2.4
	return score
}

func rerankCardCandidates(ctx context.Context, host *pluginapi.HostClient, query string, candidates []scoredCard) []scoredCard {
	if len(candidates) <= 1 || strings.TrimSpace(query) == "" {
		return candidates
	}
	documents, indexByDoc := buildCardRerankDocuments(candidates)
	reranked, err := rerankWithinBudget(ctx, host, query, documents, minInt(len(documents), maxPromptCards), contextRerankTimeout)
	if err != nil || len(reranked) == 0 {
		return candidates
	}
	return applyCardRerankResults(candidates, reranked, indexByDoc)
}

func buildCardRerankDocuments(candidates []scoredCard) ([]string, map[string][]int) {
	documents := make([]string, 0, len(candidates))
	indexByDoc := map[string][]int{}
	for index, item := range candidates {
		document := joinNonEmpty("\n", item.Card.Kind, item.Card.Title, item.Card.Content)
		documents = append(documents, document)
		indexByDoc[document] = append(indexByDoc[document], index)
	}
	return documents, indexByDoc
}

func applyCardRerankResults(candidates []scoredCard, reranked []string, indexByDoc map[string][]int) []scoredCard {
	boosted := make([]scoredCard, 0, len(candidates))
	used := map[int]struct{}{}
	consumedByDoc := map[string]int{}
	for rank, doc := range reranked {
		indexes, ok := indexByDoc[doc]
		if !ok {
			continue
		}
		offset := consumedByDoc[doc]
		if offset >= len(indexes) {
			continue
		}
		index := indexes[offset]
		consumedByDoc[doc] = offset + 1
		item := candidates[index]
		item.Score += 1.6 - float64(rank)*0.18
		boosted = append(boosted, item)
		used[index] = struct{}{}
	}
	for index, item := range candidates {
		if _, ok := used[index]; ok {
			continue
		}
		boosted = append(boosted, item)
	}
	sort.Slice(boosted, func(i, j int) bool {
		return boosted[i].Score > boosted[j].Score
	})
	return boosted
}

func embedWithinBudget(ctx context.Context, host *pluginapi.HostClient, input string, stepTimeout time.Duration) ([]float64, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	callCtx, cancel, ok := boundedStepContext(ctx, stepTimeout, contextStepReserve)
	if !ok {
		return nil, nil
	}
	defer cancel()
	return host.Embed(callCtx, input)
}

func rerankWithinBudget(ctx context.Context, host *pluginapi.HostClient, query string, documents []string, topN int, stepTimeout time.Duration) ([]string, error) {
	if strings.TrimSpace(query) == "" || len(documents) == 0 || topN <= 0 {
		return nil, nil
	}
	callCtx, cancel, ok := boundedStepContext(ctx, stepTimeout, contextStepReserve)
	if !ok {
		return nil, nil
	}
	defer cancel()
	return host.Rerank(callCtx, query, documents, topN)
}

func boundedStepContext(parent context.Context, timeout, reserve time.Duration) (context.Context, context.CancelFunc, bool) {
	if timeout <= 0 {
		timeout = time.Second
	}
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline) - reserve
		if remaining <= 0 {
			return parent, func() {}, false
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	child, cancel := context.WithTimeout(parent, timeout)
	return child, cancel, true
}

func cardKindWeight(kind string) float64 {
	switch strings.TrimSpace(kind) {
	case "style", "slang":
		return 0.9
	case "relationship":
		return 1.1
	case "preference":
		return 1.0
	case "topic":
		return 0.7
	default:
		return 0.8
	}
}

func recencyWeight(value string, fullWindow time.Duration) float64 {
	if fullWindow <= 0 {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	age := time.Since(parsed)
	if age <= 0 {
		return 1.4
	}
	if age >= fullWindow {
		return 0
	}
	return (1 - float64(age)/float64(fullWindow)) * 1.4
}

func lexicalScore(query, text string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	text = strings.ToLower(strings.TrimSpace(text))
	if query == "" || text == "" {
		return 0
	}
	if strings.Contains(text, query) {
		return 1
	}
	score := 0.0
	for _, token := range strings.FieldsFunc(query, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || r == ' ' || r == ',' || r == '，' || r == '。' || r == ':' || r == '：'
	}) {
		token = strings.TrimSpace(token)
		if len([]rune(token)) < 2 {
			continue
		}
		if strings.Contains(text, token) {
			score += 0.22
		}
	}
	if score > 1 {
		score = 1
	}
	return score
}

func cosineScore(left, right []float64) float64 {
	if len(left) == 0 || len(right) == 0 || len(left) != len(right) {
		return 0
	}
	return math.Max(0, cosineSimilarity(left, right))
}

func cosineSimilarity(left, right []float64) float64 {
	var dot float64
	var leftNorm float64
	var rightNorm float64
	for index := range left {
		dot += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func encodeEmbeddingVector(vector []float64) string {
	if len(vector) == 0 {
		return ""
	}
	buf := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(buf[index*4:(index+1)*4], math.Float32bits(float32(value)))
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}

func decodeEmbeddingVector(encoded string) []float64 {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(payload)%4 != 0 {
		return nil
	}
	vector := make([]float64, 0, len(payload)/4)
	for offset := 0; offset < len(payload); offset += 4 {
		value := math.Float32frombits(binary.LittleEndian.Uint32(payload[offset : offset+4]))
		vector = append(vector, float64(value))
	}
	return vector
}

func normalizeCardRecord(record memoryCardRecord) memoryCardRecord {
	record.Kind = normalizeCardKind(record.Kind)
	record.SubjectID = strings.TrimSpace(record.SubjectID)
	record.SubjectName = strings.TrimSpace(record.SubjectName)
	record.Title = strings.TrimSpace(record.Title)
	record.Content = strings.TrimSpace(record.Content)
	record.Confidence = clampConfidence(record.Confidence)
	record.Evidence = trimStrings(record.Evidence, 4)
	record.RelatedUserIDs = trimStrings(record.RelatedUserIDs, 8)
	record.UpdatedAt = strings.TrimSpace(record.UpdatedAt)
	record.ExpiresAt = strings.TrimSpace(record.ExpiresAt)
	record.SourceEpisode = strings.TrimSpace(record.SourceEpisode)
	record.EmbeddingBase64 = strings.TrimSpace(record.EmbeddingBase64)
	return record
}

func normalizeEpisodeRecord(record memoryEpisodeRecord) memoryEpisodeRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.SceneSummary = strings.TrimSpace(record.SceneSummary)
	record.Summary = strings.TrimSpace(record.Summary)
	record.StyleSummary = strings.TrimSpace(record.StyleSummary)
	record.SlangSummary = strings.TrimSpace(record.SlangSummary)
	record.RelationshipSummary = strings.TrimSpace(record.RelationshipSummary)
	record.IntentSummary = strings.TrimSpace(record.IntentSummary)
	record.MoodSummary = strings.TrimSpace(record.MoodSummary)
	record.Highlights = trimStrings(record.Highlights, maxHighlights)
	record.ParticipantIDs = trimStrings(record.ParticipantIDs, 16)
	record.EvidenceMessageIDs = trimStrings(record.EvidenceMessageIDs, 16)
	record.StartedAt = strings.TrimSpace(record.StartedAt)
	record.EndedAt = strings.TrimSpace(record.EndedAt)
	record.UpdatedAt = strings.TrimSpace(record.UpdatedAt)
	record.EmbeddingBase64 = strings.TrimSpace(record.EmbeddingBase64)
	return record
}

func normalizeCardKind(kind string) string {
	kind = strings.TrimSpace(strings.ToLower(kind))
	switch kind {
	case "fact", "preference", "style", "slang", "relationship", "topic":
		return kind
	default:
		return "fact"
	}
}

func normalizeTTLDays(ttlDays int, kind string) int {
	if ttlDays > 0 {
		if ttlDays > 180 {
			return 180
		}
		return ttlDays
	}
	switch kind {
	case "topic":
		return 14
	case "slang", "style":
		return 60
	case "relationship":
		return 90
	default:
		return 0
	}
}

func clampConfidence(value float64) float64 {
	switch {
	case value <= 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func cardConfidenceWeight(value float64) float64 {
	return clampConfidence(value) * 1.2
}

func isExpiredAt(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return time.Now().After(parsed)
}

func episodeStorageKey(scope pluginapi.PersonaScope, episodeID string) string {
	return fmt.Sprintf("%s|%s", scopeKey(scope), strings.TrimSpace(episodeID))
}

func cardStorageKey(scope pluginapi.PersonaScope, record memoryCardRecord) string {
	identity := strings.ToLower(joinNonEmpty("|", normalizeCardKind(record.Kind), strings.TrimSpace(record.SubjectID), strings.TrimSpace(record.SubjectName), strings.TrimSpace(record.Title)))
	hasher := sha1.Sum([]byte(identity))
	return fmt.Sprintf("%s|%s|%s", scopeKey(scope), normalizeCardKind(record.Kind), hex.EncodeToString(hasher[:])[:16])
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
