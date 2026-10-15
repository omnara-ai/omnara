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

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const maxWriteFileScriptBytes = 64 * 1024

// Machine writes carry their content in the process offer, which must fit one daemon message.
const maxMachineWriteBytes = 512 * 1024

type writeFileRequest struct {
	Path      string          `json:"path"`
	Content   *string         `json:"content,omitempty"`
	Script    *string         `json:"script,omitempty"`
	Append    bool            `json:"append,omitempty"`
	Force     bool            `json:"force,omitempty"`
	MachineID json.RawMessage `json:"machine_id,omitempty"`

	machineID uuid.UUID
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
	var err error
	if input.machineID, err = resolveOptionalMachineID(input.MachineID); err != nil {
		return input, err
	}
	if onMachine(input.Path, input.machineID) {
		if err := validateMachinePath(input.Path); err != nil {
			return input, err
		}
		if input.Script != nil {
			return input, errors.New(
				"script only applies to memory files; write machine files with content, or edit them with run_command",
			)
		}
		if input.Content != nil && len(*input.Content) > maxMachineWriteBytes {
			return input, errors.New("machine file content must be at most 512 KiB")
		}
	} else if _, _, err := memorystore.ParsePath(input.Path); err != nil {
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
	if input.Script != nil && len(*input.Script) > maxWriteFileScriptBytes {
		return input, errors.New("script must be at most 64 KiB")
	}
	return input, nil
}

func machineWriteFile(raw json.RawMessage) (machineFileCall, bool, error) {
	input, err := resolveWriteFileRequest(raw)
	if err != nil || !onMachine(input.Path, input.machineID) {
		return machineFileCall{}, false, err
	}
	return machineFileCall{machineID: input.machineID, kind: processcmd.KindFileWrite, operation: processcmd.FileOperation{
		Path: input.Path, Content: input.Content, Append: input.Append,
	}}, true, nil
}

func runWriteFileAsync(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	input, err := resolveWriteFileRequest(call.Call.Input)
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
	write := memorystore.WriteInput{Scope: scope, StoreID: store.ID, Path: path, Force: input.Force}
	var seen memorystore.FileView
	var found bool
	if !input.Force {
		if seen, found, err = memories.LastSeen(ctx, call.Turn.AgentID, input.Path); err != nil {
			return nil, err
		}
		if found {
			write.ExpectedDigest = &seen.Digest
		}
	}
	unseen := func() error {
		if !found {
			return storeerr.Tag(&storeerr.FileContentConflictError{},
				fmt.Errorf("%s exists but you have not read it; read it before writing", input.Path))
		}
		modified, err := memories.ModifiedAt(ctx, scope, store.ID, path)
		if err != nil {
			return err
		}
		updated, last := memoryChangeTimes(modified, seen.At)
		return storeerr.Tag(&storeerr.FileContentConflictError{}, fmt.Errorf(
			"%s was updated at %s after your last read at %s; read it again before writing", input.Path, updated, last,
		))
	}
	var content []byte
	if input.Content != nil {
		content = []byte(*input.Content)
	}
	if input.Append || input.Script != nil {
		digest, current, err := memories.Read(ctx, scope, store.ID, path)
		if errors.Is(err, storeerr.ErrNotFound) && input.Append {
			write.ExpectedDigest = nil
		} else if err != nil {
			return nil, err
		} else if !input.Force && (!found || seen.Digest != digest) {
			return nil, unseen()
		} else {
			write.ExpectedDigest = &digest
		}
		if !fileread.IsText(current) {
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
	write.Content = content
	result, err := memories.Write(ctx, write)
	var conflict *storeerr.FileContentConflictError
	if errors.As(err, &conflict) && conflict.CurrentDigest == "" && input.Script == nil && !input.Append {
		write.ExpectedDigest = nil
		result, err = memories.Write(ctx, write)
	}
	if !input.Force && (errors.Is(err, storeerr.ErrExpectedDigestRequired) ||
		errors.As(err, &conflict) && conflict.CurrentDigest != "") {
		return nil, unseen()
	}
	if err != nil {
		return nil, err
	}
	return completeFileTool(map[string]any{"path": result.Path, "digest": result.Digest})
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
	if !fileread.IsText(output) {
		return nil, errors.New("result must be UTF-8 text without NUL bytes")
	}
	return output, nil
}
