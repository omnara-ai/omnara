package tools

import (
	"context"
	"errors"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (e Executor) completeAsyncAppPost(
	ctx context.Context,
	call asyncToolContext,
	content toolResultContent,
	follow executionstore.ConfirmedAppFollow,
) error {
	parts, err := content.contentParts()
	if err != nil {
		return err
	}
	err = retryAsyncToolPersistence(ctx, func(ctx context.Context) error {
		_, err := e.Store.Execution().CompleteAppPostToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
			ProjectID:          call.Turn.ProjectID,
			AgentID:            call.Turn.AgentID,
			RuntimeLockID:      call.Turn.RuntimeLockID,
			ID:                 call.ToolCallID,
			Outcome:            executionstore.ToolResultOutcomeSucceeded,
			ResultContentParts: parts,
		}, follow)
		return err
	})
	if err == nil {
		return nil
	}
	// The provider already confirmed publication. Never repeat it to recover a
	// failed local follow. Preserve the post receipt in the model-visible error.
	failure, marshalErr := structuredToolResultContent(map[string]any{
		"code":        "follow_registration_failed",
		"message":     "The message was posted, but listening for replies could not be confirmed. Do not resend the message to repair this.",
		"post_result": parts,
	})
	if marshalErr != nil {
		return errors.Join(err, marshalErr)
	}
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncToolCompletionTimeout)
	defer cancel()
	return e.completeAsyncToolFailure(completionCtx, call.Turn, call.ToolCallID, failure, err)
}
