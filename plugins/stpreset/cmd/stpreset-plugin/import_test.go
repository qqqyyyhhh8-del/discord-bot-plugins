package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"discord-bot-plugins/sdk/pluginapi"
)

func TestPresetImportAttachmentFindsNestedAttachment(t *testing.T) {
	attachment := presetImportAttachment([]pluginapi.CommandOptionValue{
		{
			Name: "group",
			Options: []pluginapi.CommandOptionValue{
				{
					Name: stPresetOptionFile,
					Attachment: &pluginapi.AttachmentInfo{
						ID:   "att-1",
						Name: "preset.json",
					},
				},
			},
		},
	})

	if attachment == nil || attachment.ID != "att-1" {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}
}

func TestImportPresetAttachmentDownloadsAndValidatesJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"prompts":[{"identifier":"main","role":"system","content":"hello"}]}`))
	}))
	defer server.Close()

	plugin := newPresetPlugin()
	imported, err := plugin.importPresetAttachment(context.Background(), &pluginapi.AttachmentInfo{
		ID:          "att-1",
		Name:        "preset.json",
		URL:         server.URL,
		ContentType: "application/json",
		Size:        72,
	})
	if err != nil {
		t.Fatalf("import preset attachment: %v", err)
	}
	if imported.Name != "preset.json" {
		t.Fatalf("unexpected imported name: %q", imported.Name)
	}
	if len(imported.Preset.Prompts) != 1 || imported.Preset.Prompts[0].Identifier != "main" {
		t.Fatalf("unexpected preset payload: %#v", imported.Preset)
	}
}

func TestLoadPresetForStatePrefersImportedJSON(t *testing.T) {
	plugin := newPresetPlugin()
	source, preset, err := plugin.loadPresetForState(presetState{
		PresetName: "preset.json",
		PresetJSON: `{"prompts":[{"identifier":"main","role":"system","content":"hello"}]}`,
		PresetPath: "/tmp/should-not-be-used.json",
	})
	if err != nil {
		t.Fatalf("load preset for state: %v", err)
	}
	if source != "preset.json" {
		t.Fatalf("unexpected source: %q", source)
	}
	if len(preset.Prompts) != 1 || preset.Prompts[0].Identifier != "main" {
		t.Fatalf("unexpected preset: %#v", preset)
	}
}
