package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"discord-bot-plugins/internal/shared"
	"discord-bot-plugins/sdk/pluginapi"
)

const (
	emojiActionRefresh     = "emoji:refresh"
	emojiActionAnalyzeInc  = "emoji:analyze-incremental"
	emojiActionAnalyzeFull = "emoji:analyze-full"
	emojiActionViewBook    = "emoji:view-worldbook"

	emojiStoragePrefix   = "emoji_profile:"
	emojiWorldBookPrefix = "emoji:guild:"
	emojiBatchSize       = 16
	emojiSheetColumns    = 4
	emojiSheetRows       = 4
	emojiSheetCellSize   = 144
	emojiSheetPadding    = 16
	emojiSheetOuterGap   = 20
	emojiPreviewLimit    = 900
	emojiAnalyzeTimeout  = 3 * time.Minute
)

type emojiProfile struct {
	GuildID         string   `json:"guild_id"`
	GuildName       string   `json:"guild_name"`
	EmojiIDs        []string `json:"emoji_ids"`
	EmojiCount      int      `json:"emoji_count"`
	Summary         string   `json:"summary"`
	WorldBookKey    string   `json:"worldbook_key"`
	LastAnalyzedAt  string   `json:"last_analyzed_at"`
	LastAnalyzedBy  string   `json:"last_analyzed_by"`
	LastAnalyzeMode string   `json:"last_analyze_mode"`
}

type emojiPlugin struct {
	pluginapi.BasePlugin
	httpClient *http.Client
	mu         sync.Mutex
	analyzing  map[string]struct{}
}

func newEmojiPlugin() *emojiPlugin {
	return &emojiPlugin{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		analyzing:  map[string]struct{}{},
	}
}

func (p *emojiPlugin) OnSlashCommand(ctx context.Context, host *pluginapi.HostClient, request pluginapi.SlashCommandRequest) (*pluginapi.InteractionResponse, error) {
	if strings.TrimSpace(request.Guild.ID) == "" {
		return ephemeralText("表情管理只能在服务器频道中使用。"), nil
	}
	message, err := p.buildPanel(ctx, host, request.Guild.ID, request.Guild.Name, request.User, "")
	if err != nil {
		return nil, err
	}
	message.Ephemeral = true
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeMessage,
		Message: message,
	}, nil
}

func (p *emojiPlugin) OnComponent(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest) (*pluginapi.InteractionResponse, error) {
	guildID := strings.TrimSpace(request.Guild.ID)
	if guildID == "" {
		return ephemeralText("表情管理只能在服务器频道中使用。"), nil
	}

	switch strings.TrimSpace(request.CustomID) {
	case emojiActionRefresh:
		return p.panelUpdate(ctx, host, request, "已刷新表情管理面板。")
	case emojiActionViewBook:
		return p.worldBookView(ctx, host, guildID, request.Guild.Name)
	case emojiActionAnalyzeInc, emojiActionAnalyzeFull:
		if !request.User.IsAdmin {
			return p.panelUpdate(ctx, host, request, "你没有权限执行这个操作。")
		}
		if !p.beginAnalysis(guildID) {
			return p.panelUpdate(ctx, host, request, "当前服务器的表情分析任务已在运行，请稍后刷新面板。")
		}

		mode := strings.TrimSpace(request.CustomID) == emojiActionAnalyzeFull
		go func(guildID, guildName, authorID string, full bool) {
			defer p.finishAnalysis(guildID)

			runCtx, cancel := context.WithTimeout(context.Background(), emojiAnalyzeTimeout)
			defer cancel()

			if err := p.runAnalysis(runCtx, host, guildID, guildName, authorID, full); err != nil {
				_ = host.Log(context.Background(), "WARN", "emoji analysis failed: "+err.Error())
			}
		}(guildID, request.Guild.Name, request.User.ID, mode)

		notice := "已开始增量分析，完成后请点刷新查看最新状态。"
		if mode {
			notice = "已开始完整重建，完成后请点刷新查看最新状态。"
		}
		return p.panelUpdate(ctx, host, request, notice)
	default:
		return p.panelUpdate(ctx, host, request, "未知的表情管理操作。")
	}
}

