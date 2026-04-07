package main

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"discord-bot-plugins/internal/shared"
	"discord-bot-plugins/sdk/pluginapi"
)

type promptOverride struct {
	Name        string   `json:"name,omitempty"`
	NameSet     bool     `json:"name_set,omitempty"`
	Role        string   `json:"role,omitempty"`
	RoleSet     bool     `json:"role_set,omitempty"`
	Content     string   `json:"content,omitempty"`
	ContentSet  bool     `json:"content_set,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
	Triggers    []string `json:"triggers,omitempty"`
	TriggersSet bool     `json:"triggers_set,omitempty"`
}

type customRegexRule struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Order       int    `json:"order,omitempty"`
	PatternSpec string `json:"pattern_spec"`
	Replacement string `json:"replacement"`
	Enabled     bool   `json:"enabled"`
}

type promptEditorItem struct {
	Identifier string
	Name       string
	Role       string
	Content    string
	Enabled    bool
	Marker     bool
	Triggers   []string
	Position   int
}

type regexEditorItem struct {
	ID            string
	Name          string
	Source        string
	PromptID      string
	PromptName    string
	Order         int
	PatternSpec   string
	Replacement   string
	Enabled       bool
	Custom        bool
	PromptEnabled bool
}

type presetEditorView struct {
	Prompts []promptEditorItem
	Regexes []regexEditorItem
}

func normalizePresetState(state presetState) presetState {
	state.PresetName = strings.TrimSpace(state.PresetName)
	state.PresetJSON = strings.TrimSpace(state.PresetJSON)
	state.PresetPath = strings.TrimSpace(state.PresetPath)
	state.CharName = strings.TrimSpace(state.CharName)
	state.CharDescription = strings.TrimSpace(state.CharDescription)
	state.CharPersonality = strings.TrimSpace(state.CharPersonality)
	state.Scenario = strings.TrimSpace(state.Scenario)
	state.PersonaDescription = strings.TrimSpace(state.PersonaDescription)
	state.DialogueExamples = strings.TrimSpace(state.DialogueExamples)
	state.SelectedPrompt = strings.TrimSpace(state.SelectedPrompt)
	state.SelectedRegex = strings.TrimSpace(state.SelectedRegex)
	state.UpdatedAt = strings.TrimSpace(state.UpdatedAt)
	if state.PromptOverrides == nil {
		state.PromptOverrides = map[string]promptOverride{}
	}
	for key, override := range state.PromptOverrides {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			delete(state.PromptOverrides, key)
			continue
		}
		override.Name = strings.TrimSpace(override.Name)
		override.Role = strings.TrimSpace(override.Role)
		if !override.ContentSet {
			override.Content = strings.TrimSpace(override.Content)
		}
		override.Triggers = normalizeTriggers(override.Triggers)
		state.PromptOverrides[trimmedKey] = override
		if trimmedKey != key {
			delete(state.PromptOverrides, key)
		}
	}
	state.PromptOrder = normalizeIdentifiers(state.PromptOrder)
	state.RegexDisabled = normalizeIdentifiers(state.RegexDisabled)
	for index := range state.CustomRegex {
		state.CustomRegex[index].ID = strings.TrimSpace(state.CustomRegex[index].ID)
		state.CustomRegex[index].Name = strings.TrimSpace(state.CustomRegex[index].Name)
		state.CustomRegex[index].PatternSpec = strings.TrimSpace(state.CustomRegex[index].PatternSpec)
		state.CustomRegex[index].Replacement = strings.TrimSpace(state.CustomRegex[index].Replacement)
	}
	if state.RegexEnabled == nil {
		defaultValue := true
		state.RegexEnabled = &defaultValue
	}
	if state.PromptPage < 0 {
		state.PromptPage = 0
	}
	if state.RegexPage < 0 {
		state.RegexPage = 0
	}
	return state
}

func (s presetState) hasInlinePreset() bool {
	return strings.TrimSpace(s.PresetJSON) != ""
}

func (s presetState) hasPresetSource() bool {
	return s.hasInlinePreset() || strings.TrimSpace(s.PresetPath) != ""
}

func buildPresetEditorView(state presetState, preset stPreset) presetEditorView {
	state = normalizePresetState(state)
	entries := applyCustomPromptOrder(preset.orderedPromptEntries(), state.PromptOrder)
	view := presetEditorView{
		Prompts: make([]promptEditorItem, 0, len(entries)),
		Regexes: make([]regexEditorItem, 0),
	}
	disabledRegex := sliceToSet(state.RegexDisabled)

	for index, entry := range entries {
		override := state.PromptOverrides[strings.TrimSpace(entry.Prompt.Identifier)]
		item := mergePromptEntry(entry, override)
		item.Position = index + 1
		view.Prompts = append(view.Prompts, item)

		_, defs, err := extractRegexTags(item.Content)
		if err != nil {
			continue
		}
		for regexIndex, def := range defs {
			id := makeRegexID("base", item.Identifier, regexIndex, def.Order, def.PatternSpec, def.Replacement)
			_, disabled := disabledRegex[id]
			view.Regexes = append(view.Regexes, regexEditorItem{
				ID:            id,
				Name:          fmt.Sprintf("%s #%d", firstNonEmpty(item.Name, item.Identifier), regexIndex+1),
				Source:        "预设条目",
				PromptID:      item.Identifier,
				PromptName:    firstNonEmpty(item.Name, item.Identifier),
				Order:         def.Order,
				PatternSpec:   def.PatternSpec,
				Replacement:   def.Replacement,
				Enabled:       state.regexEngineEnabled() && item.Enabled && !disabled,
				Custom:        false,
				PromptEnabled: item.Enabled,
			})
		}
	}

	for index, rule := range state.CustomRegex {
		if strings.TrimSpace(rule.ID) == "" {
			rule.ID = makeRegexID("custom", rule.Name, index, rule.Order, rule.PatternSpec, rule.Replacement)
			state.CustomRegex[index] = rule
		}
		view.Regexes = append(view.Regexes, regexEditorItem{
			ID:            rule.ID,
			Name:          firstNonEmpty(rule.Name, fmt.Sprintf("自定义正则 #%d", index+1)),
			Source:        "自定义",
			Order:         rule.Order,
			PatternSpec:   rule.PatternSpec,
			Replacement:   rule.Replacement,
			Enabled:       state.regexEngineEnabled() && rule.Enabled,
			Custom:        true,
			PromptEnabled: true,
		})
	}

	return view
}

func mergePromptEntry(entry orderedPromptEntry, override promptOverride) promptEditorItem {
	name := strings.TrimSpace(entry.Prompt.Name)
	if override.NameSet {
		name = strings.TrimSpace(override.Name)
	}
	role := strings.TrimSpace(entry.Prompt.Role)
	if override.RoleSet {
		role = strings.TrimSpace(override.Role)
	}
	content := entry.Prompt.Content
	if override.ContentSet {
		content = override.Content
	}
	triggers := entry.Prompt.InjectionTrigger
	if override.TriggersSet {
		triggers = override.Triggers
	}
	enabled := entry.Enabled
	if override.Enabled != nil {
		enabled = *override.Enabled
	}
	return promptEditorItem{
		Identifier: strings.TrimSpace(entry.Prompt.Identifier),
		Name:       name,
		Role:       role,
		Content:    content,
		Enabled:    enabled,
		Marker:     entry.Prompt.Marker,
		Triggers:   normalizeTriggers(triggers),
	}
}

func applyCustomPromptOrder(entries []orderedPromptEntry, custom []string) []orderedPromptEntry {
	if len(entries) == 0 {
		return nil
	}
	if len(custom) == 0 {
		return append([]orderedPromptEntry(nil), entries...)
	}

	byID := make(map[string]orderedPromptEntry, len(entries))
	for _, entry := range entries {
		identifier := strings.TrimSpace(entry.Prompt.Identifier)
		if identifier == "" {
			continue
		}
		byID[identifier] = entry
	}

	ordered := make([]orderedPromptEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, identifier := range custom {
		identifier = strings.TrimSpace(identifier)
		entry, ok := byID[identifier]
		if !ok {
			continue
		}
		if _, exists := seen[identifier]; exists {
			continue
		}
		seen[identifier] = struct{}{}
		ordered = append(ordered, entry)
	}
	for _, entry := range entries {
		identifier := strings.TrimSpace(entry.Prompt.Identifier)
		if identifier == "" {
			continue
		}
		if _, exists := seen[identifier]; exists {
			continue
		}
		ordered = append(ordered, entry)
	}
	return ordered
}

func (s presetState) regexEngineEnabled() bool {
	if s.RegexEnabled == nil {
		return true
	}
	return *s.RegexEnabled
}

func normalizeTriggers(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func normalizeIdentifiers(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func sliceToSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result[value] = struct{}{}
	}
	return result
}

func makeRegexID(prefix, key string, index, order int, pattern, replacement string) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(prefix))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strings.TrimSpace(key)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strconv.Itoa(index)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strconv.Itoa(order)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strings.TrimSpace(pattern)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strings.TrimSpace(replacement)))
	return fmt.Sprintf("%s:%x", prefix, hasher.Sum64())
}

func selectedPrompt(view presetEditorView, state *presetState) (promptEditorItem, bool) {
	if state == nil || len(view.Prompts) == 0 {
		return promptEditorItem{}, false
	}
	selected := strings.TrimSpace(state.SelectedPrompt)
	for _, item := range view.Prompts {
		if item.Identifier == selected {
			return item, true
		}
	}
	state.SelectedPrompt = view.Prompts[0].Identifier
	return view.Prompts[0], true
}

func selectedRegex(view presetEditorView, state *presetState) (regexEditorItem, bool) {
	if state == nil || len(view.Regexes) == 0 {
		return regexEditorItem{}, false
	}
	selected := strings.TrimSpace(state.SelectedRegex)
	for _, item := range view.Regexes {
		if item.ID == selected {
			return item, true
		}
	}
	state.SelectedRegex = view.Regexes[0].ID
	return view.Regexes[0], true
}

func buildPromptSelectMenu(view presetEditorView, state *presetState) pluginapi.SelectMenu {
	page, totalPages, start, end := pageWindow(len(view.Prompts), state.PromptPage, stPresetPageSize)
	state.PromptPage = page
	options := make([]pluginapi.SelectOption, 0, stPresetPageSize+2)
	if totalPages > 1 && page > 0 {
		options = append(options, pluginapi.SelectOption{
			Label:       "上一页",
			Value:       stPresetPagePrevValue,
			Description: fmt.Sprintf("跳到第 %d/%d 页", page, totalPages),
		})
	}
	for index := start; index < end; index++ {
		item := view.Prompts[index]
		label := firstNonEmpty(item.Name, item.Identifier)
		description := fmt.Sprintf("%s | %s | %s", item.Identifier, firstNonEmpty(item.Role, "system"), promptEnabledText(item.Enabled))
		options = append(options, pluginapi.SelectOption{
			Label:       shared.TruncateRunes(label, 80),
			Value:       item.Identifier,
			Description: shared.TruncateRunes(description, 90),
			Default:     item.Identifier == strings.TrimSpace(state.SelectedPrompt),
		})
	}
	if totalPages > 1 && page < totalPages-1 {
		options = append(options, pluginapi.SelectOption{
			Label:       "下一页",
			Value:       stPresetPageNextValue,
			Description: fmt.Sprintf("跳到第 %d/%d 页", page+2, totalPages),
		})
	}
	if len(options) == 0 {
		options = append(options, pluginapi.SelectOption{
			Label:       "当前没有可选条目",
			Value:       "__empty__",
			Description: "请先配置并读取预设文件",
		})
	}
	return pluginapi.SelectMenu{
		CustomID:    stPresetActionSelectPrompt,
		Placeholder: fmt.Sprintf("选择预设条目 (%d/%d)", page+1, maxInt(totalPages, 1)),
		Options:     options,
		MaxValues:   1,
		MinValues:   1,
		Disabled:    len(view.Prompts) == 0,
	}
}

func buildRegexSelectMenu(view presetEditorView, state *presetState) pluginapi.SelectMenu {
	page, totalPages, start, end := pageWindow(len(view.Regexes), state.RegexPage, stPresetPageSize)
	state.RegexPage = page
	options := make([]pluginapi.SelectOption, 0, stPresetPageSize+2)
	if totalPages > 1 && page > 0 {
		options = append(options, pluginapi.SelectOption{
			Label:       "上一页",
			Value:       stPresetPagePrevValue,
			Description: fmt.Sprintf("跳到第 %d/%d 页", page, totalPages),
		})
	}
	for index := start; index < end; index++ {
		item := view.Regexes[index]
		description := fmt.Sprintf("%s | order=%d | %s", item.Source, item.Order, promptEnabledText(item.Enabled))
		options = append(options, pluginapi.SelectOption{
			Label:       shared.TruncateRunes(firstNonEmpty(item.Name, item.ID), 80),
			Value:       item.ID,
			Description: shared.TruncateRunes(description, 90),
			Default:     item.ID == strings.TrimSpace(state.SelectedRegex),
		})
	}
	if totalPages > 1 && page < totalPages-1 {
		options = append(options, pluginapi.SelectOption{
			Label:       "下一页",
			Value:       stPresetPageNextValue,
			Description: fmt.Sprintf("跳到第 %d/%d 页", page+2, totalPages),
		})
	}
	if len(options) == 0 {
		options = append(options, pluginapi.SelectOption{
			Label:       "当前没有可选正则",
			Value:       "__empty__",
			Description: "预设或自定义正则为空",
		})
	}
	return pluginapi.SelectMenu{
		CustomID:    stPresetActionSelectRegex,
		Placeholder: fmt.Sprintf("选择正则规则 (%d/%d)", page+1, maxInt(totalPages, 1)),
		Options:     options,
		MaxValues:   1,
		MinValues:   1,
		Disabled:    len(view.Regexes) == 0,
	}
}

func pageWindow(total, current, pageSize int) (page, totalPages, start, end int) {
	if pageSize <= 0 {
		pageSize = stPresetPageSize
	}
	if total <= 0 {
		return 0, 0, 0, 0
	}
	totalPages = (total + pageSize - 1) / pageSize
	if current < 0 {
		current = 0
	}
	if current >= totalPages {
		current = totalPages - 1
	}
	start = current * pageSize
	end = start + pageSize
	if end > total {
		end = total
	}
	return current, totalPages, start, end
}

func parseTriggersInput(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	splitter := func(r rune) bool {
		switch r {
		case '\n', ',', '，', ';', '；':
			return true
		default:
			return false
		}
	}
	return normalizeTriggers(strings.FieldsFunc(input, splitter))
}

func promptMatchesTriggers(prompt promptEditorItem, current pluginapi.MessageContext) bool {
	if len(prompt.Triggers) == 0 {
		return true
	}
	haystacks := []string{
		strings.ToLower(strings.TrimSpace(current.Content)),
	}
	if current.ReplyTo != nil {
		haystacks = append(haystacks, strings.ToLower(strings.TrimSpace(current.ReplyTo.Content)))
	}
	for _, trigger := range prompt.Triggers {
		trigger = strings.ToLower(strings.TrimSpace(trigger))
		if trigger == "" {
			continue
		}
		for _, haystack := range haystacks {
			if strings.Contains(haystack, trigger) {
				return true
			}
		}
	}
	return false
}

func movePromptIdentifier(order []string, identifier string, delta int, baseEntries []orderedPromptEntry) []string {
	orderedEntries := applyCustomPromptOrder(baseEntries, order)
	ids := make([]string, 0, len(orderedEntries))
	position := -1
	for _, entry := range orderedEntries {
		id := strings.TrimSpace(entry.Prompt.Identifier)
		if id == "" {
			continue
		}
		if id == identifier {
			position = len(ids)
		}
		ids = append(ids, id)
	}
	if position < 0 {
		return normalizeIdentifiers(ids)
	}
	next := position + delta
	if next < 0 || next >= len(ids) {
		return normalizeIdentifiers(ids)
	}
	ids[position], ids[next] = ids[next], ids[position]
	return normalizeIdentifiers(ids)
}

func movePromptIdentifierTo(order []string, identifier string, target int, baseEntries []orderedPromptEntry) []string {
	orderedEntries := applyCustomPromptOrder(baseEntries, order)
	ids := make([]string, 0, len(orderedEntries))
	position := -1
	for _, entry := range orderedEntries {
		id := strings.TrimSpace(entry.Prompt.Identifier)
		if id == "" {
			continue
		}
		if id == identifier {
			position = len(ids)
		}
		ids = append(ids, id)
	}
	if position < 0 {
		return normalizeIdentifiers(ids)
	}
	if target < 0 {
		target = 0
	}
	if target >= len(ids) {
		target = len(ids) - 1
	}
	if target == position {
		return normalizeIdentifiers(ids)
	}
	value := ids[position]
	ids = append(ids[:position], ids[position+1:]...)
	prefix := append([]string{}, ids[:target]...)
	suffix := append([]string{value}, ids[target:]...)
	return normalizeIdentifiers(append(prefix, suffix...))
}

func parseRegexImportBlock(input string) ([]customRegexRule, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("正则导入内容不能为空")
	}

	_, defs, err := extractRegexTags(input)
	if err == nil && len(defs) > 0 {
		items := make([]customRegexRule, 0, len(defs))
		for index, def := range defs {
			if _, compileErr := compilePresetRegex(def.PatternSpec); compileErr != nil {
				return nil, compileErr
			}
			items = append(items, customRegexRule{
				ID:          makeRegexID("custom", "import", index, def.Order, def.PatternSpec, def.Replacement),
				Name:        fmt.Sprintf("导入正则 #%d", index+1),
				Order:       def.Order,
				PatternSpec: def.PatternSpec,
				Replacement: def.Replacement,
				Enabled:     true,
			})
		}
		return items, nil
	}

	lines := strings.Split(input, "\n")
	items := make([]customRegexRule, 0, len(lines))
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=>", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("无法解析第 %d 行，请使用 `<regex ...>` 或 `/pattern/flags => replacement`", index+1)
		}
		pattern := strings.TrimSpace(parts[0])
		replacement := strings.TrimSpace(parts[1])
		if _, compileErr := compilePresetRegex(pattern); compileErr != nil {
			return nil, compileErr
		}
		items = append(items, customRegexRule{
			ID:          makeRegexID("custom", "line", index, 0, pattern, replacement),
			Name:        fmt.Sprintf("导入正则 #%d", index+1),
			Order:       0,
			PatternSpec: pattern,
			Replacement: replacement,
			Enabled:     true,
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("没有解析到任何正则规则")
	}
	return items, nil
}

func regexDisabledToggle(disabled []string, identifier string, enabled bool) []string {
	set := sliceToSet(disabled)
	if enabled {
		delete(set, identifier)
	} else {
		set[identifier] = struct{}{}
	}
	values := make([]string, 0, len(set))
	for key := range set {
		values = append(values, key)
	}
	sort.Strings(values)
	return values
}

func promptEnabledText(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "已停用"
}

func boolIcon(value bool) string {
	if value {
		return "开启"
	}
	return "关闭"
}

func previewPromptContent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "当前条目没有内容。"
	}
	return "```text\n" + shared.TruncateRunes(value, stPresetPromptPreviewRunes) + "\n```"
}

func previewRegexBody(pattern, replacement string) string {
	body := fmt.Sprintf("pattern: %s\nreplace: %s", strings.TrimSpace(pattern), strings.TrimSpace(replacement))
	return "```text\n" + shared.TruncateRunes(body, stPresetRegexPreviewRunes) + "\n```"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
