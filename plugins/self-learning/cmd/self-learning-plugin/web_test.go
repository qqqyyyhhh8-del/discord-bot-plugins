package main

import (
	"testing"

	"discord-bot-plugins/sdk/pluginapi"
)

func TestBuildLocalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "default host", addr: "127.0.0.1:17888", want: "http://127.0.0.1:17888"},
		{name: "wildcard host", addr: "0.0.0.0:17888", want: "http://127.0.0.1:17888"},
		{name: "empty host", addr: ":18888", want: "http://127.0.0.1:18888"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := buildLocalURL(test.addr); got != test.want {
				t.Fatalf("buildLocalURL(%q) = %q, want %q", test.addr, got, test.want)
			}
		})
	}
}

func TestBuildMemoryGraphIncludesTargetAndAffinity(t *testing.T) {
	t.Parallel()

	graph := buildMemoryGraph(
		learningProfile{
			Relations: []relationEdge{
				{
					LeftUserID:  "100",
					RightUserID: "200",
					Type:        "好友",
					Strength:    7,
				},
			},
			Affinity: map[string]int{
				"100": 88,
				"200": 42,
			},
		},
		learningConfig{
			TargetUserIDs: []string{"100"},
		},
		[]conversationEvent{
			{
				Author: pluginUser("100", "Alice"),
			},
			{
				Author: pluginUser("200", "Bob"),
			},
		},
	)

	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 graph edge, got %d", len(graph.Edges))
	}
	if len(graph.Nodes) != 2 {
		t.Fatalf("expected 2 graph nodes, got %d", len(graph.Nodes))
	}

	var targetFound bool
	var affinityFound bool
	for _, node := range graph.Nodes {
		if node.ID == "100" && node.Target {
			targetFound = true
		}
		if node.ID == "100" && node.Affinity == 88 {
			affinityFound = true
		}
	}
	if !targetFound {
		t.Fatal("expected target node flag for user 100")
	}
	if !affinityFound {
		t.Fatal("expected affinity score for user 100")
	}
}

func pluginUser(id, name string) pluginapi.UserInfo {
	return pluginapi.UserInfo{
		ID:          id,
		DisplayName: name,
	}
}
