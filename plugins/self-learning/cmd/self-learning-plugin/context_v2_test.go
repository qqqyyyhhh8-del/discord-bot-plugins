package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"discord-bot-plugins/sdk/pluginapi"
)

func TestBuildRecentPromptMessagesIncludesCurrentMessage(t *testing.T) {
	t.Parallel()

	recent := buildRecentPromptMessages([]conversationEvent{
		{
			MessageID: "old-1",
			Role:      "user",
			Time:      "2026-03-19T10:00:00Z",
			Author:    pluginUser("100", "Alice"),
			Content:   "hello",
		},
	}, pluginapi.MessageContext{
		MessageID: "current-1",
		Time:      "2026-03-19T10:01:00Z",
		Author:    pluginUser("200", "Bob"),
		Content:   "current payload",
	})

	if len(recent) != 2 {
		t.Fatalf("expected 2 recent messages, got %d", len(recent))
	}
	if got := recent[len(recent)-1].Content; got == "" || !containsAll(got, []string{"current payload", "Bob"}) {
		t.Fatalf("expected current message content in recent window, got %q", got)
	}
}

func TestNormalizeLearnCardBuildsStableKey(t *testing.T) {
	t.Parallel()

	scope := pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGuild, GuildID: "guild-1"}
	card, key := normalizeLearnCard(scope, "episode-1", learnCardResult{
		Kind:        "style",
		SubjectID:   "100",
		SubjectName: "Alice",
		Title:       "常用语气词",
		Content:     "喜欢在句尾加上啦、呢。",
		Confidence:  0.88,
	})
	if key == "" {
		t.Fatal("expected storage key")
	}
	if card.Kind != "style" {
		t.Fatalf("unexpected card kind: %q", card.Kind)
	}
	if card.SourceEpisode != "episode-1" {
		t.Fatalf("unexpected source episode: %q", card.SourceEpisode)
	}
}

func TestApplyCardRerankResultsPreservesDuplicateDocuments(t *testing.T) {
	t.Parallel()

	candidates := []scoredCard{
		{
			Key:   "card-a",
			Score: 2,
			Card: memoryCardRecord{
				Kind:    "style",
				Title:   "重复卡片",
				Content: "一样的内容",
			},
		},
		{
			Key:   "card-b",
			Score: 2,
			Card: memoryCardRecord{
				Kind:    "style",
				Title:   "重复卡片",
				Content: "一样的内容",
			},
		},
		{
			Key:   "card-c",
			Score: 1,
			Card: memoryCardRecord{
				Kind:    "fact",
				Title:   "不同卡片",
				Content: "另一个内容",
			},
		},
	}

	documents, indexByDoc := buildCardRerankDocuments(candidates)
	boosted := applyCardRerankResults(candidates, []string{documents[0], documents[1], documents[2]}, indexByDoc)

	if len(boosted) != 3 {
		t.Fatalf("expected 3 boosted cards, got %d", len(boosted))
	}
	seen := map[string]struct{}{}
	for _, item := range boosted {
		seen[item.Key] = struct{}{}
	}
	for _, key := range []string{"card-a", "card-b", "card-c"} {
		if _, ok := seen[key]; !ok {
			t.Fatalf("expected rerank result to keep %s, got %#v", key, boosted)
		}
	}
}

func TestLoadProfileFallsBackToLegacyCollection(t *testing.T) {
	t.Parallel()

	host, rpcHost, cleanup := newHostClientPair()
	defer cleanup()

	scope := pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGuild, GuildID: "guild-legacy"}
	legacy := learningProfile{
		PersonaPrompt: "legacy prompt",
		SceneSummary:  "legacy scene",
		EventCount:    12,
	}

	rpcHost.RegisterHandler(pluginapi.MethodHostRecordsGet, func(ctx context.Context, params json.RawMessage) (any, error) {
		var request pluginapi.RecordsGetRequest
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		switch request.Collection {
		case profileCollection:
			return pluginapi.RecordsGetResponse{Found: false}, nil
		case legacyProfileCollection:
			payload, err := json.Marshal(legacy)
			if err != nil {
				return nil, err
			}
			return pluginapi.RecordsGetResponse{Found: true, Value: payload}, nil
		default:
			return pluginapi.RecordsGetResponse{Found: false}, nil
		}
	})

	profile, err := loadProfile(context.Background(), host, scope)
	if err != nil {
		t.Fatalf("loadProfile: %v", err)
	}
	if profile.PersonaPrompt != legacy.PersonaPrompt {
		t.Fatalf("expected legacy persona prompt %q, got %q", legacy.PersonaPrompt, profile.PersonaPrompt)
	}
	if profile.SceneSummary != legacy.SceneSummary {
		t.Fatalf("expected legacy scene summary %q, got %q", legacy.SceneSummary, profile.SceneSummary)
	}
	if profile.Scope != scope {
		t.Fatalf("expected scope %#v, got %#v", scope, profile.Scope)
	}
}

func containsAll(input string, values []string) bool {
	for _, value := range values {
		if value != "" && !strings.Contains(input, value) {
			return false
		}
	}
	return true
}

func newHostClientPair() (*pluginapi.HostClient, *pluginapi.RPCSession, func()) {
	clientReader, hostWriter := io.Pipe()
	hostReader, clientWriter := io.Pipe()

	clientSession := pluginapi.NewRPCSession(clientReader, clientWriter)
	hostSession := pluginapi.NewRPCSession(hostReader, hostWriter)

	cleanup := func() {
		clientSession.CloseWithError(nil)
		hostSession.CloseWithError(nil)
		_ = clientReader.Close()
		_ = hostReader.Close()
		_ = clientWriter.Close()
		_ = hostWriter.Close()
	}
	return pluginapi.NewHostClient(clientSession), hostSession, cleanup
}
