package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ClaimNextIntegrationControl reserves one bounded payload. Consecutive failed
// claims saturate the backoff counter; they never exhaust transient work.
func (s *Store) ClaimNextIntegrationControl(
	ctx context.Context, input ClaimNextIntegrationControlInput,
) (IntegrationControlReceipt, bool, error) {
	if input.LeaseDuration < time.Millisecond || input.LeaseDuration > 5*time.Minute {
		return IntegrationControlReceipt{}, false, storeerr.InvalidRequest(
			errors.New("control lease must be at least one millisecond and cannot exceed five minutes"))
	}
	capability, err := normalizedClaimCapability(input.Capability)
	if err != nil {
		return IntegrationControlReceipt{}, false, err
	}
	row, err := s.q.ClaimNextIntegrationControlReceipt(ctx, dbsqlc.ClaimNextIntegrationControlReceiptParams{
		ConnectorKey: capability.ConnectorKey, Provider: capability.Provider,
		LeaseMicroseconds: input.LeaseDuration.Microseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationControlReceipt{}, false, nil
	}
	if err != nil {
		return IntegrationControlReceipt{}, false, fmt.Errorf("claim integration control: %w", err)
	}
	return integrationControlReceiptFromSQLC(row), true, nil
}

// FinishIntegrationControl checkpoints only confirmed forward progress. Retry
// may keep that prefix and wait; yield must advance. No provider calls occur in
// this transaction, and app authority and the exact unexpired lease are rechecked.
func (s *Store) FinishIntegrationControl(
	ctx context.Context, input FinishIntegrationControlInput,
) (IntegrationControlReceipt, error) {
	lastError, err := validateFinishIntegrationControl(input)
	if err != nil {
		return IntegrationControlReceipt{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationControlReceipt{}, fmt.Errorf("begin integration control completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := s.lockIntegrationControlOwner(ctx, tx, input.IntegrationAppID, input.Capabilities); err != nil {
		return IntegrationControlReceipt{}, err
	}
	row, err := s.q.WithTx(tx).FinishIntegrationControlReceipt(ctx, dbsqlc.FinishIntegrationControlReceiptParams{
		IntegrationAppID: input.IntegrationAppID, ID: input.ID, LeaseToken: input.LeaseToken,
		LeaseGeneration: input.LeaseGeneration, Outcome: string(input.Outcome), LastInstallID: input.LastInstallID,
		RetryAfterMicroseconds: min(input.RetryAfter, MaxIntegrationControlRetryAfter).Microseconds(), LastError: lastError,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationControlReceipt{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return IntegrationControlReceipt{}, fmt.Errorf("finish integration control: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationControlReceipt{}, fmt.Errorf("commit integration control completion: %w", err)
	}
	return integrationControlReceiptFromSQLC(row), nil
}

func validateFinishIntegrationControl(input FinishIntegrationControlInput) (json.RawMessage, error) {
	if input.IntegrationAppID == uuid.Nil || input.ID == uuid.Nil || input.LeaseToken == uuid.Nil ||
		input.LeaseGeneration <= 0 {
		return nil, storeerr.InvalidRequest(errors.New("control scope and current lease are required"))
	}
	switch input.Outcome {
	case IntegrationControlCompleted, IntegrationControlYield, IntegrationControlRetry, IntegrationControlFailed:
	default:
		return nil, storeerr.InvalidRequest(errors.New("control outcome must be completed, yield, retry, or failed"))
	}
	if input.RetryAfter < 0 || (input.RetryAfter != 0 && input.Outcome != IntegrationControlRetry) {
		return nil, storeerr.InvalidRequest(errors.New("retry delay must be nonnegative and is only valid for retry"))
	}
	if input.Outcome == IntegrationControlYield && (input.LastInstallID == nil || *input.LastInstallID == uuid.Nil) {
		return nil, storeerr.InvalidRequest(errors.New("yield requires confirmed forward progress"))
	}
	rawError := input.LastError
	if len(rawError) == 0 {
		rawError = json.RawMessage(`{}`)
	}
	lastError, err := normalizeIntegrationControlObject(rawError, MaxIntegrationControlErrorBytes)
	if err != nil {
		return nil, err
	}
	success := input.Outcome == IntegrationControlCompleted || input.Outcome == IntegrationControlYield
	if success != jsoncanonical.Equal(lastError, json.RawMessage(`{}`)) {
		return nil, storeerr.InvalidRequest(errors.New("retry and failed outcomes require an error; success must omit it"))
	}
	return lastError, nil
}
