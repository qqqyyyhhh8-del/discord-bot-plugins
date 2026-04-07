package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"discord-bot-plugins/sdk/pluginapi"
)

var (
	stMacroPattern   = regexp.MustCompile(`(?s)\{\{(.*?)\}\}`)
	stRegexTagRegexp = regexp.MustCompile(`(?s)<regex(?:\s+order=(\d+))?>(.*?)</regex>`)
	stUTC8Location   = time.FixedZone("UTC+8", 8*60*60)
)

type compiledRegex struct {
	Order       int
	Pattern     *regexp.Regexp
	Replacement string
}

type regexDefinition struct {
	Order       int
	PatternSpec string
	Replacement string
}

type stRenderer struct {
	state   presetState
	current pluginapi.MessageContext
	history []pluginapi.MemoryMessage
	rand    *rand.Rand
	vars    map[string]string
}

func newSTRenderer(state presetState, current pluginapi.MessageContext, history []pluginapi.MemoryMessage, random *rand.Rand) *stRenderer {
	return &stRenderer{
		state:   state,
		current: current,
		history: append([]pluginapi.MemoryMessage(nil), history...),
		rand:    random,
		vars:    map[string]string{},
	}
}

func (r *stRenderer) renderPrompt(preset stPreset, prompt stPrompt) ([]pluginapi.PromptBlock, []regexDefinition, error) {
	role := normalizeBlockRole(prompt.Role)
	identifier := strings.TrimSpace(prompt.Identifier)

	switch identifier {
	case "chatHistory":
		return r.historyPromptBlocks(), nil, nil
	case "dialogueExamples":
		return r.renderTextBlock(role, r.state.DialogueExamples, nil)
	case "charDescription":
		return r.renderTextBlock(role, r.state.CharDescription, nil)
	case "charPersonality":
		if strings.TrimSpace(r.state.CharPersonality) == "" {
			return nil, nil, nil
		}
		content := r.state.CharPersonality
		if strings.TrimSpace(preset.PersonalityFormat) != "" {
			content = preset.PersonalityFormat
		}
		return r.renderTextBlock(role, content, nil)
	case "scenario":
		if strings.TrimSpace(r.state.Scenario) == "" {
			return nil, nil, nil
		}
		content := r.state.Scenario
		if strings.TrimSpace(preset.ScenarioFormat) != "" {
			content = preset.ScenarioFormat
		}
		return r.renderTextBlock(role, content, nil)
	case "personaDescription":
		return r.renderTextBlock(role, r.state.PersonaDescription, nil)
	case "worldInfoBefore", "worldInfoAfter":
		return nil, nil, nil
	default:
		if prompt.Marker && strings.TrimSpace(prompt.Content) == "" {
			return nil, nil, nil
		}
		return r.renderTextBlock(role, prompt.Content, nil)
	}
}

func (r *stRenderer) renderTextBlock(role, content string, images []pluginapi.ImageReference) ([]pluginapi.PromptBlock, []regexDefinition, error) {
	rendered, err := r.renderString(content)
	if err != nil {
		return nil, nil, err
	}
	rendered, regexes, err := extractRegexTags(rendered)
	if err != nil {
		return nil, nil, err
	}
	rendered = strings.TrimSpace(rendered)
	if rendered == "" && len(images) == 0 {
		return nil, regexes, nil
	}
	return []pluginapi.PromptBlock{
		{
			Role:    normalizeBlockRole(role),
			Content: rendered,
			Images:  append([]pluginapi.ImageReference(nil), images...),
		},
	}, regexes, nil
}