func (p *emojiPlugin) panelUpdate(ctx context.Context, host *pluginapi.HostClient, request pluginapi.ComponentRequest, notice string) (*pluginapi.InteractionResponse, error) {
	message, err := p.buildPanel(ctx, host, request.Guild.ID, request.Guild.Name, request.User, notice)
	if err != nil {
		return nil, err
	}
	return &pluginapi.InteractionResponse{
		Type:    pluginapi.InteractionResponseTypeUpdate,
		Message: message,
	}, nil
}

func (p *emojiPlugin) worldBookView(ctx context.Context, host *pluginapi.HostClient, guildID, guildName string) (*pluginapi.InteractionResponse, error) {
	profile, err := loadEmojiProfile(ctx, host, guildID)
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(profile.Summary)
	if entry, err := host.GetWorldBook(ctx, emojiWorldBookKey(guildID)); err == nil && entry != nil && strings.TrimSpace(entry.Content) != "" {
		content = strings.TrimSpace(entry.Content)
	}
	if content == "" {
		return ephemeralText("当前服务器还没有表情世界书内容。"), nil
	}
	return &pluginapi.InteractionResponse{
		Type: pluginapi.InteractionResponseTypeMessage,
		Message: &pluginapi.InteractionMessage{
			Ephemeral: true,
			Embeds: []pluginapi.Embed{
				{
					Title:       "世界书预览",
					Description: "服务器: " + emojiGuildLabel(guildID, guildName) + "\n\n```text\n" + shared.TruncateRunes(content, 3900) + "\n```",
					Color:       0x3B82F6,
				},
			},
		},
	}, nil
}

func (p *emojiPlugin) buildPanel(ctx context.Context, host *pluginapi.HostClient, guildID, guildName string, user pluginapi.UserInfo, notice string) (*pluginapi.InteractionMessage, error) {
	profile, err := loadEmojiProfile(ctx, host, guildID)
	if err != nil {
		return nil, err
	}
	guildName = emojiGuildLabel(guildID, guildName)
	worldBookText := strings.TrimSpace(profile.Summary)
	if entry, err := host.GetWorldBook(ctx, emojiWorldBookKey(guildID)); err == nil && entry != nil && strings.TrimSpace(entry.Content) != "" {
		worldBookText = strings.TrimSpace(entry.Content)
	}
	analyzing := p.isAnalyzing(guildID)
	status := "待分析"
	switch {
	case analyzing:
		status = "分析中"
	case strings.TrimSpace(profile.LastAnalyzedAt) != "":
		status = "已完成"
	}

	description := []string{"把服务器自定义表情做成 4x4 图组送去分析，并把总结写入世界书。"}
	if !user.IsAdmin {
		description = append(description, "你当前只有查看权限。")
	}
	if strings.TrimSpace(notice) != "" {
		description = append(description, "提示: "+strings.TrimSpace(notice))
	}

	return &pluginapi.InteractionMessage{
		Content: strings.TrimSpace(notice),
		Embeds: []pluginapi.Embed{
			{
				Title:       "服务器表情管理",
				Description: strings.Join(description, "\n"),
				Color:       emojiPanelColor(user.IsAdmin, analyzing, worldBookText != ""),
				Fields: []pluginapi.EmbedField{
					{Name: "服务器", Value: fmt.Sprintf("%s\nID: `%s`", shared.TruncateRunes(guildName, 120), guildID), Inline: false},
					{Name: "状态", Value: emojiStatusFieldValue(profile, status, analyzing), Inline: false},
					{Name: "世界书预览", Value: emojiWorldBookPreview(worldBookText), Inline: false},
				},
				Footer: "增量分析只处理新增表情；若检测到删除，会自动回退为全量重建。",
			},
		},
		Components: []pluginapi.ActionRow{
			{
				Buttons: []pluginapi.Button{
					{CustomID: emojiActionAnalyzeInc, Label: "增量分析", Style: "primary", Disabled: !user.IsAdmin || analyzing},
					{CustomID: emojiActionAnalyzeFull, Label: "完整重建", Style: "danger", Disabled: !user.IsAdmin || analyzing},
					{CustomID: emojiActionRefresh, Label: "刷新", Style: "secondary"},
					{CustomID: emojiActionViewBook, Label: "查看世界书", Style: "success", Disabled: worldBookText == ""},
				},
			},
		},
	}, nil
}

