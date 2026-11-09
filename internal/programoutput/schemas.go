// Package programoutput describes the JSON values `omnarad call` prints for
// the built-in tools commands may call: a successful call's first structured
// result part. Struct results are reflected from the types the tools return;
// storage_read builds maps, so its schema is written out. The tools' tests
// check real results against every schema.
package programoutput

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

// storageReadSchema covers the text pages readFileLines and
// readFileChars return and completeImageRead's metadata.
const storageReadSchema = `{
	"description": "One page of a text file, or an image file's metadata (the image is a media_ref in --output=envelope content).",
	"anyOf": [
		{
			"title": "text page by lines",
			"type": "object",
			"properties": {
				"path": {"type": "string"},
				"content": {"type": "string"},
				"content_type": {"type": "string"},
				"size_bytes": {"type": "integer"},
				"offset_line": {"type": "integer"},
				"lines_read": {"type": "integer"},
				"bytes_read": {"type": "integer"},
				"has_more": {"type": "boolean"},
				"next_offset_line": {"type": "integer"},
				"next_offset_char": {"type": "integer"},
				"notice": {"type": "string"}
			},
			"required": [
				"path", "content", "content_type", "size_bytes", "offset_line", "lines_read", "bytes_read", "has_more"
			],
			"additionalProperties": false
		},
		{
			"title": "text page by characters",
			"type": "object",
			"properties": {
				"path": {"type": "string"},
				"content": {"type": "string"},
				"content_type": {"type": "string"},
				"size_bytes": {"type": "integer"},
				"offset_char": {"type": "integer"},
				"bytes_read": {"type": "integer"},
				"has_more": {"type": "boolean"},
				"next_offset_char": {"type": "integer"}
			},
			"required": ["path", "content", "content_type", "size_bytes", "offset_char", "bytes_read", "has_more"],
			"additionalProperties": false
		},
		{
			"title": "image",
			"type": "object",
			"properties": {
				"path": {"type": "string"},
				"content_type": {"type": "string"},
				"size_bytes": {"type": "integer"}
			},
			"required": ["path", "content_type", "size_bytes"],
			"additionalProperties": false
		}
	]
}`

// StorageSearch is storage_search's result.
type StorageSearch struct {
	Lines            []StorageSearchLine `json:"lines,omitempty"`
	Files            []StorageSearchFile `json:"files,omitempty"`
	MatchCount       int                 `json:"match_count"`
	Truncated        bool                `json:"truncated"`
	IncompleteReason string              `json:"incomplete_reason,omitempty"`
}

// StorageSearchLine is one matching or context line, or a multiline block.
type StorageSearchLine struct {
	Path       string `json:"path"`
	LineNumber int    `json:"line_number"`
	EndLine    int    `json:"end_line"`
	Text       string `json:"text"`
	IsMatch    bool   `json:"is_match"`
}

// StorageSearchFile is a matching file in -l and -c modes.
type StorageSearchFile struct {
	Path  string  `json:"path"`
	Count *uint64 `json:"count,omitempty"`
}

// WebSearch is web_search's result for commands; model calls get the results
// rendered as text.
type WebSearch struct {
	Query   string                   `json:"query"`
	Results []webaccess.SearchResult `json:"results"`
}

// WebFetch is web_fetch's result for commands: the page metadata model calls
// get, plus the content they get as text.
type WebFetch struct {
	webaccess.FetchResult
	Content string `json:"content"`
}

// GitHubPendingReview is the pending_review section of a GitHub PR read.
type GitHubPendingReview struct {
	Review *github.Review `json:"review"`
}

