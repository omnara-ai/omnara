package omnarad

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/filesearch"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/textutil"
)

// Safety bounds for file operations. The backend sets the agent-facing
// limits in each operation; these only stop runaway work.
const (
	maxFileOperationInputBytes = 4 * 1024 * 1024
	maxFileOperationEntries    = 1000
	fileOperationTimeLimit     = 15 * time.Second
	maxListVisitedEntries      = 100_000
	maxListResultBytes         = 128 * 1024
	maxSearchScannedBytes      = 256 * 1024 * 1024
)

var (
	errFileOperationFull  = errors.New("file operation result is full")
	errFileOperationBound = errors.New("file operation reached a resource limit")
)

// runFileOperation executes the processcmd.FileOperation spec read from input
// and writes a daemonprotocol.FileOperationResult. Operation failures are part
// of the result, so it only fails when the result cannot be written.
func runFileOperation(ctx context.Context, processID string, input io.Reader, resultWriter io.Writer) error {
	result := executeFileOperation(ctx, processID, input)
	if err := json.NewEncoder(resultWriter).Encode(result); err != nil {
		return fmt.Errorf("write file operation result: %w", err)
	}
	return nil
}

func executeFileOperation(ctx context.Context, processID string, input io.Reader) daemonprotocol.FileOperationResult {
	value, artifact, err := dispatchFileOperation(ctx, processID, input)
	var raw []byte
	if err == nil {
		raw, err = json.Marshal(value)
		if err == nil && len(raw) > daemonprotocol.MaxFileOperationResultBytes {
			err = errors.New("file operation result exceeds the size limit; narrow the request")
		}
	}
	if err != nil {
		failure := daemonprotocol.FileTransferError{Code: "file_operation_failed", Message: err.Error()}
		var apiErr *daemonprotocol.FileTransferError
		if errors.As(err, &apiErr) {
			failure = *apiErr
		}
		failure.Message = textutil.TruncateBytes(failure.Message, maxFileTransferErrorBytes)
		return daemonprotocol.FileOperationResult{Error: &failure}
	}
	return daemonprotocol.FileOperationResult{Value: raw, Artifact: artifact}
}

func dispatchFileOperation(ctx context.Context, processID string, input io.Reader) (any, string, error) {
	body, err := io.ReadAll(io.LimitReader(input, maxFileOperationInputBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read file operation: %w", err)
	}
	var spec processcmd.ExecutionSpec
	if len(body) > maxFileOperationInputBytes || json.Unmarshal(body, &spec) != nil ||
		spec.ValidateLocal() != nil || spec.File == nil {
		return nil, "", errors.New("invalid file operation")
	}
	operation := *spec.File
	if spec.Kind == processcmd.KindFileRead {
		return readMachineFile(ctx, processID, operation)
	}
	if spec.Kind == processcmd.KindFileWrite {
		value, err := writeMachineFile(operation)
		return value, "", err
	}
	if operation.Limit < 1 || operation.Limit > maxFileOperationEntries {
		return nil, "", errors.New("invalid file operation limit")
	}
	ctx, cancel := context.WithTimeout(ctx, fileOperationTimeLimit)
	defer cancel()
	if spec.Kind == processcmd.KindFileList {
		value, err := listMachineFiles(ctx, operation)
		return value, "", err
	}
	value, err := searchMachineFiles(ctx, operation)
	return value, "", err
}

func fileDigest(content []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(content))
}

// readMachineFile reads a regular file of at most the transfer size limit.
func readMachineFile(ctx context.Context, processID string, operation processcmd.FileOperation) (any, string, error) {
	paging, err := operation.Paging.Normalize()
	if err != nil {
		return nil, "", err
	}
	name, err := resolveMachinePath(operation.Path)
	if err != nil {
		return nil, "", err
	}
	content, err := readRegularFile(name)
	if err != nil {
		return nil, "", err
	}
	digest := fileDigest(content)
	contentType := http.DetectContentType(content)
	metadata := map[string]any{
		"path": operation.Path, "content_type": contentType, "size_bytes": len(content), "digest": digest,
	}
	if fileread.IsViewableImage(contentType, content) {
		uploaded, err := postTransferFile(ctx, processID, filepath.Base(operation.Path),
			bytes.NewReader(content), int64(len(content)))
		if err != nil {
			return nil, "", err
		}
		if uploaded.Error != nil || uploaded.Digest != digest || uploaded.Path == "" {
			return nil, "", errors.New("image upload returned unexpected metadata")
		}
		return metadata, uploaded.Path, nil
	}
	page, err := paging.Page(content, operation.Path)
	if err != nil {
		return nil, "", err
	}
	maps.Copy(page, metadata)
	return page, "", nil
}