func (r *stRenderer) renderString(input string) (string, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return "", nil
	}

	for pass := 0; pass < 8; pass++ {
		changed := false
		var firstErr error
		text = stMacroPattern.ReplaceAllStringFunc(text, func(match string) string {
			if firstErr != nil {
				return match
			}
			inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}"))
			replacement, handled, err := r.evalMacro(inner, match)
			if err != nil {
				firstErr = err
				return match
			}
			if handled && replacement != match {
				changed = true
			}
			if handled {
				return replacement
			}
			return match
		})
		if firstErr != nil {
			return "", firstErr
		}

		replaced := strings.ReplaceAll(text, "<user>", r.userName())
		replaced = strings.ReplaceAll(replaced, "<char>", r.charName())
		if replaced != text {
			changed = true
			text = replaced
		}
		if !changed {
			break
		}
	}
	return strings.TrimSpace(text), nil
}

func (r *stRenderer) evalMacro(inner, fallback string) (string, bool, error) {
	lower := strings.ToLower(strings.TrimSpace(inner))
	switch {
	case strings.HasPrefix(lower, "//"):
		return "", true, nil
	case strings.HasPrefix(lower, "setvar::"):
		rest := inner[len("setvar::"):]
		parts := strings.SplitN(rest, "::", 2)
		if len(parts) != 2 {
			return "", true, nil
		}
		key := strings.TrimSpace(parts[0])
		value, err := r.renderString(parts[1])
		if err != nil {
			return "", true, err
		}
		if key != "" {
			r.vars[key] = value
		}
		return "", true, nil
	case strings.HasPrefix(lower, "getvar::"):
		key := strings.TrimSpace(inner[len("getvar::"):])
		return strings.TrimSpace(r.vars[key]), true, nil
	case strings.HasPrefix(lower, "random::"):
		options := parseRandomOptions(inner[len("random::"):])
		if len(options) == 0 {
			return "", true, nil
		}
		if r.rand == nil {
			return options[0], true, nil
		}
		return options[r.rand.Intn(len(options))], true, nil
	case strings.HasPrefix(lower, "roll "):
		return rollDice(strings.TrimSpace(inner[len("roll "):]), r.rand), true, nil
	case lower == "user":
		return r.userName(), true, nil
	case lower == "char":
		return r.charName(), true, nil
	case lower == "scenario":
		return strings.TrimSpace(r.state.Scenario), true, nil
	case lower == "personality":
		return strings.TrimSpace(r.state.CharPersonality), true, nil
	case lower == "lastchatmessage":
		return r.lastChatMessage(), true, nil
	case lower == "lastusermessage":
		return r.lastUserMessage(), true, nil
	default:
		return fallback, false, nil
	}
}

func parseRandomOptions(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	var raw []string
	if strings.Contains(input, "::") {
		raw = strings.Split(input, "::")
	} else {
		raw = strings.Split(input, ",")
	}
	options := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		options = append(options, item)
	}
	return options
}

func rollDice(expr string, random *rand.Rand) string {
	expr = strings.TrimSpace(strings.ToLower(expr))
	if expr == "" {
		return "0"
	}
	count := 1
	sides := 0
	if strings.Contains(expr, "d") {
		parts := strings.SplitN(expr, "d", 2)
		if strings.TrimSpace(parts[0]) != "" {
			if parsed, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && parsed > 0 {
				count = parsed
			}
		}
		if parsed, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && parsed > 0 {
			sides = parsed
		}
	} else if parsed, err := strconv.Atoi(expr); err == nil && parsed > 0 {
		sides = parsed
	}
	if sides <= 0 {
		return "0"
	}
	total := 0
	for i := 0; i < count; i++ {
		if random == nil {
			total++
			continue
		}
		total += random.Intn(sides) + 1
	}
	return strconv.Itoa(total)
}

