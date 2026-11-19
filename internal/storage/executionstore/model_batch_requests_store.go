package executionstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// ModelWorkCollect resumes a model call whose provider batch was submitted by
// an earlier claim. The runtime lock was held the whole time.
const ModelWorkCollect ModelWorkKind = "collect"

type ModelBatchRequestRecord struct {
	ID                   uuid.UUID
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	RuntimeLockID        uuid.UUID
	ModelCallContextID   uuid.UUID
	TurnID               uuid.UUID
	OpeningInputIDs      []uuid.UUID
	OpeningEventSequence int64
	CustomID             string
	InputFingerprint     string
	ProviderBatchID      string
	DeadlineAt           *time.Time
	CreatedAt            time.Time
}

type CreateModelBatchRequestInput struct {
	ID                   uuid.UUID
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	RuntimeLockID        uuid.UUID
	ModelCallContextID   uuid.UUID
	TurnID               uuid.UUID
	OpeningInputIDs      []uuid.UUID
	OpeningEventSequence int64
	CallbackTokenHash    []byte
	CustomID             string
	InputFingerprint     string
}

type ParkModelBatchRuntimeInput struct {
	ID              uuid.UUID
	ProjectID       uuid.UUID
	AgentID         uuid.UUID
	RuntimeLockID   uuid.UUID
	ProviderBatchID string
	// HoldFor bounds how long the lock stays parked; only the first park sets it.
	HoldFor   time.Duration
	PollDelay time.Duration
}

