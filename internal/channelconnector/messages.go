package channelconnector

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const MaxMessageTextBytes = 64 * 1024

// Message is one logical publication, even when a provider needs several API
// calls to upload its attachments. Artifact identity validation is structural;
// the operation owner must authorize each artifact for the sending agent.
type Message struct {
	Text        string   `json:"text,omitempty"`
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
}

func (m Message) Validate() error {
	if strings.TrimSpace(m.Text) == "" && len(m.ArtifactIDs) == 0 {
		return errors.New("message requires nonempty text or an artifact")
	}
	return m.validateContent()
}

// Observations may contain whitespace or unavailable attachment content. Their
// structure still has the same bounds as a message submitted for publication.
func (m Message) validateContent() error {
	if len(m.Text) > MaxMessageTextBytes {
		return errors.New("message text exceeds the 64 KiB limit")
	}
	if err := dbsafe.Text(m.Text); err != nil {
		return fmt.Errorf("message text: %w", err)
	}
	if len(m.ArtifactIDs) > MaxOperationArtifacts {
		return fmt.Errorf("message exceeds the %d artifact limit", MaxOperationArtifacts)
	}
	seen := make(map[string]struct{}, len(m.ArtifactIDs))
	for _, id := range m.ArtifactIDs {
		if _, err := publicid.Decode(publicid.KindArtifact, id); err != nil {
			return fmt.Errorf("invalid artifact ID: %w", err)
		}
		if _, exists := seen[id]; exists {
			return errors.New("message artifact IDs must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// ValidateSendParams applies the current channel contract without projecting or
// modifying tool arguments. Only an omitted params object defaults to {}; JSON
// Schema defaults are annotations and never silently insert argument values.
func ValidateSendParams(schema, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	object, err := jsoncanonical.ParseObject(params, MaxMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("send params: %w", err)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("send params: %w", err)
	}
	if err := dbsafe.JSONB(normalized, MaxMetadataBytes); err != nil {
		return nil, fmt.Errorf("send params: %w", err)
	}
	if err := jsonschema.Validate(schema, normalized); err != nil {
		return nil, fmt.Errorf("send params do not match the channel schema: %w", err)
	}
	return normalized, nil
}
