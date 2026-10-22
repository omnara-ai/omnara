package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type storageListRequest struct {
	Pattern string `json:"pattern"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func validateStorageList(raw json.RawMessage) error {
	var input storageListRequest
	if err := decodeSingleStrictJSON(raw, &input, "storage_list request"); err != nil {
		return err
	}
	if input.Offset < 0 || input.Offset > toolcatalog.StorageListMaxOffset {
		return fmt.Errorf("offset must be between 0 and %d", toolcatalog.StorageListMaxOffset)
	}
	_, err := storage.CompileFilePattern(input.Pattern)
	return err
}

func runStorageList(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	var input storageListRequest
	if err := json.Unmarshal(call.Call.Input, &input); err != nil {
		return nil, err
	}
	result, err := call.Executor.Store.ListFiles(
		ctx, call.Turn.ProjectID, call.Turn.AgentID, input.Pattern, input.Offset, input.Limit,
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, errors.New("file listing timed out; narrow the pattern and retry")
		}
		return nil, err
	}
	// Digests are bookkeeping for memory read tracking, not something the model acts on.
	for i := range result.Entries {
		result.Entries[i].Digest = ""
	}
	return completeFileTool(listing.NewFilePage(result.Entries, result.HasMore, input.Offset))
}
