package appdefinition

import (
	"encoding/json"
	"fmt"
	"slices"
)

// SubscriptionDefinition exports a named app-owned receive capability. Its
// conversation is one concrete provider address, independent of agent config.
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
	properties, required, err := destinationProperties(d.Provider)
	if err != nil {
		return nil, err
	}
	return objectSchema(properties, required)
}

// Prepare resolves omitted events to all supported events and returns an owned,
// sorted selection. Explicit empty, duplicate or unknown events are rejected.
func (d SubscriptionDefinition) Prepare(conversation json.RawMessage, events []string) (PreparedSubscription, error) {
	scope, err := ResolveDestination(d.Provider, nil, conversation)
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
	Provider    string
	Config      json.RawMessage
	Description string
	InputSchema json.RawMessage
}

func (d InteractionHandlerDefinition) ConfigSchema() (json.RawMessage, error) {
	return DestinationConfigSchema(d.Provider)
}
func (d InteractionHandlerDefinition) Prepare(raw json.RawMessage) (PreparedInteractionHandler, error) {
	config, err := CanonicalDestinationConfig(d.Provider, raw)
	if err != nil {
		return PreparedInteractionHandler{}, err
	}
	properties, required, err := DestinationArguments(d.Provider, config)
	if err != nil {
		return PreparedInteractionHandler{}, err
	}
	description, _ := DestinationDescription(d.Provider, config)
	schema, err := objectSchema(properties, required)
	if err != nil {
		return PreparedInteractionHandler{}, err
	}
	return PreparedInteractionHandler{
		Provider:    d.Provider,
		Config:      config,
		Description: "Questions and approvals: " + description + ".",
		InputSchema: schema,
	}, nil
}
func (d InteractionHandlerDefinition) ResolveArgs(config, args json.RawMessage) (Scope, error) {
	return ResolveDestination(d.Provider, config, args)
}

// ArgsForDestination derives flexible handler arguments from a verified input
// address. Every configured fixed field must match; callers resolve zero or
// multiple matching handlers to dashboard-only. This does not authorize sends.
func (d InteractionHandlerDefinition) ArgsForDestination(
	config json.RawMessage,
	destination Scope,
) (json.RawMessage, error) {
	if err := destination.Validate(d.Provider); err != nil {
		return nil, err
	}
	canonical, err := CanonicalDestinationConfig(d.Provider, config)
	if err != nil {
		return nil, err
	}
	var typed any
	switch d.Provider {
	case ProviderSlack:
		typed = destination.Slack
	case ProviderGitHub:
		typed = destination.GitHub
	case ProviderDiscord:
		typed = destination.Discord
	}
	raw, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	var fields, fixed map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	_ = json.Unmarshal(canonical, &fixed)
	for key, value := range fixed {
		if string(fields[key]) != string(value) {
			return nil, fmt.Errorf("destination does not match fixed %s", key)
		}
		delete(fields, key)
	}
	args, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if _, err := d.ResolveArgs(canonical, args); err != nil {
		return nil, err
	}
	return args, nil
}
