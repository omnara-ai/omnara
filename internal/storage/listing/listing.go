package listing

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const timestampLayout = "2006-01-02T15:04:05.000000"

type KeysetCursor struct {
	Set       bool
	CreatedAt time.Time
	ID        uuid.UUID
}

type Options struct {
	NamePattern string
	SortField   string
	SortDesc    bool
	After       Cursor
}

type Cursor struct {
	Set    bool
	IsNull bool
	Key    string
	ID     uuid.UUID
}

func SortAllowed(got string, allowed ...string) bool {
	for _, candidate := range allowed {
		if got == candidate {
			return true
		}
	}
	return false
}

func Normalize(options Options) Options {
	if options.SortField == "" {
		options.SortField = "created_at"
		options.SortDesc = true
	}
	return options
}

func TimestampKey(value time.Time) string {
	return value.UTC().Format(timestampLayout)
}

func ParseTimestampKey(value string) (time.Time, error) {
	return time.ParseInLocation(timestampLayout, value, time.UTC)
}

type FileType string

const (
	FileTypeFile      FileType = "file"
	FileTypeDirectory FileType = "directory"
)

type FileEntry struct {
	Path        string     `json:"path"`
	Type        FileType   `json:"type"`
	Filename    string     `json:"filename,omitempty"`
	Description string     `json:"description,omitempty"`
	Access      string     `json:"access,omitempty"`
	Digest      string     `json:"digest,omitempty"`
	SizeBytes   *int64     `json:"size_bytes,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
}

type FileListResult struct {
	Entries []FileEntry
	Next    Cursor
}

// FileCursor is the payload of a list_files next_cursor. Scope binds it to
// one listing; Key and ID locate the last returned entry.
type FileCursor struct {
	Version int       `json:"v"`
	Scope   string    `json:"scope"`
	Key     string    `json:"key"`
	ID      uuid.UUID `json:"id"`
}

const FileCursorVersion = 1

// FileCursorScope hashes the values a list_files cursor is bound to.
func FileCursorScope(parts ...string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "/"))))
}

func (c FileCursor) Encode() (string, error) {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(c); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload.Bytes()), nil
}

// DecodeFileCursor reports false for a malformed cursor or one from another version.
func DecodeFileCursor(raw string) (FileCursor, bool) {
	var cursor FileCursor
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(payload, &cursor) != nil ||
		cursor.Version != FileCursorVersion || cursor.Scope == "" {
		return FileCursor{}, false
	}
	return cursor, true
}
