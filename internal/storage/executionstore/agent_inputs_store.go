package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CancelQueuedBacklogInputInput struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	InputID   uuid.UUID
}

type MoveQueuedBacklogInputPosition string

const (
	MoveQueuedBacklogInputToFront MoveQueuedBacklogInputPosition = "front"
	MoveQueuedBacklogInputToBack  MoveQueuedBacklogInputPosition = "back"
	MoveQueuedBacklogInputBefore  MoveQueuedBacklogInputPosition = "before"
	MoveQueuedBacklogInputAfter   MoveQueuedBacklogInputPosition = "after"
)

type MoveQueuedBacklogInputInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	InputID       uuid.UUID
	Position      MoveQueuedBacklogInputPosition
	AnchorInputID uuid.UUID
}

type ClaimNextAgentWorkInput struct {
	WorkerProcessID uuid.UUID
	LeaseDuration   time.Duration
}

type AgentWorkKind uint8

const (
	AgentWorkNone AgentWorkKind = iota
	AgentWorkModel
	AgentWorkTool
)

type ModelWorkKind string

const (
	ModelWorkStart    ModelWorkKind = "start"
	ModelWorkResume   ModelWorkKind = "resume"
	ModelWorkContinue ModelWorkKind = "continue"
)

type ClaimedModelWork struct {
	Prepared                 *PreparedNormalModelCall
	Kind                     ModelWorkKind
	ModelCallContextID       uuid.UUID
	SourceModelCallContextID uuid.UUID
	SourceModelOutputID      uuid.UUID
	TurnID                   uuid.UUID
	InputIDs                 []uuid.UUID
	OpeningEventSequence     int64
	AdmittedInputTurn        AdmittedAgentInputTurn
}

type ClaimedToolWork struct {
	Prepared           *PreparedToolWork
	TurnID             uuid.UUID
	ModelCallContextID uuid.UUID
	ModelOutputID      uuid.UUID
	SourceEventID      uuid.UUID
}

type ClaimedAgentWork struct {
	LocalLeaseBudgetStartedAt time.Time
	OrgID                     uuid.UUID
	ProjectID                 uuid.UUID
	AgentID                   uuid.UUID
	Kind                      AgentWorkKind
	RuntimeLock               AgentRuntimeLockRecord
	Model                     ClaimedModelWork
	Tool                      ClaimedToolWork
}

type AdmittedAgentInputTurn struct {
	Inputs []AgentInputRecord
	Events []events.Event
	Turn   AgentTurnRecord
}

func (s *Store) ClaimNextAgentWork(
	ctx context.Context,
	input ClaimNextAgentWorkInput,
) (ClaimedAgentWork, bool, error) {
	if input.WorkerProcessID == uuid.Nil {
		return ClaimedAgentWork{}, false, errors.New("worker process id is required")
	}
	if err := validateAgentRuntimeLockLeaseDuration(input.LeaseDuration); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	route, err := unit.ClaimAgent(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimedAgentWork{}, false, storeerr.ErrNoClaimableAgentWakeup
	}
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	h, err := unit.Agent(route)
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	started := time.Now() //nolint:omnaralint // Monotonic lease budget.
	work, err := h.Claim(ctx, input.WorkerProcessID, input.LeaseDuration)
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	if err := applyAdmissionDestination(ctx, unit, route.ProjectID, route.AgentID, work.Admission); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	claim, err := shapeOwnedWork(ctx, unit, route.ProjectID, route.AgentID, work)
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	claim.LocalLeaseBudgetStartedAt = started
	if err = unit.Commit(ctx, "claim agent work"); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	return claim, true, nil
}

func (s *Store) ListQueuedBacklogInputs(
	ctx context.Context,
	input ListQueuedBacklogInputsInput,
) (ListQueuedBacklogInputsResult, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil {
		return ListQueuedBacklogInputsResult{}, errors.New("project id and agent id are required")
	}
	if input.Limit <= 0 {
		return ListQueuedBacklogInputsResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListQueuedBacklogInputsParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		RowLimit:  int64(input.Limit) + 1,
	}
	if input.After.Set {
		mode := string(input.After.DeliveryMode)
		rank := input.After.InputRank
		queuedAt := input.After.QueuedAt
		id := input.After.ID
		params.CursorDeliveryMode = &mode
		params.CursorInputRank = &rank
		params.CursorQueuedAt = &queuedAt
		params.CursorID = &id
	}
	rows, err := s.q.ListQueuedBacklogInputs(ctx, params)
	if err != nil {
		return ListQueuedBacklogInputsResult{}, fmt.Errorf("list queued backlog inputs: %w", err)
	}
	result := ListQueuedBacklogInputsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Inputs = make([]AgentInputRecord, 0, len(rows))
	inputIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		record := agentInputRecordFromBacklogSQLC(row)
		result.Inputs = append(result.Inputs, record)
		inputIDs = append(inputIDs, record.ID)
	}
	contentBlocks, err := agentInputContentBlocks(ctx, s.q, input.ProjectID, input.AgentID, inputIDs)
	if err != nil {
		return ListQueuedBacklogInputsResult{}, err
	}
	for index := range result.Inputs {
		result.Inputs[index].ContentBlocks = contentBlocks[result.Inputs[index].ID]
	}
	return result, nil
}

func (s *Store) CancelQueuedBacklogInput(ctx context.Context, input CancelQueuedBacklogInputInput) error {
	return s.changeBacklog(ctx, input.ProjectID, input.AgentID,
		agentexecution.BacklogChange{ID: input.InputID, DeliveryMode: "queued", Cancel: true}, false)
}

func (s *Store) MoveQueuedBacklogInput(ctx context.Context, input MoveQueuedBacklogInputInput) error {
	return s.changeBacklog(
		ctx,
		input.ProjectID,
		input.AgentID,
		agentexecution.BacklogChange{
			ID:           input.InputID,
			DeliveryMode: "queued",
			Position:     string(input.Position),
			BeforeID:     input.AnchorInputID,
		},
		false,
	)
}
