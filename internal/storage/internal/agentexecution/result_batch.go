package agentexecution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (h *Handle) createOutputEvent(ctx context.Context, turn uuid.UUID, input AcceptOutputInput,
	replay *json.RawMessage) (uuid.UUID, ExecutionEvent, error) {
	id := uuid.New()
	batch := factsBatch{DBTX: h.unit.DB()}
	q := executiondb.New()
	output := q.QueueExecutionOutput(ctx, &batch, []executiondb.QueueExecutionOutputParams{{
		ID: id, AgentID: h.route.AgentID, ContextID: input.ContextID, ServedModel: input.Response.ServedProviderModelSlug,
		StopReason: string(input.Response.Normalized.StopReason), Replay: replay,
	}})
	event := ExecutionEvent{ID: uuid.New(), TurnID: turn}
	appended := q.QueueExecutionEvent(ctx, &batch, []executiondb.QueueExecutionEventParams{{
		ID: event.ID, ProjectID: h.route.ProjectID, AgentID: h.route.AgentID, TurnID: &turn, Kind: "model_output",
		OutputID: &id,
	}})
	batch.readers = append(batch.readers, func() {
		output.QueryRow(func(_ int, row executiondb.QueueExecutionOutputRow, err error) {
			if err == nil && row.ID != id {
				err = ErrInvalidState
			}
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, output.Close())
	}, func() {
		appended.QueryRow(func(_ int, row executiondb.QueueExecutionEventRow, err error) {
			event.EventBoundary = EventBoundary{Sequence: row.Sequence, Time: row.CreatedAt}
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, appended.Close())
	})
	if err := batch.read(ctx); err != nil {
		return uuid.Nil, ExecutionEvent{}, err
	}
	h.unit.Notifications().AddAgentEvent(h.route.AgentID, event.Sequence, "model_output")
	return id, event, nil
}

func (h *Handle) queueOutputContent(ctx context.Context, batch *factsBatch, id uuid.UUID, parts PreparedContent) {
	if parts.count == 0 {
		return
	}
	writes := executiondb.New().QueueExecutionContent(ctx, batch, []executiondb.QueueExecutionContentParams{{
		AgentID: h.route.AgentID, ProjectID: h.route.ProjectID, OwnerKind: "model_output", OutputID: &id, Parts: parts.data,
	}})
	batch.readers = append(batch.readers, func() {
		writes.QueryRow(func(_ int, count int64, err error) {
			if err == nil && count != parts.count {
				err = ErrInvalidState
			}
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, writes.Close())
	})
}

func (h *Handle) queueOutputSuccess(ctx context.Context, batch *factsBatch,
	source executiondb.ReadExecutionAttemptRow, evidence ModelEvidence) error {
	args, err := evidenceParams(evidence)
	if err != nil {
		return err
	}
	args.AgentID = h.route.AgentID
	args.ID = source.ID
	args.RuntimeID = source.RuntimeLockID
	args.State = "succeeded"
	args.ErrorDetails = json.RawMessage(`{}`)
	writes := executiondb.New().QueueExecutionSuccess(ctx, batch,
		[]executiondb.QueueExecutionSuccessParams{executiondb.QueueExecutionSuccessParams(args)})
	batch.readers = append(batch.readers, func() {
		writes.QueryRow(func(_ int, id uuid.UUID, err error) {
			if errors.Is(err, pgx.ErrNoRows) || err == nil && id != source.ID {
				err = storeerr.ErrRuntimeLockInactive
			}
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, writes.Close())
	})
	return nil
}

func (h *Handle) queueOutputRelease(ctx context.Context, batch *factsBatch, id uuid.UUID) error {
	if id == uuid.Nil || h.lockedRuntimeID != id {
		return storeerr.ErrRuntimeLockInactive
	}
	writes := executiondb.New().QueueExecutionRelease(ctx, batch,
		[]executiondb.QueueExecutionReleaseParams{{AgentID: h.route.AgentID, ID: id}})
	batch.readers = append(batch.readers, func() {
		writes.QueryRow(func(_ int, released uuid.UUID, err error) {
			if errors.Is(err, pgx.ErrNoRows) || err == nil && released != id {
				err = storeerr.ErrRuntimeLockInactive
			}
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, writes.Close())
	})
	return nil
}
