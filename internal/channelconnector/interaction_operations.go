package channelconnector

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
)

// InteractionPayload presents a copy of an existing interaction. The canonical
// interaction, its destination and its resolution remain owned by core.
type InteractionPayload struct {
	Destination   OperationDestination `json:"destination"`
	InteractionID string               `json:"interaction_id"`
	AgentID       string               `json:"agent_id"`
	ChannelID     string               `json:"channel_id"`
	Kind          string               `json:"kind"`
	Form          interactionform.Form `json:"form"`
}

func (p InteractionPayload) Validate() error {
	for _, identity := range []struct {
		kind publicid.Kind
		id   string
	}{
		{publicid.KindAgentInteraction, p.InteractionID},
		{publicid.KindAgent, p.AgentID},
		{publicid.KindIntegrationTarget, p.ChannelID},
	} {
		if _, err := publicid.Decode(identity.kind, identity.id); err != nil {
			return fmt.Errorf("invalid interaction presentation %s: %w", identity.kind, err)
		}
	}
	if p.Kind != "permission" && p.Kind != "question" {
		return fmt.Errorf("invalid interaction presentation kind %q", p.Kind)
	}
	// Validate a private copy: validating must not alter the canonical form.
	raw, err := json.Marshal(p.Form)
	if err != nil {
		return err
	}
	_, err = interactionform.Parse(raw)
	return err
}

// InteractionResult records presentation only. It never approves a tool or
// answers a question; those actions use the interaction resolution API.
type InteractionResult struct {
	MessageID string          `json:"message_id,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

func DecodeInteractionResult(raw json.RawMessage) (InteractionResult, error) {
	var result InteractionResult
	if _, err := decodeMessageResult(raw, &result); err != nil {
		return InteractionResult{}, err
	}
	if err := boundedResultText(result.MessageID, 2048); err != nil {
		return InteractionResult{}, err
	}
	metadata, err := NormalizeOpaqueObject(result.Metadata)
	if err != nil {
		return InteractionResult{}, err
	}
	result.Metadata = metadata
	return result, nil
}
