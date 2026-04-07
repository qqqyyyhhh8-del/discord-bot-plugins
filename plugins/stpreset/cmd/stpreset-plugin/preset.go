package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"discord-bot-plugins/sdk/pluginapi"
)

type stPreset struct {
	Name              string             `json:"name,omitempty"`
	DefaultCharName   string             `json:"char_name,omitempty"`
	WIFormat          string             `json:"wi_format,omitempty"`
	ScenarioFormat    string             `json:"scenario_format,omitempty"`
	PersonalityFormat string             `json:"personality_format,omitempty"`
	Impersonation     string             `json:"impersonation_prompt,omitempty"`
	ContinueNudge     string             `json:"continue_nudge_prompt,omitempty"`
	Prompts           []stPrompt         `json:"prompts,omitempty"`
	PromptOrder       []stPromptOrderSet `json:"prompt_order,omitempty"`
}

type stPrompt struct {
	Identifier        string   `json:"identifier,omitempty"`
	Name              string   `json:"name,omitempty"`
	Role              string   `json:"role,omitempty"`
	Content           string   `json:"content,omitempty"`
	Marker            bool     `json:"marker,omitempty"`
	Enabled           *bool    `json:"enabled,omitempty"`
	InjectionPosition int      `json:"injection_position,omitempty"`
	InjectionDepth    int      `json:"injection_depth,omitempty"`
	InjectionOrder    int      `json:"injection_order,omitempty"`
	InjectionTrigger  []string `json:"injection_trigger,omitempty"`
}

type stPromptOrderSet struct {
	CharacterID int64                `json:"character_id,omitempty"`
	Order       []stPromptOrderEntry `json:"order,omitempty"`
}

type stPromptOrderEntry struct {
	Identifier string `json:"identifier,omitempty"`
	Enabled    bool   `json:"enabled,omitempty"`
}

type orderedPromptEntry struct {
	Prompt   stPrompt
	Enabled  bool
	Position int
}

type cachedPreset struct {
	Signature string
	ModTime   time.Time
	Size      int64
	Preset    stPreset
}

func (p *presetPlugin) loadPresetForState(state presetState) (string, stPreset, error) {
	state = normalizePresetState(state)
	switch {
	case state.hasInlinePreset():
		return p.loadPresetPayload(firstNonEmpty(state.PresetName, "imported-preset.json"), state.PresetJSON)
	case strings.TrimSpace(state.PresetPath) != "":
		return p.loadPresetPath(state.PresetPath)
	default:
		return "", stPreset{}, fmt.Errorf("preset is not configured")
	}
}

func (p *presetPlugin) loadPresetPath(path string) (string, stPreset, error) {
	resolved, err := resolvePresetPath(path)
	if err != nil {
		return "", stPreset{}, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", stPreset{}, err
	}
	if info.IsDir() {
		return "", stPreset{}, fmt.Errorf("preset path is a directory: %s", resolved)
	}

	p.mu.Lock()
	if cached, ok := p.loaded[resolved]; ok && cached.Signature == resolved && cached.Size == info.Size() && cached.ModTime.Equal(info.ModTime()) {
		p.mu.Unlock()
		return resolved, cached.Preset, nil
	}
	p.mu.Unlock()

	payload, err := os.ReadFile(resolved)
	if err != nil {
		return "", stPreset{}, err
	}

	preset, err := decodePresetPayload(payload)
	if err != nil {
		return "", stPreset{}, err
	}

	p.mu.Lock()
	p.loaded[resolved] = cachedPreset{
		Signature: resolved,
		ModTime:   info.ModTime(),
		Size:      info.Size(),
		Preset:    preset,
	}
	p.mu.Unlock()
	return resolved, preset, nil
}

func (p *presetPlugin) loadPresetPayload(name, payload string) (string, stPreset, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", stPreset{}, fmt.Errorf("preset payload is empty")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "imported-preset.json"
	}
	checksum := sha256.Sum256([]byte(payload))
	cacheKey := fmt.Sprintf("inline:%x", checksum)

	p.mu.Lock()
	if cached, ok := p.loaded[cacheKey]; ok && cached.Signature == cacheKey {
		p.mu.Unlock()
		return name, cached.Preset, nil
	}
	p.mu.Unlock()

	preset, err := decodePresetPayload([]byte(payload))
	if err != nil {
		return "", stPreset{}, err
	}

	p.mu.Lock()
	p.loaded[cacheKey] = cachedPreset{
		Signature: cacheKey,
		Size:      int64(len(payload)),
		Preset:    preset,
	}
	p.mu.Unlock()
	return name, preset, nil
}

func decodePresetPayload(payload []byte) (stPreset, error) {
	var preset stPreset
	if err := json.Unmarshal(payload, &preset); err != nil {
		return stPreset{}, err
	}
	if len(preset.Prompts) == 0 {
		return stPreset{}, fmt.Errorf("preset contains no prompts")
	}
	return preset, nil
}

func resolvePresetPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("preset path is empty")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(path)
}

