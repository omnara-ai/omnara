package omnarad

import (
	"bytes"
	"context"
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

// Safety bounds only; the backend sets the agent-facing limits in each operation.
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

func runFileOperation(ctx context.Context, processID string, input io.Reader, resultWriter io.Writer) error {
	value, artifact, err := dispatchFileOperation(ctx, processID, input)
	var raw []byte
	if err == nil {
		raw, err = json.Marshal(value)
		if err == nil && len(raw) > daemonprotocol.MaxFileOperationResultBytes {
			err = errors.New("file operation result exceeds the size limit; narrow the request")
		}
	}
	result := daemonprotocol.FileOperationResult{Value: raw, Artifact: artifact}
	if err != nil {
		failure := daemonprotocol.FileTransferError{Code: "file_operation_failed", Message: err.Error()}
		var apiErr *daemonprotocol.FileTransferError
		if errors.As(err, &apiErr) {
			failure = *apiErr
		}
		failure.Message = textutil.TruncateBytes(failure.Message, maxFileTransferErrorBytes)
		result = daemonprotocol.FileOperationResult{Error: &failure}
	}
	if err := json.NewEncoder(resultWriter).Encode(result); err != nil {
		return fmt.Errorf("write file operation result: %w", err)
	}
	return nil
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

const machinePageReadBytes = 64 * 1024

func readMachineFile(ctx context.Context, processID string, operation processcmd.FileOperation) (any, string, error) {
	paging, err := operation.Paging.Normalize()
	if err != nil {
		return nil, "", err
	}
	name, err := resolveMachinePath(operation.Path)
	if err != nil {
		return nil, "", err
	}
	file, info, err := openRegularFile(name)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = file.Close() }()
	// DetectContentType only inspects the first 512 bytes.
	content, eof, err := readUpTo(file, nil, 512)
	if err != nil {
		return nil, "", err
	}
	contentType := http.DetectContentType(content)
	metadata := map[string]any{"path": operation.Path, "content_type": contentType, "size_bytes": info.Size()}
	if slices.Contains(fileread.ViewableImageTypes(), contentType) {
		if content, eof, err = readUpTo(file, content, daemonprotocol.MaxFileTransferBytes); err != nil {
			return nil, "", err
		}
		if fileread.IsViewableImage(contentType, content) {
			uploaded, err := postTransferFile(ctx, processID, filepath.Base(operation.Path),
				bytes.NewReader(content), int64(len(content)))
			if err != nil {
				return nil, "", err
			}
			if uploaded.Error != nil || uploaded.Path == "" {
				return nil, "", errors.New("image upload returned unexpected metadata")
			}
			return metadata, uploaded.Path, nil
		}
	}
	// Read only as far as the requested page needs, doubling the prefix until the page fits.
	for target := machinePageReadBytes; ; target *= 2 {
		if content, eof, err = readUpTo(file, content, min(target, daemonprotocol.MaxFileTransferBytes)); err != nil {
			return nil, "", err
		}
		text := content
		if !eof {
			text = trimPartialRune(content)
		}
		page, err := paging.Page(text, operation.Path)
		if err != nil {
			return nil, "", err
		}
		if eof || page["has_more"] == true || len(content) >= daemonprotocol.MaxFileTransferBytes {
			maps.Copy(page, metadata)
			return page, "", nil
		}
	}
}

// readUpTo extends content to size bytes from r and reports whether r ended first.
func readUpTo(r io.Reader, content []byte, size int) ([]byte, bool, error) {
	if len(content) >= size {
		return content, false, nil
	}
	n := len(content)
	content = slices.Grow(content, size-n)[:size]
	read, err := io.ReadFull(r, content[n:])
	content = content[:n+read]
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return content, true, nil
	}
	return content, false, err
}

func trimPartialRune(content []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(content); i++ {
		if utf8.RuneStart(content[len(content)-i]) {
			if !utf8.FullRune(content[len(content)-i:]) {
				return content[:len(content)-i]
			}
			break
		}
	}
	return content
}

func resolveMachinePath(name string) (string, error) {
	resolved, err := processcmd.ExpandHomeRelativePath(name)
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return resolved, nil
}

