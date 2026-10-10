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

type ActivateConfigInput struct {
	ID             uuid.UUID
	ConfigID       uuid.UUID
	ActorID        uuid.UUID
	IdempotencyKey string
	Metadata       json.RawMessage
}

type ActivatedConfig struct {
	InputID uuid.UUID
	Event   ExecutionEvent
	Created bool
}

func (h *Handle) captureOpening(ctx context.Context, turn uuid.UUID, watermark, stop int64) (Opening, error) {
	rows, err := executiondb.New().CaptureExecutionOpening(ctx, h.unit.DB(), executiondb.CaptureExecutionOpeningParams{
		ProjectID: h.route.ProjectID, AgentID: h.route.AgentID, TurnID: turn, Watermark: watermark, StopSequence: stop})
	if err != nil {
		return Opening{}, err
	}
	opening := Opening{InputIDs: make([]uuid.UUID, 0, len(rows))}
	for i, row := range rows {
		opening.InputIDs = append(opening.InputIDs, row.ID)
		if i == 0 {
			opening.EventSequence = row.Sequence
		}
	}
	return opening, nil
}

func (h *Handle) ActivateConfig(ctx context.Context, input ActivateConfigInput) (ActivatedConfig, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (ActivatedConfig, error) {
		if input.ConfigID == uuid.Nil {
			return ActivatedConfig{}, errors.New("config is required")
		}
		metadata, err := objectJSON(input.Metadata)
		if err != nil {
			return ActivatedConfig{}, err
		}
		q := executiondb.New()
		existing, err := q.FindExecutionInput(
			ctx,
			h.unit.DB(),
			executiondb.FindExecutionInputParams{AgentID: h.route.AgentID,
				ID: input.ID, Scope: optionalText("agent_config_change"), Key: optionalText(input.IdempotencyKey)},
		)
		if err == nil {
			if existing.InputKind != "config_change" || valueOrZero(existing.AgentConfigID) != input.ConfigID ||
				valueOrZero(existing.ActorID) != input.ActorID || !equalJSON(existing.Metadata, metadata) {
				return ActivatedConfig{}, storeerr.ErrIdempotencyConflict
			}
			if existing.State != "resolved" {
				return ActivatedConfig{}, storeerr.ErrStateTransitionConflict
			}
			event, err := q.FindExecutionInputEvent(
				ctx,
				h.unit.DB(),
				executiondb.FindExecutionInputEventParams{AgentID: h.route.AgentID, InputID: &existing.ID},
			)
			return ActivatedConfig{InputID: existing.ID, Event: ExecutionEvent{ID: event.ID, TurnID: event.TurnID,
				EventBoundary: EventBoundary{Sequence: event.Sequence, Time: event.CreatedAt}}}, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ActivatedConfig{}, err
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return ActivatedConfig{}, err
		}
		if input.ID == uuid.Nil {
			input.ID = uuid.New()
		}
		row, err := q.ReceiveExecutionInput(ctx, h.unit.DB(), executiondb.ReceiveExecutionInputParams{
			ID: input.ID, AgentID: h.route.AgentID, ProjectID: h.route.ProjectID, Kind: "config_change", Mode: "immediate",
			ConfigID: &input.ConfigID, ActorID: nullableID(input.ActorID), Scope: optionalText("agent_config_change"),
			Key: optionalText(input.IdempotencyKey), Metadata: metadata})
		if err != nil {
			return ActivatedConfig{}, err
		}
		turn := m.head.CurrentTurnID
		newTurn := !snapshot.Selection.TurnContinuable || turn == uuid.Nil
		if newTurn {
			turn = uuid.New()
		}
		event, err := h.appendEvent(ctx, turn, "agent_input", row.ID, uuid.Nil, uuid.Nil, newTurn)
		if err != nil {
			return ActivatedConfig{}, err
		}
		opening, err := h.captureOpening(ctx, turn, event.Sequence, m.head.StopSequence)
		if err != nil {
			return ActivatedConfig{}, err
		}
		n, err := q.ResolveExecutionInput(ctx, h.unit.DB(), executiondb.ResolveExecutionInputParams{
			AgentID: h.route.AgentID, ID: row.ID, EventID: &event.ID,
			OpeningIds: opening.InputIDs, OpeningSequence: optionalSequence(opening.EventSequence)})
		if err != nil {
			return ActivatedConfig{}, err
		}
		if n != 1 {
			return ActivatedConfig{}, storeerr.ErrStateTransitionConflict
		}
		n, err = q.ActivateExecutionConfig(ctx, h.unit.DB(), executiondb.ActivateExecutionConfigParams{
			AgentID: h.route.AgentID, ProjectID: h.route.ProjectID, ConfigID: input.ConfigID})
		if err != nil {
			return ActivatedConfig{}, err
		}
		if n != 1 {
			return ActivatedConfig{}, storeerr.ErrStateTransitionConflict
		}
		if newTurn {
			if err = q.OpenExecutionTurn(ctx,
				h.unit.DB(),
				executiondb.OpenExecutionTurnParams{AgentID: h.route.AgentID,
					ID:      turn,
					EventID: event.ID}); err != nil {
				return ActivatedConfig{}, err
			}
			m.openTurn(turn)
		} else {
			if err = h.advanceTurn(ctx, event, true); err != nil {
				return ActivatedConfig{}, err
			}
			if snapshot.View.Turn.FirstContentSequence > 0 && event.Sequence > m.head.MaxNormalInputSequence {
				m.head.PendingConfigInputID = row.ID
			}
		}
		m.changed()
		return ActivatedConfig{InputID: row.ID, Event: event, Created: true}, nil
	})
}
