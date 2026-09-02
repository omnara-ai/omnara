package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CancelAgentResult struct {
	Event                  events.Event
	RuntimeCancelRequested bool
	Affected               bool
	ActorID                ID
}

type CancelAgentInput struct {
	ProjectID ID
	AgentID   ID
	Actor     *ActorParams
}

func (s *Store) CancelAgent(
	ctx context.Context,
	input CancelAgentInput,
) (CancelAgentResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) {
		return CancelAgentResult{}, errors.New("project and agent are required")
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CancelAgentResult{}, fmt.Errorf("begin cancel agent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
		return CancelAgentResult{}, fmt.Errorf("lock agent for cancel: %w", err)
	}
	actorID, err := resolveActorTx(ctx, qtx, input.ProjectID, input.AgentID, input.Actor, NilID)
	if err != nil {
		return CancelAgentResult{}, err
	}
	result, err := cancelAgentTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		cancelAgentTxInput{
			ProjectID:        input.ProjectID,
			AgentID:          input.AgentID,
			ActorID:          actorID,
			ReasonCode:       "agent_canceled",
			ModelCallMessage: "The model call was canceled by an explicit agent cancellation.",
		},
	)
	if err != nil {
		return CancelAgentResult{}, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		fmt.Sprintf("cancel agent %s", input.AgentID.String()),
	); err != nil {
		return CancelAgentResult{}, err
	}
	return result, nil
}

type cancelAgentTxInput struct {
	ProjectID                           ID
	AgentID                             ID
	ActorID                             ID
	ReasonCode                          string
	ModelCallMessage                    string
	CancelRuntimeWithoutContinuableTurn bool
}

func cancelAgentTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input cancelAgentTxInput,
) (CancelAgentResult, error) {
	projectID, agentID, actorID := input.ProjectID, input.AgentID, input.ActorID
	latest, err := qtx.LatestAgentEvent(ctx, dbsqlc.LatestAgentEventParams{ProjectID: projectID, AgentID: agentID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CancelAgentResult{}, fmt.Errorf("load latest agent event for cancel: %w", err)
	}

	afterSequence := int64(0)
	if !isNilID(latest.ID) {
		afterSequence = latest.Sequence
	}
	var event events.Event
	currentTurn, currentTurnErr := qtx.CurrentContinuableAgentTurn(
		ctx,
		dbsqlc.CurrentContinuableAgentTurnParams{ProjectID: projectID, AgentID: agentID},
	)
	if currentTurnErr != nil && !errors.Is(currentTurnErr, pgx.ErrNoRows) {
		return CancelAgentResult{}, fmt.Errorf("load current turn for cancel: %w", currentTurnErr)
	}
	hasActiveContexts, err := qtx.AgentHasLiveModelCallContexts(
		ctx,
		dbsqlc.AgentHasLiveModelCallContextsParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return CancelAgentResult{}, fmt.Errorf("check active model call contexts for cancel: %w", err)
	}
	runtimeCancelRequested := false
	var runtimeToCancel AgentRuntimeLockRecord
	recordRuntimeCancel := func(record AgentRuntimeLockRecord) {
		if isNilID(record.ID) {
			return
		}
		runtimeCancelRequested = true
		runtimeToCancel = record
		txNotifications.AddWorkerControlCancel(record.WorkerProcessID, agentID, record.ID)
	}
	recordPendingRuntimeCancel := func() error {
		row, err := qtx.GetPendingAgentRuntimeCancel(ctx, dbsqlc.GetPendingAgentRuntimeCancelParams{
			ProjectID: projectID,
			AgentID:   agentID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load pending runtime cancel: %w", err)
		}
		recordRuntimeCancel(agentRuntimeLockRecordFromSQLC(row))
		return nil
	}
	affectedTurnID := NilID
	if currentTurnErr == nil {
		affectedTurnID = currentTurn.ID
	}
	if currentTurnErr == nil || hasActiveContexts || input.CancelRuntimeWithoutContinuableTurn {
		params := dbsqlc.RequestAgentRuntimeCancelParams{
			ProjectID: projectID,
			AgentID:   agentID,
		}
		if row, err := qtx.RequestAgentRuntimeCancel(ctx, params); err == nil {
			recordRuntimeCancel(agentRuntimeLockRecordFromSQLC(row))
		} else if errors.Is(err, pgx.ErrNoRows) {
			if err := recordPendingRuntimeCancel(); err != nil {
				return CancelAgentResult{}, err
			}
		} else {
			return CancelAgentResult{}, fmt.Errorf("request runtime cancel: %w", err)
		}
	} else if err := recordPendingRuntimeCancel(); err != nil {
		return CancelAgentResult{}, err
	}
	if err := terminalizeAgentModelCallsForLifecycleUnderAgentLockTx(
		ctx,
		qtx,
		projectID,
		agentID,
		runtimeToCancel.ID,
		input.ReasonCode,
		input.ModelCallMessage,
	); err != nil {
		return CancelAgentResult{}, err
	}
	affected := !isNilID(affectedTurnID) || hasActiveContexts
	cancelInputID := NilID
	if affected {
		cancelSteeringParams := dbsqlc.CancelSteeringAgentInputsForAgentParams{
			ProjectID: projectID,
			AgentID:   agentID,
		}
		if _, err := qtx.CancelSteeringAgentInputsForAgent(ctx, cancelSteeringParams); err != nil {
			return CancelAgentResult{}, fmt.Errorf("cancel steering agent inputs: %w", err)
		}
		controlType := "cancel_current"
		idempotencyKey := fmt.Sprintf("agent-cancel:%s:after:%d", agentID.String(), afterSequence)
		controlInput, insertErr := qtx.InsertControlAgentInput(ctx, dbsqlc.InsertControlAgentInputParams{
			ProjectID:           projectID,
			AgentID:             agentID,
			ActorID:             sqlcIDFromNil(actorID),
			ControlType:         &controlType,
			IdempotencyScope:    sqlcTextFromEmpty("agent_control"),
			InputIdempotencyKey: sqlcTextFromEmpty(idempotencyKey),
			Metadata:            json.RawMessage(`{}`),
		})
		if insertErr != nil {
			return CancelAgentResult{}, fmt.Errorf("insert cancel control input: %w", insertErr)
		}
		inputRecord := agentInputRecordFromControlSQLC(controlInput)
		cancelInputID = inputRecord.ID
		eventRecord, eventErr := appendTypedAgentEventTx(ctx, txNotifications, tx, AppendTypedAgentEventInput{
			ProjectID:      projectID,
			AgentID:        agentID,
			TurnID:         affectedTurnID,
			Kind:           events.KindAgentInput,
			IdempotencyKey: "agent_input:" + inputRecord.ID.String(),
			AgentInputID:   inputRecord.ID,
		})
		if eventErr != nil {
			return CancelAgentResult{}, eventErr
		}
		if err := updateAgentTurnLatestEventQuery(
			ctx,
			qtx,
			projectID,
			agentID,
			affectedTurnID,
			eventRecord.Event.ID,
			NilID,
		); err != nil {
			return CancelAgentResult{}, err
		}
		resolveParams := dbsqlc.ResolveControlAgentInputParams{
			ProjectID:   projectID,
			AgentID:     agentID,
			ID:          inputRecord.ID,
			ControlType: &controlType,
			EventID:     &eventRecord.Event.ID,
		}
		if err := qtx.ResolveControlAgentInput(ctx, resolveParams); err != nil {
			return CancelAgentResult{}, fmt.Errorf("resolve cancel control input: %w", err)
		}
		event = eventRecord.Event
	}
	var interactionRows []dbsqlc.AgentInteractionReadProjection
	if currentTurnErr == nil {
		interactionIDs, cancelErr := qtx.CancelOpenAgentInteractionsForAgent(
			ctx,
			dbsqlc.CancelOpenAgentInteractionsForAgentParams{
				ProjectID:         projectID,
				AgentID:           agentID,
				TurnID:            currentTurn.ID,
				Reason:            input.ReasonCode,
				ResolvedByInputID: sqlcIDFromNil(cancelInputID),
			},
		)
		if cancelErr != nil {
			return CancelAgentResult{}, fmt.Errorf("cancel open agent interactions: %w", cancelErr)
		}
		interactionRows, err = qtx.ListAgentInteractionsByIDs(
			ctx,
			dbsqlc.ListAgentInteractionsByIDsParams{
				ProjectID: projectID,
				AgentID:   agentID,
				Ids:       interactionIDs,
			},
		)
		if err != nil {
			return CancelAgentResult{}, fmt.Errorf("load canceled agent interactions: %w", err)
		}
	}
	for _, row := range interactionRows {
		interaction := agentInteractionRecordFromSQLC(row)
		if !isNilID(interaction.TurnID) {
			params := dbsqlc.MarkAgentWakeupParams{
				ProjectID: interaction.ProjectID,
				AgentID:   interaction.AgentID,
				Metadata:  []byte(`{"reason":"interaction_canceled"}`),
			}
			if err := qtx.MarkAgentWakeup(ctx, params); err != nil {
				return CancelAgentResult{}, err
			}
		}
	}
	var toolCallRows []dbsqlc.CancelNonTerminalToolCallsForAgentRow
	if currentTurnErr == nil {
		processRows, err := qtx.CancelUnresolvedProcessesForAgentTurn(
			ctx,
			dbsqlc.CancelUnresolvedProcessesForAgentTurnParams{
				ProjectID: projectID,
				AgentID:   agentID,
				TurnID:    currentTurn.ID,
			},
		)
		if err != nil {
			return CancelAgentResult{}, fmt.Errorf(
				"cancel unresolved processes with their agent turn: %w",
				err,
			)
		}
		if _, err := qtx.CancelQueuedProcessActionsForAgentTurn(
			ctx,
			dbsqlc.CancelQueuedProcessActionsForAgentTurnParams{
				ProjectID: projectID,
				AgentID:   agentID,
				TurnID:    currentTurn.ID,
			},
		); err != nil {
			return CancelAgentResult{}, fmt.Errorf(
				"cancel queued process actions before grant: %w",
				err,
			)
		}
		if _, err := qtx.CancelAcceptedProcessActionsForAgentTurn(
			ctx,
			dbsqlc.CancelAcceptedProcessActionsForAgentTurnParams{
				ProjectID: projectID,
				AgentID:   agentID,
				TurnID:    currentTurn.ID,
			},
		); err != nil {
			return CancelAgentResult{}, fmt.Errorf(
				"mark accepted process actions unknown after cancel: %w",
				err,
			)
		}
		for _, process := range processRows {
			if err := completeQueuedProcessActionsFailedTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				process.OrgID,
				process.ID,
				"agent_canceled_before_grant",
			); err != nil {
				return CancelAgentResult{}, err
			}
			if err := completeAcceptedProcessActionsWithoutEvidenceTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				process.OrgID,
				process.ID,
				"agent_canceled_after_grant",
			); err != nil {
				return CancelAgentResult{}, err
			}
			if ProcessState(process.State) == ProcessStateUnknown {
				txNotifications.AddDaemonProcessTermination(
					process.MachineID,
					process.ID,
				)
			}
		}
		toolCallRows, err = qtx.CancelNonTerminalToolCallsForAgent(
			ctx,
			dbsqlc.CancelNonTerminalToolCallsForAgentParams{
				ProjectID: projectID,
				AgentID:   agentID,
				TurnID:    currentTurn.ID,
			},
		)
		if err != nil {
			return CancelAgentResult{}, fmt.Errorf("cancel non-terminal tool calls: %w", err)
		}
	}
	for _, row := range toolCallRows {
		resultRecord := toolCallRecordFromCancelSQLC(row)
		if affectedTurnID != resultRecord.TurnID {
			return CancelAgentResult{}, storeerr.ErrStateTransitionConflict
		}
	}
	for _, row := range toolCallRows {
		resultRecord := toolCallRecordFromCancelSQLC(row)
		resultRecord.ResultContentParts, err = canceledToolResultContentParts()
		if err != nil {
			return CancelAgentResult{}, fmt.Errorf("marshal canceled tool result content parts: %w", err)
		}
		if _, err := appendToolResultEventTx(ctx, txNotifications, tx, resultRecord); err != nil {
			return CancelAgentResult{}, fmt.Errorf("append canceled tool result event: %w", err)
		}
	}
	wakeupMetadata, err := marshalJSON(map[string]string{"reason": input.ReasonCode})
	if err != nil {
		return CancelAgentResult{}, fmt.Errorf("marshal canceled agent wakeup metadata: %w", err)
	}
	params := dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: projectID,
		AgentID:   agentID,
		Metadata:  wakeupMetadata,
	}
	if err := qtx.ReconcileAgentWakeup(ctx, params); err != nil {
		return CancelAgentResult{}, fmt.Errorf("reconcile canceled agent wakeup: %w", err)
	}
	if err := cancelOpenAgentWaitsTx(ctx, qtx, projectID, agentID); err != nil {
		return CancelAgentResult{}, err
	}
	if input.ReasonCode == "agent_canceled" {
		if err := handleSubagentTurnEndedTx(ctx, txNotifications, tx, qtx, projectID, agentID, subagentMessage{
			Kind:           SubagentMessageKindCanceled,
			IdempotencyKey: fmt.Sprintf("canceled:%s:%d", agentID.String(), afterSequence),
		}); err != nil {
			return CancelAgentResult{}, err
		}
	}
	return CancelAgentResult{
		Event:                  event,
		RuntimeCancelRequested: runtimeCancelRequested,
		Affected:               affected,
		ActorID:                actorID,
	}, nil
}

