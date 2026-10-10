package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func shapeOwnedWork(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID uuid.UUID,
	work agentexecution.OwnedWork,
) (ClaimedAgentWork, error) {
	result := ClaimedAgentWork{ProjectID: projectID, AgentID: agentID}
	if work.Released || work.Selection.Work == agentexecution.WorkNone {
		return result, nil
	}
	q := dbsqlc.New(unit.DB())
	agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: agentID})
	if err != nil {
		return result, err
	}
	result.OrgID = agent.OrgID
	runtime, err := q.GetAgentRuntimeLockForRelease(
		ctx,
		dbsqlc.GetAgentRuntimeLockForReleaseParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        work.RuntimeID,
		},
	)
	if err != nil {
		return result, err
	}
	result.RuntimeLock = agentRuntimeLockRecordFromSQLC(runtime)
	if selected := work.Selection.Tool; selected != nil {
		result.Kind = AgentWorkTool
		result.Tool = ClaimedToolWork{TurnID: selected.TurnID, ModelCallContextID: selected.SourceContextID,
			ModelOutputID: selected.OutputID, SourceEventID: selected.SourceEventID}
		result.Tool.Prepared, err = prepareToolWorkTx(ctx, q, projectID, agentID, result.Tool)
		return result, err
	}
	selected := work.Selection.Model
	result.Kind = AgentWorkModel
	result.Model = ClaimedModelWork{Kind: ModelWorkKind(selected.Kind), TurnID: selected.TurnID,
		InputIDs: selected.Opening.InputIDs, OpeningEventSequence: selected.Opening.EventSequence}
	if selected.Kind == agentexecution.ModelResume {
		result.Model.ModelCallContextID = selected.SourceContextID
	}
	if selected.Kind == agentexecution.ModelContinue {
		result.Model.SourceModelCallContextID = selected.SourceContextID
		result.Model.SourceModelOutputID = selected.SourceOutputID
	}
	if work.Model.Context.ID != uuid.Nil {
		prepared, shapeErr := shapePreparedModel(ctx, unit, projectID, agentID, work.Model)
		if shapeErr != nil {
			return result, shapeErr
		}
		result.Model.Prepared = &prepared
	}
	if work.Admission.TurnID != uuid.Nil {
		result.Model.AdmittedInputTurn, err = shapeAdmission(ctx, q, projectID, agentID, work.Admission)
		if err != nil {
			return result, err
		}
	}

	return result, nil
}

func shapePreparedModel(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID uuid.UUID,
	prepared agentexecution.PreparedModel,
) (PreparedNormalModelCall, error) {
	q := dbsqlc.New(unit.DB())
	record, err := loadModelCallContextByID(ctx, q, projectID, agentID, prepared.Context.ID)
	if err != nil {
		return PreparedNormalModelCall{}, err
	}
	config, err := q.GetAgentConfig(
		ctx,
		dbsqlc.GetAgentConfigParams{ProjectID: projectID, ID: record.AgentConfigID},
	)
	if err != nil {
		return PreparedNormalModelCall{}, err
	}
	result := PreparedNormalModelCall{
		Claim: ModelCallClaim{Context: record, Created: prepared.Created, Claimed: prepared.Claimed},
		Snapshot: AgentConfigSnapshotRecord{
			AgentConfig:        agentConfigRecordFromSQLC(config),
			InputEventSequence: record.InputEventSequence,
		},
	}
	if prepared.Claimed {
		result.ContextData, err = loadModelContextDataTx(ctx, q, record)
	}
	return result, err
}

func shapeAdmission(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	admission agentexecution.InputAdmission,
) (AdmittedAgentInputTurn, error) {
	var result AdmittedAgentInputTurn
	if len(admission.Inputs) == 0 {
		return result, nil
	}
	turn, err := q.GetAgentTurn(
		ctx,
		dbsqlc.GetAgentTurnParams{ProjectID: projectID, AgentID: agentID, ID: admission.TurnID},
	)
	if err != nil {
		return result, err
	}
	result.Turn = AgentTurnRecord{
		ID:                    turn.ID,
		ProjectID:             projectID,
		AgentID:               agentID,
		TurnSequence:          turn.TurnSequence,
		LatestEventID:         turn.LatestEventID,
		LatestSemanticEventID: turn.LatestSemanticEventID,
	}
	for _, admitted := range admission.Inputs {
		row, err := q.GetAgentInput(
			ctx,
			dbsqlc.GetAgentInputParams{ProjectID: projectID, AgentID: agentID, ID: admitted.ID},
		)
		if err != nil {
			return result, err
		}
		result.Inputs = append(result.Inputs, agentInputRecordFromGetSQLC(row))
		result.Events = append(
			result.Events,
			events.Event{
				ID:             admitted.Event.ID,
				AgentID:        agentID,
				Sequence:       admitted.Event.Sequence,
				Kind:           events.KindAgentInput,
				At:             admitted.Event.Time,
				IdempotencyKey: "agent_input:" + admitted.ID.String(),
			},
		)
	}
	return result, nil
}
