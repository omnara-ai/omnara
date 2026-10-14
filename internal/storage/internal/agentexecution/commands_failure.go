package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ModelEvidence struct {
	APIFormat  modelprotocol.APIFormat
	APIVariant modelprotocol.APIVariant
	RequestID  string
	ResponseID string
	Usage      modelenvelope.Usage
	Cost       modelenvelope.ProviderReportedCostUSD
	Metadata   modelenvelope.ProviderMetadata
}

type ModelFailure struct {
	ContextID     uuid.UUID
	RuntimeLockID uuid.UUID
	Recovery      RecoveryKind
	RetryDelay    time.Duration
	ErrorKind     modelprotocol.ErrorKind
	ErrorCode     string
	ErrorMessage  string
	ErrorDetails  json.RawMessage
	ServedModel   string
	Evidence      ModelEvidence
}

func normalizedUsage(usage modelenvelope.Usage) modelenvelope.Usage {
	usage = modelenvelope.NormalizeUsage(usage)
	for _, n := range []int{usage.InputTokens,
		usage.UncachedInputTokens,
		usage.CacheReadTokens,
		usage.CacheWriteTokens,
		usage.OutputTokens,
		usage.ReasoningTokens} {
		if n > math.MaxInt32 {
			return modelenvelope.Usage{}
		}
	}
	return usage
}

func tokenCount(n int) *int32 {
	if n <= 0 {
		return nil
	}
	value := int32(n)
	return &value
}

func evidenceParams(e ModelEvidence) (executiondb.FinishExecutionAttemptParams, error) {
	if err := modelenvelope.ValidateProviderReportedCostUSD(e.Cost); err != nil {
		return executiondb.FinishExecutionAttemptParams{}, err
	}
	if (e.APIFormat == "") != (e.APIVariant == "") ||
		e.APIFormat == "" &&
			(e.RequestID != "" || e.ResponseID != "" || e.Cost != "" || e.Usage != (modelenvelope.Usage{})) {
		return executiondb.FinishExecutionAttemptParams{}, errors.New(
			"provider evidence requires API identity",
		)
	}
	metadata, err := json.Marshal(e.Metadata)
	if err != nil {
		return executiondb.FinishExecutionAttemptParams{}, err
	}
	usage := normalizedUsage(e.Usage)
	return executiondb.FinishExecutionAttemptParams{
		ApiFormat:      string(e.APIFormat),
		ApiVariant:     string(e.APIVariant),
		RequestID:      e.RequestID,
		ResponseID:     e.ResponseID,
		InputTokens:    tokenCount(usage.InputTokens),
		UncachedTokens: tokenCount(usage.UncachedInputTokens),
		CacheReadTokens: tokenCount(
			usage.CacheReadTokens,
		),
		CacheWriteTokens: tokenCount(usage.CacheWriteTokens),
		OutputTokens:     tokenCount(usage.OutputTokens),
		ReasoningTokens:  tokenCount(usage.ReasoningTokens),
		Cost:             storeutil.TextFromEmpty(string(e.Cost)),
		ProviderMetadata: metadata,
		ErrorDetails:     json.RawMessage(`{}`),
	}, nil
}

