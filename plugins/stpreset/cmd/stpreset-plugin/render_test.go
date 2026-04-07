package main

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"discord-bot-plugins/sdk/pluginapi"
)

func TestOrderedPromptsPrefersLargestEnabledGroup(t *testing.T) {
	preset := stPreset{
		Prompts: []stPrompt{
			{Identifier: "main", Name: "main"},
			{Identifier: "scenario", Name: "scenario"},
			{Identifier: "chatHistory", Name: "chatHistory"},
		},
		PromptOrder: []stPromptOrderSet{
			{
				CharacterID: 1,
				Order: []stPromptOrderEntry{
					{Identifier: "main", Enabled: true},
				},
			},
			{
				CharacterID: 2,
				Order: []stPromptOrderEntry{
					{Identifier: "main", Enabled: true},
					{Identifier: "scenario", Enabled: true},
					{Identifier: "chatHistory", Enabled: true},
				},
			},
		},
	}

	ordered := preset.orderedPrompts()
	if len(ordered) != 3 {
		t.Fatalf("expected 3 ordered prompts, got %d", len(ordered))
	}
	if ordered[1].Identifier != "scenario" || ordered[2].Identifier != "chatHistory" {
		t.Fatalf("unexpected ordered prompts: %#v", ordered)
	}
}

func TestRenderStringSupportsVariablesAndPlaceholders(t *testing.T) {
	renderer := newSTRenderer(
		presetState{
			CharName:        "Kizuna",
			CharPersonality: "冷淡",
			Scenario:        "酒吧",
		},
		pluginapi.MessageContext{
			Content: "你好",
			Author: pluginapi.UserInfo{
				ID:          "user-1",
				Username:    "alice",
				DisplayName: "Alice",
			},
		},
		[]pluginapi.MemoryMessage{
			{Role: "assistant", Content: "上一句"},
			{Role: "user", Content: "用户上一句"},
		},
		rand.New(rand.NewSource(1)),
	)

	text, err := renderer.renderString("{{setvar::mood::开心}}<user> 对 <char> 说：{{getvar::mood}} / {{scenario}} / {{personality}} / {{lastUsermessage}} {{//hidden}}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "hidden") {
		t.Fatalf("expected comment macro to be removed, got %q", text)
	}
	if !strings.Contains(text, "Alice 对 Kizuna 说：开心 / 酒吧 / 冷淡 / 用户上一句") {
		t.Fatalf("unexpected rendered text: %q", text)
	}
}

