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
	IntegrationOperationReviewComment     = "review_comment"
	IntegrationOperationReply             = "reply"
)

type IntegrationToolScope string

const (
	IntegrationToolScopeIntegration  IntegrationToolScope = "integration"
	IntegrationToolScopeConversation IntegrationToolScope = "conversation"
)

type IntegrationToolDefinition struct {
	Operation       string
	IntegrationKind integrationdefinition.Kind
	Scope           IntegrationToolScope
	Description     string
	required        []string
	properties      map[string]any
}

var integrationToolDefinitions = buildIntegrationToolDefinitions()

func LookupIntegrationTool(
	integrationKind integrationdefinition.Kind,
	operation string,
) (IntegrationToolDefinition, bool) {
	integration, ok := integrationdefinition.Lookup(integrationKind)
	if !ok || !slices.Contains(integration.Tools, operation) {
		return IntegrationToolDefinition{}, false
	}
	for _, tool := range integrationToolDefinitions {
		if tool.IntegrationKind == integrationKind && tool.Operation == operation {
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
	if d.IntegrationKind == integrationdefinition.GitHubPR && d.Operation == IntegrationOperationReviewComment {
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
			IntegrationKind: integrationdefinition.SlackThread,
			Scope:           IntegrationToolScopeConversation,
			Description: "Read one bounded page from this agent's assigned Slack conversation (default limit 15). " +
				"Thread replies are oldest-first, starting at the root; channel history is newest-first. " +
				"Pass next_cursor as cursor to continue; a thread's first page may not include recent replies. " +
				"Use next_cursor to inspect later thread pages after an uncertain post; absence from an earlier page is not proof of failure.",
			properties: map[string]any{"cursor": text(), "limit": limit()},
		},
		{
			Operation:       IntegrationOperationPostMessage,
			IntegrationKind: integrationdefinition.SlackThread,
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
			IntegrationKind: integrationdefinition.GitHubPR,
			Scope:           IntegrationToolScopeConversation,
			Description: "Read the selected GitHub pull request. section defaults to pull_request; " +
				"reviews returns submitted review bodies and states. Comments, reviews and files use page and limit; " +
				"follow next_page even after an empty page. review_threads returns resolution/outdated state and " +
				"a comment_id linking to review_comments/reply; use cursor and limit, following next_cursor. " +
				"Diff reads over 2 MiB fail; use section=files and follow next_page instead. " +
				"File patches can be incomplete for very large or binary changes.",
			properties: map[string]any{
				"section": map[string]any{
					"type": "string",
					"enum": []string{"pull_request", "discussion_comments", "review_comments", "reviews", "review_threads", "files", "diff"},
				},
				"page":   positive(),
				"limit":  limit(),
				"cursor": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
			},
		},
		{
			Operation:       IntegrationOperationDiscussionComment,
			IntegrationKind: integrationdefinition.GitHubPR,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Post a discussion comment on the selected GitHub pull request.",
			required:        []string{"body"},
			properties:      map[string]any{"body": text()},
		},
		{
			Operation:       IntegrationOperationReviewComment,
			IntegrationKind: integrationdefinition.GitHubPR,
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
			IntegrationKind: integrationdefinition.GitHubPR,
			Scope:           IntegrationToolScopeConversation,
			Description: "Reply to a review comment in the selected GitHub pull request. " +
				"Use comment_id from an incoming inline review comment, the id in read section=review_comments, " +
				"or comment_id in read section=review_threads; discussion comment IDs and review IDs cannot be used.",
			required:   []string{"comment_id", "body"},
			properties: map[string]any{"comment_id": positive(), "body": text()},
		},
		{
			Operation:       IntegrationOperationRead,
			IntegrationKind: integrationdefinition.DiscordThread,
			Scope:           IntegrationToolScopeConversation,
			Description:     "Read messages from this agent's assigned Discord thread.",
			properties:      map[string]any{"before": text(), "limit": limit()},
		},
		{
			Operation:       IntegrationOperationPostMessage,
			IntegrationKind: integrationdefinition.DiscordThread,
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
