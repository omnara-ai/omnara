package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestFileListCursorValidation(t *testing.T) {
	for _, test := range []struct {
		key   string
		id    uuid.UUID
		valid bool
	}{
		{"/artifacts", uuid.Nil, true},
		{"/artifacts", uuid.New(), true},
		{"/memory", uuid.Nil, true},
		{"/memory", uuid.New(), false},
		{"/artifacts/", uuid.New(), false},
		{"/artifacts/note.txt", uuid.New(), false},
		{"/memory/a-b", uuid.Nil, true},
		{"/memory/a/nested/note.txt", uuid.Nil, true},
		{"/memory/a/" + strings.Repeat("<", 250) + "/" + strings.Repeat("\u2028", 80), uuid.Nil, true},
		{"/memory/a/../other", uuid.Nil, false},
		{"/memory/a\x00", uuid.Nil, false},
		{"/memory/a/note\x00.txt", uuid.Nil, false},
		{"/memory/a/note.txt", uuid.New(), false},
		{"/artifacts/a/b", uuid.New(), false},
		{"/artifacts/note.txt", uuid.Nil, false},
		{"/other", uuid.Nil, false},
	} {
		t.Run(test.key, func(t *testing.T) {
			want := fileListCursor{Version: 1, Scope: "scope", Key: test.key, ID: test.id}
			var payload bytes.Buffer
			encoder := json.NewEncoder(&payload)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(want); err != nil {
				t.Fatal(err)
			}
			token := base64.RawURLEncoding.EncodeToString(payload.Bytes())
			got, err := decodeFileListCursor(token)
			if test.valid {
				if err != nil || got != want {
					t.Fatalf("cursor round trip: %+v, %v", got, err)
				}
			} else if !errors.Is(err, errInvalidFileListCursor) {
				t.Fatalf("accepted invalid cursor: %v", err)
			}
		})
	}
	for _, raw := range []string{"bad", strings.Repeat("a", toolcatalog.ListFilesMaxCursorLength+1), "e30"} {
		if _, err := decodeFileListCursor(raw); !errors.Is(err, errInvalidFileListCursor) {
			t.Fatalf("accepted malformed cursor: %v", err)
		}
	}
}

func TestListFilesLiteralRootValidation(t *testing.T) {
	for _, pattern := range []string{
		"/memory", "/artifacts", "/memory/*", "/memory/**", "/memory/*-archive/**/*.md",
		"/artifacts/*.pdf", "/artifacts/art_*", "/*", "/**", "/**/review-note.txt", "/mem*/*", "/unknown/*",
	} {
		raw, err := json.Marshal(listFilesRequest{Pattern: pattern})
		if err != nil {
			t.Fatal(err)
		}
		err = validateRegisteredToolInput(toolcatalog.ToolNameListFiles, raw)
		valid := pattern == "/memory" || pattern == "/artifacts" ||
			strings.HasPrefix(pattern, "/memory/") || strings.HasPrefix(pattern, "/artifacts/")
		if valid && err != nil {
			t.Errorf("valid pattern %q rejected: %v", pattern, err)
		} else if !valid && (err == nil || !strings.Contains(err.Error(), "/memory or /artifacts")) {
			t.Errorf("pattern %q: expected allowed roots in error, got %v", pattern, err)
		}
	}
}
