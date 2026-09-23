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
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	InteractionDestinationMaxBytes = 4 * 1024
	InteractionReceiptMaxBytes     = 16 * 1024
)

type InteractionSelection struct {
	AppTargetID uuid.UUID       `json:"app_target_id"`
	HandlerKey  string          `json:"handler_key"`
	Args        json.RawMessage `json:"args"`
}

type InteractionDestination struct {
	AppType     appdefinition.Type           `json:"app_type"`
	HandlerKey  string                       `json:"handler_key"`
	AppID       uuid.UUID                    `json:"app_id"`
	Args        json.RawMessage              `json:"args"`
	AppTargetID uuid.UUID                    `json:"app_target_id"`
	Address     appstore.ConversationAddress `json:"address"`
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
	if d.AppTargetID == uuid.Nil || d.AppID == uuid.Nil ||
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
	if d.Address != (appstore.ConversationAddress{Kind: kind, Ref: ref}) {
		return errors.New("interaction destination does not match handler args")
	}
	return d.Address.Validate()
}

func sameInteractionDestination(a, b InteractionDestination) bool {
	return a.AppType == b.AppType && a.HandlerKey == b.HandlerKey &&
		a.AppID == b.AppID && a.AppTargetID == b.AppTargetID && a.Address == b.Address &&
		jsoncanonical.Equal(a.Args, b.Args)
}

func validateInteractionObject(raw json.RawMessage, limit int) error {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) || !json.Valid(raw) ||
		bytes.TrimSpace(raw)[0] != '{' {
		return fmt.Errorf("must be a JSON object of at most %d bytes", limit)
	}
	return dbsafe.JSONStrings(raw)
}

func interactionArgsForOrigin(
	provider string,
	address appstore.ConversationAddress,
) (json.RawMessage, bool) {
	scope, err := appdefinition.ParseConversation(provider, address.Kind, address.Ref)
	if err != nil {
		return nil, false
	}
	args, err := scope.ConversationJSON()
	return args, err == nil
}
