package tooloutput

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	schemacheck "github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

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

type StorageSearch struct {
	Lines            []StorageSearchLine `json:"lines,omitempty"`
	Files            []StorageSearchFile `json:"files,omitempty"`
	MatchCount       int                 `json:"match_count"`
	Truncated        bool                `json:"truncated"`
	IncompleteReason string              `json:"incomplete_reason,omitempty"`
}

type StorageSearchLine struct {
	Path       string `json:"path"`
	LineNumber int    `json:"line_number"`
	EndLine    int    `json:"end_line"`
	Text       string `json:"text"`
	IsMatch    bool   `json:"is_match"`
}

type StorageSearchFile struct {
	Path  string  `json:"path"`
	Count *uint64 `json:"count,omitempty"`
}

type WebSearch struct {
	Query   string                   `json:"query"`
	Results []webaccess.SearchResult `json:"results"`
}

type WebFetch struct {
	webaccess.FetchResult
	Content string `json:"content"`
}

type GitHubPendingReview struct {
	Review *github.Review `json:"review"`
}

var (
	StorageReadSchema   = outputSchema(literalSchema(storageReadSchema))
	StorageListSchema   = outputSchema(reflectOutputSchema[listing.FilePage])
	StorageSearchSchema = outputSchema(reflectOutputSchema[StorageSearch])
	WebSearchSchema     = outputSchema(reflectOutputSchema[WebSearch])
	WebFetchSchema      = outputSchema(reflectOutputSchema[WebFetch])
	SlackReadSchema     = outputSchema(reflectOutputSchema[slack.MessagePage])
	GitHubReadSchema    = outputSchema(githubReadOutputSchema)
	DiscordReadSchema   = outputSchema(reflectOutputSchema[discord.MessagePage])
)

func githubReadOutputSchema() (json.RawMessage, error) {
	return anyOfSchema("The shape depends on the section read; each alternative is titled with its section.",
		titled("section=pull_request", reflectSchema[github.PullRequest]),
		titled("section=discussion_comments", reflectSchema[github.DiscussionCommentsPage]),
		titled("section=review_comments", reflectSchema[github.ReviewCommentsPage]),
		titled("section=reviews", reflectSchema[github.ReviewsPage]),
		titled("section=review", reflectSchema[github.Review]),
		titled("section=pending_review", reflectSchema[GitHubPendingReview]),
		titled("section=review_threads", reflectSchema[github.ReviewThreadsPage]),
		titled("section=files", reflectSchema[github.FilesPage]),
		titled("section=diff", reflectSchema[github.Diff]),
	)
}

func outputSchema(build func() (json.RawMessage, error)) func() (json.RawMessage, error) {
	return sync.OnceValues(func() (json.RawMessage, error) {
		schema, err := build()
		if err != nil {
			return nil, err
		}
		if err := schemacheck.ValidateSchema(schema); err != nil {
			return nil, err
		}
		return schema, nil
	})
}

func literalSchema(schema string) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return json.RawMessage(schema), nil }
}

func titled(title string, build func() (*jsonschema.Schema, error)) func() (*jsonschema.Schema, error) {
	return func() (*jsonschema.Schema, error) {
		schema, err := build()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", title, err)
		}
		schema.Title = title
		return schema, nil
	}
}

func anyOfSchema(description string, alternatives ...func() (*jsonschema.Schema, error)) (json.RawMessage, error) {
	schema := &jsonschema.Schema{Description: description}
	for _, build := range alternatives {
		alternative, err := build()
		if err != nil {
			return nil, err
		}
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
	return jsonschema.For[T](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}},
	})
}
