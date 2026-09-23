package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/storage"
)

type listFilesRequest struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit,omitempty"`
}

func validateListFiles(raw json.RawMessage) error {
	var input listFilesRequest
	if err := decodeSingleStrictJSON(raw, &input, "list_files request"); err != nil {
		return err
	}
	_, err := storage.CompileFilePattern(input.Pattern)
	return err
}

func runListFiles(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	var input listFilesRequest
	if err := json.Unmarshal(call.Call.Input, &input); err != nil {
		return nil, err
	}
	result, err := call.Executor.Store.ListFiles(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		input.Pattern,
		input.Limit,
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, errors.New("file listing timed out; narrow the pattern and retry")
		}
		return nil, err
	}
	return completeFileTool(result)
}
