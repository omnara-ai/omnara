package toolcatalog

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
)

const (
	AppOperationRead              = "read"
	AppOperationPostMessage       = "post_message"
	AppOperationDiscussionComment = "discussion_comment"
	AppOperationInlineComment     = "inline_comment"
	AppOperationReply             = "reply"
)

// AppToolDefinition is provider metadata, independent of saved app identities.
// Use Prepare for model exposure and ResolveArgs before provider execution.
type AppToolDefinition struct {
	Operation          string
	Provider           string
	Description        string
	FollowSubscription string
	required           []string
	properties         map[string]any
}

// Definitions and their private property maps are read-only after construction.
// Prepare copies properties into a fresh destination schema before specializing it.
var appToolDefinitions = buildAppToolDefinitions()

func LookupAppTool(definition, operation string) (AppToolDefinition, bool) {
	app, ok := appdefinition.Lookup(definition)
	if !ok || !slices.Contains(app.Tools, operation) {
		return AppToolDefinition{}, false
	}
	for _, tool := range appToolDefinitions {
		if tool.Provider == app.Provider && tool.Operation == operation {
			return tool, true
		}
	}
	return AppToolDefinition{}, false
}

func (d AppToolDefinition) ConfigSchema() (json.RawMessage, error) {
	return appdefinition.DestinationConfigSchema(d.Provider)
}
func (d AppToolDefinition) CanonicalConfig(raw json.RawMessage) (json.RawMessage, error) {
	return appdefinition.CanonicalDestinationConfig(d.Provider, raw)
}
func (d AppToolDefinition) Describe(config json.RawMessage) (string, error) {
	destination, err := appdefinition.DestinationDescription(d.Provider, config)
	if err != nil {
		return "", err
	}
	return d.Description + " Destination: " + destination + ".", nil
}

func (d AppToolDefinition) Prepare(name string, config json.RawMessage) (Entry, error) {
	_, operation, ok := SplitAppToolName(name)
	if !ok || operation != d.Operation {
		return Entry{}, fmt.Errorf("invalid qualified app operation %q", name)
	}
	properties, required, err := appdefinition.DestinationArguments(d.Provider, config)
	if err != nil {
		return Entry{}, err
	}
	maps.Copy(properties, d.properties)
	required = append(required, d.required...)
	description, err := d.Describe(config)
	if err != nil {
		return Entry{}, err
	}
	entry, err := toolEntry(name, description, required, properties)
	if err != nil {
		return Entry{}, err
	}
	if d.Provider == appdefinition.ProviderGitHub && d.Operation == AppOperationInlineComment {
		var schema map[string]any
		_ = json.Unmarshal(entry.InputSchema, &schema)
		schema["dependentRequired"] = map[string][]string{"start_line": {"start_side"}, "start_side": {"start_line"}}
		entry.InputSchema, err = json.Marshal(schema)
	}
	return entry, err
}

// AppToolArguments carries the concrete typed destination and validated action
// arguments separately. Hidden destination settings never become model defaults.
type AppToolArguments struct {
	Destination   appdefinition.Scope
	Arguments     json.RawMessage
	FollowReplies bool
}

func (d AppToolDefinition) ResolveArgs(config, raw json.RawMessage) (AppToolArguments, error) {
	entry, err := d.Prepare(AppToolName("app", d.Operation), config)
	if err != nil {
		return AppToolArguments{}, err
	}
	if err := jsonschema.Validate(entry.InputSchema, raw); err != nil {
		return AppToolArguments{}, err
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return AppToolArguments{}, err
	}
	destinations, _, _ := appdefinition.DestinationArguments(d.Provider, config)
	address := map[string]json.RawMessage{}
	for key := range destinations {
		if value, ok := args[key]; ok {
			address[key] = value
			delete(args, key)
		}
	}
	addressJSON, err := json.Marshal(address)
	if err != nil {
		return AppToolArguments{}, err
	}
	destination, err := appdefinition.ResolveDestination(d.Provider, config, addressJSON)
	if err != nil {
		return AppToolArguments{}, err
	}
	var follow bool
	if value, ok := args["follow_replies"]; ok {
		_ = json.Unmarshal(value, &follow)
	}
	action, err := json.Marshal(args)
	return AppToolArguments{Destination: destination, Arguments: action, FollowReplies: follow}, err
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
		{Operation: AppOperationRead, Provider: appdefinition.ProviderSlack,
			Description: "Read messages from the Slack channel or thread at the supplied destination.",
			properties:  map[string]any{"cursor": text(), "limit": limit()}},
		{
			Operation: AppOperationPostMessage, Provider: appdefinition.ProviderSlack,
			FollowSubscription: "thread_messages",
			Description: "Post a Slack message at the supplied destination. " +
				"follow_replies subscribes this agent to replies after successful posting and local registration.",
			required: []string{"text"},
			properties: map[string]any{
				"text":           text(),
				"artifact_ids":   artifacts(20),
				"follow_replies": map[string]any{"type": "boolean"},
			},
		},
		{
			Operation: AppOperationRead, Provider: appdefinition.ProviderGitHub,
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
			Operation: AppOperationDiscussionComment, Provider: appdefinition.ProviderGitHub,
			Description: "Post a discussion comment on the selected GitHub pull request.",
			required:    []string{"body"},
			properties:  map[string]any{"body": text()},
		},
		{
			Operation: AppOperationInlineComment, Provider: appdefinition.ProviderGitHub,
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
			Operation: AppOperationReply, Provider: appdefinition.ProviderGitHub,
			Description: "Reply to a review comment in the selected GitHub pull request.",
			required:    []string{"comment_id", "body"},
			properties:  map[string]any{"comment_id": positive(), "body": text()},
		},
		{Operation: AppOperationRead, Provider: appdefinition.ProviderDiscord,
			Description: "Read messages from the Discord channel or thread at the supplied destination.",
			properties:  map[string]any{"before": text(), "limit": limit()}},
		{
			Operation: AppOperationPostMessage, Provider: appdefinition.ProviderDiscord,
			FollowSubscription: "thread_messages",
			Description: "Post a Discord message at the supplied destination. " +
				"follow_replies subscribes this agent to replies after successful posting and local registration.",
			required: []string{"content"},
			properties: map[string]any{
				"content":        text(),
				"artifact_ids":   artifacts(10),
				"follow_replies": map[string]any{"type": "boolean"},
			},
		},
	}
}
