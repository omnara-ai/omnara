package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) ListFiles(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	pattern string,
	limit int,
	after listing.Cursor,
) (listing.FileListResult, error) {
	matcher, err := CompileFilePattern(pattern)
	if err != nil {
		return listing.FileListResult{}, storeerr.InvalidRequest(err)
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return listing.FileListResult{}, storeerr.InvalidRequest(errors.New("limit must be between 1 and 100"))
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	attachments, err := s.memories.LoadAgentAttachments(ctx, projectID, agentID)
	if err != nil {
		return listing.FileListResult{}, fmt.Errorf("load file listing scope: %w", err)
	}
	root, suffix, descends := strings.Cut(pattern[1:], "/")
	result := listing.FileListResult{Entries: []listing.FileEntry{}}
	var listErr error
	switch {
	case !descends:
		if !after.Set {
			result.Entries = append(result.Entries, listing.FileEntry{Path: pattern, Type: listing.FileTypeDirectory})
			after = listing.Cursor{Set: true, Key: pattern}
		}
	case root == "artifacts":
		var artifactID *uuid.UUID
		if id, decodeErr := publicid.Decode(publicid.KindArtifact, suffix); decodeErr == nil {
			artifactID = &id
		}
		result.Entries, after, listErr = s.artifacts.ListFiles(
			ctx, agentID, artifactID, matcher.String(), limit, after,
		)
	case root == "memory":
		result.Entries, listErr = s.memories.ListFiles(
			ctx, projectID, attachments, pattern, matcher, limit, after.Key,
		)
		if len(result.Entries) != 0 {
			after = listing.Cursor{Set: true, Key: result.Entries[len(result.Entries)-1].Path}
		}
	}
	if parent.Err() != nil {
		return listing.FileListResult{}, parent.Err()
	}
	limited := errors.Is(listErr, memorystore.ErrFileTraversalLimit) ||
		(errors.Is(listErr, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded))
	if listErr != nil && (!limited || len(result.Entries) == 0) {
		return listing.FileListResult{}, listErr
	}
	if limited || len(result.Entries) == limit {
		result.Next = after
	}
	return result, nil
}

func CompileFilePattern(pattern string) (*regexp.Regexp, error) {
	const maxPatternBytes = len(memorystore.Root) + skills.MaxSkillNameChars + memorystore.MaxPathBytes + 2
	if len(pattern) > maxPatternBytes || !strings.HasPrefix(pattern, "/") || !utf8.ValidString(pattern) {
		return nil, errors.New("pattern must be an absolute path")
	}
	if strings.ContainsAny(pattern, "\\\x00") {
		return nil, errors.New("invalid glob pattern")
	}
	parts := strings.Split(pattern[1:], "/")
	if parts[0] != "memory" && parts[0] != "artifacts" {
		return nil, errors.New("pattern must start with /memory or /artifacts; globs are allowed only below that root")
	}
	var out strings.Builder
	out.WriteString("(?s)^")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("invalid glob component")
		}
		if i == 0 || parts[i-1] != "**" {
			out.WriteByte('/')
		}
		if part == "**" {
			if i == len(parts)-1 {
				out.WriteString(".*")
			} else {
				out.WriteString("(?:[^/]+/)*")
			}
			continue
		}
		for _, r := range part {
			switch r {
			case '*':
				out.WriteString("[^/]*")
			case '?':
				out.WriteString("[^/]")
			default:
				out.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
	}
	out.WriteByte('$')
	return regexp.Compile(out.String())
}
