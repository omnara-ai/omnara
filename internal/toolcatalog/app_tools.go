package toolcatalog

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
)

const (
	ToolNameSlackRead               = "slack_read"
	ToolNameSlackPostMessage        = "slack_post_message"
	ToolNameGitHubRead              = "github_read"
	ToolNameGitHubDiscussionComment = "github_discussion_comment"
	ToolNameGitHubInlineComment     = "github_inline_comment"
	ToolNameGitHubReply             = "github_reply"
	ToolNameDiscordRead             = "discord_read"
	ToolNameDiscordPostMessage      = "discord_post_message"
)

// App tool metadata and schemas live together so provider authority and reply
// following cannot drift into independent allowlists. The definitions are immutable.
type appToolDefinition struct {
	name, provider, description string
	followReplies               bool
	required                    []string
	properties                  map[string]any
}

var appToolDefinitions = buildAppToolDefinitions()

func AppToolProvider(name string) string {
	for _, def := range appToolDefinitions {
		if def.name == name {
			return def.provider
		}
	}
	return ""
}

// AppToolSupportsFollow identifies actions whose confirmed result can authorize
// a reply subscription, subject to the original and current app resource policy.
func AppToolSupportsFollow(name string) bool {
	for _, def := range appToolDefinitions {
		if def.name == name {
			return def.followReplies
		}
	}
	return false
}

func buildAppToolDefinitions() []appToolDefinition {
	text := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	positive := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1} }
	limit := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1, "maximum": 100} }
	artifacts := func(maxItems int) map[string]any {
		return map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "pattern": `^art_[a-z2-7]{26}$`},
			"uniqueItems": true,
			"maxItems":    maxItems,
		}
	}
	return []appToolDefinition{
		{name: ToolNameSlackRead, provider: appdefinition.ProviderSlack,
			description: "Read messages from the Slack channel or thread selected by an app resource.",
			properties:  map[string]any{"thread_ts": text(), "cursor": text(), "limit": limit()}},
		{
			name: ToolNameSlackPostMessage, provider: appdefinition.ProviderSlack,
			followReplies: true,
			description: "Post a Slack message within the selected resource scope. " +
				"follow_replies requires an explicit follow policy and successful local registration after posting.",
			required: []string{"text"},
			properties: map[string]any{
				"text":           text(),
				"thread_ts":      text(),
				"artifact_ids":   artifacts(20),
				"follow_replies": map[string]any{"type": "boolean"},
			},
		},
		{
			name: ToolNameGitHubRead, provider: appdefinition.ProviderGitHub,
			description: "Read the selected GitHub pull request. section defaults to pull_request; " +
				"comment and file sections are paginated with page and limit. " +
				"Diff/file patches can be incomplete for very large or binary changes.",
			properties: map[string]any{
				"section": map[string]any{
					"type": "string",
					"enum": []string{"pull_request", "discussion_comments", "review_comments", "files", "diff"},
				},
				"page":  positive(),
				"limit": limit(),
			},
		},
		{
			name: ToolNameGitHubDiscussionComment, provider: appdefinition.ProviderGitHub,
			description: "Post a discussion comment on the selected GitHub pull request.",
			required:    []string{"body"},
			properties:  map[string]any{"body": text()},
		},
		{
			name: ToolNameGitHubInlineComment, provider: appdefinition.ProviderGitHub,
			description: "Post a review comment on a diff in the selected GitHub pull request. " +
				"Use start_line and start_side together for a multiline comment.",
			required: []string{"body", "commit_id", "path", "line", "side"},
			properties: map[string]any{
				"body":       text(),
				"commit_id":  text(),
				"path":       text(),
				"line":       positive(),
				"side":       map[string]any{"type": "string", "enum": []string{"LEFT", "RIGHT"}},
				"start_line": positive(),
				"start_side": map[string]any{"type": "string", "enum": []string{"LEFT", "RIGHT"}},
			},
		},
		{
			name: ToolNameGitHubReply, provider: appdefinition.ProviderGitHub,
			description: "Reply to a review comment in the selected GitHub pull request.",
			required:    []string{"comment_id", "body"},
			properties:  map[string]any{"comment_id": positive(), "body": text()},
		},
		{name: ToolNameDiscordRead, provider: appdefinition.ProviderDiscord,
			description: "Read messages from the Discord channel or thread selected by an app resource.",
			properties:  map[string]any{"thread_id": text(), "before": text(), "limit": limit()}},
		{
			name: ToolNameDiscordPostMessage, provider: appdefinition.ProviderDiscord,
			followReplies: true,
			description: "Post a Discord message within the selected resource scope. " +
				"follow_replies requires an explicit follow policy and successful local registration after posting.",
			required: []string{"content"},
			properties: map[string]any{
				"content":        text(),
				"thread_id":      text(),
				"artifact_ids":   artifacts(10),
				"follow_replies": map[string]any{"type": "boolean"},
			},
		},
	}
}

func appTools() ([]Entry, error) {
	entries := make([]Entry, 0, len(appToolDefinitions))
	for _, def := range appToolDefinitions {
		properties := maps.Clone(def.properties)
		properties["resource"] = map[string]any{
			"type":        "string",
			"minLength":   1,
			"description": "Config app resource key. Required when more than one resource supports this action.",
		}
		entry, err := toolEntry(def.name, def.description, def.required, properties)
		if err != nil {
			return nil, err
		}
		if def.name == ToolNameGitHubInlineComment {
			var schema map[string]any
			if err := json.Unmarshal(entry.InputSchema, &schema); err != nil {
				return nil, err
			}
			schema["dependentRequired"] = map[string][]string{
				"start_line": {"start_side"},
				"start_side": {"start_line"},
			}
			entry.InputSchema, err = json.Marshal(schema)
			if err != nil {
				return nil, err
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// WithAppResources specializes only provider actions. Custom tools retain their
// author's schema. The catalog's shared schema is never modified in place.
func (entry Entry) WithAppResources(keys []string) (Entry, error) {
	if AppToolProvider(entry.Name) == "" {
		return Entry{}, fmt.Errorf("tool %q is not an app provider action", entry.Name)
	}
	keys = slices.Clone(keys)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	if len(keys) == 0 {
		return Entry{}, fmt.Errorf("tool %q requires an enabled app resource", entry.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(entry.InputSchema, &schema); err != nil {
		return Entry{}, err
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return Entry{}, fmt.Errorf("app tool %q is missing object properties", entry.Name)
	}
	resource, ok := properties["resource"].(map[string]any)
	if !ok {
		return Entry{}, fmt.Errorf("app tool %q is missing its resource selector", entry.Name)
	}
	resource["enum"] = keys
	if len(keys) > 1 {
		required, _ := schema["required"].([]any)
		schema["required"] = append(required, "resource")
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return Entry{}, err
	}
	entry.InputSchema = raw
	return entry, nil
}