func (s *Store) CreateModelBatchRequest(ctx context.Context, input CreateModelBatchRequestInput) error {
	if input.ID == uuid.Nil || input.ModelCallContextID == uuid.Nil || input.TurnID == uuid.Nil ||
		len(input.OpeningInputIDs) == 0 || len(input.CallbackTokenHash) == 0 || input.CustomID == "" {
		return errors.New("model batch request identity, context, turn, opening inputs, and callback token are required")
	}
	_, err := agentexecution.Retry(ctx, "create_model_batch_request",
		func(ctx context.Context) (struct{}, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return struct{}{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			if err := unit.CreateBatchRequest(ctx,
				agentexecution.AgentRoute{ProjectID: input.ProjectID, AgentID: input.AgentID},
				agentexecution.CreateBatchRequestInput{
					ID:                   input.ID,
					RuntimeID:            input.RuntimeLockID,
					ContextID:            input.ModelCallContextID,
					TurnID:               input.TurnID,
					OpeningInputIDs:      input.OpeningInputIDs,
					OpeningEventSequence: input.OpeningEventSequence,
					CallbackTokenHash:    input.CallbackTokenHash,
					CustomID:             input.CustomID,
					InputFingerprint:     input.InputFingerprint,
				}); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, unit.Commit(ctx, "create model batch request")
		})
	return err
}

func (s *Store) GetModelBatchRequest(
	ctx context.Context, projectID, agentID, runtimeLockID, id uuid.UUID,
) (ModelBatchRequestRecord, error) {
	return agentexecution.Retry(ctx, "get_model_batch_request",
		func(ctx context.Context) (ModelBatchRequestRecord, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return ModelBatchRequestRecord{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			request, err := unit.BatchRequest(ctx,
				agentexecution.AgentRoute{ProjectID: projectID, AgentID: agentID}, runtimeLockID, id)
			if err != nil {
				return ModelBatchRequestRecord{}, err
			}
			return modelBatchRequestRecord(request), nil
		})
}

// ParkModelBatchRuntime keeps the runtime lock held without a worker until the
// batch is resumed by its callback, the fallback poll, or the deadline.
func (s *Store) ParkModelBatchRuntime(ctx context.Context, input ParkModelBatchRuntimeInput) (time.Time, error) {
	return agentexecution.Retry(ctx, "park_model_batch_runtime",
		func(ctx context.Context) (time.Time, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return time.Time{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			resumeAt, err := unit.ParkRuntimeForBatch(ctx,
				agentexecution.AgentRoute{ProjectID: input.ProjectID, AgentID: input.AgentID},
				agentexecution.ParkBatchInput{
					ID:              input.ID,
					RuntimeID:       input.RuntimeLockID,
					ProviderBatchID: input.ProviderBatchID,
					HoldFor:         input.HoldFor,
					PollDelay:       input.PollDelay,
				})
			if err != nil {
				return time.Time{}, err
			}
			return resumeAt, unit.Commit(ctx, "park model batch runtime")
		})
}

// ClaimNextParkedModelBatch hands a resumable parked runtime to this worker.
func (s *Store) ClaimNextParkedModelBatch(
	ctx context.Context, input ClaimNextAgentWorkInput,
) (ClaimedAgentWork, bool, error) {
	type claimResult struct {
		work  ClaimedAgentWork
		found bool
	}
	result, err := agentexecution.Retry(ctx, "claim_next_parked_model_batch",
		func(ctx context.Context) (claimResult, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return claimResult{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			request, renewal, err := unit.ClaimParkedBatch(ctx, input.WorkerProcessID, input.LeaseDuration)
			if errors.Is(err, pgx.ErrNoRows) {
				return claimResult{}, nil
			}
			if err != nil {
				return claimResult{}, err
			}
			q := dbsqlc.New(unit.DB())
			agent, err := q.GetAgentInProject(ctx,
				dbsqlc.GetAgentInProjectParams{ProjectID: request.ProjectID, ID: request.AgentID})
			if err != nil {
				return claimResult{}, err
			}
			runtime, err := q.GetAgentRuntimeLockForRelease(ctx, dbsqlc.GetAgentRuntimeLockForReleaseParams{
				ProjectID: request.ProjectID, AgentID: request.AgentID, ID: request.RuntimeID,
			})
			if err != nil {
				return claimResult{}, err
			}
			if err := unit.Commit(ctx, "claim parked model batch"); err != nil {
				return claimResult{}, err
			}
			return claimResult{found: true, work: ClaimedAgentWork{
				LocalLeaseBudgetStartedAt: renewal.LocalStartedAt,
				OrgID:                     agent.OrgID,
				ProjectID:                 request.ProjectID,
				AgentID:                   request.AgentID,
				Kind:                      AgentWorkModel,
				RuntimeLock:               agentRuntimeLockRecordFromSQLC(runtime),
				Model: ClaimedModelWork{
					Kind:                 ModelWorkCollect,
					ModelCallContextID:   request.ContextID,
					BatchRequestID:       request.ID,
					TurnID:               request.TurnID,
					InputIDs:             request.OpeningInputIDs,
					OpeningEventSequence: request.OpeningEventSequence,
				},
			}}, nil
		})
	return result.work, result.found, err
}

// WakeModelBatchRequest marks a batch resumable now. It reports false when the
// request is unknown or the callback token does not match.
func (s *Store) WakeModelBatchRequest(ctx context.Context, id uuid.UUID, callbackTokenHash []byte) (bool, error) {
	if id == uuid.Nil || len(callbackTokenHash) == 0 {
		return false, nil
	}
	return agentexecution.Retry(ctx, "wake_model_batch_request",
		func(ctx context.Context) (bool, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return false, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			woken, err := unit.WakeBatchRequest(ctx, id, callbackTokenHash)
			if err != nil || !woken {
				return false, err
			}
			return true, unit.Commit(ctx, "wake model batch request")
		})
}

// DeleteModelBatchRequest forgets a batch request once its model call has an
// outcome. It is idempotent.
func (s *Store) DeleteModelBatchRequest(ctx context.Context, runtimeLockID, id uuid.UUID) error {
	_, err := agentexecution.Retry(ctx, "delete_model_batch_request",
		func(ctx context.Context) (struct{}, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return struct{}{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			if err := unit.DeleteBatchRequest(ctx, runtimeLockID, id); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, unit.Commit(ctx, "delete model batch request")
		})
	return err
}

// purgeOrphanModelBatchRequests removes batch requests left behind by runtime
// locks that were released or reaped before their model call finished.
func (s *Store) purgeOrphanModelBatchRequests(ctx context.Context) error {
	_, err := agentexecution.Retry(ctx, "purge_orphan_model_batch_requests",
		func(ctx context.Context) (struct{}, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return struct{}{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			if _, err := unit.PurgeOrphanBatchRequests(ctx); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, unit.Commit(ctx, "purge orphan model batch requests")
		})
	return err
}

func modelBatchRequestRecord(request agentexecution.BatchRequest) ModelBatchRequestRecord {
	return ModelBatchRequestRecord{
		ID:                   request.ID,
		ProjectID:            request.ProjectID,
		AgentID:              request.AgentID,
		RuntimeLockID:        request.RuntimeID,
		ModelCallContextID:   request.ContextID,
		TurnID:               request.TurnID,
		OpeningInputIDs:      request.OpeningInputIDs,
		OpeningEventSequence: request.OpeningEventSequence,
		CustomID:             request.CustomID,
		InputFingerprint:     request.InputFingerprint,
		ProviderBatchID:      request.ProviderBatchID,
		DeadlineAt:           request.DeadlineAt,
		CreatedAt:            request.CreatedAt,
	}
}
