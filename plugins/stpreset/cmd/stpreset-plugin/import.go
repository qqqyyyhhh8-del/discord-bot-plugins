package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discord-bot-plugins/sdk/pluginapi"
)

const (
	stPresetOptionFile     = "preset_file"
	stPresetImportMaxBytes = 2 << 20
)

type importedPreset struct {
	Name   string
	JSON   string
	Preset stPreset
}

func presetImportAttachment(options []pluginapi.CommandOptionValue) *pluginapi.AttachmentInfo {
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Name), stPresetOptionFile) && option.Attachment != nil {
			return option.Attachment
		}
		if attachment := presetImportAttachment(option.Options); attachment != nil {
			return attachment
		}
	}
	return nil
}

func (p *presetPlugin) importPresetAttachment(ctx context.Context, attachment *pluginapi.AttachmentInfo) (*importedPreset, error) {
	if attachment == nil {
		return nil, fmt.Errorf("preset attachment is required")
	}
	if attachment.Size > stPresetImportMaxBytes {
		return nil, fmt.Errorf("preset file is too large (max %d bytes)", stPresetImportMaxBytes)
	}
	url := strings.TrimSpace(attachment.URL)
	if url == "" {
		return nil, fmt.Errorf("preset attachment URL is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("attachment download failed: %s", resp.Status)
	}

	reader := io.LimitReader(resp.Body, stPresetImportMaxBytes+1)
	payload, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(payload) > stPresetImportMaxBytes {
		return nil, fmt.Errorf("preset file is too large (max %d bytes)", stPresetImportMaxBytes)
	}

	preset, err := decodePresetPayload(payload)
	if err != nil {
		return nil, err
	}

	return &importedPreset{
		Name:   firstNonEmpty(strings.TrimSpace(attachment.Name), "imported-preset.json"),
		JSON:   strings.TrimSpace(string(payload)),
		Preset: preset,
	}, nil
}
