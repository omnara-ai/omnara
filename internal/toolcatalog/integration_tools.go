package toolcatalog

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
)

const (
	IntegrationOperationRead              = "read"
	IntegrationOperationPostMessage       = "post_message"
	IntegrationOperationDiscussionComment = "discussion_comment"
	IntegrationOperationInlineComment     = "inline_comment"
	IntegrationOperationReply             = "reply"
)

type IntegrationToolScope string

const (
	IntegrationToolScopeIntegration  IntegrationToolScope = "integration"
	IntegrationToolScopeConversation IntegrationToolScope = "conversation"
)

type IntegrationToolDefinition struct {
	Operation       string
	IntegrationType integrationdefinition.Type
	Scope           IntegrationToolScope
	Description     string
	required        []string
	properties      map[string]any
}

var integrationToolDefinitions = buildIntegrationToolDefinitions()

func LookupIntegrationTool(
	integrationType integrationdefinition.Type,
	operation string,
) (IntegrationToolDefinition, bool) {
	integration, ok := integrationdefinition.Lookup(integrationType)
	if !ok || !slices.Contains(integration.Tools, operation) {
		return IntegrationToolDefinition{}, false
	}
	for _, tool := range integrationToolDefinitions {
		if tool.IntegrationType == integrationType && tool.Operation == operation {
			return tool, true
		}
	}
	return IntegrationToolDefinition{}, false
}

func (d IntegrationToolDefinition) Prepare(name string) (Entry, error) {
	_, operation, ok := SplitIntegrationToolName(name)
	if !ok || operation != d.Operation {
		return Entry{}, fmt.Errorf("invalid qualified integration operation %q", name)
	}
	if d.Scope != IntegrationToolScopeIntegration && d.Scope != IntegrationToolScopeConversation {
		return Entry{}, fmt.Errorf("integration tool %q requires an explicit scope", name)
	}
	entry, err := toolEntry(name, d.Description, d.required, d.properties)
	if err != nil {
		return Entry{}, err
	}
	if d.IntegrationType == integrationdefinition.GitHubPR && d.Operation == IntegrationOperationInlineComment {
		var schema map[string]any
		_ = json.Unmarshal(entry.InputSchema, &schema)
		schema["dependentRequired"] = map[string][]string{"start_line": {"start_side"}, "start_side": {"start_line"}}
		entry.InputSchema, err = json.Marshal(schema)
	}
	return entry, err
}

func buildIntegrationToolDefinitions() []IntegrationToolDefinition {
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
	return []IntegrationToolDefinition{
		{
			Operation:       IntegrationOperationRead,
			IntegrationType: integrationdefinition.SlackThread,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Read messages from this agent's assigned Slack conversation.",
			properties:      map[string]any{"cursor": text(), "limit": limit()},
		},
		{
			Operation:       IntegrationOperationPostMessage,
			IntegrationType: integrationdefinition.SlackThread,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Post a message to this agent's assigned Slack conversation.",
			required:        []string{"text"},
			properties: map[string]any{
				"text":         text(),
				"artifact_ids": artifacts(20),
			},
		},
		{
			Operation:       IntegrationOperationRead,
			IntegrationType: integrationdefinition.GitHubPR,
			Scope:           IntegrationToolScopeConversation,
			Description: "Read the selected GitHub pull request. section defaults to pull_request; " +
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
			Operation:       IntegrationOperationDiscussionComment,
			IntegrationType: integrationdefinition.GitHubPR,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Post a discussion comment on the selected GitHub pull request.",
			required:        []string{"body"},
			properties:      map[string]any{"body": text()},
		},
		{
			Operation:       IntegrationOperationInlineComment,
			IntegrationType: integrationdefinition.GitHubPR,
			Scope:           IntegrationToolScopeConversation,
			Description: "Post a review comment on a diff in the selected GitHub pull request. " +
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
			Operation:       IntegrationOperationReply,
			IntegrationType: integrationdefinition.GitHubPR,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Reply to a review comment in the selected GitHub pull request.",
			required:        []string{"comment_id", "body"},
			properties:      map[string]any{"comment_id": positive(), "body": text()},
		},
		{
			Operation:       IntegrationOperationRead,
			IntegrationType: integrationdefinition.DiscordThread,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Read messages from this agent's assigned Discord thread.",
			properties:      map[string]any{"before": text(), "limit": limit()},
		},
		{
			Operation:       IntegrationOperationPostMessage,
			IntegrationType: integrationdefinition.DiscordThread,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Post a message to this agent's assigned Discord thread. Content must be at most 2000 characters.",
			required:        []string{"content"},
			properties: map[string]any{
				"content":      map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
				"artifact_ids": artifacts(10),
			},
		},
	}
}