func extractRegexTags(input string) (string, []regexDefinition, error) {
	matches := stRegexTagRegexp.FindAllStringSubmatchIndex(input, -1)
	if len(matches) == 0 {
		return input, nil, nil
	}

	var builder strings.Builder
	regexes := make([]regexDefinition, 0, len(matches))
	last := 0
	for _, match := range matches {
		builder.WriteString(input[last:match[0]])
		last = match[1]

		order := 0
		if match[2] >= 0 && match[3] >= 0 {
			parsed, err := strconv.Atoi(strings.TrimSpace(input[match[2]:match[3]]))
			if err != nil {
				return "", nil, err
			}
			order = parsed
		}

		body := input[match[4]:match[5]]
		patternSpec, replacement, err := parseRegexDefinition(body)
		if err != nil {
			return "", nil, err
		}
		regexes = append(regexes, regexDefinition{
			Order:       order,
			PatternSpec: patternSpec,
			Replacement: replacement,
		})
	}
	builder.WriteString(input[last:])
	return builder.String(), regexes, nil
}

func parseRegexDefinition(body string) (string, string, error) {
	payload := "{" + strings.TrimSpace(body) + "}"
	parsed := map[string]string{}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return "", "", err
	}
	for key, value := range parsed {
		return key, value, nil
	}
	return "", "", fmt.Errorf("empty regex definition")
}

func compilePresetRegex(spec string) (*regexp.Regexp, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("regex pattern is empty")
	}
	if !strings.HasPrefix(spec, "/") {
		return regexp.Compile(spec)
	}
	lastSlash := lastUnescapedSlash(spec)
	if lastSlash <= 0 {
		return regexp.Compile(spec)
	}
	body := spec[1:lastSlash]
	flags := spec[lastSlash+1:]
	prefix := strings.Builder{}
	for _, flag := range flags {
		switch flag {
		case 'i':
			prefix.WriteString("(?i)")
		case 'm':
			prefix.WriteString("(?m)")
		case 's':
			prefix.WriteString("(?s)")
		}
	}
	return regexp.Compile(prefix.String() + body)
}

func lastUnescapedSlash(value string) int {
	escaped := false
	for index := len(value) - 1; index > 0; index-- {
		switch value[index] {
		case '/':
			if !escaped {
				return index
			}
			escaped = false
		case '\\':
			escaped = !escaped
		default:
			escaped = false
		}
	}
	return -1
}

func prepareConversationHistory(current pluginapi.MessageContext, memories []pluginapi.MemoryMessage) []pluginapi.MemoryMessage {
	history := append([]pluginapi.MemoryMessage(nil), memories...)
	currentMessage := memoryMessageFromContext(current, "user")
	if len(history) == 0 {
		return []pluginapi.MemoryMessage{currentMessage}
	}

	last := history[len(history)-1]
	if strings.TrimSpace(last.Role) != "user" {
		return append(history, currentMessage)
	}
	if current.Author.ID != "" && last.Author.ID != "" && current.Author.ID != last.Author.ID {
		return append(history, currentMessage)
	}

	last.Content = firstNonEmpty(strings.TrimSpace(current.Content), strings.TrimSpace(last.Content))
	last.Time = firstNonEmpty(strings.TrimSpace(current.Time), strings.TrimSpace(last.Time))
	if strings.TrimSpace(current.Author.ID) != "" {
		last.Author = current.Author
	}
	if current.ReplyTo != nil {
		last.ReplyTo = current.ReplyTo
	}
	if len(current.Images) > 0 {
		last.Images = append([]pluginapi.ImageReference(nil), current.Images...)
	}
	history[len(history)-1] = last
	return history
}

func (r *stRenderer) historyPromptBlocks() []pluginapi.PromptBlock {
	lastUserIndex := -1
	for index := len(r.history) - 1; index >= 0; index-- {
		if strings.TrimSpace(r.history[index].Role) == "user" {
			lastUserIndex = index
			break
		}
	}

	blocks := make([]pluginapi.PromptBlock, 0, len(r.history))
	for index, message := range r.history {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		isCurrentUserMessage := role == "user" && index == lastUserIndex
		switch role {
		case "assistant":
			content := strings.TrimSpace(message.Content)
			if content == "" {
				continue
			}
			blocks = append(blocks, pluginapi.PromptBlock{
				Role:    "assistant",
				Content: content,
			})
		case "user":
			blocks = append(blocks, renderUserHistoryBlock(message, r.current, isCurrentUserMessage))
		default:
			content := strings.TrimSpace(message.Content)
			if content == "" {
				continue
			}
			blocks = append(blocks, pluginapi.PromptBlock{
				Role:    normalizeBlockRole(role),
				Content: content,
			})
		}
	}
	return blocks
}