func openRegularFile(name string) (*os.File, fs.FileInfo, error) {
	file, err := openTransferFile(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	switch {
	case err != nil:
	case info.IsDir():
		err = &daemonprotocol.FileTransferError{
			Code: "not_a_file", Message: "path is a directory; list its contents and choose a file",
		}
	case !info.Mode().IsRegular():
		err = errors.New("path must be a regular file")
	case info.Size() > daemonprotocol.MaxFileTransferBytes:
		err = errors.New("file exceeds the 10 MiB limit")
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func readRegularFile(name string) ([]byte, error) {
	file, _, err := openRegularFile(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, daemonprotocol.MaxFileTransferBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > daemonprotocol.MaxFileTransferBytes {
		return nil, errors.New("file exceeds the 10 MiB limit")
	}
	return content, nil
}

const maxSymlinkHops = 40

// resolveSymlinkTarget follows symlinks so writes replace the linked file, not the link.
// Unlike filepath.EvalSymlinks it also follows dangling links, which a write then creates.
func resolveSymlinkTarget(name string) (string, error) {
	for range maxSymlinkHops {
		info, err := os.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return name, nil
		}
		target, err := os.Readlink(name)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			dir, err := filepath.EvalSymlinks(filepath.Dir(name))
			if err != nil {
				return "", err
			}
			target = filepath.Join(dir, target)
		}
		name = target
	}
	return "", errors.New("path has too many levels of symbolic links")
}

func writeMachineFile(operation processcmd.FileOperation) (any, error) {
	name, err := resolveMachinePath(operation.Path)
	if err != nil {
		return nil, err
	}
	if name, err = resolveSymlinkTarget(name); err != nil {
		return nil, err
	}
	content := []byte(*operation.Content)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return nil, fmt.Errorf("create parent directories: %w", err)
	}
	if operation.Append {
		err = appendMachineFile(name, content)
	} else {
		err = replaceMachineFile(name, content)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": operation.Path}, nil
}

func appendMachineFile(name string, content []byte) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("path must be a regular file")
	}
	if info.Size()+int64(len(content)) > daemonprotocol.MaxFileTransferBytes {
		return errors.New("file content must be at most 10 MiB")
	}
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return file.Close()
}

func replaceMachineFile(name string, content []byte) error {
	perm := fs.FileMode(0o644)
	info, err := os.Stat(name)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return errors.New("path must be a regular file")
		}
		perm = info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if len(content) > daemonprotocol.MaxFileTransferBytes {
		return errors.New("file content must be at most 10 MiB")
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".omnara-file-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if err := temporary.Chmod(perm); err != nil {
		return fmt.Errorf("set file permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(temporary.Name(), name); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}

// globRoot splits a glob into its literal base and pattern; glob is empty for a plain path.
func globRoot(pattern string) (base, root, glob string, err error) {
	base = pattern
	if strings.ContainsAny(pattern, "*?") {
		base, glob = doublestar.SplitPattern(filepath.ToSlash(pattern))
		if !doublestar.ValidatePattern(glob) {
			return "", "", "", doublestar.ErrBadPattern
		}
	}
	root, err = resolveMachinePath(base)
	return base, root, glob, err
}

func walkGlob(ctx context.Context, root, glob, after string, visit func(string, fs.DirEntry) error) error {
	segments := strings.Split(glob, "/")
	afterSegments := strings.Split(after, "/")
	braces := strings.Contains(glob, "{")
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
		descend := entry.IsDir() && (braces || mayContainMatches(segments, name))
		if after != "" && slices.Compare(strings.Split(name, "/"), afterSegments) <= 0 {
			if entry.IsDir() && !(descend && strings.HasPrefix(after+"/", name+"/")) {
				return fs.SkipDir
			}
			return nil
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

func mayContainMatches(segments []string, dir string) bool {
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
		if entry, ok := listedEntry(operation.Path, root); ok && operation.After == "" {
			entries = append(entries, entry)
		}
		return map[string]any{"entries": entries, "next_cursor": nil}, nil
	}
	size, last := 0, ""
	err = walkGlob(ctx, root, glob, operation.After, func(name string, _ fs.DirEntry) error {
		entry, ok := listedEntry(path.Join(base, name), filepath.Join(root, filepath.FromSlash(name)))
		if !ok {
			return nil
		}
		// Stop at the first match past a full page so next_cursor means more entries exist.
		if len(entries) == operation.Limit {
			return errFileOperationFull
		}
		if size += len(entry.Path) + 64; size > maxListResultBytes {
			return errFileOperationBound
		}
		entries, last = append(entries, entry), name
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

func listedEntry(display, name string) (listing.FileEntry, bool) {
	info, err := os.Stat(name)
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
		content, err := readRegularFile(root)
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
		return search(path.Join(base, name), content)
	})
	if errors.Is(err, errFileOperationBound) {
		collector.MarkIncomplete("search resource limit reached; narrow the search")
		err = nil
	}
	return collector.Finish(err)
}
