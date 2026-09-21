package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const memoryListingTraversalLimit = 10000

var errFileListFull = errors.New("file listing is full")

type FileEntry struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Filename    string `json:"filename,omitempty"`
	Description string `json:"description,omitempty"`
	Access      string `json:"access,omitempty"`
	Digest      string `json:"digest,omitempty"`
	SizeBytes   *int64 `json:"size_bytes,omitempty"`
}

type FileListResult struct {
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

func (s *Store) ListFiles(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	pattern string,
	limit int,
) (FileListResult, error) {
	matcher, err := CompileFilePattern(pattern)
	if err != nil {
		return FileListResult{}, storeerr.InvalidRequest(err)
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return FileListResult{}, storeerr.InvalidRequest(errors.New("limit must be between 1 and 100"))
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	q := dbsqlc.New(s.pool)
	raw, err := q.GetAgentMemoryConfig(ctx, dbsqlc.GetAgentMemoryConfigParams{ProjectID: projectID, AgentID: agentID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = storeerr.ErrNotFound
		}
		return FileListResult{}, fmt.Errorf("load file listing scope: %w", err)
	}
	entries := make([]FileEntry, 0, limit+1)
	add := func(entry FileEntry) { entries = append(entries, entry) }
	truncated := false
	prefix := pattern
	if i := strings.IndexAny(prefix, "*?"); i >= 0 {
		prefix = prefix[:i]
	}
	parts := strings.Split(pattern[1:], "/")
	recursive := slices.Contains(parts, "**")
	descend := len(parts) > 1 || recursive
	relevant := func(root string) bool {
		return strings.HasPrefix(root, prefix) || strings.HasPrefix(prefix, root+"/") || prefix == root
	}
	if matcher.MatchString("/artifacts") {
		add(FileEntry{Path: "/artifacts", Type: "directory"})
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
			return FileListResult{}, fmt.Errorf("list artifact files: %w", queryErr)
		}
		for _, row := range rows {
			id, encodeErr := publicid.Encode(publicid.KindArtifact, row.ID)
			if encodeErr != nil {
				return FileListResult{}, fmt.Errorf("list artifact files: %w", encodeErr)
			}
			entry := FileEntry{Path: "/artifacts/" + id, Type: "file", SizeBytes: row.SizeBytes}
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
		add(FileEntry{Path: memorystore.Root, Type: "directory"})
	}
	if len(entries) <= limit && relevant(memorystore.Root) && descend {
		var rowLimit *int32
		if len(parts) == 2 && !recursive {
			value := int32(limit + 1 - len(entries))
			rowLimit = &value
		}
		rows, access, queryErr := s.memoryListingStores(ctx, projectID, raw, pattern, matcher, rowLimit)
		if queryErr != nil {
			return FileListResult{}, queryErr
		}
		remaining := memoryListingTraversalLimit
		for _, row := range rows {
			root := memorystore.Root + "/" + row.Name
			if matcher.MatchString(root) {
				mode := access[row.ID]
				if row.ReadOnly {
					mode = "read_only"
				}
				add(FileEntry{Path: root, Type: "directory", Description: row.Description, Access: mode})
			}
			if len(entries) > limit {
				break
			}
			visit := func(name string, item fs.DirEntry) error {
				full := root + "/" + name
				if !matcher.MatchString(full) {
					return nil
				}
				entry := FileEntry{Path: full, Type: "directory"}
				if !item.IsDir() {
					info, err := item.Info()
					if errors.Is(err, fs.ErrNotExist) {
						return nil
					}
					if err != nil {
						return err
					}
					size := info.Size()
					entry.Type, entry.SizeBytes = "file", &size
				}
				add(entry)
				if len(entries) > limit {
					return errFileListFull
				}
				return nil
			}
			queryErr = s.globMemoryFiles(ctx, projectID, row, memoryFilePattern(pattern, row.Name), &remaining, visit)
			if errors.Is(queryErr, errFileListFull) || errors.Is(queryErr, errFileTraversalLimit) {
				truncated = true
				break
			}
			if queryErr != nil {
				return FileListResult{}, fmt.Errorf("list memory files: %w", queryErr)
			}
		}
	}

	if len(entries) > limit {
		truncated = true
		entries = entries[:limit]
	}
	return FileListResult{Entries: entries, Truncated: truncated}, nil
}

func memoryFilePattern(pattern, store string) string {
	escaped := strings.NewReplacer("[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}").Replace(pattern[1:])
	parts := strings.Split(escaped, "/")
	if i := slices.Index(parts, "**"); i >= 0 {
		parts = parts[:i+1]
	}
	for _, component := range []string{"memory", store} {
		if len(parts) == 0 {
			return ""
		}
		if parts[0] == "**" {
			break
		}
		if match, _ := doublestar.Match(parts[0], component); !match {
			return ""
		}
		parts = parts[1:]
	}
	return strings.Join(parts, "/")
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

var errFileTraversalLimit = errors.New("memory listing traversal limit reached")

type globFS struct {
	root          *os.Root
	checkCanceled func() error
	remaining     *int
}

func (g globFS) Open(name string) (fs.File, error) {
	if err := g.checkCanceled(); err != nil {
		return nil, err
	}
	if err := memoryops.CheckPath(g.root, name); err != nil {
		return nil, err
	}
	return g.root.Open(name)
}

func (g globFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := g.checkCanceled(); err != nil {
		return nil, err
	}
	if *g.remaining <= 0 {
		return nil, errFileTraversalLimit
	}
	file, err := g.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, errors.New("memory path is not a directory")
	}
	entries, err := directory.ReadDir(*g.remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > *g.remaining {
		entries = entries[:*g.remaining]
		*g.remaining = -1
	} else {
		*g.remaining -= len(entries)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (s *Store) globMemoryFiles(
	ctx context.Context,
	projectID uuid.UUID,
	store dbsqlc.ListAttachedMemoryStoresRow,
	pattern string,
	remaining *int,
	visit func(string, fs.DirEntry) error,
) error {
	if pattern == "" {
		return nil
	}
	return s.withMemoryStoreRoot(ctx, projectID, store, func(root *os.Root) error {
		return globFiles(ctx, root, pattern, remaining, visit)
	})
}

func (s *Store) withMemoryStoreRoot(
	ctx context.Context, projectID uuid.UUID, store dbsqlc.ListAttachedMemoryStoresRow, visit func(*os.Root) error,
) error {
	ref, err := memoryops.NewStoreRef(store.OrgID, projectID, store.ID, store.Name)
	if err != nil {
		return err
	}
	root, err := s.memoryFS.OpenStore(ref)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := dbsqlc.New(s.pool).GetMemoryStore(ctx, dbsqlc.GetMemoryStoreParams{
		ProjectID: projectID, ID: store.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	return visit(root)
}

func globFiles(
	ctx context.Context, root *os.Root, pattern string, remaining *int, visit func(string, fs.DirEntry) error,
) error {
	fsys := globFS{root: root, checkCanceled: ctx.Err, remaining: remaining}
	err := doublestar.GlobWalk(fsys, pattern, func(name string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return nil
		}
		return visit(name, entry)
	}, doublestar.WithFailOnIOErrors(), doublestar.WithNoFollow())
	if err != nil {
		return err
	}
	if *remaining < 0 {
		return errFileTraversalLimit
	}
	return nil
}

func (s *Store) memoryListingStores(
	ctx context.Context, projectID uuid.UUID, raw json.RawMessage, pattern string, matcher *regexp.Regexp, limit *int32,
) ([]dbsqlc.ListAttachedMemoryStoresRow, map[uuid.UUID]string, error) {
	var stores []agentconfig.MemoryStoreCompiled
	if err := json.Unmarshal(raw, &stores); err != nil {
		return nil, nil, fmt.Errorf("load memory attachments: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(stores))
	access := make(map[uuid.UUID]string, len(stores))
	for _, attached := range stores {
		id, err := publicid.Decode(publicid.KindMemoryStore, attached.PublicID)
		if err != nil {
			return nil, nil, fmt.Errorf("load memory attachments: %w", err)
		}
		ids = append(ids, id)
		access[id] = attached.Access
	}
	prefix := pattern
	if i := strings.IndexAny(prefix, "*?"); i >= 0 {
		prefix = prefix[:i]
	}
	params := dbsqlc.ListAttachedMemoryStoresParams{ProjectID: projectID, StoreIds: ids}
	if rest, ok := strings.CutPrefix(prefix, memorystore.Root+"/"); ok {
		name, _, complete := strings.Cut(rest, "/")
		params.StorePrefix = name
		if complete || prefix == pattern {
			params.StoreName = name
		}
	}
	if limit != nil {
		params.RootPattern, params.RowLimit = matcher.String(), limit
	}
	rows, err := dbsqlc.New(s.pool).ListAttachedMemoryStores(ctx, params)
	return rows, access, err
}
