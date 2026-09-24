package integrationdefinition

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/jsonschema"
)

type SubscriptionDefinition struct {
	Provider string
	Events   []string
}

func (d SubscriptionDefinition) ConversationSchema() (json.RawMessage, error) {
	properties, required, err := DestinationProperties(d.Provider)
	if err != nil {
		return nil, err
	}
	return objectSchema(properties, required)
}

func (d SubscriptionDefinition) Prepare(conversation json.RawMessage) (Scope, error) {
	scope, err := ResolveDestination(d.Provider, conversation)
	if err != nil {
		return Scope{}, fmt.Errorf("subscription conversation: %w", err)
	}
	return scope, nil
}

type InteractionHandlerDefinition struct{ Provider string }
type PreparedInteractionHandler struct {
	Description string
	InputSchema json.RawMessage
}

func (d InteractionHandlerDefinition) Prepare() (PreparedInteractionHandler, error) {
	schema, err := objectSchema(map[string]any{}, nil)
	if err != nil {
		return PreparedInteractionHandler{}, err
	}
	return PreparedInteractionHandler{
		Description: "Questions and approvals in this agent's assigned " + d.Provider +
			" conversation. Select with empty args.",
		InputSchema: schema,
	}, nil
}

func (d InteractionHandlerDefinition) ValidateArgs(args json.RawMessage) error {
	prepared, err := d.Prepare()
	if err != nil {
		return err
	}
	return jsonschema.Validate(prepared.InputSchema, args)
}
