package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type writeFileRequest struct {
	Path           string  `json:"path"`
	Content        *string `json:"content,omitempty"`
	Script         *string `json:"script,omitempty"`
	Append         bool    `json:"append,omitempty"`
	ExpectedDigest *string `json:"expected_digest,omitempty"`
}

func validateWriteFileInput(raw json.RawMessage) error {
	_, err := resolveWriteFileRequest(raw)
	return err
}

func resolveWriteFileRequest(raw json.RawMessage) (writeFileRequest, error) {
	var input writeFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "write_file request"); err != nil {
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
	if input.Script != nil && len(*input.Script) > 64*1024 {
		return input, errors.New("script must be at most 64 KiB")
	}
	if input.ExpectedDigest != nil {
		if err := daemonprotocol.ValidateFileDigest(*input.ExpectedDigest); err != nil {
			return input, err
		}
	}
	return input, nil
}

func runWriteFileAsync(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	input, err := resolveWriteFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
	var content []byte
	if input.Content != nil {
		content = []byte(*input.Content)
	}
	if input.Append || input.Script != nil {
		digest, current, err := memories.Read(ctx, scope, store.ID, path)
		if errors.Is(err, storeerr.ErrNotFound) && input.Append && input.ExpectedDigest == nil {
			current = nil
		} else if err != nil {
			return nil, err
		} else if input.ExpectedDigest == nil {
			return nil, fmt.Errorf("expected_digest is required to change an existing file: %w", storeerr.ErrConflict)
		} else if *input.ExpectedDigest != digest {
			return nil, fmt.Errorf("memory changed; read it and retry: %w", storeerr.ErrConflict)
		}
		if !utf8.Valid(current) || bytes.IndexByte(current, 0) >= 0 {
			return nil, errors.New("file must contain UTF-8 text without NUL bytes")
		}
		if input.Append {
			if len(current)+len(content) > daemonprotocol.MaxFileTransferBytes {
				return nil, errors.New("memory content must be at most 10 MiB")
			}
			content = append(current, content...)
		} else {
			content, err = editFileText(ctx, current, *input.Script)
			if err != nil {
				return nil, err
			}
		}
	}
	result, err := memories.Write(ctx, memorystore.WriteInput{
		Scope: scope, StoreID: store.ID, Path: path, Content: content, ExpectedDigest: input.ExpectedDigest,
	})
	if err != nil {
		return nil, err
	}
	return completeFileTool(map[string]any{"path": result.Path, "digest": result.Digest})
}

func editFileText(ctx context.Context, content []byte, script string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command, err := newFileExecCommand(ctx, "sed", nil, "--sandbox", "-E", "-e", script, "--", "-")
	if err != nil {
		return nil, fmt.Errorf("start script execution: %w", err)
	}
	command.Stdin = bytes.NewReader(content)
	command.Env = []string{"LANG=C.UTF-8"}
	command.WaitDelay = time.Second
	var stderr boundedStderrBuffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare script execution: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start script execution: %w", err)
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
		return nil, fmt.Errorf("script execution timed out: %w", ctx.Err())
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("script execution canceled: %w", ctx.Err())
	}
	if waitErr != nil {
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