func TestExtractRegexTagsParsesAndApplies(t *testing.T) {
	rendered, regexes, err := extractRegexTags(`<regex order=3>"/User: /gs":"成员: "</regex>User: hello`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(rendered) != "User: hello" {
		t.Fatalf("unexpected stripped content: %q", rendered)
	}
	if len(regexes) != 1 {
		t.Fatalf("expected 1 regex, got %d", len(regexes))
	}
	compiled, err := compilePresetRegex(regexes[0].PatternSpec)
	if err != nil {
		t.Fatal(err)
	}
	applied := compiled.ReplaceAllString(strings.TrimSpace(rendered), regexes[0].Replacement)
	if applied != "成员: hello" {
		t.Fatalf("unexpected regex result: %q", applied)
	}
}

func TestParseRegexDefinitionSupportsPresetEscapes(t *testing.T) {
	pattern, replacement, err := parseRegexDefinition(`"/User: <additional_settings>\[Start Interaction\]/gs":"User: [Start Interaction]<additional_settings>"`)
	if err != nil {
		t.Fatal(err)
	}
	if pattern != `/User: <additional_settings>\[Start Interaction\]/gs` {
		t.Fatalf("unexpected pattern: %q", pattern)
	}
	if replacement != `User: [Start Interaction]<additional_settings>` {
		t.Fatalf("unexpected replacement: %q", replacement)
	}
}

func TestExtractRegexTagsSupportsSillyTavernPresetEscapes(t *testing.T) {
	rendered, regexes, err := extractRegexTags(`<regex order=3>"/<additional_settings>\s*<\/additional_settings>/gs":""</regex><additional_settings></additional_settings>`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(rendered) != "<additional_settings></additional_settings>" {
		t.Fatalf("unexpected stripped content: %q", rendered)
	}
	if len(regexes) != 1 {
		t.Fatalf("expected 1 regex, got %d", len(regexes))
	}

	compiled, err := compilePresetRegex(regexes[0].PatternSpec)
	if err != nil {
		t.Fatal(err)
	}
	applied := compiled.ReplaceAllString(strings.TrimSpace(rendered), regexes[0].Replacement)
	if applied != "" {
		t.Fatalf("unexpected regex result: %q", applied)
	}
}

func TestBuildPresetEditorViewAppliesPromptOverridesAndCustomRegex(t *testing.T) {
	enabled := true
	state := presetState{
		PromptOverrides: map[string]promptOverride{
			"main": {
				Name:        "自定义主提示",
				NameSet:     true,
				Role:        "user",
				RoleSet:     true,
				Content:     "hello<regex order=2>\"/hello/\":\"world\"</regex>",
				ContentSet:  true,
				Enabled:     &enabled,
				Triggers:    []string{"test"},
				TriggersSet: true,
			},
		},
		CustomRegex: []customRegexRule{
			{
				ID:          "custom-1",
				Name:        "extra",
				Order:       5,
				PatternSpec: "/world/",
				Replacement: "done",
				Enabled:     true,
			},
		},
	}
	preset := stPreset{
		Prompts: []stPrompt{
			{Identifier: "main", Name: "主提示", Role: "system", Content: "raw"},
		},
	}

	view := buildPresetEditorView(state, preset)
	if len(view.Prompts) != 1 {
		t.Fatalf("expected 1 prompt item, got %d", len(view.Prompts))
	}
	item := view.Prompts[0]
	if item.Name != "自定义主提示" || item.Role != "user" {
		t.Fatalf("unexpected prompt override result: %#v", item)
	}
	if len(item.Triggers) != 1 || item.Triggers[0] != "test" {
		t.Fatalf("unexpected prompt triggers: %#v", item.Triggers)
	}
	if len(view.Regexes) != 2 {
		t.Fatalf("expected base + custom regex, got %d", len(view.Regexes))
	}
}

func TestMovePromptIdentifierToReordersPosition(t *testing.T) {
	preset := stPreset{
		Prompts: []stPrompt{
			{Identifier: "a"},
			{Identifier: "b"},
			{Identifier: "c"},
		},
	}
	ordered := movePromptIdentifierTo(nil, "a", 2, preset.orderedPromptEntries())
	if strings.Join(ordered, ",") != "b,c,a" {
		t.Fatalf("unexpected order: %v", ordered)
	}
}

func TestParseRegexImportBlockSupportsSimpleLineFormat(t *testing.T) {
	items, err := parseRegexImportBlock("/foo/ => bar\n/baz/i => qux")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 imported regexes, got %d", len(items))
	}
	if items[0].PatternSpec != "/foo/" || items[1].Replacement != "qux" {
		t.Fatalf("unexpected imported items: %#v", items)
	}
}

func TestHistoryPromptBlocksKeepCurrentMessageImages(t *testing.T) {
	now := time.Date(2026, 4, 7, 6, 0, 0, 0, time.UTC)
	renderer := newSTRenderer(
		presetState{CharName: "Kizuna"},
		pluginapi.MessageContext{
			Content: "看这个",
			Time:    now.Format(time.RFC3339),
			Author: pluginapi.UserInfo{
				ID:          "user-1",
				Username:    "alice",
				DisplayName: "Alice",
			},
			Images: []pluginapi.ImageReference{
				{Kind: "attachment", Name: "demo.png", URL: "https://example.com/demo.png", ContentType: "image/png"},
			},
		},
		prepareConversationHistory(
			pluginapi.MessageContext{
				Content: "看这个",
				Time:    now.Format(time.RFC3339),
				Author: pluginapi.UserInfo{
					ID:          "user-1",
					Username:    "alice",
					DisplayName: "Alice",
				},
				Images: []pluginapi.ImageReference{
					{Kind: "attachment", Name: "demo.png", URL: "https://example.com/demo.png", ContentType: "image/png"},
				},
			},
			nil,
		),
		rand.New(rand.NewSource(1)),
	)

	blocks := renderer.historyPromptBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 history block, got %d", len(blocks))
	}
	if len(blocks[0].Images) != 1 {
		t.Fatalf("expected current user images to stay attached, got %#v", blocks[0].Images)
	}
	if !strings.Contains(blocks[0].Content, "发送者ID: user-1") {
		t.Fatalf("expected detailed current user metadata, got %q", blocks[0].Content)
	}
}
