package main

import "testing"

func TestBuildPresetContentModalRespectsDiscordFieldLimit(t *testing.T) {
	modal := buildPresetContentModal(presetState{})
	if len(modal.Fields) > 5 {
		t.Fatalf("expected at most 5 modal fields, got %d", len(modal.Fields))
	}
}

func TestBuildCharNameModalUsesSingleField(t *testing.T) {
	modal := buildCharNameModal(presetState{CharName: "Kizuna"})
	if len(modal.Fields) != 1 {
		t.Fatalf("expected 1 modal field, got %d", len(modal.Fields))
	}
	if modal.Fields[0].CustomID != stPresetFieldCharName {
		t.Fatalf("unexpected field custom id: %q", modal.Fields[0].CustomID)
	}
}
