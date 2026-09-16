package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

var integrationTargetRefPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*-[a-z2-9]{4}$`)

type integrationMessageRequest struct {
	Text  string   `json:"text"`
	Paths []string `json:"paths,omitempty"`
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
	for _, filePath := range input.Paths {
		if strings.HasPrefix(filePath, memorystore.Root+"/") {
			if _, _, err := memorystore.ParsePath(filePath); err != nil {
				return integrationMessageRequest{}, fmt.Errorf("invalid attachment path: %w", err)
			}
			continue
		}
		artifactID, ok := strings.CutPrefix(filePath, toolcatalog.ArtifactVFSRoot+"/")
		if !ok {
			return integrationMessageRequest{}, errors.New(
				"attachment path must be /artifacts/<artifact_id> or /memory/<store>/<file>",
			)
		}
		if _, err := publicid.Decode(publicid.KindArtifact, artifactID); err != nil {
			return integrationMessageRequest{}, fmt.Errorf("invalid artifact attachment path: %w", err)
		}
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
