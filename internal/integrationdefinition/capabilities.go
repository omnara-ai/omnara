package integrationdefinition

import (
	"encoding/json"
	"fmt"
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
	properties, required, err := DestinationProperties(d.Provider)
	if err != nil {
		return PreparedInteractionHandler{}, err
	}
	schema, err := objectSchema(properties, required)
	if err != nil {
		return PreparedInteractionHandler{}, err
	}
	return PreparedInteractionHandler{
		Description: "Questions and approvals at the supplied " + d.Provider + " destination.",
		InputSchema: schema,
	}, nil
}
func (d InteractionHandlerDefinition) ResolveArgs(args json.RawMessage) (Scope, error) {
	return ResolveDestination(d.Provider, args)
}
