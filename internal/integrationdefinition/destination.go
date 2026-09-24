package integrationdefinition

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/jsonschema"
)

func DestinationProperties(provider string) (map[string]any, []string, error) {
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
		return nil, nil, fmt.Errorf("unknown integration provider %q", provider)
	}
}

func objectSchema(properties map[string]any, required []string) (json.RawMessage, error) {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return json.Marshal(schema)
}

func ResolveDestination(provider string, args json.RawMessage) (Scope, error) {
	properties, required, err := DestinationProperties(provider)
	if err != nil {
		return Scope{}, err
	}
	schema, err := objectSchema(properties, required)
	if err != nil {
		return Scope{}, err
	}
	if err := jsonschema.Validate(schema, args); err != nil {
		return Scope{}, err
	}
	var scope Scope
	switch provider {
	case ProviderSlack:
		scope.Slack = &SlackScope{}
		err = json.Unmarshal(args, scope.Slack)
	case ProviderGitHub:
		scope.GitHub = &GitHubScope{}
		err = json.Unmarshal(args, scope.GitHub)
	case ProviderDiscord:
		scope.Discord = &DiscordScope{}
		err = json.Unmarshal(args, scope.Discord)
	}
	if err != nil {
		return Scope{}, err
	}
	return scope, scope.Validate(provider)
}