// resolveMachinePath expands ~; relative paths stay relative to the process
// working directory, which is the machine working directory.
func resolveMachinePath(name string) (string, error) {
	resolved, err := processcmd.ExpandHomeRelativePath(name)
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return resolved, nil
}

func readRegularFile(name string) ([]byte, error) {
	file, err := openTransferFile(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, &daemonprotocol.FileTransferError{
			Code: "not_a_file", Message: "path is a directory; list its contents and choose a file",
		}
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path must be a regular file")
	}
	if info.Size() > daemonprotocol.MaxFileTransferBytes {
		return nil, errors.New("file exceeds the 10 MiB limit")
	}
	content, err := io.ReadAll(io.LimitReader(file, daemonprotocol.MaxFileTransferBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > daemonprotocol.MaxFileTransferBytes {
		return nil, errors.New("file exceeds the 10 MiB limit")
	}
	return content, nil
}

// writeMachineFile replaces or appends to a text file atomically, creating
// it and its parent directories when missing.
func writeMachineFile(operation processcmd.FileOperation) (any, error) {
	name, err := resolveMachinePath(operation.Path)
	if err != nil {
		return nil, err
	}
	content := []byte(*operation.Content)
	perm := fs.FileMode(0o644)
	info, err := os.Stat(name)
	switch {
	case err == nil:
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil, errors.New("path must be a regular file")
		}
		perm = info.Mode().Perm()
		if operation.Append {
			current, err := readRegularFile(name)
			if err != nil {
				return nil, err
			}
			if !utf8.Valid(current) || bytes.IndexByte(current, 0) >= 0 {
				return nil, errors.New("file must contain UTF-8 text without NUL bytes")
			}
			content = append(current, content...)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	if len(content) > daemonprotocol.MaxFileTransferBytes {
		return nil, errors.New("file content must be at most 10 MiB")
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return nil, fmt.Errorf("create parent directories: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".omnara-file-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if err := temporary.Chmod(perm); err != nil {
		return nil, fmt.Errorf("set file permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(temporary.Name(), name); err != nil {
		return nil, fmt.Errorf("replace file: %w", err)
	}
	return map[string]any{"path": operation.Path, "digest": fileDigest(content)}, nil
}

// globRoot splits a machine glob into the directory it walks, as written and
// as resolved, and the pattern below it. Paths without * or ? are literal.
func globRoot(pattern string) (base, root, glob string, err error) {
	if !strings.ContainsAny(pattern, "*?") {
		return "", "", "", nil
	}
	base, glob = doublestar.SplitPattern(filepath.ToSlash(pattern))
	if !doublestar.ValidatePattern(glob) {
		return "", "", "", doublestar.ErrBadPattern
	}
	root, err = resolveMachinePath(base)
	return base, root, glob, err
}

func globPath(base, name string) string {
	if base == "." {
		return name
	}
	return path.Join(base, name)
}

// walkGlob visits the paths below root that match glob in lexical order,
// which list cursors resume from, skipping paths up to and including after.
// Symlinks are not followed and unreadable directories are skipped.
func walkGlob(ctx context.Context, root, glob, after string, visit func(string, fs.DirEntry) error) error {
	segments := strings.Split(glob, "/")
	afterSegments := strings.Split(after, "/")
	visited := 0
	return fs.WalkDir(os.DirFS(root), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return nil //nolint:nilerr // Globs skip unreadable paths.
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if visited++; visited > maxListVisitedEntries {
			return errFileOperationBound
		}
		descend := entry.IsDir() && mayContainMatches(segments, name)
		if after != "" && slices.Compare(strings.Split(name, "/"), afterSegments) <= 0 {
			if descend && (name == after || strings.HasPrefix(after, name+"/")) {
				return nil
			}
			return skipEntry(entry)
		}
		if matched, _ := doublestar.Match(glob, name); matched {
			if err := visit(name, entry); err != nil {
				return err
			}
		}
		if entry.IsDir() && !descend {
			return fs.SkipDir
		}
		return nil
	})
}

func skipEntry(entry fs.DirEntry) error {
	if entry.IsDir() {
		return fs.SkipDir
	}
	return nil
}

// mayContainMatches reports whether directory dir can contain paths matching
// the glob segments.
func mayContainMatches(segments []string, dir string) bool {
	if slices.ContainsFunc(segments, func(segment string) bool { return strings.Contains(segment, "{") }) {
		return true
	}
	parts := strings.Split(dir, "/")
	for i, part := range parts {
		if i >= len(segments) {
			return false
		}
		if segments[i] == "**" {
			return true
		}
		if matched, _ := doublestar.Match(segments[i], part); !matched {
			return false
		}
	}
	return len(parts) < len(segments)
}

func listMachineFiles(ctx context.Context, operation processcmd.FileOperation) (any, error) {
	base, root, glob, err := globRoot(operation.Path)
	if err != nil {
		return nil, err
	}
	entries := []listing.FileEntry{}
	if glob == "" {
		if operation.After != "" {
			return map[string]any{"entries": entries, "next_cursor": nil}, nil
		}
		name, err := resolveMachinePath(operation.Path)
		if err != nil {
			return nil, err
		}
		if entry, ok := listedEntry(operation.Path, func() (fs.FileInfo, error) { return os.Stat(name) }); ok {
			entries = append(entries, entry)
		}
		return map[string]any{"entries": entries, "next_cursor": nil}, nil
	}
	fsys := os.DirFS(root)
	size, last := 0, ""
	err = walkGlob(ctx, root, glob, operation.After, func(name string, _ fs.DirEntry) error {
		entry, ok := listedEntry(globPath(base, name), func() (fs.FileInfo, error) { return fs.Stat(fsys, name) })
		if !ok {
			return nil
		}
		if size += len(entry.Path) + 64; size > maxListResultBytes {
			return errFileOperationBound
		}
		entries, last = append(entries, entry), name
		if len(entries) == operation.Limit {
			return errFileOperationFull
		}
		return nil
	})
	limited := errors.Is(err, errFileOperationBound) || errors.Is(err, context.DeadlineExceeded)
	if limited && len(entries) == 0 {
		return nil, errors.New("file listing timed out; narrow the pattern and retry")
	}
	if err != nil && !limited && !errors.Is(err, errFileOperationFull) {
		return nil, err
	}
	var next *string
	if err != nil {
		token, err := listing.FileCursor{
			Version: listing.FileCursorVersion, Scope: operation.CursorScope, Key: last,
		}.Encode()
		if err != nil {
			return nil, err
		}
		next = &token
	}
	return map[string]any{"entries": entries, "next_cursor": next}, nil
}

// listedEntry describes a file or directory, following symlinks; other
// file types are omitted like in Omnara listings.
func listedEntry(display string, stat func() (fs.FileInfo, error)) (listing.FileEntry, bool) {
	info, err := stat()
	if err != nil {
		return listing.FileEntry{}, false
	}
	entry := listing.FileEntry{Path: display, Type: listing.FileTypeDirectory}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return listing.FileEntry{}, false
		}
		size := info.Size()
		entry.Type, entry.SizeBytes = listing.FileTypeFile, &size
	}
	return entry, true
}

func searchMachineFiles(ctx context.Context, operation processcmd.FileOperation) (any, error) {
	options, err := filesearch.ParseArgs(operation.Args)
	if err != nil {
		return nil, err
	}
	matcher, err := filesearch.Compile(options)
	if err != nil {
		return nil, err
	}
	collector := &filesearch.Collector{Mode: options.Mode, Limit: operation.Limit, OffsetLine: 1}
	if operation.OffsetLine != nil {
		collector.OffsetLine = *operation.OffsetLine
	}
	base, root, glob, err := globRoot(operation.Path)
	if err != nil {
		return nil, err
	}
	scanned := 0
	search := func(display string, content []byte) error {
		if bytes.IndexByte(content, 0) >= 0 {
			if glob == "" {
				collector.MarkIncomplete("binary data detected; results may be incomplete")
			}
			return nil
		}
		if scanned += len(content); scanned > maxSearchScannedBytes {
			return errFileOperationBound
		}
		return collector.Search(display, content, matcher, options)
	}
	if glob == "" {
		name, err := resolveMachinePath(operation.Path)
		if err != nil {
			return nil, err
		}
		content, err := readRegularFile(name)
		if err != nil {
			return nil, err
		}
		err = search(operation.Path, content)
		return collector.Finish(err)
	}
	err = walkGlob(ctx, root, glob, "", func(name string, entry fs.DirEntry) error {
		if !entry.Type().IsRegular() {
			return nil
		}
		content, err := readRegularFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil //nolint:nilerr // Glob searches skip unreadable and oversized files.
		}
		return search(globPath(base, name), content)
	})
	if errors.Is(err, errFileOperationBound) {
		collector.MarkIncomplete("search resource limit reached; narrow the search")
		err = nil
	}
	return collector.Finish(err)
}