func (p *emojiPlugin) runAnalysis(ctx context.Context, host *pluginapi.HostClient, guildID, guildName, authorID string, forceFull bool) error {
	emojis, err := host.ListGuildEmojis(ctx, guildID)
	if err != nil {
		return err
	}

	profile, err := loadEmojiProfile(ctx, host, guildID)
	if err != nil {
		return err
	}

	assets := collectGuildEmojiAssets(emojis)
	currentIDs := emojiAssetIDs(assets)
	worldBookKey := emojiWorldBookKey(guildID)
	timestamp := emojiAnalysisTimestamp()

	if len(assets) == 0 {
		if err := host.DeleteWorldBook(ctx, worldBookKey); err != nil {
			return err
		}
		return saveEmojiProfile(ctx, host, guildID, emojiProfile{
			GuildID:         guildID,
			GuildName:       guildName,
			WorldBookKey:    worldBookKey,
			LastAnalyzedAt:  timestamp,
			LastAnalyzedBy:  authorID,
			LastAnalyzeMode: "空服务器",
		})
	}

	previousIDs := make(map[string]struct{}, len(profile.EmojiIDs))
	for _, id := range profile.EmojiIDs {
		previousIDs[id] = struct{}{}
	}
	currentSet := make(map[string]struct{}, len(currentIDs))
	for _, id := range currentIDs {
		currentSet[id] = struct{}{}
	}
	removedCount := 0
	for id := range previousIDs {
		if _, ok := currentSet[id]; !ok {
			removedCount++
		}
	}

	modeLabel := "增量分析"
	existingSummary := ""
	targetAssets := assets
	switch {
	case forceFull:
		modeLabel = "完整重建"
	case len(profile.EmojiIDs) == 0 || strings.TrimSpace(profile.Summary) == "":
		modeLabel = "完整重建"
	case removedCount > 0:
		modeLabel = "完整重建"
	default:
		existingSummary = strings.TrimSpace(profile.Summary)
		targetAssets = filterNewEmojiAssets(assets, previousIDs)
		if len(targetAssets) == 0 {
			profile.GuildID = guildID
			profile.GuildName = guildName
			profile.EmojiIDs = currentIDs
			profile.EmojiCount = len(currentIDs)
			profile.WorldBookKey = worldBookKey
			return saveEmojiProfile(ctx, host, guildID, profile)
		}
	}

	summary, err := p.generateWorldBook(ctx, host, guildID, guildName, authorID, targetAssets, existingSummary, modeLabel)
	if err != nil {
		return err
	}
	if err := host.UpsertWorldBook(ctx, pluginapi.UpsertWorldBookRequest{
		Key:     worldBookKey,
		Title:   emojiWorldBookTitle(guildName),
		Content: summary,
		GuildID: guildID,
		Source:  "emoji_analysis",
	}); err != nil {
		return err
	}
	return saveEmojiProfile(ctx, host, guildID, emojiProfile{
		GuildID:         guildID,
		GuildName:       guildName,
		EmojiIDs:        currentIDs,
		EmojiCount:      len(currentIDs),
		Summary:         summary,
		WorldBookKey:    worldBookKey,
		LastAnalyzedAt:  timestamp,
		LastAnalyzedBy:  authorID,
		LastAnalyzeMode: modeLabel,
	})
}

