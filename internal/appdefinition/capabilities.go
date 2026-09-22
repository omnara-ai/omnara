package appdefinition

import (
	"encoding/json"
	"fmt"
	"slices"
)

type SubscriptionDefinition struct {
	Name     string
	Provider string
	Events   []string
}

type PreparedSubscription struct {
	Scope  Scope
	Events []string
}

func (d SubscriptionDefinition) ConversationSchema() (json.RawMessage, error) {
	properties, required, err := DestinationProperties(d.Provider)
	if err != nil {
		return nil, err
	}
	return objectSchema(properties, required)
}

func (d SubscriptionDefinition) Prepare(conversation json.RawMessage, events []string) (PreparedSubscription, error) {
	scope, err := ResolveDestination(d.Provider, conversation)
	if err != nil {
		return PreparedSubscription{}, fmt.Errorf("subscription %s conversation: %w", d.Name, err)
	}
	if events == nil {
		events = d.Events
	}
	if len(events) == 0 {
		return PreparedSubscription{}, fmt.Errorf("subscription %s requires at least one event", d.Name)
	}
	if len(events) > len(d.Events) {
		return PreparedSubscription{}, fmt.Errorf("subscription %s accepts at most %d events", d.Name, len(d.Events))
	}
	selected := slices.Clone(events)
	slices.Sort(selected)
	for i, event := range selected {
		if !slices.Contains(d.Events, event) {
			return PreparedSubscription{}, fmt.Errorf("subscription %s does not support event %q", d.Name, event)
		}
		if i > 0 && selected[i-1] == event {
			return PreparedSubscription{}, fmt.Errorf("subscription %s repeats event %q", d.Name, event)
		}
	}
	return PreparedSubscription{Scope: scope, Events: selected}, nil
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
