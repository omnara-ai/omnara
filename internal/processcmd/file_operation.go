package processcmd

import (
	"errors"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/fileread"
)

const (
	KindFileRead   ExecutionKind = "file_read"
	KindFileWrite  ExecutionKind = "file_write"
	KindFileList   ExecutionKind = "file_list"
	KindFileSearch ExecutionKind = "file_search"
)

// FileOperationDaemonVersion gates file operation offers; older daemons cannot parse them.
const FileOperationDaemonVersion = "0.2.6"

type FileOperation struct {
	Path string `json:"path"`
	fileread.Paging
	Content     *string  `json:"content,omitempty"`
	Append      bool     `json:"append,omitempty"`
	Args        []string `json:"args,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	After       string   `json:"after,omitempty"`
	CursorScope string   `json:"cursor_scope,omitempty"`
}

func ForFileOperation(kind ExecutionKind, operation FileOperation) ExecutionSpec {
	return ExecutionSpec{Kind: kind, File: &operation}
}

func IsFileOperation(kind ExecutionKind) bool {
	switch kind {
	case KindFileRead, KindFileWrite, KindFileList, KindFileSearch:
		return true
	default:
		return false
	}
}

func SupportsFileOperations(daemonVersion string) bool {
	if daemonVersion == daemonversion.Development {
		return true
	}
	release, err := daemonversion.ParseRelease(daemonVersion)
	if err != nil {
		return false
	}
	minimum, err := daemonversion.ParseRelease(FileOperationDaemonVersion)
	return err == nil && daemonversion.Compare(release, minimum) >= 0
}

func (o FileOperation) validate(kind ExecutionKind) error {
	if o.Path == "" || strings.ContainsRune(o.Path, 0) {
		return errors.New("file operation path must be non-empty and cannot contain NUL")
	}
	if (kind == KindFileWrite) != (o.Content != nil) {
		return errors.New("only file writes carry content")
	}
	if kind == KindFileSearch && len(o.Args) == 0 {
		return errors.New("file search requires args")
	}
	return nil
}

func (o FileOperation) label(kind ExecutionKind) string {
	return CommandLabel(strings.TrimPrefix(string(kind), "file_") + " " + o.Path)
}
