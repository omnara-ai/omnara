package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CancelQueuedBacklogInputInput struct {
	ProjectID ID
	AgentID   ID
	InputID   ID
}

type MoveQueuedBacklogInputPosition string

const (
	MoveQueuedBacklogInputToFront MoveQueuedBacklogInputPosition = "front"
	MoveQueuedBacklogInputToBack  MoveQueuedBacklogInputPosition = "back"
	MoveQueuedBacklogInputBefore  MoveQueuedBacklogInputPosition = "before"
	MoveQueuedBacklogInputAfter   MoveQueuedBacklogInputPosition = "after"
)

type MoveQueuedBacklogInputInput struct {
	ProjectID     ID
	AgentID       ID
	InputID       ID
	Position      MoveQueuedBacklogInputPosition
	AnchorInputID ID
}

type ClaimNextAgentWorkInput struct {
	WorkerProcessID ID
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
	Kind                     ModelWorkKind
	ModelCallContextID       ID
	SourceModelCallContextID ID
	SourceModelOutputID      ID
	TurnID                   ID
	InputIDs                 []ID
	OpeningEventSequence     int64
	AdmittedInputTurn        AdmittedAgentInputTurn
}

type ClaimedToolWork struct {
	TurnID             ID
	ModelCallContextID ID
	ModelOutputID      ID
	SourceEventID      ID
}

type ClaimedAgentWork struct {
	OrgID       ID
	ProjectID   ID
	AgentID     ID
	Kind        AgentWorkKind
	RuntimeLock AgentRuntimeLockRecord
	Model       ClaimedModelWork
	Tool        ClaimedToolWork
}

type admitAgentInputAndOpenTurnInput struct {
	ProjectID ID
	AgentID   ID
}

type AdmittedAgentInputTurn struct {
	Inputs []AgentInputRecord
	Events []events.Event
	Turn   AgentTurnRecord
}

