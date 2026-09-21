package executionstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	InteractionDestinationMaxBytes = 4 * 1024
	InteractionReceiptMaxBytes     = 16 * 1024
)

// InteractionSelection is mutable routing for future prompts, never authority.
// The zero value selects dashboard only. Args are validated handler arguments;
// the target is canonical attribution for their resolved concrete address.
type InteractionSelection struct {
	IntegrationTargetID uuid.UUID       `json:"integration_target_id"`
	HandlerKey          string          `json:"handler_key"`
	Args                json.RawMessage `json:"args"`
}

// InteractionDestination is immutable per prompt. Current handler availability and
// live app state must still authorize presentation and provider responses.
type InteractionDestination struct {
	AppType             appdefinition.Type                   `json:"app_type"`
	HandlerKey          string                               `json:"handler_key"`
	AppID               uuid.UUID                            `json:"app_id"`
	Args                json.RawMessage                      `json:"args"`
	IntegrationTargetID uuid.UUID                            `json:"integration_target_id"`
	Address             integrationstore.ConversationAddress `json:"address"`
}

// CapturedDestination never reconstructs a missing snapshot from today's selection.
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
	if d.IntegrationTargetID == uuid.Nil || d.AppID == uuid.Nil ||
		toolcatalog.ValidateAppName(d.HandlerKey) != nil {
		return errors.New("interaction destination requires target, app and handler key")
	}
	definition, ok := appdefinition.Lookup(d.AppType)
	if !ok || definition.InteractionHandler == nil {
		return errors.New("interaction destination requires an app handler definition")
	}
	if err := validateInteractionObject(d.Args, InteractionDestinationMaxBytes); err != nil {
		return err
	}
	scope, err := definition.InteractionHandler.ResolveArgs(d.Args)
	if err != nil {
		return err
	}
	kind, ref, err := scope.Conversation()
	if err != nil {
		return err
	}
	if d.Address != (integrationstore.ConversationAddress{Kind: kind, Ref: ref}) {
		return errors.New("interaction destination does not match handler args")
	}
	return d.Address.Validate()
}

func sameInteractionDestination(a, b InteractionDestination) bool {
	return a.AppType == b.AppType && a.HandlerKey == b.HandlerKey &&
		a.AppID == b.AppID && a.IntegrationTargetID == b.IntegrationTargetID && a.Address == b.Address &&
		jsoncanonical.Equal(a.Args, b.Args)
}

func validateInteractionObject(raw json.RawMessage, limit int) error {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) || !json.Valid(raw) ||
		bytes.TrimSpace(raw)[0] != '{' {
		return fmt.Errorf("must be a JSON object of at most %d bytes", limit)
	}
	return dbsafe.JSONStrings(raw)
}

// Convert the verified origin into complete handler arguments. Sending context
// does not constrain where the model may route an interaction.
func interactionArgsForOrigin(
	provider string,
	address integrationstore.ConversationAddress,
) (json.RawMessage, bool) {
	scope, err := appdefinition.ParseConversation(provider, address.Kind, address.Ref)
	if err != nil {
		return nil, false
	}
	args, err := scope.ConversationJSON()
	return args, err == nil
}