func (p *presetPlugin) renderPromptBlocks(state presetState, preset stPreset, current pluginapi.MessageContext, memories []pluginapi.MemoryMessage) ([]pluginapi.PromptBlock, error) {
	seed := time.Now().UnixNano()
	p.mu.Lock()
	if p.rand != nil {
		seed = p.rand.Int63()
	}
	p.mu.Unlock()

	state = normalizePresetState(state)
	history := prepareConversationHistory(current, memories)
	renderer := newSTRenderer(state, current, history, rand.New(rand.NewSource(seed)))
	view := buildPresetEditorView(state, preset)

	blocks := make([]pluginapi.PromptBlock, 0, len(view.Prompts)+len(history))
	regexes := make([]compiledRegex, 0)
	disabledRegex := sliceToSet(state.RegexDisabled)
	for _, item := range view.Prompts {
		if !item.Enabled || !promptMatchesTriggers(item, current) {
			continue
		}
		prompt := stPrompt{
			Identifier: item.Identifier,
			Name:       item.Name,
			Role:       item.Role,
			Content:    item.Content,
			Marker:     item.Marker,
		}
		renderedBlocks, foundRegexes, err := renderer.renderPrompt(preset, prompt)
		if err != nil {
			return nil, err
		}
		if state.regexEngineEnabled() {
			for regexIndex, rule := range foundRegexes {
				id := makeRegexID("base", item.Identifier, regexIndex, rule.Order, rule.PatternSpec, rule.Replacement)
				if _, disabled := disabledRegex[id]; disabled {
					continue
				}
				compiled, err := compilePresetRegex(rule.PatternSpec)
				if err != nil {
					return nil, err
				}
				regexes = append(regexes, compiledRegex{
					Order:       rule.Order,
					Pattern:     compiled,
					Replacement: rule.Replacement,
				})
			}
		}
		blocks = append(blocks, renderedBlocks...)
	}

	if state.regexEngineEnabled() {
		for index, rule := range state.CustomRegex {
			if !rule.Enabled {
				continue
			}
			compiled, err := compilePresetRegex(rule.PatternSpec)
			if err != nil {
				return nil, err
			}
			regexes = append(regexes, compiledRegex{
				Order:       rule.Order,
				Pattern:     compiled,
				Replacement: rule.Replacement,
			})
			if strings.TrimSpace(state.CustomRegex[index].ID) == "" {
				state.CustomRegex[index].ID = makeRegexID("custom", rule.Name, index, rule.Order, rule.PatternSpec, rule.Replacement)
			}
		}
	}

	if len(regexes) > 0 {
		sort.SliceStable(regexes, func(i, j int) bool {
			return regexes[i].Order < regexes[j].Order
		})
		for index := range blocks {
			content := strings.TrimSpace(blocks[index].Content)
			for _, rule := range regexes {
				content = rule.Pattern.ReplaceAllString(content, rule.Replacement)
			}
			blocks[index].Content = strings.TrimSpace(content)
		}
	}

	filtered := make([]pluginapi.PromptBlock, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Content) == "" && len(block.Images) == 0 {
			continue
		}
		filtered = append(filtered, block)
	}
	return filtered, nil
}

func (p stPreset) orderedPrompts() []stPrompt {
	entries := p.orderedPromptEntries()
	items := make([]stPrompt, 0, len(entries))
	for _, entry := range entries {
		if !entry.Enabled {
			continue
		}
		items = append(items, entry.Prompt)
	}
	return items
}

func (p stPreset) orderedPromptEntries() []orderedPromptEntry {
	if len(p.Prompts) == 0 {
		return nil
	}

	definitions := make(map[string]stPrompt, len(p.Prompts))
	for _, prompt := range p.Prompts {
		identifier := strings.TrimSpace(prompt.Identifier)
		if identifier == "" {
			continue
		}
		definitions[identifier] = prompt
	}

	group := p.activePromptOrder()
	if len(group.Order) == 0 {
		items := make([]orderedPromptEntry, 0, len(p.Prompts))
		for index, prompt := range p.Prompts {
			enabled := true
			if prompt.Enabled != nil {
				enabled = *prompt.Enabled
			}
			items = append(items, orderedPromptEntry{
				Prompt:   prompt,
				Enabled:  enabled,
				Position: index,
			})
		}
		return items
	}

	items := make([]orderedPromptEntry, 0, len(group.Order))
	for index, entry := range group.Order {
		prompt, ok := definitions[strings.TrimSpace(entry.Identifier)]
		if !ok {
			continue
		}
		items = append(items, orderedPromptEntry{
			Prompt:   prompt,
			Enabled:  entry.Enabled,
			Position: index,
		})
	}
	return items
}

func (p stPreset) activePromptOrder() stPromptOrderSet {
	best := stPromptOrderSet{}
	bestEnabled := -1
	for _, group := range p.PromptOrder {
		enabledCount := 0
		for _, entry := range group.Order {
			if entry.Enabled {
				enabledCount++
			}
		}
		if enabledCount >= bestEnabled {
			bestEnabled = enabledCount
			best = group
		}
	}
	return best
}