func terminalizeAgentModelCallsForLifecycleUnderAgentLockTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeLockID ID,
	reasonCode, contextMessage string,
) error {
	if !isNilID(runtimeLockID) {
		errorDetails, err := marshalJSON(map[string]any{
			"code":            reasonCode,
			"message":         contextMessage,
			"runtime_lock_id": runtimeLockID,
		})
		if err != nil {
			return fmt.Errorf("marshal lifecycle model call details: %w", err)
		}
		if _, err := qtx.CancelRuntimeModelCallContextsForLifecycle(
			ctx,
			dbsqlc.CancelRuntimeModelCallContextsForLifecycleParams{
				ErrorCode:     reasonCode,
				ErrorMessage:  contextMessage,
				ErrorDetails:  errorDetails,
				ProjectID:     projectID,
				AgentID:       agentID,
				RuntimeLockID: runtimeLockID,
			},
		); err != nil {
			return fmt.Errorf("cancel lifecycle model call contexts: %w", err)
		}
	}
	live, err := qtx.AgentHasLiveModelCallContexts(
		ctx,
		dbsqlc.AgentHasLiveModelCallContextsParams{
			ProjectID: projectID,
			AgentID:   agentID,
		},
	)
	if err != nil {
		return fmt.Errorf("verify lifecycle model call contexts: %w", err)
	}
	if live {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}
