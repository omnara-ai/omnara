package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"

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
	rows, err := q.ListExecutionBacklog(
		ctx,
		h.unit.DB(),
		executiondb.ListExecutionBacklogParams{AgentID: h.route.AgentID},
	)
	if err != nil {
		return InputAdmission{}, err
	}
	result := InputAdmission{TurnID: uuid.New()}
	for _, row := range rows {
		if kind == AdmitAllSteering && row.DeliveryMode != "steering" ||
			kind == AdmitOneQueued && row.DeliveryMode != "queued" {
			continue
		}
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
		if kind == AdmitOneQueued {
			break
		}
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
	admitted := make(map[uuid.UUID]bool, len(result.Inputs))
	for _, input := range result.Inputs {
		admitted[input.ID] = true
	}
	for _, row := range rows {
		if admitted[row.ID] {
			continue
		}
		if row.DeliveryMode == "steering" {
			loaded.View.Inputs.Steering = true
		} else {
			loaded.View.Inputs.Queued = true
		}
	}
	loaded.databaseNow = last.Time
	if err := m.updated(*loaded); err != nil {
		return InputAdmission{}, err
	}
	return result, nil
}

type BacklogChange struct {
	ID           uuid.UUID
	DeliveryMode string
	Cancel       bool
	BeforeID     uuid.UUID
	Position     string
}

func (h *Handle) ChangeBacklog(ctx context.Context, change BacklogChange) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		if change.ID == uuid.Nil || change.DeliveryMode != "queued" && change.DeliveryMode != "steering" {
			return false, errors.New("invalid backlog change")
		}
		switch change.Position {
		case "", "front", "back":
			if change.BeforeID != uuid.Nil {
				return false, errors.New("backlog change does not accept an anchor")
			}
		case "before", "after":
			if change.BeforeID == uuid.Nil || change.BeforeID == change.ID {
				return false, storeerr.ErrStateTransitionConflict
			}
		default:
			return false, errors.New("invalid backlog position")
		}
		if change.Cancel && change.Position != "" ||
			(change.Cancel || change.Position != "") && change.DeliveryMode != "queued" {
			return false, errors.New("only queued backlog supports cancellation and movement")
		}
		q := executiondb.New()
		rows, err := q.ListExecutionBacklog(ctx, h.unit.DB(),
			executiondb.ListExecutionBacklogParams{AgentID: h.route.AgentID})
		if err != nil {
			return false, err
		}
		index := slices.IndexFunc(
			rows,
			func(row executiondb.ListExecutionBacklogRow) bool { return row.ID == change.ID },
		)
		if index < 0 {
			if change.DeliveryMode == "steering" {
				row, err := q.FindExecutionInput(ctx, h.unit.DB(),
					executiondb.FindExecutionInputParams{AgentID: h.route.AgentID, ID: change.ID})
				if err == nil && row.InputKind == "content" && row.State == "resolved" &&
					row.AdmittedEventID != nil {
					return false, nil
				}
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return false, err
				}
			}
			return false, storeerr.ErrStateTransitionConflict
		}
		selected := rows[index]
		if change.Cancel {
			if selected.DeliveryMode != "queued" {
				return false, storeerr.ErrStateTransitionConflict
			}
			return h.changeBacklogRow(ctx, m, selected, true)
		}
		if change.Position != "" && selected.DeliveryMode != "queued" {
			return false, storeerr.ErrStateTransitionConflict
		}
		if change.Position == "" && change.DeliveryMode == selected.DeliveryMode {
			if change.DeliveryMode == "steering" {
				return false, nil
			}
			return false, storeerr.ErrStateTransitionConflict
		}
		rows = slices.DeleteFunc(rows, func(row executiondb.ListExecutionBacklogRow) bool {
			return row.DeliveryMode != change.DeliveryMode
		})
		rank, available, err := backlogRank(rows, change)
		if err != nil {
			return false, err
		}
		var changed bool
		if !available {
			for i := range rows {
				rows[i].InputRank = int64(i+1) * 1024
				updated, err := h.changeBacklogRow(ctx, m, rows[i], false)
				if err != nil {
					return false, err
				}
				changed = changed || updated
			}
			rank, available, err = backlogRank(rows, change)
			if err != nil {
				return false, err
			}
			if !available {
				return false, storeerr.ErrStateTransitionConflict
			}
		}
		selected.DeliveryMode = change.DeliveryMode
		selected.InputRank = rank
		updated, err := h.changeBacklogRow(ctx, m, selected, false)
		return changed || updated, err
	})
}

func (h *Handle) changeBacklogRow(ctx context.Context, m *executionMutation,
	row executiondb.ListExecutionBacklogRow, cancel bool) (bool, error) {
	n, err := executiondb.New().ChangeExecutionInput(ctx, h.unit.DB(), executiondb.ChangeExecutionInputParams{
		AgentID: h.route.AgentID, ID: row.ID, Mode: row.DeliveryMode, Rank: row.InputRank, Cancel: cancel})
	if n > 0 {
		m.changed()
	}
	return n > 0, err
}

func backlogRank(rows []executiondb.ListExecutionBacklogRow, change BacklogChange) (int64, bool, error) {
	neighbors := make([]int64, 0, len(rows))
	anchor := -1
	for _, row := range rows {
		if row.ID == change.ID {
			continue
		}
		if row.ID == change.BeforeID {
			anchor = len(neighbors)
		}
		neighbors = append(neighbors, row.InputRank)
	}
	destination := len(neighbors)
	switch change.Position {
	case "front":
		destination = 0
	case "before", "after":
		if anchor < 0 {
			return 0, false, storeerr.ErrStateTransitionConflict
		}
		destination = anchor
		if change.Position == "after" {
			destination++
		}
	}
	var lower int64
	if destination > 0 {
		lower = neighbors[destination-1]
	}
	if destination < len(neighbors) {
		upper := neighbors[destination]
		return lower + (upper-lower)/2, upper-lower > 1, nil
	}
	if lower > math.MaxInt64-1024 {
		return 0, false, errors.New("backlog rank overflow")
	}
	return lower + 1024, true, nil
}
