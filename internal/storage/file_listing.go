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
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) ListFiles(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	pattern string,
	limit int,
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	q := dbsqlc.New(s.pool)
	attachments, err := s.memories.LoadAgentAttachments(ctx, projectID, agentID)
	if err != nil {
		return listing.FileListResult{}, fmt.Errorf("load file listing scope: %w", err)
	}
	entries := make([]listing.FileEntry, 0, limit+1)
	add := func(entry listing.FileEntry) { entries = append(entries, entry) }
	truncated := false
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
	if matcher.MatchString("/artifacts") {
		add(listing.FileEntry{Path: "/artifacts", Type: "directory"})
	}
	if len(entries) <= limit && relevant("/artifacts") && descend {
		var artifactID *uuid.UUID
		if prefix == pattern {
			id, decodeErr := publicid.Decode(publicid.KindArtifact, strings.TrimPrefix(pattern, "/artifacts/"))
			if decodeErr == nil {
				artifactID = &id
			}
		}
		rows, queryErr := q.ListAgentArtifacts(ctx, dbsqlc.ListAgentArtifactsParams{
			AgentID: agentID, ArtifactID: artifactID,
			Pattern: matcher.String(), RowLimit: int32(limit + 1 - len(entries)),
		})
		if queryErr != nil {
			return listing.FileListResult{}, fmt.Errorf("list artifact files: %w", queryErr)
		}
		for _, row := range rows {
			id, encodeErr := publicid.Encode(publicid.KindArtifact, row.ID)
			if encodeErr != nil {
				return listing.FileListResult{}, fmt.Errorf("list artifact files: %w", encodeErr)
			}
			entry := listing.FileEntry{Path: "/artifacts/" + id, Type: "file", SizeBytes: row.SizeBytes}
			if row.Filename != nil {
				entry.Filename = *row.Filename
			}
			if row.Digest != nil {
				entry.Digest = *row.Digest
			}
			add(entry)
		}
	}
	if len(entries) <= limit && matcher.MatchString(memorystore.Root) {
		add(listing.FileEntry{Path: memorystore.Root, Type: "directory"})
	}
	if len(entries) <= limit && relevant(memorystore.Root) && descend {
		result, err := s.memories.ListFiles(ctx, projectID, attachments, pattern, matcher, limit-len(entries))
		if err != nil {
			return listing.FileListResult{}, err
		}
		entries = append(entries, result.Entries...)
		truncated = result.Truncated
	}

	if len(entries) > limit {
		truncated = true
		entries = entries[:limit]
	}
	return listing.FileListResult{Entries: entries, Truncated: truncated}, nil
}

func CompileFilePattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > memorystore.MaxPathBytes+73 || !strings.HasPrefix(pattern, "/") || !utf8.ValidString(pattern) {
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
		out.WriteByte('/')
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
	expr := strings.ReplaceAll(out.String(), "(?:[^/]+/)*/", "(?:[^/]+/)*") + "$"
	return regexp.Compile(expr)
}
