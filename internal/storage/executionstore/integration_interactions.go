package executionstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	InteractionDestinationMaxBytes = 4 * 1024
	InteractionReceiptMaxBytes     = 16 * 1024
)

type InteractionSelection struct {
	IntegrationTargetID uuid.UUID `json:"integration_target_id"`
	HandlerKey          string    `json:"handler_key"`
}

type InteractionDestination struct {
	IntegrationType     integrationdefinition.Type           `json:"integration_type"`
	HandlerKey          string                               `json:"handler_key"`
	IntegrationID       uuid.UUID                            `json:"integration_id"`
	IntegrationTargetID uuid.UUID                            `json:"integration_target_id"`
	Address             integrationstore.ConversationAddress `json:"address"`
}

func (record AgentInteractionRecord) CapturedDestination() (*InteractionDestination, error) {
	if len(record.Destination) == 0 {
		return nil, nil //nolint:nilnil // Dashboard-only prompts have no external snapshot.
	}
	if err := validateInteractionObject(
		record.Destination,
		InteractionDestinationMaxBytes,
	); err != nil {
		return nil, fmt.Errorf("interaction destination: %w", err)
	}
	var destination InteractionDestination
	decoder := json.NewDecoder(bytes.NewReader(record.Destination))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&destination); err != nil {
		return nil, fmt.Errorf("decode interaction destination: %w", err)
	}
	if err := destination.validate(); err != nil {
		return nil, err
	}
	return &destination, nil
}

func (d InteractionDestination) validate() error {
	if d.IntegrationTargetID == uuid.Nil || d.IntegrationID == uuid.Nil ||
		toolcatalog.ValidateIntegrationName(d.HandlerKey) != nil {
		return errors.New("interaction destination requires target, integration and handler key")
	}
	definition, ok := integrationdefinition.Lookup(d.IntegrationType)
	if !ok || definition.InteractionHandler == nil {
		return errors.New("interaction destination requires an integration handler definition")
	}
	if _, err := integrationdefinition.ParseConversation(definition.Provider, d.Address.Kind, d.Address.Ref); err != nil {
		return err
	}
	return d.Address.Validate()
}

func validateInteractionObject(raw json.RawMessage, limit int) error {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) || !json.Valid(raw) ||
		bytes.TrimSpace(raw)[0] != '{' {
		return fmt.Errorf("must be a JSON object of at most %d bytes", limit)
	}
	return dbsafe.JSONStrings(raw)
}