func (p *emojiPlugin) generateWorldBook(ctx context.Context, host *pluginapi.HostClient, guildID, guildName, authorID string, assets []emojiAsset, existingSummary, modeLabel string) (string, error) {
	messages := []pluginapi.ChatMessage{
		{
			Role: "system",
			Content: strings.TrimSpace(`你是 Discord 服务器的表情管理 agent。
你的任务是根据给出的服务器自定义表情图组，编写一段适合注入 system prompt 的“世界书”文本。
要求：
1. 必须用简洁中文输出。
2. 必须保留每个表情的可直接发送语法，例如 <:name:id> 或 <a:name:id>。
3. 重点描述每个表情大致表达的情绪、语气、适用场景、在聊天里什么时候适合发。
4. 不确定时要明确写“可能”“猜测”“大致”等，不要伪装成确定结论。
5. 输出应适合长期注入 prompt，不要写分析过程，不要输出 JSON。`),
		},
		{
			Role: "user",
			Content: strings.Join([]string{
				"服务器名称: " + emojiGuildLabel(guildID, guildName),
				"服务器ID: " + guildID,
				"本次操作: " + strings.TrimSpace(modeLabel),
				"触发管理员ID: " + strings.TrimSpace(authorID),
				renderExistingEmojiSummary(existingSummary),
			}, "\n\n"),
		},
	}

	batches := chunkEmojiAssets(assets, emojiBatchSize)
	appended := 0
	for batchIndex, batch := range batches {
		sheetURL, rendered, err := p.renderEmojiBatchSheet(ctx, batch)
		if err != nil {
			return "", err
		}
		if len(rendered) == 0 {
			continue
		}
		appended++
		lines := []string{
			fmt.Sprintf("第 %d / %d 组表情图。图内排布顺序为从左到右、从上到下。", batchIndex+1, len(batches)),
			"请结合这张图识别这些表情的大致用法：",
		}
		for index, asset := range rendered {
			lines = append(lines, fmt.Sprintf("%d. %s (名称: %s, ID: %s)", index+1, asset.Syntax, asset.Name, asset.ID))
		}
		messages = append(messages, pluginapi.ChatMessage{
			Role: "user",
			Parts: []pluginapi.ChatContentPart{
				{Type: "text", Text: strings.Join(lines, "\n")},
				{Type: "image_url", ImageURL: &pluginapi.ChatImageURL{URL: sheetURL}},
			},
		})
	}
	if appended == 0 {
		return "", fmt.Errorf("没有可提交给模型的表情图组")
	}

	messages = append(messages, pluginapi.ChatMessage{
		Role: "user",
		Content: strings.TrimSpace(`现在请直接输出更新后的“服务器表情世界书”正文。
输出要求：
1. 先给一小段整体风格说明。
2. 再给“表情清单”，尽量覆盖所有本次看到的表情。
3. 每条都要保留表情语法，并解释适用语境。
4. 如果是增量分析，要把已有世界书里仍然有效的内容保留下来并融合。
5. 不要输出 JSON，不要加无关客套。`),
	})

	content, err := host.Chat(ctx, messages)
	if err != nil {
		return "", err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("模型没有返回表情世界书内容")
	}
	return content, nil
}

