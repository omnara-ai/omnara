package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type runtimeModelCallRecoveryEvidence struct {
	Code    string
	Message string
}

func recoverRuntimeModelCallContextTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeLockID uuid.UUID,
	evidence runtimeModelCallRecoveryEvidence,
	retryBackoff func(int, string) time.Duration,
) error {
	contextID, err := qtx.GetLiveModelCallContextForRuntime(
		ctx,
		dbsqlc.GetLiveModelCallContextForRuntimeParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			RuntimeLockID: runtimeLockID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load live model call context for runtime recovery: %w", err)
	}
	contextRecord, err := loadModelCallContextByID(ctx, qtx, projectID, agentID, contextID)
	if err != nil {
		return fmt.Errorf("load live model call context for runtime recovery: %w", err)
	}
	details, err := marshalJSON(map[string]any{
		"attempt_number":    contextRecord.AttemptNumber,
		"code":              evidence.Code,
		"message":           evidence.Message,
		"outcome_ambiguous": true,
		"runtime_lock_id":   runtimeLockID,
	})
	if err != nil {
		return fmt.Errorf("marshal runtime recovery error: %w", err)
	}
	recovery, err := getModelCallRecoveryStateTx(ctx, qtx, projectID, agentID, contextRecord.ID)
	if err != nil {
		return err
	}
	retryCount := recovery.NormalRetryCount
	if contextRecord.OperationKind == ModelCallOperationCompaction {
		retryCount = recovery.CompactionRetryCount
	}
	if contextRecord.OperationKind == ModelCallOperationCompaction &&
		(recovery.ParentRecoveryKind == ModelCallRecoveryCompactOptional ||
			(contextRecord.ReplacesCheckpointID != uuid.Nil && retryCount >= MaxModelCallRetriesPerOperation)) {
		var outcome OptionalCompactionOutcome
		if recovery.ParentRecoveryKind == ModelCallRecoveryCompactOptional {
			outcome = OptionalCompactionInterrupted
		}
		_, err := recordCompactionFailureAndResumeNormalTx(ctx, qtx, RecordCompactionFailureAndResumeNormalInput{
			Outcome:            outcome,
			ProjectID:          projectID,
			AgentID:            agentID,
			RuntimeLockID:      runtimeLockID,
			ModelCallContextID: contextRecord.ID,
			ErrorKind:          modelprotocol.ErrorKindRuntime,
			ErrorCode:          evidence.Code,
			ErrorMessage:       evidence.Message,
			ErrorDetails:       details,
		}, modelCallContextRuntimeTeardown)
		return err
	}
	if retryCount >= MaxModelCallRetriesPerOperation {
		return terminalizeExhaustedRuntimeModelCallContextTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			projectID,
			agentID,
			runtimeLockID,
			contextRecord,
			evidence,
			details,
		)
	}
	changed, err := qtx.InterruptRuntimeModelCallContextForRetry(
		ctx,
		dbsqlc.InterruptRuntimeModelCallContextForRetryParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			ID:            contextRecord.ID,
			RuntimeLockID: runtimeLockID,
			ErrorKind:     string(modelprotocol.ErrorKindRuntime),
			ErrorCode:     evidence.Code,
			ErrorMessage:  evidence.Message,
			ErrorDetails:  details,
			RetryDelayMicroseconds: retryBackoff(
				retryCount+1,
				contextRecord.ID.String(),
			).Microseconds(),
		},
	)
	if err != nil {
		return fmt.Errorf("interrupt runtime model call context: %w", err)
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func terminalizeExhaustedRuntimeModelCallContextTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeLockID uuid.UUID,
	contextRecord ModelCallContextRecord,
	evidence runtimeModelCallRecoveryEvidence,
	details json.RawMessage,
) error {
	if contextRecord.ID == uuid.Nil {
		return errors.New("model call context is required for runtime recovery")
	}
	switch contextRecord.OperationKind {
	case ModelCallOperationNormal:
		if _, err := recordTerminalModelCallFailureTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			RecordModelCallErrorAndCompleteContextInput{
				ProjectID:          projectID,
				AgentID:            agentID,
				RuntimeLockID:      runtimeLockID,
				ModelCallContextID: contextRecord.ID,
				ErrorKind:          modelprotocol.ErrorKindRuntime,
				ErrorCode:          evidence.Code,
				ErrorMessage:       evidence.Message,
				ErrorDetails:       details,
			},
			modelCallContextRuntimeTeardown,
			ModelCallOperationNormal,
		); err != nil {
			return fmt.Errorf("terminalize exhausted normal model call: %w", err)
		}
	case ModelCallOperationCompaction:
		if err := recordTerminalCompactionFailureTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			RecordTerminalCompactionFailureInput{
				ProjectID:          projectID,
				AgentID:            agentID,
				RuntimeLockID:      runtimeLockID,
				ModelCallContextID: contextRecord.ID,
				ErrorKind:          modelprotocol.ErrorKindRuntime,
				ErrorCode:          evidence.Code,
				ErrorMessage:       evidence.Message,
				ErrorDetails:       details,
			},
			modelCallContextRuntimeTeardown,
		); err != nil {
			return fmt.Errorf("terminalize exhausted compaction model call: %w", err)
		}
	default:
		return fmt.Errorf("unsupported model call operation %q", contextRecord.OperationKind)
	}
	return nil
}