func (h *Handle) finishAttempt(
	ctx context.Context,
	m *executionMutation,
	row executiondb.ReadExecutionAttemptRow,
	state ContextState,
	failure ModelFailure,
	evidence ModelEvidence,
) (bool, error) {
	if row.RuntimeLockID != failure.RuntimeLockID {
		return false, storeerr.ErrRuntimeLockInactive
	}
	if row.State != "started" {
		if row.State != string(state) {
			return false, storeerr.ErrIdempotencyConflict
		}
		return false, nil
	}
	args, err := evidenceParams(evidence)
	if err != nil {
		return false, err
	}
	details, err := objectJSON(failure.ErrorDetails)
	if err != nil {
		return false, err
	}
	if failure.Recovery == RecoveryRetry &&
		(row.AttemptNumber > executionpolicy.MaxModelCallRetriesPerOperation || failure.RetryDelay < 0) {
		return false, storeerr.ErrStateTransitionConflict
	}
	if failure.Recovery == RecoveryRetry {
		delay := failure.RetryDelay.Microseconds()
		args.RetryMicroseconds = &delay
	}
	args.State = string(state)
	args.Recovery = storeutil.TextFromEmpty(string(failure.Recovery))
	args.ErrorKind = string(failure.ErrorKind)
	args.ErrorCode = failure.ErrorCode
	args.ErrorMessage = failure.ErrorMessage
	args.ErrorDetails = details
	args.AgentID = h.route.AgentID
	args.ID = row.ID
	args.RuntimeID = failure.RuntimeLockID
	n, err := executiondb.New().FinishExecutionAttempt(ctx, h.unit.DB(), args)
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, storeerr.ErrRuntimeLockInactive
	}
	m.changed()
	return true, nil
}

func (h *Handle) FailModel(ctx context.Context, input ModelFailure) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		if input.ErrorKind == "" || input.ErrorMessage == "" ||
			input.Recovery != RecoveryRetry && input.Recovery != RecoveryNone {
			return false, errors.New("model failure requires error and retry or terminal disposition")
		}
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return false, err
		}
		if input.Recovery == RecoveryNone {
			return h.terminalFailure(ctx, m, input)
		}
		row, err := executiondb.New().
			ReadExecutionAttempt(ctx,
				h.unit.DB(),
				executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID,
					ID: input.ContextID})
		if err != nil {
			return false, err
		}
		return h.finishAttempt(ctx, m, row, ContextFailed, input, input.Evidence)
	})
}

func (h *Handle) terminalFailure(
	ctx context.Context,
	m *executionMutation,
	input ModelFailure,
) (bool, error) {
	q := executiondb.New()
	row, err := q.ReadExecutionAttempt(
		ctx,
		h.unit.DB(),
		executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: input.ContextID},
	)
	if err != nil {
		return false, err
	}
	snapshot, err := h.LoadExecution(ctx)
	if err != nil {
		return false, err
	}
	changed, err := h.finishAttempt(ctx, m, row, ContextFailed, input, input.Evidence)
	if err != nil {
		return false, err
	}
	return h.publishTerminalFailure(ctx, m, input, row, snapshot, changed)
}

func (h *Handle) publishTerminalFailure(ctx context.Context, m *executionMutation, input ModelFailure,
	row executiondb.ReadExecutionAttemptRow, snapshot ExecutionSnapshot, changed bool) (bool, error) {
	q := executiondb.New()
	parts := []Content{{Kind: "error", Text: input.ErrorMessage}}
	if !changed {
		return false, nil
	}
	output, err := q.CreateExecutionOutput(ctx, h.unit.DB(), executiondb.CreateExecutionOutputParams{
		AgentID: h.route.AgentID, ContextID: input.ContextID, StopReason: "error", ServedModel: input.ServedModel})
	if err != nil {
		return false, err
	}
	event, err := h.appendEvent(ctx, row.TurnID, "model_output", uuid.Nil, output.ID, uuid.Nil, false)
	if err != nil {
		return false, err
	}
	if err = h.writeContent(ctx, "model_output", output.ID, parts); err != nil {
		return false, err
	}
	if err = h.advanceTurn(ctx, event, true); err != nil {
		return false, err
	}
	sourceContext := attemptRecord(row).Context
	sourceContext.State = ContextFailed
	if err = applyAcceptedOutput(m,
		snapshot,
		sourceContext,
		output.ID,
		event,
		false,
		false); err != nil {
		return false, err
	}
	if err = h.parentEffect(ctx,
		"failed",
		input.ErrorMessage,
		"model_output:"+output.ID.String(),
		uuid.Nil); err != nil {
		return false, err
	}
	return true, nil
}