func (p *emojiPlugin) renderEmojiBatchSheet(ctx context.Context, assets []emojiAsset) (string, []emojiAsset, error) {
	width := emojiSheetColumns*emojiSheetCellSize + (emojiSheetColumns-1)*emojiSheetPadding + emojiSheetOuterGap*2
	height := emojiSheetRows*emojiSheetCellSize + (emojiSheetRows-1)*emojiSheetPadding + emojiSheetOuterGap*2

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{245, 243, 239, 255}}, image.Point{}, draw.Src)

	rendered := make([]emojiAsset, 0, len(assets))
	for _, asset := range assets {
		img, err := p.fetchEmojiImage(ctx, asset.URL)
		if err != nil {
			continue
		}

		index := len(rendered)
		col := index % emojiSheetColumns
		row := index / emojiSheetColumns
		cellMinX := emojiSheetOuterGap + col*(emojiSheetCellSize+emojiSheetPadding)
		cellMinY := emojiSheetOuterGap + row*(emojiSheetCellSize+emojiSheetPadding)
		cellRect := image.Rect(cellMinX, cellMinY, cellMinX+emojiSheetCellSize, cellMinY+emojiSheetCellSize)

		draw.Draw(canvas, cellRect, &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
		bounds := img.Bounds()
		offsetX := cellRect.Min.X + (emojiSheetCellSize-bounds.Dx())/2
		offsetY := cellRect.Min.Y + (emojiSheetCellSize-bounds.Dy())/2
		target := image.Rect(offsetX, offsetY, offsetX+bounds.Dx(), offsetY+bounds.Dy())
		draw.Draw(canvas, target, img, bounds.Min, draw.Over)
		rendered = append(rendered, asset)
	}
	if len(rendered) == 0 {
		return "", nil, fmt.Errorf("无法下载任何表情图片")
	}

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		return "", nil, err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes()), rendered, nil
}

func (p *emojiPlugin) fetchEmojiImage(ctx context.Context, url string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(url), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("emoji image request failed with status %s", resp.Status)
	}
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func (p *emojiPlugin) beginAnalysis(guildID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return false
	}
	if _, ok := p.analyzing[guildID]; ok {
		return false
	}
	p.analyzing[guildID] = struct{}{}
	return true
}

func (p *emojiPlugin) finishAnalysis(guildID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.analyzing, strings.TrimSpace(guildID))
}

func (p *emojiPlugin) isAnalyzing(guildID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.analyzing[strings.TrimSpace(guildID)]
	return ok
}

func loadEmojiProfile(ctx context.Context, host *pluginapi.HostClient, guildID string) (emojiProfile, error) {
	profile := emojiProfile{}
	_, err := host.StorageGet(ctx, emojiStoragePrefix+strings.TrimSpace(guildID), &profile)
	if err != nil {
		return emojiProfile{}, err
	}
	return profile, nil
}

func saveEmojiProfile(ctx context.Context, host *pluginapi.HostClient, guildID string, profile emojiProfile) error {
	return host.StorageSet(ctx, emojiStoragePrefix+strings.TrimSpace(guildID), profile)
}

type emojiAsset struct {
	ID       string
	Name     string
	Animated bool
	URL      string
	Syntax   string
}

func collectGuildEmojiAssets(emojis []pluginapi.GuildEmoji) []emojiAsset {
	assets := make([]emojiAsset, 0, len(emojis))
	seen := map[string]struct{}{}
	for _, emoji := range emojis {
		id := strings.TrimSpace(emoji.ID)
		name := strings.TrimSpace(emoji.Name)
		url := strings.TrimSpace(emoji.URL)
		if id == "" || name == "" || url == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		assets = append(assets, emojiAsset{
			ID:       id,
			Name:     name,
			Animated: emoji.Animated,
			URL:      url,
			Syntax:   firstNonEmpty(strings.TrimSpace(emoji.Syntax), emojiSyntax(name, id, emoji.Animated)),
		})
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].Name == assets[j].Name {
			return assets[i].ID < assets[j].ID
		}
		return assets[i].Name < assets[j].Name
	})
	return assets
}

func filterNewEmojiAssets(assets []emojiAsset, knownIDs map[string]struct{}) []emojiAsset {
	filtered := make([]emojiAsset, 0, len(assets))
	for _, asset := range assets {
		if _, ok := knownIDs[asset.ID]; ok {
			continue
		}
		filtered = append(filtered, asset)
	}
	return filtered
}