// builtInSchemas and integrationReadSchemas are the tools commands may call,
// besides MCP tools: each one's result type is part of its contract.
var (
	builtInSchemas = map[string]func() (json.RawMessage, error){
		toolcatalog.ToolNameStorageRead: func() (json.RawMessage, error) {
			return json.RawMessage(storageReadSchema), nil
		},
		toolcatalog.ToolNameStorageList:   reflectOutputSchema[listing.FilePage],
		toolcatalog.ToolNameStorageSearch: reflectOutputSchema[StorageSearch],
		toolcatalog.ToolNameWebSearch:     reflectOutputSchema[WebSearch],
		toolcatalog.ToolNameWebFetch:      reflectOutputSchema[WebFetch],
	}
	integrationReadSchemas = map[integrationdefinition.Kind]func() (json.RawMessage, error){
		integrationdefinition.SlackThread:   reflectOutputSchema[slack.MessagePage],
		integrationdefinition.GitHubPR:      githubReadOutputSchema,
		integrationdefinition.DiscordThread: reflectOutputSchema[discord.MessagePage],
	}
)

type schemaSet struct {
	builtIns     map[string]json.RawMessage
	integrations map[integrationdefinition.Kind]json.RawMessage
}

var schemas = sync.OnceValues(buildSchemas)

// HasBuiltIn reports whether commands may call the built-in tool name.
func HasBuiltIn(name string) bool {
	_, ok := builtInSchemas[name]
	return ok
}

// HasIntegrationRead reports whether commands may call the read tool of an
// integration of kind.
func HasIntegrationRead(kind integrationdefinition.Kind) bool {
	_, ok := integrationReadSchemas[kind]
	return ok
}

// BuiltIn returns the schema of what commands get from the built-in tool
// name, or nil when commands cannot call it.
func BuiltIn(name string) (json.RawMessage, error) {
	set, err := schemas()
	return set.builtIns[name], err
}

// IntegrationRead returns the schema of what commands get from the read tool
// of an integration of kind, or nil when commands cannot call it.
func IntegrationRead(kind integrationdefinition.Kind) (json.RawMessage, error) {
	set, err := schemas()
	return set.integrations[kind], err
}

func buildSchemas() (schemaSet, error) {
	set := schemaSet{
		builtIns:     make(map[string]json.RawMessage, len(builtInSchemas)),
		integrations: make(map[integrationdefinition.Kind]json.RawMessage, len(integrationReadSchemas)),
	}
	var err error
	for name, schema := range builtInSchemas {
		if set.builtIns[name], err = schema(); err != nil {
			return schemaSet{}, fmt.Errorf("%s output schema: %w", name, err)
		}
	}
	for kind, schema := range integrationReadSchemas {
		if set.integrations[kind], err = schema(); err != nil {
			return schemaSet{}, fmt.Errorf("%s read output schema: %w", kind, err)
		}
	}
	return set, nil
}

// githubReadOutputSchema is one alternative per read section, titled with it.
func githubReadOutputSchema() (json.RawMessage, error) {
	sections := []struct {
		section string
		schema  func() (*jsonschema.Schema, error)
	}{
		{"pull_request", reflectSchema[github.PullRequest]},
		{"discussion_comments", reflectSchema[github.DiscussionCommentsPage]},
		{"review_comments", reflectSchema[github.ReviewCommentsPage]},
		{"reviews", reflectSchema[github.ReviewsPage]},
		{"review", reflectSchema[github.Review]},
		{"pending_review", reflectSchema[GitHubPendingReview]},
		{"review_threads", reflectSchema[github.ReviewThreadsPage]},
		{"files", reflectSchema[github.FilesPage]},
		{"diff", reflectSchema[github.Diff]},
	}
	schema := &jsonschema.Schema{Description: "The shape depends on the section read; each alternative is titled " +
		"with its section."}
	for _, section := range sections {
		alternative, err := section.schema()
		if err != nil {
			return nil, fmt.Errorf("github %s read output schema: %w", section.section, err)
		}
		alternative.Title = "section=" + section.section
		schema.AnyOf = append(schema.AnyOf, alternative)
	}
	return json.Marshal(schema)
}

func reflectOutputSchema[T any]() (json.RawMessage, error) {
	schema, err := reflectSchema[T]()
	if err != nil {
		return nil, err
	}
	return json.Marshal(schema)
}

func reflectSchema[T any]() (*jsonschema.Schema, error) {
	// Raw JSON fields, such as a Discord message nonce, may hold any value.
	return jsonschema.For[T](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}},
	})
}
