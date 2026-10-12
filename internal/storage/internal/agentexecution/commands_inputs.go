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

type ReceiveContentInput struct {
	ID                  uuid.UUID
	ActorID             uuid.UUID
	IntegrationTargetID uuid.UUID
	DeliveryMode        string
	IdempotencyScope    string
	IdempotencyKey      string
	Metadata            json.RawMessage
	Content             []Content
}

type ReceivedContent struct {
	ID      uuid.UUID
	Created bool
}

type AdmittedContent struct {
	ActorID             uuid.UUID
	ID                  uuid.UUID
	IntegrationTargetID uuid.UUID
	Event               ExecutionEvent
}

type InputAdmission struct {
	TurnID uuid.UUID
	Inputs []AdmittedContent
}

func (h *Handle) ReceiveContent(ctx context.Context, input ReceiveContentInput) (ReceivedContent, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (ReceivedContent, error) {
		if input.DeliveryMode != "queued" && input.DeliveryMode != "steering" {
			return ReceivedContent{}, errors.New("invalid content delivery mode")
		}
		if input.IdempotencyKey != "" && input.IdempotencyScope == "" {
			return ReceivedContent{}, errors.New("input idempotency key requires a scope")
		}
		for _, part := range input.Content {
			if part.Kind != "text" && part.Kind != "artifact" && part.Kind != "structured_data" {
				return ReceivedContent{}, errors.New("invalid input content")
			}
		}
		metadata, err := objectJSON(input.Metadata)
		if err != nil {
			return ReceivedContent{}, err
		}
		q := executiondb.New()
		existing, err := q.FindExecutionInput(
			ctx,
			h.unit.DB(),
			executiondb.FindExecutionInputParams{AgentID: h.route.AgentID,
				ID: input.ID, Scope: optionalText(input.IdempotencyScope), Key: optionalText(input.IdempotencyKey)},
		)
		if err == nil {
			if existing.InputKind != "content" || existing.DeliveryMode != input.DeliveryMode ||
				valueOrZero(existing.ActorID) != input.ActorID ||
				valueOrZero(existing.IntegrationTargetID) != input.IntegrationTargetID ||
				!equalJSON(existing.Metadata, metadata) {
				return ReceivedContent{}, storeerr.ErrIdempotencyConflict
			}
			return ReceivedContent{
				ID: existing.ID,
			}, h.contentMatches(
				ctx,
				existing.ID,
				uuid.Nil,
				input.Content,
			)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ReceivedContent{}, err
		}
		if input.ID == uuid.Nil {
			input.ID = uuid.New()
		}
		row, err := q.ReceiveExecutionInput(ctx, h.unit.DB(), executiondb.ReceiveExecutionInputParams{
			ID: input.ID, AgentID: h.route.AgentID, ProjectID: h.route.ProjectID, ActorID: nullableID(input.ActorID),
			TargetID: nullableID(
				input.IntegrationTargetID,
			), Mode: input.DeliveryMode, Kind: "content", Metadata: metadata,
			Scope: optionalText(input.IdempotencyScope), Key: optionalText(input.IdempotencyKey)})
		if err != nil {
			return ReceivedContent{}, err
		}
		if err = h.writeContent(ctx, "agent_input", row.ID, input.Content); err != nil {
			return ReceivedContent{}, err
		}
		if m.loaded != nil {
			m.loaded.databaseNow = row.QueuedAt
			if input.DeliveryMode == "steering" {
				m.loaded.View.Inputs.Steering = true
			} else {
				m.loaded.View.Inputs.Queued = true
			}
			if err := m.updated(*m.loaded); err != nil {
				return ReceivedContent{}, err
			}
		} else {
			m.changed()
		}
		return ReceivedContent{ID: row.ID, Created: true}, nil
	})
}

func (h *Handle) AdmitInputs(ctx context.Context) (InputAdmission, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (InputAdmission, error) {
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return InputAdmission{}, err
		}
		if snapshot.Selection.Work != WorkInput {
			return InputAdmission{}, nil
		}
		return h.admitInputs(ctx, m, snapshot.Selection.Admission)
	})
}

func (h *Handle) admitInputs(
	ctx context.Context,
	m *executionMutation,
	kind AdmissionKind,
) (InputAdmission, error) {
	q := executiondb.New()
	mode := "steering"
	var limit *int64
	if kind == AdmitOneQueued {
		mode = "queued"
		limit = new(int64(1))
	}
	rows, err := q.SelectExecutionAdmission(ctx, h.unit.DB(), executiondb.SelectExecutionAdmissionParams{
		AgentID: h.route.AgentID, Mode: mode, RowLimit: limit,
	})
	if err != nil {
		return InputAdmission{}, err
	}
	result := InputAdmission{TurnID: uuid.New()}
	for _, row := range rows {
		event, err := h.appendEvent(ctx, result.TurnID, "agent_input", row.ID, uuid.Nil, uuid.Nil, true)
		if err != nil {
			return InputAdmission{}, err
		}
		n, err := q.ResolveExecutionInput(ctx, h.unit.DB(), executiondb.ResolveExecutionInputParams{
			AgentID: h.route.AgentID, ID: row.ID, EventID: &event.ID})
		if err != nil {
			return InputAdmission{}, err
		}
		if n != 1 {
			return InputAdmission{}, storeerr.ErrStateTransitionConflict
		}
		result.Inputs = append(
			result.Inputs,
			AdmittedContent{
				ID:                  row.ID,
				ActorID:             valueOrZero(row.ActorID),
				IntegrationTargetID: valueOrZero(row.IntegrationTargetID),
				Event:               event,
			},
		)
	}
	if len(result.Inputs) == 0 {
		return InputAdmission{}, nil
	}
	last := result.Inputs[len(result.Inputs)-1].Event
	if err := q.OpenExecutionTurn(ctx, h.unit.DB(), executiondb.OpenExecutionTurnParams{
		AgentID: h.route.AgentID, ID: result.TurnID, EventID: last.ID}); err != nil {
		return InputAdmission{}, err
	}
	loaded := m.loaded
	m.openTurn(result.TurnID)
	if loaded == nil {
		return result, nil
	}
	opening, err := q.CaptureExecutionOpening(ctx, h.unit.DB(), executiondb.CaptureExecutionOpeningParams{
		AgentID: h.route.AgentID, ProjectID: h.route.ProjectID, TurnID: result.TurnID,
		Watermark: last.Sequence, StopSequence: m.head.StopSequence,
	})
	if err != nil {
		return InputAdmission{}, err
	}
	if len(opening) == 0 {
		return InputAdmission{}, ErrInvalidState
	}
	first := result.Inputs[0].Event
	turn := &Turn{ID: result.TurnID, FirstOpeningSequence: first.Sequence, LastOpeningSequence: last.Sequence,
		FirstContentSequence: first.Sequence, LatestSemantic: last.EventBoundary,
		InitialOpening: Opening{EventSequence: opening[0].Sequence}, InitialReadyAt: opening[0].CreatedAt}
	for _, row := range opening {
		turn.InitialOpening.InputIDs = append(turn.InitialOpening.InputIDs, row.ID)
	}
	loaded.View = ExecutionView{AgentID: h.route.AgentID, State: loaded.View.State, Turn: turn}
	loaded.View.Inputs = InputAvailability{
		Steering: valueOrZero(rows[0].RemainingSteering), Queued: rows[0].RemainingQueued,
	}
	loaded.databaseNow = last.Time
	if err := m.updated(*loaded); err != nil {
		return InputAdmission{}, err
	}
	return result, nil
}