func chunkEmojiAssets(assets []emojiAsset, batchSize int) [][]emojiAsset {
	if batchSize <= 0 || len(assets) == 0 {
		return nil
	}
	chunks := make([][]emojiAsset, 0, (len(assets)+batchSize-1)/batchSize)
	for start := 0; start < len(assets); start += batchSize {
		end := start + batchSize
		if end > len(assets) {
			end = len(assets)
		}
		chunks = append(chunks, append([]emojiAsset(nil), assets[start:end]...))
	}
	return chunks
}

func emojiAssetIDs(assets []emojiAsset) []string {
	ids := make([]string, 0, len(assets))
	for _, asset := range assets {
		ids = append(ids, asset.ID)
	}
	return ids
}

func emojiWorldBookKey(guildID string) string {
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return ""
	}
	return emojiWorldBookPrefix + guildID
}

func emojiWorldBookTitle(guildName string) string {
	guildName = strings.TrimSpace(guildName)
	if guildName == "" {
		return "服务器表情世界书"
	}
	return guildName + " 表情世界书"
}

func emojiGuildLabel(guildID, guildName string) string {
	if strings.TrimSpace(guildName) != "" {
		return strings.TrimSpace(guildName)
	}
	if strings.TrimSpace(guildID) == "" {
		return "未知服务器"
	}
	return "Guild " + strings.TrimSpace(guildID)
}

func emojiStatusFieldValue(profile emojiProfile, status string, analyzing bool) string {
	lines := []string{
		"当前状态: " + status,
		fmt.Sprintf("当前记录表情数: %d", profile.EmojiCount),
	}
	if analyzing {
		lines = append(lines, "分析任务已经开始，完成后请手动刷新这个面板。")
	}
	if strings.TrimSpace(profile.LastAnalyzeMode) != "" {
		lines = append(lines, "上次模式: "+strings.TrimSpace(profile.LastAnalyzeMode))
	}
	if strings.TrimSpace(profile.LastAnalyzedAt) != "" {
		lines = append(lines, "上次分析时间: "+strings.TrimSpace(profile.LastAnalyzedAt))
	}
	if strings.TrimSpace(profile.LastAnalyzedBy) != "" {
		lines = append(lines, "上次分析人: "+strings.TrimSpace(profile.LastAnalyzedBy))
	}
	return strings.Join(lines, "\n")
}

func emojiWorldBookPreview(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "当前还没有写入世界书；点击“增量分析”或“完整重建”后会在这里展示预览。"
	}
	return "```text\n" + shared.TruncateRunes(content, emojiPreviewLimit) + "\n```"
}

func emojiPanelColor(isAdmin, analyzing, hasWorldBook bool) int {
	switch {
	case analyzing:
		return 0xF59E0B
	case hasWorldBook:
		return 0x10B981
	case isAdmin:
		return 0x3B82F6
	default:
		return 0x6B7280
	}
}

func renderExistingEmojiSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "当前还没有已有世界书，请直接基于本次图组生成完整内容。"
	}
	return "当前已有世界书，请在保留有效内容的前提下合并新增表情：\n\n" + summary
}

func emojiAnalysisTimestamp() string {
	location := time.FixedZone("UTC+8", 8*60*60)
	return time.Now().In(location).Format("2006-01-02 15:04:05 UTC+8")
}

func emojiSyntax(name, id string, animated bool) string {
	name = strings.TrimSpace(name)
	id = strings.TrimSpace(id)
	if name == "" || id == "" {
		return ""
	}
	if animated {
		return fmt.Sprintf("<a:%s:%s>", name, id)
	}
	return fmt.Sprintf("<:%s:%s>", name, id)
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
	if err := pluginapi.Serve(manifest, newEmojiPlugin()); err != nil {
		log.Fatal(err)
	}
}