func renderUserHistoryBlock(message pluginapi.MemoryMessage, current pluginapi.MessageContext, currentUser bool) pluginapi.PromptBlock {
	content := strings.TrimSpace(message.Content)
	author := message.Author
	reply := message.ReplyTo
	images := append([]pluginapi.ImageReference(nil), message.Images...)
	when := parseTimestamp(message.Time)
	if currentUser {
		content = firstNonEmpty(strings.TrimSpace(current.Content), content)
		if strings.TrimSpace(current.Time) != "" {
			when = parseTimestamp(current.Time)
		}
		if strings.TrimSpace(current.Author.ID) != "" {
			author = current.Author
		}
		if current.ReplyTo != nil {
			reply = current.ReplyTo
		}
		if len(current.Images) > 0 {
			images = append([]pluginapi.ImageReference(nil), current.Images...)
		}
	}

	rendered := renderCompactUserMessage(when, author, content, reply, images)
	if currentUser {
		rendered = renderDetailedUserMessage(when, author, content, reply, images)
	}
	block := pluginapi.PromptBlock{
		Role:    "user",
		Content: rendered,
	}
	if currentUser && len(images) > 0 {
		block.Images = images
	}
	return block
}

func renderDetailedUserMessage(when time.Time, author pluginapi.UserInfo, content string, reply *pluginapi.ReplyInfo, images []pluginapi.ImageReference) string {
	lines := []string{
		fmt.Sprintf("时间(UTC+8): %s", renderUTC8Timestamp(when)),
		"发送者ID: " + valueOrUnknown(author.ID),
		"发送者用户名: " + valueOrUnknown(author.Username),
		"发送者全局名: " + valueOrUnknown(author.GlobalName),
		"发送者频道昵称: " + valueOrUnknown(author.Nick),
		"发送者显示名: " + valueOrUnknown(displayName(author)),
		"内容:",
		strings.TrimSpace(content),
	}

	if len(images) > 0 {
		lines = append(lines, "", "附带图片/表情:")
		for index, image := range images {
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, renderImageReference(image)))
		}
	}
	if reply != nil {
		replyTime := parseTimestamp(reply.Time)
		lines = append(lines,
			"",
			"这条消息是在回复以下消息:",
			"被回复消息ID: "+valueOrUnknown(reply.MessageID),
			fmt.Sprintf("被回复消息时间(UTC+8): %s", renderUTC8Timestamp(replyTime)),
			"被回复消息角色: "+valueOrUnknown(reply.Role),
			"被回复发送者ID: "+valueOrUnknown(reply.Author.ID),
			"被回复发送者用户名: "+valueOrUnknown(reply.Author.Username),
			"被回复发送者全局名: "+valueOrUnknown(reply.Author.GlobalName),
			"被回复发送者频道昵称: "+valueOrUnknown(reply.Author.Nick),
			"被回复发送者显示名: "+valueOrUnknown(displayName(reply.Author)),
			"被回复消息内容:",
			strings.TrimSpace(reply.Content),
		)
	}
	return strings.Join(lines, "\n")
}

func renderCompactUserMessage(when time.Time, author pluginapi.UserInfo, content string, reply *pluginapi.ReplyInfo, images []pluginapi.ImageReference) string {
	lines := []string{
		fmt.Sprintf("[%s] 用户 %s", renderUTC8Timestamp(when), valueOrUnknown(displayName(author))),
		compactBodyText(content),
	}
	if len(images) > 0 {
		items := make([]string, 0, len(images))
		for _, image := range images {
			items = append(items, renderCompactImageReference(image))
		}
		lines = append(lines, "附带图片/表情: "+strings.Join(items, " | "))
	}
	if reply != nil {
		lines = append(lines, "回复: "+renderCompactReplyReference(*reply))
	}
	return strings.Join(lines, "\n")
}

