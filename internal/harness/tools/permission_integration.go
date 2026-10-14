//go:build integration

package tools

import (
	"context"

	"github.com/omnara-ai/omnara/internal/model"
)

func (e Executor) PrepareToolCallPermission(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
) error {
	proposal, err := e.recordedToolCall(ctx, turn, call)
	if err != nil {
		return err
	}
	_, err = e.PrepareRecordedToolCallPermission(ctx, turn, proposal)
	return err
}
