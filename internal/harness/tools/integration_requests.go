package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var integrationTargetRefPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*-[a-z2-9]{4}$`)

type integrationMessageRequest struct {
	Text string `json:"text"`
}

type integrationTargetRequest struct {
	TargetRef string `json:"target_ref"`
}

func resolveIntegrationMessageRequest(raw json.RawMessage) (integrationMessageRequest, error) {
	var input integrationMessageRequest
	if err := decodeSingleStrictJSON(raw, &input, "integration message request"); err != nil {
		return integrationMessageRequest{}, fmt.Errorf("parse integration message request: %w", err)
	}
	if strings.TrimSpace(input.Text) == "" {
		return integrationMessageRequest{}, errors.New("text is required")
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