func renderCompactReplyReference(reply pluginapi.ReplyInfo) string {
	return fmt.Sprintf("用户 %s [%s]: %s", valueOrUnknown(displayName(reply.Author)), renderUTC8Timestamp(parseTimestamp(reply.Time)), compactBodyText(reply.Content))
}

func renderImageReference(image pluginapi.ImageReference) string {
	url := valueOrUnknown(image.URL)
	switch strings.TrimSpace(image.Kind) {
	case "custom_emoji":
		tag := valueOrUnknown(image.Name)
		if strings.TrimSpace(image.EmojiID) != "" && strings.TrimSpace(image.Name) != "" {
			if image.Animated {
				tag = fmt.Sprintf("<a:%s:%s>", image.Name, image.EmojiID)
			} else {
				tag = fmt.Sprintf("<:%s:%s>", image.Name, image.EmojiID)
			}
		}
		return fmt.Sprintf("自定义表情 %s, 图片URL: %s", tag, url)
	case "attachment":
		label := valueOrUnknown(image.Name)
		if strings.TrimSpace(image.ContentType) != "" {
			label += " (" + strings.TrimSpace(image.ContentType) + ")"
		}
		return fmt.Sprintf("图片附件 %s, 图片URL: %s", label, url)
	default:
		return fmt.Sprintf("图片资源 %s, 图片URL: %s", valueOrUnknown(image.Name), url)
	}
}

func renderCompactImageReference(image pluginapi.ImageReference) string {
	switch strings.TrimSpace(image.Kind) {
	case "custom_emoji":
		if strings.TrimSpace(image.Name) != "" {
			return "自定义表情 " + strings.TrimSpace(image.Name)
		}
		return "自定义表情"
	case "attachment":
		label := firstNonEmpty(strings.TrimSpace(image.Name), strings.TrimSpace(image.ContentType))
		if label == "" {
			return "图片附件"
		}
		return "图片附件 " + label
	default:
		label := firstNonEmpty(strings.TrimSpace(image.Name), strings.TrimSpace(image.ContentType))
		if label == "" {
			return "图片资源"
		}
		return "图片资源 " + label
	}
}

func compactBodyText(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "(无文本内容)"
	}
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "(无文本内容)"
	}
	content = strings.Join(fields, " ")
	runes := []rune(content)
	if len(runes) > 140 {
		return string(runes[:140]) + "..."
	}
	return content
}

func parseTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	return time.Now().UTC()
}

func renderUTC8Timestamp(when time.Time) string {
	return when.In(stUTC8Location).Format("2006-01-02 15:04:05")
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "未设置"
	}
	return value
}

func displayName(user pluginapi.UserInfo) string {
	return firstNonEmpty(user.DisplayName, user.Nick, user.GlobalName, user.Username, user.ID)
}

func normalizeBlockRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "system"
	}
	return role
}

func (r *stRenderer) userName() string {
	return valueOrUnknown(displayName(r.current.Author))
}

func (r *stRenderer) charName() string {
	return valueOrUnknown(r.state.CharName)
}

func (r *stRenderer) lastChatMessage() string {
	for index := len(r.history) - 1; index >= 0; index-- {
		content := strings.TrimSpace(r.history[index].Content)
		if content != "" {
			return content
		}
	}
	return strings.TrimSpace(r.current.Content)
}

func (r *stRenderer) lastUserMessage() string {
	for index := len(r.history) - 1; index >= 0; index-- {
		if strings.TrimSpace(r.history[index].Role) != "user" {
			continue
		}
		content := strings.TrimSpace(r.history[index].Content)
		if content != "" {
			return content
		}
	}
	return strings.TrimSpace(r.current.Content)
}
