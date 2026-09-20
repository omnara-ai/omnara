package appdefinition

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/omnara-ai/omnara/internal/jsonschema"
)

type ListenerDefinition struct {
	Name     string
	Provider string
	Events   []string
}

type SlackListenerConfig struct {
	Conversations []SlackScope `json:"conversations,omitempty"`
	Events        []string     `json:"events,omitempty"`
}
type GitHubListenerConfig struct {
	Conversations []GitHubScope `json:"conversations,omitempty"`
	Events        []string      `json:"events,omitempty"`
}
type DiscordListenerConfig struct {
	Conversations []DiscordScope `json:"conversations,omitempty"`
	Events        []string       `json:"events,omitempty"`
}

// PreparedListener exposes initial subscriptions separately from ongoing event
// authority. An empty Conversations slice grants no initial subscriptions.
type PreparedListener struct {
	Name          string
	Provider      string
	Config        json.RawMessage
	Conversations []Scope
	Events        []string
}

func (d ListenerDefinition) ConfigSchema() (json.RawMessage, error) {
	properties, required, err := destinationProperties(d.Provider)
	if err != nil {
		return nil, err
	}
	conversationSchema, err := objectSchema(properties, required)
	if err != nil {
		return nil, err
	}
	return objectSchema(map[string]any{
		"conversations": map[string]any{"type": "array", "items": conversationSchema, "uniqueItems": true},
		"events": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "enum": d.Events},
			"minItems":    1,
			"uniqueItems": true,
		},
	}, nil)
}

func (d ListenerDefinition) Prepare(raw json.RawMessage) (PreparedListener, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	schema, err := d.ConfigSchema()
	if err != nil {
		return PreparedListener{}, err
	}
	if err := jsonschema.Validate(schema, raw); err != nil {
		return PreparedListener{}, fmt.Errorf("listener %s config: %w", d.Name, err)
	}
	result := PreparedListener{Name: d.Name, Provider: d.Provider}
	var canonical any
	switch d.Provider {
	case ProviderSlack:
		var config SlackListenerConfig
		if err := json.Unmarshal(raw, &config); err != nil {
			return result, err
		}
		if len(config.Events) == 0 {
			config.Events = slices.Clone(d.Events)
		}
		slices.Sort(config.Events)
		result.Events, canonical = config.Events, config
		for _, address := range config.Conversations {
			result.Conversations = append(result.Conversations, Scope{Slack: &address})
		}
	case ProviderGitHub:
		var config GitHubListenerConfig
		if err := json.Unmarshal(raw, &config); err != nil {
			return result, err
		}
		if len(config.Events) == 0 {
			config.Events = slices.Clone(d.Events)
		}
		slices.Sort(config.Events)
		result.Events, canonical = config.Events, config
		for _, address := range config.Conversations {
			result.Conversations = append(result.Conversations, Scope{GitHub: &address})
		}
	case ProviderDiscord:
		var config DiscordListenerConfig
		if err := json.Unmarshal(raw, &config); err != nil {
			return result, err
		}
		if len(config.Events) == 0 {
			config.Events = slices.Clone(d.Events)
		}
		slices.Sort(config.Events)
		result.Events, canonical = config.Events, config
		for _, address := range config.Conversations {
			result.Conversations = append(result.Conversations, Scope{Discord: &address})
		}
	}
	for _, address := range result.Conversations {
		if err := address.Validate(d.Provider); err != nil {
			return result, err
		}
	}
	result.Config, err = json.Marshal(canonical)
	return result, err
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
