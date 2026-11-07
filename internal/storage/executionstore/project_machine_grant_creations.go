package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Creation receipts own idempotency keys independently of authorization edges.
// Reserve before taking the target's lifecycle lock. An existing receipt is read
// without a row lock: revocation takes the lifecycle lock before its FK update.
// The caller rereads a replay's grant under that lifecycle lock before returning.
//
// Fingerprints identify the normalized intent's JSONB text: object key order and
// whitespace disappear, but numeric scale is retained. Changing the intent's
// fields or normalization requires an explicit policy for existing receipts;
// their original request bodies cannot be recovered from the stored digests.
func reserveProjectMachineGrantCreation(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateProjectMachineGrantInput,
	metadata json.RawMessage,
) (uuid.UUID, error) {
	if input.IdempotencyKey == "" {
		return uuid.Nil, nil
	}
	request, err := json.Marshal(struct {
		MachineID   uuid.UUID       `json:"machine_id"`
		Description string          `json:"description"`
		Metadata    json.RawMessage `json:"metadata"`
	}{input.MachineID, input.Description, metadata})
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode machine grant create intent: %w", err)
	}
	_, err = qtx.ReserveProjectMachineGrantCreation(ctx, dbsqlc.ReserveProjectMachineGrantCreationParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, Request: request,
	})
	if err == nil {
		return uuid.Nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("reserve machine grant creation: %w", err)
	}
	receipt, err := qtx.GetProjectMachineGrantCreation(ctx, dbsqlc.GetProjectMachineGrantCreationParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, Request: request,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("get machine grant creation: %w", err)
	}
	if !receipt.MatchesRequest || receipt.GrantID == nil {
		return uuid.Nil, storeerr.ErrIdempotencyConflict
	}
	return *receipt.GrantID, nil
}

func reserveProjectMachinePoolGrantCreation(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateProjectMachinePoolGrantInput,
	config projectMachinePoolGrantConfig,
	metadata json.RawMessage,
) (uuid.UUID, error) {
	if input.IdempotencyKey == "" {
		return uuid.Nil, nil
	}
	// Secret environment overlays contain only UUID references (or null removals),
	// validated by machineEnvironmentOverlayFromColumns before this point.
	request, err := json.Marshal(struct {
		projectMachinePoolGrantConfig
		MachinePoolID uuid.UUID       `json:"machine_pool_id"`
		Description   string          `json:"description"`
		Metadata      json.RawMessage `json:"metadata"`
	}{config, input.MachinePoolID, input.Description, metadata})
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode machine pool grant create intent: %w", err)
	}
	_, err = qtx.ReserveProjectMachinePoolGrantCreation(ctx, dbsqlc.ReserveProjectMachinePoolGrantCreationParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, Request: request,
	})
	if err == nil {
		return uuid.Nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("reserve machine pool grant creation: %w", err)
	}
	receipt, err := qtx.GetProjectMachinePoolGrantCreation(ctx, dbsqlc.GetProjectMachinePoolGrantCreationParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, Request: request,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("get machine pool grant creation: %w", err)
	}
	if !receipt.MatchesRequest || receipt.GrantID == nil {
		return uuid.Nil, storeerr.ErrIdempotencyConflict
	}
	return *receipt.GrantID, nil
}
