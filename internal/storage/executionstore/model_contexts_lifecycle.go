package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) RecordRetryableModelCallFailure(
	ctx context.Context,
	input RecordRecoverableModelCallFailureInput,
) (ModelCallContextRecord, error) {
	if input.RecoveryKind == "" {
		input.RecoveryKind = ModelCallRecoveryRetry
	}
	if input.RecoveryKind != ModelCallRecoveryRetry {
		return ModelCallContextRecord{}, fmt.Errorf(
			"retryable model call failure requires recovery kind %q",
			ModelCallRecoveryRetry,
		)
	}
	if err := validateRecoverableModelCallFailure(input); err != nil {
		return ModelCallContextRecord{}, err
	}
	var err error
	input.ErrorDetails, err = normalizedJSONObject(input.ErrorDetails, "model call error details")
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	input.Usage = modelUsageForStorage(input.Usage)
	if err := validateModelCallFailureEvidence(
		input.APIFormat,
		input.APIVariant,
		"",
		input.ProviderRequestID,
		input.ProviderResponseID,
		input.Usage != (modelenvelope.Usage{}),
	); err != nil {
		return ModelCallContextRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("begin retryable model call failure: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ModelCallContextRecord{}, err
	}
	currentContext, err := loadModelCallContextByID(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("load retryable model call context: %w", err)
	}
	if currentContext.State != ModelCallContextStarted ||
		currentContext.AttemptNumber > MaxModelCallRetriesPerOperation {
		return ModelCallContextRecord{}, storeerr.ErrStateTransitionConflict
	}
	contextRecord, err := finishModelCallContextTx(ctx, q, finishModelCallContextInput{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		ModelCallContextID: input.ModelCallContextID,
		RuntimeLockID:      input.RuntimeLockID,
		ToState:            ModelCallContextFailed,
		RecoveryKind:       ModelCallRecoveryRetry,
		APIFormat:          input.APIFormat,
		APIVariant:         input.APIVariant,
		ProviderRequestID:  input.ProviderRequestID,
		ProviderResponseID: input.ProviderResponseID,
		ErrorKind:          input.ErrorKind,
		ErrorCode:          input.ErrorCode,
		ErrorMessage:       input.ErrorMessage,
		ErrorDetails:       input.ErrorDetails,
		RetryDelay:         &input.RetryDelay,
		Usage:              input.Usage,
	})
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	if err := q.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		Metadata:  json.RawMessage(`{"reason":"model_call_retry"}`),
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
	}); err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("reconcile model call retry wakeup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("commit retryable model call failure: %w", err)
	}
	return contextRecord, nil
}

func validateRecoverableModelCallFailure(input RecordRecoverableModelCallFailureInput) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ModelCallContextID) ||
		isNilID(input.RuntimeLockID) || input.ErrorKind == "" ||
		input.ErrorMessage == "" {
		return errors.New("project, agent, context, runtime, and error are required")
	}
	if input.RetryDelay < 0 {
		return errors.New("model call retry delay cannot be negative")
	}
	if input.RecoveryKind != ModelCallRecoveryRetry && input.RecoveryKind != ModelCallRecoveryCompact {
		return fmt.Errorf("unsupported model call recovery kind %q", input.RecoveryKind)
	}
	return nil
}

func validateModelCallFailureEvidence(
	apiFormat modelprotocol.APIFormat,
	apiVariant modelprotocol.APIVariant,
	servedProviderModelSlug,
	providerRequestID,
	providerResponseID string,
	hasUsage bool,
) error {
	if (apiFormat == "") != (apiVariant == "") {
		return errors.New("model call API format and variant must be recorded together")
	}
	if apiFormat == "" && (servedProviderModelSlug != "" || providerRequestID != "" ||
		providerResponseID != "" || hasUsage) {
		return errors.New("provider evidence requires a model call API identity")
	}
	return nil
}

