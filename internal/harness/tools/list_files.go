package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type listFilesRequest struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
}

type fileListCursor struct {
	Version int       `json:"v"`
	Scope   string    `json:"scope"`
	Key     string    `json:"key"`
	ID      uuid.UUID `json:"id"`
}

var errInvalidFileListCursor = errors.New("invalid file listing cursor; keep the same pattern or restart the listing")

func decodeFileListCursor(raw string) (fileListCursor, error) {
	var cursor fileListCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > toolcatalog.ListFilesMaxCursorLength {
		return cursor, errInvalidFileListCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(payload, &cursor) != nil || cursor.Version != 1 || cursor.Scope == "" ||
		dbsafe.Text(cursor.Key) != nil {
		return cursor, errInvalidFileListCursor
	}
	switch {
	case cursor.Key == "/artifacts":
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
	var input listFilesRequest
	if err := decodeSingleStrictJSON(raw, &input, "list_files request"); err != nil {
		return err
	}
	if _, err := storage.CompileFilePattern(input.Pattern); err != nil {
		return err
	}
	_, err := decodeFileListCursor(input.Cursor)
	return err
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
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(
		call.Turn.ProjectID.String()+"/"+call.Turn.AgentID.String()+"/"+input.Pattern,
	)))
	if input.Cursor != "" && cursor.Scope != scope {
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
		var payload bytes.Buffer
		encoder := json.NewEncoder(&payload)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(fileListCursor{
			Version: 1, Scope: scope, Key: result.Next.Key, ID: result.Next.ID,
		}); err != nil {
			return nil, err
		}
		token := base64.RawURLEncoding.EncodeToString(payload.Bytes())
		next = &token
	}
	return completeFileTool(struct {
		Entries    []listing.FileEntry `json:"entries"`
		NextCursor *string             `json:"next_cursor"`
	}{Entries: result.Entries, NextCursor: next})
}