func (s *Store) ClaimNextAgentWork(ctx context.Context, input ClaimNextAgentWorkInput) (ClaimedAgentWork, bool, error) {
	if isNilID(input.WorkerProcessID) {
		return ClaimedAgentWork{}, false, errors.New("worker process id is required")
	}
	if err := validateAgentRuntimeLockLeaseDuration(input.LeaseDuration); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ClaimedAgentWork{}, false, fmt.Errorf("begin claim agent work: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	wakeup, err := qtx.ClaimNextAgentWakeup(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimedAgentWork{}, false, storeerr.ErrNoClaimableAgentWakeup
	}
	if err != nil {
		return ClaimedAgentWork{}, false, fmt.Errorf("claim agent wakeup: %w", err)
	}
	claim := ClaimedAgentWork{ProjectID: wakeup.ProjectID, AgentID: wakeup.AgentID}

	toolWork, toolErr := qtx.NextAgentToolWork(
		ctx,
		dbsqlc.NextAgentToolWorkParams{ProjectID: wakeup.ProjectID, AgentID: wakeup.AgentID},
	)
	hasToolWork := toolErr == nil
	if toolErr != nil && !errors.Is(toolErr, pgx.ErrNoRows) {
		return ClaimedAgentWork{}, false, fmt.Errorf("load next tool work: %w", toolErr)
	}
	modelWork, modelErr := qtx.NextAgentModelWork(ctx, dbsqlc.NextAgentModelWorkParams{
		ProjectID: wakeup.ProjectID,
		AgentID:   wakeup.AgentID,
	})
	hasModelWork := modelErr == nil
	if modelErr != nil && !errors.Is(modelErr, pgx.ErrNoRows) {
		return ClaimedAgentWork{}, false, fmt.Errorf("load next model work: %w", modelErr)
	}
	if hasToolWork && hasModelWork {
		return ClaimedAgentWork{}, false, errors.New(
			"agent has both tool work and model work for its current frontier",
		)
	}
	if hasToolWork {
		claim.Kind = AgentWorkTool
		claim.Tool = ClaimedToolWork{
			TurnID:             toolWork.TurnID,
			ModelCallContextID: toolWork.ModelCallContextID,
			ModelOutputID:      toolWork.ModelOutputID,
			SourceEventID:      toolWork.SourceEventID,
		}
	}

	var selectedInputs []AgentInputRecord
	if claim.Kind != AgentWorkTool {
		incompleteToolBatch, err := qtx.AgentHasIncompleteToolBatch(
			ctx,
			dbsqlc.AgentHasIncompleteToolBatchParams{
				ProjectID: wakeup.ProjectID,
				AgentID:   wakeup.AgentID,
			},
		)
		if err != nil {
			return ClaimedAgentWork{}, false, fmt.Errorf("check incomplete tool batch: %w", err)
		}
		if incompleteToolBatch {
			if hasModelWork {
				return ClaimedAgentWork{}, false, errors.New(
					"agent has model work while its current tool batch is incomplete",
				)
			}
			return s.reconcileClaimedWakeupTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				claim,
				"tool_batch_wait",
			)
		}

		steering, err := selectLockedSteeringAgentInputsForAdmissionTx(
			ctx,
			qtx,
			wakeup.ProjectID,
			wakeup.AgentID,
		)
		if err != nil {
			return ClaimedAgentWork{}, false, err
		}
		switch {
		case len(steering) > 0:
			claim.Kind = AgentWorkModel
			claim.Model.Kind = ModelWorkStart
			selectedInputs = steering
		case hasModelWork && modelWork.IsReady:
			claim.Kind = AgentWorkModel
			claim.Model = claimedModelWorkFromSQLC(modelWork)
		case hasModelWork:
			return s.reconcileClaimedWakeupTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				claim,
				"model_retry_wait",
			)
		default:
			queued, err := selectLockedQueuedAgentInputForAdmissionTx(
				ctx,
				qtx,
				wakeup.ProjectID,
				wakeup.AgentID,
			)
			if err != nil {
				return ClaimedAgentWork{}, false, err
			}
			if len(queued) == 0 {
				return s.reconcileClaimedWakeupTx(
					ctx,
					txNotifications,
					tx,
					qtx,
					claim,
					"idle",
				)
			}
			claim.Kind = AgentWorkModel
			claim.Model.Kind = ModelWorkStart
			selectedInputs = queued
		}
	}

	runtime, orgID, err := acquireAgentRuntimeLockTx(
		ctx,
		qtx,
		wakeup.ProjectID,
		wakeup.AgentID,
		input.WorkerProcessID,
		input.LeaseDuration,
	)
	if err != nil {
		if errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
			return ClaimedAgentWork{}, true, nil
		}
		return ClaimedAgentWork{}, false, err
	}
	claim.RuntimeLock = runtime
	claim.OrgID = orgID
	if err := consumeClaimedAgentWakeupTx(
		ctx,
		qtx,
		claim.ProjectID,
		claim.AgentID,
	); err != nil {
		return ClaimedAgentWork{}, false, err
	}

	if len(selectedInputs) > 0 {
		admitted, err := admitLockedAgentInputsAndOpenTurnTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			admitAgentInputAndOpenTurnInput{
				ProjectID: wakeup.ProjectID,
				AgentID:   wakeup.AgentID,
			},
			selectedInputs,
		)
		if err != nil {
			return ClaimedAgentWork{}, false, err
		}
		claim.Model.AdmittedInputTurn = admitted
		claim.Model.TurnID = admitted.Turn.ID
		claim.Model.InputIDs, claim.Model.OpeningEventSequence, err = modelCallOpeningInputSet(
			ctx,
			qtx,
			claim.ProjectID,
			claim.AgentID,
			claim.Model.TurnID,
			latestEventSequence(admitted.Events),
		)
		if err != nil {
			return ClaimedAgentWork{}, false, err
		}
	}
	if claim.Kind == AgentWorkModel {
		if err := validateClaimedModelWork(claim.Model); err != nil {
			return ClaimedAgentWork{}, false, err
		}
	}

	renewal, err := renewAgentRuntimeLockTx(
		ctx,
		qtx,
		claim.ProjectID,
		claim.AgentID,
		claim.RuntimeLock.ID,
		input.LeaseDuration,
	)
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	claim.RuntimeLock = renewal.RuntimeLock
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "claim agent work"); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	return claim, true, nil
}

func claimedModelWorkFromSQLC(row dbsqlc.NextAgentModelWorkRow) ClaimedModelWork {
	work := ClaimedModelWork{
		Kind:                 ModelWorkKind(row.WorkKind),
		TurnID:               row.TurnID,
		InputIDs:             row.InputIds,
		OpeningEventSequence: row.OpeningEventSequence,
	}
	switch work.Kind {
	case ModelWorkStart:
	case ModelWorkResume:
		work.ModelCallContextID = row.ModelCallContextID
	case ModelWorkContinue:
		work.SourceModelCallContextID = row.ModelCallContextID
		work.SourceModelOutputID = row.ModelOutputID
	}
	return work
}

func (s *Store) reconcileClaimedWakeupTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	claim ClaimedAgentWork,
	reason string,
) (ClaimedAgentWork, bool, error) {
	metadata, err := marshalJSON(map[string]any{"reason": reason})
	if err != nil {
		return ClaimedAgentWork{}, false, fmt.Errorf("marshal wakeup reconciliation metadata: %w", err)
	}
	if err := qtx.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: claim.ProjectID,
		AgentID:   claim.AgentID,
		Metadata:  metadata,
	}); err != nil {
		return ClaimedAgentWork{}, false, fmt.Errorf("reconcile claimed agent wakeup: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "reconcile claimed agent wakeup"); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	return ClaimedAgentWork{}, true, nil
}

func consumeClaimedAgentWakeupTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
) error {
	changed, err := qtx.ConsumeAgentWakeup(
		ctx,
		dbsqlc.ConsumeAgentWakeupParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return fmt.Errorf("consume claimed agent wakeup: %w", err)
	}
	if changed != 1 {
		return storeerr.ErrNoClaimableAgentWakeup
	}
	return nil
}

func validateClaimedModelWork(work ClaimedModelWork) error {
	if work.TurnID == NilID || len(work.InputIDs) == 0 || work.OpeningEventSequence <= 0 {
		return errors.New("claimed model work requires a turn, opening inputs, and opening event sequence")
	}
	switch work.Kind {
	case ModelWorkStart:
		if work.ModelCallContextID != NilID ||
			work.SourceModelCallContextID != NilID ||
			work.SourceModelOutputID != NilID {
			return errors.New("start model work has invalid source identity")
		}
	case ModelWorkResume:
		if work.ModelCallContextID == NilID ||
			work.SourceModelCallContextID != NilID ||
			work.SourceModelOutputID != NilID {
			return errors.New("resume model work requires only its active context")
		}
	case ModelWorkContinue:
		if work.ModelCallContextID != NilID ||
			work.SourceModelCallContextID == NilID ||
			work.SourceModelOutputID == NilID {
			return errors.New("continue model work requires its source context and source output")
		}
	default:
		return fmt.Errorf("unsupported model work kind %q", work.Kind)
	}
	return nil
}

func latestEventSequence(events []events.Event) int64 {
	var sequence int64
	for _, event := range events {
		if event.Sequence > sequence {
			sequence = event.Sequence
		}
	}
	return sequence
}

