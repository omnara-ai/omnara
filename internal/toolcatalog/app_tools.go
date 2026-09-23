package toolcatalog

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
)

const (
	AppOperationRead              = "read"
	AppOperationPostMessage       = "post_message"
	AppOperationDiscussionComment = "discussion_comment"
	AppOperationInlineComment     = "inline_comment"
	AppOperationReply             = "reply"
)

type AppToolScope string

const (
	AppToolScopeApp          AppToolScope = "app"
	AppToolScopeConversation AppToolScope = "conversation"
)

type AppToolDefinition struct {
	Operation   string
	AppType     appdefinition.Type
	Scope       AppToolScope
	Description string
	required    []string
	properties  map[string]any
}

var appToolDefinitions = buildAppToolDefinitions()

func LookupAppTool(appType appdefinition.Type, operation string) (AppToolDefinition, bool) {
	app, ok := appdefinition.Lookup(appType)
	if !ok || !slices.Contains(app.Tools, operation) {
		return AppToolDefinition{}, false
	}
	for _, tool := range appToolDefinitions {
		if tool.AppType == appType && tool.Operation == operation {
			return tool, true
		}
	}
	return AppToolDefinition{}, false
}

func (d AppToolDefinition) Prepare(name string) (Entry, error) {
	_, operation, ok := SplitAppToolName(name)
	if !ok || operation != d.Operation {
		return Entry{}, fmt.Errorf("invalid qualified app operation %q", name)
	}
	if d.Scope != AppToolScopeApp && d.Scope != AppToolScopeConversation {
		return Entry{}, fmt.Errorf("app tool %q requires an explicit scope", name)
	}
	entry, err := toolEntry(name, d.Description, d.required, d.properties)
	if err != nil {
		return Entry{}, err
	}
	if d.AppType == appdefinition.GitHubPR && d.Operation == AppOperationInlineComment {
		var schema map[string]any
		_ = json.Unmarshal(entry.InputSchema, &schema)
		schema["dependentRequired"] = map[string][]string{"start_line": {"start_side"}, "start_side": {"start_line"}}
		entry.InputSchema, err = json.Marshal(schema)
	}
	return entry, err
}

func buildAppToolDefinitions() []AppToolDefinition {
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
	return []AppToolDefinition{
		{Operation: AppOperationRead, AppType: appdefinition.SlackThread, Scope: AppToolScopeConversation,
			Description: "Read messages from this agent's assigned Slack conversation.",
			properties:  map[string]any{"cursor": text(), "limit": limit()}},
		{
			Operation: AppOperationPostMessage, AppType: appdefinition.SlackThread, Scope: AppToolScopeConversation,
			Description: "Post a message to this agent's assigned Slack conversation.",
			required:    []string{"text"},
			properties: map[string]any{
				"text":         text(),
				"artifact_ids": artifacts(20),
			},
		},
		{
			Operation: AppOperationRead, AppType: appdefinition.GitHubPR, Scope: AppToolScopeConversation,
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
			Operation: AppOperationDiscussionComment, AppType: appdefinition.GitHubPR, Scope: AppToolScopeConversation,
			Description: "Post a discussion comment on the selected GitHub pull request.",
			required:    []string{"body"},
			properties:  map[string]any{"body": text()},
		},
		{
			Operation: AppOperationInlineComment, AppType: appdefinition.GitHubPR, Scope: AppToolScopeConversation,
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
			Operation: AppOperationReply, AppType: appdefinition.GitHubPR, Scope: AppToolScopeConversation,
			Description: "Reply to a review comment in the selected GitHub pull request.",
			required:    []string{"comment_id", "body"},
			properties:  map[string]any{"comment_id": positive(), "body": text()},
		},
		{Operation: AppOperationRead, AppType: appdefinition.DiscordThread, Scope: AppToolScopeConversation,
			Description: "Read messages from this agent's assigned Discord thread.",
			properties:  map[string]any{"before": text(), "limit": limit()}},
		{
			Operation: AppOperationPostMessage, AppType: appdefinition.DiscordThread, Scope: AppToolScopeConversation,
			Description: "Post a message to this agent's assigned Discord thread. Content must be at most 2000 characters.",
			required:    []string{"content"},
			properties: map[string]any{
				"content":      map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
				"artifact_ids": artifacts(10),
			},
		},
	}
}
