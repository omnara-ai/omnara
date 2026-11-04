package tools

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
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

// `omnarad call` prints a successful call's first structured result part.
// Program output schemas describe that value for the built-ins commands may
// call, so tool_search can return them. Struct results are reflected from the
// types the tools return; storage_read builds maps, so its schema is written
// out. Tests check real results against every schema.

// storageReadOutputSchema covers the text pages readFileLines and
// readFileChars return and completeImageRead's metadata.
const storageReadOutputSchema = `{
	"description": "One page of a text file, or an image file's metadata (the image is a media_ref in --json content).",
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

// webSearchProgramResult is web_search's result for commands; model calls get
// the results rendered as text.
type webSearchProgramResult struct {
	Query   string                   `json:"query"`
	Results []webaccess.SearchResult `json:"results"`
}

// webFetchProgramResult is web_fetch's result for commands: the page metadata
// model calls get, plus the content they get as text.
type webFetchProgramResult struct {
	webaccess.FetchResult
	Content string `json:"content"`
}

type githubPendingReviewResult struct {
	Review *github.Review `json:"review"`
}

type programOutputSchemaSet struct {
	builtIns     map[string]json.RawMessage
	integrations map[integrationdefinition.Kind]json.RawMessage
}

var programOutputSchemas = sync.OnceValues(buildProgramOutputSchemas)

// programOutputSchema returns the schema of the result commands get from
// spec, or nil when it is not known.
func programOutputSchema(spec modelcontext.ToolSpec) (json.RawMessage, error) {
	if spec.Type == toolcatalog.ToolTypeMCP {
		return spec.OutputSchema, nil
	}
	if spec.Type != toolcatalog.ToolTypeBuiltIn {
		return nil, nil
	}
	schemas, err := programOutputSchemas()
	if err != nil {
		return nil, err
	}
	if _, operation, ok := toolcatalog.SplitIntegrationToolName(spec.Name); ok {
		if operation != toolcatalog.IntegrationOperationRead {
			return nil, nil
		}
		return schemas.integrations[spec.IntegrationKind], nil
	}
	return schemas.builtIns[spec.Name], nil
}

func buildProgramOutputSchemas() (programOutputSchemaSet, error) {
	set := programOutputSchemaSet{
		builtIns:     map[string]json.RawMessage{toolcatalog.ToolNameStorageRead: json.RawMessage(storageReadOutputSchema)},
		integrations: map[integrationdefinition.Kind]json.RawMessage{},
	}
	var err error
	for name, schema := range map[string]func() (*jsonschema.Schema, error){
		toolcatalog.ToolNameStorageList:   reflectOutputSchema[listing.FilePage],
		toolcatalog.ToolNameStorageSearch: reflectOutputSchema[searchResult],
		toolcatalog.ToolNameWebSearch:     reflectOutputSchema[webSearchProgramResult],
		toolcatalog.ToolNameWebFetch:      reflectOutputSchema[webFetchProgramResult],
	} {
		if set.builtIns[name], err = encodeOutputSchema(schema()); err != nil {
			return programOutputSchemaSet{}, fmt.Errorf("%s output schema: %w", name, err)
		}
	}
	for kind, schema := range map[integrationdefinition.Kind]func() (*jsonschema.Schema, error){
		integrationdefinition.SlackThread:   reflectOutputSchema[slack.MessagePage],
		integrationdefinition.GitHubPR:      githubReadOutputSchema,
		integrationdefinition.DiscordThread: reflectOutputSchema[discord.MessagePage],
	} {
		if set.integrations[kind], err = encodeOutputSchema(schema()); err != nil {
			return programOutputSchemaSet{}, fmt.Errorf("%s read output schema: %w", kind, err)
		}
	}
	return set, nil
}

// githubReadOutputSchema is one alternative per read section, titled with it.
func githubReadOutputSchema() (*jsonschema.Schema, error) {
	sections := []struct {
		section string
		schema  func() (*jsonschema.Schema, error)
	}{
		{"pull_request", reflectOutputSchema[github.PullRequest]},
		{"discussion_comments", reflectOutputSchema[github.DiscussionCommentsPage]},
		{"review_comments", reflectOutputSchema[github.ReviewCommentsPage]},
		{"reviews", reflectOutputSchema[github.ReviewsPage]},
		{"review", reflectOutputSchema[github.Review]},
		{"pending_review", reflectOutputSchema[githubPendingReviewResult]},
		{"review_threads", reflectOutputSchema[github.ReviewThreadsPage]},
		{"files", reflectOutputSchema[github.FilesPage]},
		{"diff", reflectOutputSchema[github.Diff]},
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
	return schema, nil
}

func reflectOutputSchema[T any]() (*jsonschema.Schema, error) {
	// Raw JSON fields, such as a Discord message nonce, may hold any value.
	return jsonschema.For[T](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}},
	})
}

func encodeOutputSchema(schema *jsonschema.Schema, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(schema)
}
