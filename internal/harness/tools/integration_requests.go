package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// Native target refs used four random characters historically. New refs use
// twelve, but old refs remain stable identifiers and must stay accepted.
var integrationTargetRefPattern = regexp.MustCompile(
	`^[a-z0-9][a-z0-9_.-]{0,127}-[a-z2-9]{4}(?:[a-z2-9]{8})?$`,
)

type integrationMessageRequest struct {
	Text string `json:"text"`
}

type integrationTargetRequest struct {
	TargetRef string `json:"target_ref"`
}

type channelMessageRequest struct {
	Text      string `json:"text"`
	ChannelID string `json:"channel_id,omitempty"`
}

func resolveIntegrationMessageRequest(raw json.RawMessage) (integrationMessageRequest, error) {
	var input integrationMessageRequest
	if err := decodeSingleStrictJSON(raw, &input, "integration message request"); err != nil {
		return integrationMessageRequest{}, fmt.Errorf("parse integration message request: %w", err)
	}
	if strings.TrimSpace(input.Text) == "" {
		return integrationMessageRequest{}, errors.New("text is required")
	}
	if len(input.Text) > toolcatalog.MaxChannelMessageTextLength {
		return integrationMessageRequest{}, errors.New("integration message text exceeds its size limit")
	}
	return input, nil
}

func resolveIntegrationTargetRequest(raw json.RawMessage) (integrationTargetRequest, error) {
	var input integrationTargetRequest
	if err := decodeSingleStrictJSON(raw, &input, "integration target request"); err != nil {
		return integrationTargetRequest{}, fmt.Errorf("parse integration target request: %w", err)
	}
	input.TargetRef = strings.ToLower(strings.TrimSpace(input.TargetRef))
	if input.TargetRef == "" {
		return integrationTargetRequest{}, errors.New("target_ref is required")
	}
	if !integrationTargetRefPattern.MatchString(input.TargetRef) {
		return integrationTargetRequest{}, errors.New(
			"target_ref must match an integration target listed in context",
		)
	}
	return input, nil
}

func resolveChannelMessageRequest(raw json.RawMessage) (channelMessageRequest, error) {
	var input channelMessageRequest
	if err := decodeSingleStrictJSON(raw, &input, "channel message request"); err != nil {
		return channelMessageRequest{}, fmt.Errorf("parse channel message request: %w", err)
	}
	if strings.TrimSpace(input.Text) == "" {
		return channelMessageRequest{}, errors.New("text is required")
	}
	if len(input.Text) > toolcatalog.MaxChannelMessageTextLength {
		return channelMessageRequest{}, errors.New("channel message text exceeds its size limit")
	}
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	return input, nil
}
