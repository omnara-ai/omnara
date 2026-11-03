package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const maxStorageWriteScriptBytes = 64 * 1024

type storageWriteRequest struct {
	Path    string  `json:"path"`
	Content *string `json:"content,omitempty"`
	Script  *string `json:"script,omitempty"`
	Append  bool    `json:"append,omitempty"`
	Force   bool    `json:"force,omitempty"`
}

func validateStorageWriteInput(raw json.RawMessage) error {
	_, err := resolveStorageWriteRequest(raw)
	return err
}

func resolveStorageWriteRequest(raw json.RawMessage) (storageWriteRequest, error) {
	var input storageWriteRequest
	if err := decodeSingleStrictJSON(raw, &input, "storage_write request"); err != nil {
		return input, err
	}
	if _, _, err := memorystore.ParsePath(input.Path); err != nil {
		return input, err
	}
	if (input.Content == nil) == (input.Script == nil) {
		return input, errors.New("supply exactly one of content or script")
	}
	if input.Append && input.Script != nil {
		return input, errors.New("append only applies to content")
	}
	for _, value := range []*string{input.Content, input.Script} {
		if value != nil && (!utf8.ValidString(*value) || strings.ContainsRune(*value, 0)) {
			return input, errors.New("content and script must be UTF-8 text without NUL bytes")
		}
	}
	if input.Content != nil && len(*input.Content) > daemonprotocol.MaxFileTransferBytes {
		return input, errors.New("memory content must be at most 10 MiB")
	}
	if input.Script != nil && len(*input.Script) > maxStorageWriteScriptBytes {
		return input, errors.New("script must be at most 64 KiB")
	}
	return input, nil
}

func runStorageWriteAsync(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	input, err := resolveStorageWriteRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeoutCause(ctx, 10*time.Second, errors.New("simplify the script and retry"))
	defer cancel()
	name, path, _ := memorystore.ParsePath(input.Path)
	memories := call.Executor.Store.Memories()
	store, err := memories.Resolve(ctx, call.Turn.ProjectID, name)
	if err != nil {
		return nil, err
	}
	scope := memorystore.Scope{
		OrgID: call.Turn.OrgID, ProjectID: call.Turn.ProjectID, AgentID: call.Turn.AgentID,
	}
	write := memorystore.WriteInput{Scope: scope, StoreID: store.ID, Path: path}
	current, err := memories.ReadFile(ctx, scope, store.ID, path)
	exists := err == nil
	if errors.Is(err, storeerr.ErrNotFound) && input.Script == nil {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	var content []byte
	if input.Content != nil {
		content = []byte(*input.Content)
	}
	if exists {
		write.ExpectedDigest = &current.Digest
		changes := input.Script != nil || input.Append || blobstore.ContentDigest(content) != current.Digest
		if changes && !input.Force {
			if err := checkMemoryFileSeen(ctx, call, input.Path, current); err != nil {
				return nil, err
			}
		}
	}
	if input.Append || input.Script != nil {
		if !utf8.Valid(current.Content) || bytes.IndexByte(current.Content, 0) >= 0 {
			return nil, errors.New("file must contain UTF-8 text without NUL bytes")
		}
		if input.Append {
			if len(current.Content)+len(content) > daemonprotocol.MaxFileTransferBytes {
				return nil, errors.New("memory content must be at most 10 MiB")
			}
			content = append(current.Content, content...)
		} else if content, err = editFileText(ctx, current.Content, *input.Script); err != nil {
			return nil, err
		}
	}
	write.Content = content
	result, err := memories.Write(ctx, write)
	var conflict *storeerr.FileContentConflictError
	if errors.As(err, &conflict) {
		return nil, storeerr.Tag(&storeerr.FileContentConflictError{},
			fmt.Errorf("%s changed while writing; read it again before writing", input.Path))
	}
	if err != nil {
		return nil, err
	}
	part, err := structuredToolResultPart(map[string]any{"path": result.Path})
	if err != nil {
		return nil, err
	}
	digestPart, err := fileDigestToolResultPart(result.Digest)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(newToolResultContent(part, digestPart)), nil
}

// checkMemoryFileSeen rejects changes to a file the agent has not read since its last change.
func checkMemoryFileSeen(ctx context.Context, call asyncToolContext, path string, current memorystore.File) error {
	seen, found, err := call.Executor.Store.Memories().LastSeen(ctx, call.Turn.AgentID, path)
	if err != nil {
		return err
	}
	if !found {
		return storeerr.Tag(&storeerr.FileContentConflictError{},
			fmt.Errorf("%s exists but you have not read it; read it before writing", path))
	}
	if seen.Digest == current.Digest {
		return nil
	}
	updated, last := memoryChangeTimes(current.ModifiedAt, seen.At)
	return storeerr.Tag(&storeerr.FileContentConflictError{}, fmt.Errorf(
		"%s was updated at %s after your last read at %s; read it again before writing", path, updated, last,
	))
}

func editFileText(ctx context.Context, content []byte, script string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, "omnara-chroot-sandbox", script)
	command.Stdin = bytes.NewReader(content)
	command.Env = []string{}
	command.WaitDelay = time.Second
	var stderr boundedStderrBuffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fileToolUnavailable(ctx, scriptEditUnavailableMessage, err)
	}
	if err := command.Start(); err != nil {
		return nil, fileToolUnavailable(ctx, scriptEditUnavailableMessage, err)
	}
	output, err := io.ReadAll(io.LimitReader(stdout, daemonprotocol.MaxFileTransferBytes+1))
	if err == nil && len(output) > daemonprotocol.MaxFileTransferBytes {
		err = errors.New("memory content must be at most 10 MiB")
	}
	if err != nil {
		cancel()
		_ = stdout.Close()
	}
	waitErr := command.Wait()
	if err != nil {
		return nil, err
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("script execution timed out: %w", context.Cause(ctx))
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("script execution canceled: %w", ctx.Err())
	}
	if waitErr != nil {
		if sandboxSetupFailed(waitErr, stderr.data) {
			return nil, fileToolUnavailable(ctx, scriptEditUnavailableMessage, fmt.Errorf("%s: %w", stderr.data, waitErr))
		}
		if diagnostic := strings.TrimSpace(string(stderr.data)); diagnostic != "" {
			return nil, fmt.Errorf("script execution failed: %s (%w)", diagnostic, waitErr)
		}
		return nil, fmt.Errorf("script execution failed: %w", waitErr)
	}
	if !utf8.Valid(output) || bytes.IndexByte(output, 0) >= 0 {
		return nil, errors.New("result must be UTF-8 text without NUL bytes")
	}
	return output, nil
}
