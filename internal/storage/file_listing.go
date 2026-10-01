package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	result := listing.FileListResult{Entries: make([]listing.FileEntry, 0, limit)}
	finish := func(err error) (listing.FileListResult, error) {
		if parent.Err() != nil {
			return listing.FileListResult{}, parent.Err()
		}
		limited := errors.Is(err, memorystore.ErrFileTraversalLimit) ||
			(errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded))
		if err != nil && (!limited || len(result.Entries) == 0) {
			return listing.FileListResult{}, err
		}
		if limited || len(result.Entries) == limit {
			result.Next = after
		}
		return result, nil
	}
	prefix := pattern
	if i := strings.IndexAny(prefix, "*?"); i >= 0 {
		prefix = prefix[:i]
	}
	parts := strings.Split(pattern[1:], "/")
	recursive := slices.Contains(parts, "**")
	descend := len(parts) > 1 || recursive
	relevant := func(root string) bool {
		return strings.HasPrefix(root, prefix) || strings.HasPrefix(prefix, root+"/")
	}
	if after.Key == "" && matcher.MatchString("/artifacts") {
		result.Entries = append(result.Entries, listing.FileEntry{Path: "/artifacts", Type: listing.FileTypeDirectory})
		after = listing.Cursor{Set: true, Key: "/artifacts"}
	}
	if len(result.Entries) < limit && after.Key < memorystore.Root && relevant("/artifacts") && descend {
		var artifactID *uuid.UUID
		if prefix == pattern {
			id, decodeErr := publicid.Decode(publicid.KindArtifact, strings.TrimPrefix(pattern, "/artifacts/"))
			if decodeErr == nil {
				artifactID = &id
			}
		}
		files, last, err := s.artifacts.ListFiles(
			ctx, agentID, artifactID, matcher.String(), limit-len(result.Entries), after,
		)
		if err != nil {
			return finish(err)
		}
		result.Entries = append(result.Entries, files...)
		after = last
	}
	if len(result.Entries) < limit && after.Key < memorystore.Root && matcher.MatchString(memorystore.Root) {
		result.Entries = append(result.Entries, listing.FileEntry{Path: memorystore.Root, Type: listing.FileTypeDirectory})
		after = listing.Cursor{Set: true, Key: memorystore.Root}
	}
	if len(result.Entries) < limit && relevant(memorystore.Root) && descend {
		memoryAfter := ""
		if strings.HasPrefix(after.Key, memorystore.Root) {
			memoryAfter = after.Key
		}
		files, err := s.memories.ListFiles(
			ctx, projectID, attachments, pattern, matcher, limit-len(result.Entries), memoryAfter,
		)
		result.Entries = append(result.Entries, files...)
		if len(files) != 0 {
			after = listing.Cursor{Set: true, Key: files[len(files)-1].Path}
		}
		return finish(err)
	}
	return finish(nil)
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
