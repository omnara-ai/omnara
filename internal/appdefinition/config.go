package appdefinition

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsonschema"
)

// SlackConfig contains hidden Slack settings, separate from model arguments.
// Omitted fields remain ordinary destination arguments of the effective tool.
type SlackConfig struct {
	ChannelID string `json:"channel_id,omitempty"`
	ThreadTS  string `json:"thread_ts,omitempty"`
}
type GitHubConfig struct {
	RepositoryID int64 `json:"repository_id,omitempty"`
	PullRequest  int   `json:"pull_request,omitempty"`
}
type DiscordConfig struct {
	GuildID   string `json:"guild_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
}

func destinationProperties(provider string) (map[string]any, []string, error) {
	text := func(pattern string) any { return map[string]any{"type": "string", "pattern": pattern} }
	positive := func() any { return map[string]any{"type": "integer", "minimum": 1} }
	switch provider {
	case ProviderSlack:
		return map[string]any{
			"channel_id": text(slackChannel.String()),
			"thread_ts":  text(slackTimestamp.String()),
		}, []string{
			"channel_id",
		}, nil
	case ProviderGitHub:
		return map[string]any{
			"repository_id": positive(),
			"pull_request":  positive(),
		}, []string{
			"repository_id",
			"pull_request",
		}, nil
	case ProviderDiscord:
		return map[string]any{
			"guild_id":   text(discordID.String()),
			"channel_id": text(discordID.String()),
			"thread_id":  text(discordID.String()),
		}, []string{
			"channel_id",
		}, nil
	default:
		return nil, nil, fmt.Errorf("unknown app provider %q", provider)
	}
}

func objectSchema(properties map[string]any, required []string) (json.RawMessage, error) {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return json.Marshal(schema)
}

func DestinationConfigSchema(provider string) (json.RawMessage, error) {
	properties, _, err := destinationProperties(provider)
	if err != nil {
		return nil, err
	}
	// Fixed children retain their parent identity. Discord guild IDs remain
	// optional: the provider adapter verifies the thread's parent channel.
	var dependencies map[string][]string
	switch provider {
	case ProviderSlack:
		dependencies = map[string][]string{"thread_ts": {"channel_id"}}
	case ProviderGitHub:
		dependencies = map[string][]string{"pull_request": {"repository_id"}}
	case ProviderDiscord:
		dependencies = map[string][]string{"thread_id": {"channel_id"}}
	}
	return json.Marshal(map[string]any{
		"type": "object", "properties": properties, "additionalProperties": false,
		"dependentRequired": dependencies,
	})
}

// CanonicalDestinationConfig validates the provider's closed config object.
func CanonicalDestinationConfig(provider string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	schema, err := DestinationConfigSchema(provider)
	if err != nil {
		return nil, err
	}
	if err := jsonschema.Validate(schema, raw); err != nil {
		return nil, fmt.Errorf("%s config: %w", provider, err)
	}
	var config any
	switch provider {
	case ProviderSlack:
		config = &SlackConfig{}
	case ProviderGitHub:
		config = &GitHubConfig{}
	case ProviderDiscord:
		config = &DiscordConfig{}
	}
	if err := json.Unmarshal(raw, config); err != nil {
		return nil, err
	}
	return json.Marshal(config)
}

// DestinationArguments builds only this provider's known destination fields.
// It never modifies a custom or MCP tool schema.
func DestinationArguments(provider string, config json.RawMessage) (map[string]any, []string, error) {
	canonical, err := CanonicalDestinationConfig(provider, config)
	if err != nil {
		return nil, nil, err
	}
	properties, required, _ := destinationProperties(provider)
	var fixed map[string]json.RawMessage
	_ = json.Unmarshal(canonical, &fixed)
	for key := range fixed {
		delete(properties, key)
	}
	required = slices.DeleteFunc(required, func(key string) bool { _, ok := fixed[key]; return ok })
	return properties, required, nil
}

// ResolveDestination rejects attempts to supply fixed fields, then decodes the
// concrete provider address. Callers validate their own operation arguments first.
func ResolveDestination(provider string, config, args json.RawMessage) (Scope, error) {
	canonical, err := CanonicalDestinationConfig(provider, config)
	if err != nil {
		return Scope{}, err
	}
	properties, required, _ := DestinationArguments(provider, canonical)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	schema, err := objectSchema(properties, required)
	if err != nil {
		return Scope{}, err
	}
	if err := jsonschema.Validate(schema, args); err != nil {
		return Scope{}, err
	}
	var fixed, supplied map[string]json.RawMessage
	_ = json.Unmarshal(canonical, &fixed)
	_ = json.Unmarshal(args, &supplied)
	for key, value := range supplied {
		fixed[key] = value
	}
	raw, err := json.Marshal(fixed)
	if err != nil {
		return Scope{}, err
	}
	var scope Scope
	switch provider {
	case ProviderSlack:
		scope.Slack = &SlackScope{}
		err = json.Unmarshal(raw, scope.Slack)
	case ProviderGitHub:
		scope.GitHub = &GitHubScope{}
		err = json.Unmarshal(raw, scope.GitHub)
	case ProviderDiscord:
		scope.Discord = &DiscordScope{}
		err = json.Unmarshal(raw, scope.Discord)
	}
	if err != nil {
		return Scope{}, err
	}
	return scope, scope.Validate(provider)
}

// DestinationDescription formats only validated, safe provider IDs for tool
// descriptions, approval summaries, and config UI. No arbitrary config is echoed.
func DestinationDescription(provider string, config json.RawMessage) (string, error) {
	canonical, err := CanonicalDestinationConfig(provider, config)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(canonical, &fields)
	parts := []string{}
	for _, key := range []string{"guild_id", "channel_id", "thread_ts", "thread_id", "repository_id", "pull_request"} {
		if raw, ok := fields[key]; ok {
			parts = append(parts, key+"="+strings.Trim(string(raw), `"`))
		}
	}
	if len(parts) == 0 {
		return provider + " destination supplied in arguments", nil
	}
	return provider + " fixed " + strings.Join(parts, ", "), nil
}
