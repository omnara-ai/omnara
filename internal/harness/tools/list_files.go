package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type listFilesRequest struct {
	Pattern   string          `json:"pattern"`
	Limit     int             `json:"limit,omitempty"`
	Cursor    string          `json:"cursor,omitempty"`
	MachineID json.RawMessage `json:"machine_id,omitempty"`
}

var errInvalidFileListCursor = errors.New("invalid file listing cursor; keep the same pattern or restart the listing")

func decodeFileListCursor(raw string) (listing.FileCursor, error) {
	if raw == "" {
		return listing.FileCursor{}, nil
	}
	if len(raw) > toolcatalog.ListFilesMaxCursorLength {
		return listing.FileCursor{}, errInvalidFileListCursor
	}
	cursor, ok := listing.DecodeFileCursor(raw)
	if !ok {
		return cursor, errInvalidFileListCursor
	}
	switch {
	case cursor.Key == toolcatalog.ArtifactVFSRoot:
		return cursor, nil
	case cursor.Key == memorystore.Root:
		if cursor.ID == uuid.Nil {
			return cursor, nil
		}
	case strings.HasPrefix(cursor.Key, memorystore.Root+"/"):
		name, path, hasPath := strings.Cut(strings.TrimPrefix(cursor.Key, memorystore.Root+"/"), "/")
		if cursor.ID == uuid.Nil && skills.ValidateName(name) == nil && (!hasPath || memorystore.ValidatePath(path) == nil) {
			return cursor, nil
		}
	}
	return cursor, errInvalidFileListCursor
}

func validateListFiles(raw json.RawMessage) error {
	_, _, err := machineListFiles(raw)
	return err
}

func machineListFiles(raw json.RawMessage) (machineFileCall, bool, error) {
	var input listFilesRequest
	if err := decodeSingleStrictJSON(raw, &input, "list_files request"); err != nil {
		return machineFileCall{}, false, err
	}
	machineID, err := resolveOptionalMachineID(input.MachineID)
	if err != nil {
		return machineFileCall{}, false, err
	}
	if onMachine(input.Pattern, machineID) {
		if input.Limit == 0 {
			input.Limit = toolcatalog.ListFilesDefaultLimit
		}
		if err := validateMachineGlob(input.Pattern); err != nil {
			return machineFileCall{}, false, err
		}
		if input.Limit < 1 || input.Limit > toolcatalog.ListFilesMaxLimit {
			return machineFileCall{}, false, fmt.Errorf("limit must be between 1 and %d", toolcatalog.ListFilesMaxLimit)
		}
		if len(input.Cursor) > toolcatalog.ListFilesMaxCursorLength {
			return machineFileCall{}, false, errInvalidFileListCursor
		}
		return machineFileCall{machineID: machineID, kind: processcmd.KindFileList, cursor: input.Cursor,
			operation: processcmd.FileOperation{Path: input.Pattern, Limit: input.Limit}}, true, nil
	}
	if _, err := storage.CompileFilePattern(input.Pattern); err != nil {
		return machineFileCall{}, false, err
	}
	_, err = decodeFileListCursor(input.Cursor)
	return machineFileCall{}, false, err
}

func validateMachineGlob(pattern string) error {
	if err := validateMachinePath(pattern); err != nil {
		return err
	}
	if strings.ContainsAny(pattern, "*?") && !doublestar.ValidatePattern(filepath.ToSlash(pattern)) {
		return errors.New("invalid glob pattern")
	}
	return nil
}

func runListFiles(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	var input listFilesRequest
	if err := json.Unmarshal(call.Call.Input, &input); err != nil {
		return nil, err
	}
	cursor, err := decodeFileListCursor(input.Cursor)
	if err != nil {
		return nil, err
	}
	scope := listing.FileCursorScope(call.Turn.ProjectID.String(), call.Turn.AgentID.String(), input.Pattern)
	root, _, _ := strings.Cut(strings.TrimPrefix(input.Pattern, "/"), "/")
	if input.Cursor != "" && (cursor.Scope != scope || !strings.HasPrefix(cursor.Key+"/", "/"+root+"/")) {
		return nil, errInvalidFileListCursor
	}
	result, err := call.Executor.Store.ListFiles(
		ctx, call.Turn.ProjectID, call.Turn.AgentID, input.Pattern, input.Limit,
		listing.Cursor{Set: input.Cursor != "", Key: cursor.Key, ID: cursor.ID},
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, errors.New("file listing timed out; narrow the pattern and retry")
		}
		return nil, err
	}
	var next *string
	if result.Next.Set {
		token, err := listing.FileCursor{
			Version: listing.FileCursorVersion, Scope: scope, Key: result.Next.Key, ID: result.Next.ID,
		}.Encode()
		if err != nil {
			return nil, err
		}
		next = &token
	}
	return completeFileTool(struct {
		Entries    []listing.FileEntry `json:"entries"`
		NextCursor *string             `json:"next_cursor"`
	}{Entries: result.Entries, NextCursor: next})
}