func modelCallOpeningInputSet(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, turnID ID,
	inputEventSequence int64,
) ([]ID, int64, error) {
	rows, err := qtx.ListModelCallOpeningContentInputs(
		ctx,
		dbsqlc.ListModelCallOpeningContentInputsParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			TurnID:             turnID,
			InputEventSequence: inputEventSequence,
		},
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list model call opening inputs: %w", err)
	}
	if len(rows) == 0 {
		return nil, 0, fmt.Errorf(
			"model call opening inputs are empty: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	inputIDs := make([]ID, 0, len(rows))
	for _, row := range rows {
		inputIDs = append(inputIDs, row.InputID)
	}
	return inputIDs, rows[0].EventSequence, nil
}

func (s *Store) ListQueuedBacklogInputs(
	ctx context.Context,
	input ListQueuedBacklogInputsInput,
) (ListQueuedBacklogInputsResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) {
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
		rank := input.After.InputRank
		queuedAt := input.After.QueuedAt
		id := input.After.ID
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
	inputIDs := make([]ID, 0, len(rows))
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

func selectLockedSteeringAgentInputsForAdmissionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
) ([]AgentInputRecord, error) {
	steeringRows, err := qtx.ListSteeringAgentInputsForAdmission(
		ctx,
		dbsqlc.ListSteeringAgentInputsForAdmissionParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list steering agent inputs: %w", err)
	}
	selected := make([]AgentInputRecord, 0, len(steeringRows))
	for _, row := range steeringRows {
		selected = append(selected, agentInputRecordFromSteeringAdmissionSQLC(row))
	}
	return selected, nil
}

func selectLockedQueuedAgentInputForAdmissionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
) ([]AgentInputRecord, error) {
	row, err := qtx.GetNextQueuedAgentInputForAdmission(
		ctx,
		dbsqlc.GetNextQueuedAgentInputForAdmissionParams{ProjectID: projectID, AgentID: agentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get next queued agent input: %w", err)
	}
	return []AgentInputRecord{agentInputRecordFromQueuedAdmissionSQLC(row)}, nil
}

func admitLockedAgentInputsAndOpenTurnTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input admitAgentInputAndOpenTurnInput,
	lockedInputs []AgentInputRecord,
) (AdmittedAgentInputTurn, error) {
	if len(lockedInputs) == 0 {
		return AdmittedAgentInputTurn{}, storeerr.ErrStateTransitionConflict
	}
	if len(lockedInputs) > 1 {
		for _, locked := range lockedInputs {
			if locked.DeliveryMode != DeliveryModeSteering {
				return AdmittedAgentInputTurn{}, storeerr.ErrStateTransitionConflict
			}
		}
	}
	admittedInputs := make([]AgentInputRecord, 0, len(lockedInputs))
	admittedEvents := make([]events.Event, 0, len(lockedInputs))
	turnID, err := uuid.NewV7()
	if err != nil {
		return AdmittedAgentInputTurn{}, fmt.Errorf("generate turn id: %w", err)
	}
	for _, agentInput := range lockedInputs {
		eventRecord, err := appendTypedAgentEventTx(
			ctx,
			txNotifications,
			tx,
			AppendTypedAgentEventInput{
				ProjectID:      input.ProjectID,
				AgentID:        input.AgentID,
				TurnID:         turnID,
				IsOpeningEvent: true,
				Kind:           events.KindAgentInput,
				IdempotencyKey: "agent_input:" + agentInput.ID.String(),
				AgentInputID:   agentInput.ID,
			},
		)
		if err != nil {
			return AdmittedAgentInputTurn{}, err
		}
		event := eventRecord.Event
		admission, err := qtx.AdmitAgentInput(
			ctx,
			dbsqlc.AdmitAgentInputParams{
				ProjectID:       input.ProjectID,
				AgentID:         input.AgentID,
				ID:              agentInput.ID,
				AdmittedEventID: event.ID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdmittedAgentInputTurn{}, storeerr.ErrStateTransitionConflict
		}
		if err != nil {
			return AdmittedAgentInputTurn{}, fmt.Errorf("admit agent input: %w", err)
		}
		agentInput.State = "resolved"
		agentInput.AdmittedEventID = event.ID
		agentInput.AdmittedAt = admission.AdmittedAt
		agentInput.ResolvedAt = admission.ResolvedAt
		admittedInputs = append(admittedInputs, agentInput)
		admittedEvents = append(admittedEvents, event)
	}
	sequence, err := qtx.NextTurnSequence(
		ctx,
		dbsqlc.NextTurnSequenceParams{ProjectID: input.ProjectID, AgentID: input.AgentID},
	)
	if err != nil {
		return AdmittedAgentInputTurn{}, fmt.Errorf("next turn sequence: %w", err)
	}
	latestEvent := admittedEvents[len(admittedEvents)-1]
	turnRow, err := qtx.InsertAgentTurn(
		ctx,
		dbsqlc.InsertAgentTurnParams{
			ID:                    turnID,
			ProjectID:             input.ProjectID,
			AgentID:               input.AgentID,
			TurnSequence:          sequence,
			LatestEventID:         latestEvent.ID,
			LatestSemanticEventID: latestEvent.ID,
		},
	)
	if err != nil {
		return AdmittedAgentInputTurn{}, fmt.Errorf("insert agent turn: %w", err)
	}
	return AdmittedAgentInputTurn{
		Inputs: admittedInputs,
		Events: admittedEvents,
		Turn:   agentTurnRecordFromInsertSQLC(turnRow),
	}, nil
}

func (s *Store) CancelQueuedBacklogInput(
	ctx context.Context,
	input CancelQueuedBacklogInputInput,
) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.InputID) {
		return errors.New("project id, agent id, and input id are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin cancel queued backlog input: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
		return fmt.Errorf("lock agent for backlog cancel: %w", err)
	}
	changed, err := qtx.CancelQueuedBacklogInput(
		ctx,
		dbsqlc.CancelQueuedBacklogInputParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ID:        input.InputID,
		},
	)
	if err != nil {
		return fmt.Errorf("cancel queued backlog input: %w", err)
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	if err := qtx.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		Metadata:  []byte(`{"reason":"queued_input_canceled"}`),
	}); err != nil {
		return fmt.Errorf("reconcile wakeup after queued backlog cancel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit cancel queued backlog input: %w", err)
	}
	return nil
}