type finishModelCallContextInput struct {
	ProjectID          ID
	AgentID            ID
	ModelCallContextID ID
	RuntimeLockID      ID
	ToState            ModelCallState
	RecoveryKind       ModelCallRecoveryKind
	APIFormat          modelprotocol.APIFormat
	APIVariant         modelprotocol.APIVariant
	ProviderRequestID  string
	ProviderResponseID string
	ErrorKind          modelprotocol.ErrorKind
	ErrorCode          string
	ErrorMessage       string
	ErrorDetails       json.RawMessage
	RetryDelay         *time.Duration
	Usage              modelenvelope.Usage
}

type modelCallContextRuntimeAuthority uint8

const (
	modelCallContextRuntimeOwned modelCallContextRuntimeAuthority = iota
	modelCallContextRuntimeTeardown
)

func (a modelCallContextRuntimeAuthority) allowsInactiveRuntimeLockForTeardown() (bool, error) {
	switch a {
	case modelCallContextRuntimeOwned:
		return false, nil
	case modelCallContextRuntimeTeardown:
		return true, nil
	default:
		return false, fmt.Errorf("unsupported model call context runtime authority %d", a)
	}
}

func finishModelCallContextWithAuthorityTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input finishModelCallContextInput,
	authority modelCallContextRuntimeAuthority,
) (ModelCallContextRecord, error) {
	allowInactiveRuntimeLockForTeardown, err := authority.allowsInactiveRuntimeLockForTeardown()
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	usage := usageColumnsFromModelUsage(input.Usage)
	var retryDelayMicroseconds *int64
	if input.RetryDelay != nil {
		if *input.RetryDelay < 0 {
			return ModelCallContextRecord{}, errors.New("model call retry delay cannot be negative")
		}
		delay := input.RetryDelay.Microseconds()
		retryDelayMicroseconds = &delay
	}
	id, err := q.FinishModelCallContext(ctx, dbsqlc.FinishModelCallContextParams{
		ToState:                             string(input.ToState),
		RecoveryKind:                        sqlcTextFromEmpty(string(input.RecoveryKind)),
		ApiFormat:                           string(input.APIFormat),
		ApiVariant:                          string(input.APIVariant),
		ProviderRequestID:                   input.ProviderRequestID,
		ProviderResponseID:                  input.ProviderResponseID,
		ErrorKind:                           string(input.ErrorKind),
		ErrorCode:                           input.ErrorCode,
		ErrorMessage:                        input.ErrorMessage,
		ErrorDetails:                        normalizedJSONOrObject(input.ErrorDetails),
		RetryDelayMicroseconds:              retryDelayMicroseconds,
		InputTokensTotal:                    sqlcInt32Ptr(usage.InputTokens),
		UncachedInputTokens:                 sqlcInt32Ptr(usage.UncachedInputTokens),
		CacheReadInputTokens:                sqlcInt32Ptr(usage.CacheReadTokens),
		CacheWriteInputTokens:               sqlcInt32Ptr(usage.CacheWriteTokens),
		OutputTokensTotal:                   sqlcInt32Ptr(usage.OutputTokens),
		ReasoningOutputTokens:               sqlcInt32Ptr(usage.ReasoningTokens),
		ID:                                  input.ModelCallContextID,
		ProjectID:                           input.ProjectID,
		AgentID:                             input.AgentID,
		RuntimeLockID:                       input.RuntimeLockID,
		AllowInactiveRuntimeLockForTeardown: allowInactiveRuntimeLockForTeardown,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if allowInactiveRuntimeLockForTeardown {
			return ModelCallContextRecord{}, storeerr.ErrStateTransitionConflict
		}
		return ModelCallContextRecord{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("finish model call context: %w", err)
	}
	record, err := loadModelCallContextByID(ctx, q, input.ProjectID, input.AgentID, id)
	if err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("load finished model call context: %w", err)
	}
	return record, nil
}

func finishModelCallContextTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input finishModelCallContextInput,
) (ModelCallContextRecord, error) {
	return finishModelCallContextWithAuthorityTx(ctx, q, input, modelCallContextRuntimeOwned)
}

func normalizedJSONOrObject(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || string(value) == "null" {
		return json.RawMessage(`{}`)
	}
	return normalizedJSON(value)
}