func (s *Store) MoveQueuedBacklogInput(
	ctx context.Context,
	input MoveQueuedBacklogInputInput,
) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.InputID) {
		return errors.New("project id, agent id, and input id are required")
	}
	switch input.Position {
	case MoveQueuedBacklogInputToFront, MoveQueuedBacklogInputToBack:
		if !isNilID(input.AnchorInputID) {
			return errors.New("front/back moves do not accept an anchor input id")
		}
	case MoveQueuedBacklogInputBefore, MoveQueuedBacklogInputAfter:
		if isNilID(input.AnchorInputID) {
			return errors.New("before/after moves require an anchor input id")
		}
		if input.AnchorInputID == input.InputID {
			return storeerr.ErrStateTransitionConflict
		}
	default:
		return errors.New("position must be front, back, before, or after")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin move queued backlog input: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
		return fmt.Errorf("lock agent for backlog move: %w", err)
	}
	valid, err := qtx.QueuedBacklogMoveIsValid(ctx, dbsqlc.QueuedBacklogMoveIsValidParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		ID:        input.InputID,
		RequiresAnchor: input.Position == MoveQueuedBacklogInputBefore ||
			input.Position == MoveQueuedBacklogInputAfter,
		AnchorID: input.AnchorInputID,
	})
	if err != nil {
		return fmt.Errorf("validate queued backlog move: %w", err)
	}
	if !valid {
		return storeerr.ErrStateTransitionConflict
	}
	changed, err := moveQueuedBacklogInputOnce(ctx, qtx, input)
	if err != nil {
		return fmt.Errorf("move queued backlog input: %w", err)
	}
	if changed == 0 {
		if _, err := qtx.RebalanceQueuedBacklogRanks(
			ctx,
			dbsqlc.RebalanceQueuedBacklogRanksParams{
				RankStride: agentInputRankStride,
				ProjectID:  input.ProjectID,
				AgentID:    input.AgentID,
			},
		); err != nil {
			return fmt.Errorf("rebalance queued backlog ranks: %w", err)
		}
		changed, err = moveQueuedBacklogInputOnce(ctx, qtx, input)
		if err != nil {
			return fmt.Errorf("move queued backlog input after rank rebalance: %w", err)
		}
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit move queued backlog input: %w", err)
	}
	return nil
}

func moveQueuedBacklogInputOnce(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input MoveQueuedBacklogInputInput,
) (int64, error) {
	switch input.Position {
	case MoveQueuedBacklogInputToFront:
		return qtx.MoveQueuedBacklogInputToFront(
			ctx,
			dbsqlc.MoveQueuedBacklogInputToFrontParams{
				ProjectID:  input.ProjectID,
				AgentID:    input.AgentID,
				ID:         input.InputID,
				RankStride: agentInputRankStride,
			},
		)
	case MoveQueuedBacklogInputToBack:
		return qtx.MoveQueuedBacklogInputToBack(
			ctx,
			dbsqlc.MoveQueuedBacklogInputToBackParams{
				ProjectID:  input.ProjectID,
				AgentID:    input.AgentID,
				ID:         input.InputID,
				RankStride: agentInputRankStride,
			},
		)
	case MoveQueuedBacklogInputBefore:
		return qtx.MoveQueuedBacklogInputBefore(
			ctx,
			dbsqlc.MoveQueuedBacklogInputBeforeParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
				ID:        input.InputID,
				AnchorID:  input.AnchorInputID,
			},
		)
	case MoveQueuedBacklogInputAfter:
		return qtx.MoveQueuedBacklogInputAfter(
			ctx,
			dbsqlc.MoveQueuedBacklogInputAfterParams{
				ProjectID:  input.ProjectID,
				AgentID:    input.AgentID,
				ID:         input.InputID,
				AnchorID:   input.AnchorInputID,
				RankStride: agentInputRankStride,
			},
		)
	default:
		return 0, errors.New("invalid queued backlog move position")
	}
}
