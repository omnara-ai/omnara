package executionstore

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) ClaimNormalModelCall(
	ctx context.Context,
	input ClaimNormalModelCallInput,
) (ModelCallClaim, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		len(input.OpeningInputIDs) == 0 || isNilID(input.AgentConfigID) ||
		input.InputEventSequence <= 0 {
		return ModelCallClaim{}, errors.New(
			"project, agent, runtime, opening inputs, agent config, and input event sequence are required",
		)
	}
	if isNilID(input.SourceModelCallContextID) != isNilID(input.SourceModelOutputID) {
		return ModelCallClaim{}, errors.New(
			"continuation source model context and output must be provided together",
		)
	}
	return s.claimModelCall(ctx, claimModelCallInput{normal: &input})
}

func (s *Store) ClaimCompactionModelCall(
	ctx context.Context,
	input ClaimCompactionModelCallInput,
) (ModelCallClaim, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		input.InputEventSequence <= 0 || input.SourceEventSequenceEnd <= 0 ||
		input.SourceEventSequenceEnd > input.InputEventSequence ||
		isNilID(input.ParentContextID) {
		return ModelCallClaim{}, errors.New(
			"project, agent, runtime, parent context, frontier, and a valid compaction source range are required",
		)
	}
	return s.claimModelCall(ctx, claimModelCallInput{compaction: &input})
}

type claimModelCallInput struct {
	normal     *ClaimNormalModelCallInput
	compaction *ClaimCompactionModelCallInput
}

func (s *Store) claimModelCall(ctx context.Context, input claimModelCallInput) (ModelCallClaim, error) {
	projectID, agentID, runtimeLockID := claimIdentity(input)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ModelCallClaim{}, fmt.Errorf("begin claim model call: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		projectID,
		agentID,
		runtimeLockID,
	); err != nil {
		return ModelCallClaim{}, err
	}

	var contextRow ModelCallContextRecord
	var created bool
	if input.normal != nil {
		contextRow, created, err = claimNormalContextTx(ctx, q, *input.normal)
	} else {
		contextRow, created, err = claimCompactionContextTx(ctx, q, *input.compaction)
	}
	if err != nil {
		return ModelCallClaim{}, err
	}
	if created && (contextRow.State != ModelCallContextStarted ||
		contextRow.RuntimeLockID != runtimeLockID) {
		return ModelCallClaim{}, errors.New("new model call context was not runtime-owned")
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelCallClaim{}, fmt.Errorf("commit model call claim: %w", err)
	}
	return ModelCallClaim{
		Context: contextRow,
		Created: created,
		Claimed: created,
	}, nil
}

func claimIdentity(input claimModelCallInput) (ID, ID, ID) {
	if input.normal != nil {
		return input.normal.ProjectID, input.normal.AgentID, input.normal.RuntimeLockID
	}
	return input.compaction.ProjectID, input.compaction.AgentID, input.compaction.RuntimeLockID
}

func claimNormalContextTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input ClaimNormalModelCallInput,
) (ModelCallContextRecord, bool, error) {
	latestEventSequence, err := q.MaxEventSequence(ctx, dbsqlc.MaxEventSequenceParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
	})
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("load model call event frontier: %w", err)
	}
	if latestEventSequence != input.InputEventSequence {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}

	matches, err := q.OpeningContentInputSetMatchesInputSequence(
		ctx,
		dbsqlc.OpeningContentInputSetMatchesInputSequenceParams{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			InputEventSequence: input.InputEventSequence,
			InputIds:           input.OpeningInputIDs,
		},
	)
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("validate model call opening inputs: %w", err)
	}
	if !matches {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}

	identity := dbsqlc.GetNormalModelCallContextByIdentityParams{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		InputEventSequence: input.InputEventSequence,
	}
	id, err := q.GetNormalModelCallContextByIdentity(ctx, identity)
	if err == nil {
		row, loadErr := loadModelCallContextByID(ctx, q, input.ProjectID, input.AgentID, id)
		if loadErr == nil && row.AgentConfigID != input.AgentConfigID {
			return ModelCallContextRecord{}, false, fmt.Errorf(
				"agent config changed after model call context creation: %w",
				storeerr.ErrStateTransitionConflict,
			)
		}
		return row, false, loadErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, fmt.Errorf("load normal model call context: %w", err)
	}

	work, err := q.NextAgentModelWork(
		ctx,
		dbsqlc.NextAgentModelWorkParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
		},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, fmt.Errorf("validate selected model work: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) || !slices.Equal(work.InputIds, input.OpeningInputIDs) {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}
	if isNilID(input.SourceModelCallContextID) {
		if ModelWorkKind(work.WorkKind) != ModelWorkStart {
			return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
		}
	} else if ModelWorkKind(work.WorkKind) != ModelWorkContinue ||
		work.ModelCallContextID != input.SourceModelCallContextID ||
		work.ModelOutputID != input.SourceModelOutputID {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}

	if live, err := q.AgentHasLiveModelCallContextBeforeFrontier(
		ctx,
		dbsqlc.AgentHasLiveModelCallContextBeforeFrontierParams{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			InputEventSequence: input.InputEventSequence,
		},
	); err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("check older live model call contexts: %w", err)
	} else if live {
		return ModelCallContextRecord{}, false, storeerr.ErrStateTransitionConflict
	}
	configuredModelRevisionID, err := getAgentConfigRevisionForModelCall(
		ctx,
		q,
		input.ProjectID,
		input.AgentConfigID,
	)
	if err != nil {
		return ModelCallContextRecord{}, false, err
	}

	id, err = q.InsertNormalModelCallContext(ctx, dbsqlc.InsertNormalModelCallContextParams{
		InputEventSequence:        input.InputEventSequence,
		ProjectID:                 input.ProjectID,
		AgentID:                   input.AgentID,
		RuntimeLockID:             input.RuntimeLockID,
		ConfiguredModelRevisionID: configuredModelRevisionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("create normal model call context: %w", err)
	}
	row, err := loadModelCallContextByID(ctx, q, input.ProjectID, input.AgentID, id)
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("load normal model call context: %w", err)
	}
	if row.AgentConfigID != input.AgentConfigID {
		return ModelCallContextRecord{}, false, fmt.Errorf(
			"agent config changed before model call context creation: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	return row, true, nil
}

func claimCompactionContextTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input ClaimCompactionModelCallInput,
) (ModelCallContextRecord, bool, error) {
	parent, err := loadModelCallContextByID(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		input.ParentContextID,
	)
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("load parent model call context: %w", err)
	}
	if parent.OperationKind != ModelCallOperationNormal ||
		parent.InputEventSequence != input.InputEventSequence ||
		parent.State != ModelCallContextFailed ||
		parent.RecoveryKind != ModelCallRecoveryCompact {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}
	summarizedThrough := int64(0)
	checkpoint, checkpointErr := q.GetLatestApplicableContextCheckpoint(
		ctx,
		dbsqlc.GetLatestApplicableContextCheckpointParams{
			ProjectID:        input.ProjectID,
			AgentID:          input.AgentID,
			MaxEventSequence: input.InputEventSequence,
		},
	)
	if checkpointErr == nil {
		summarizedThrough = checkpoint.SummarizedThroughEventSequence
	} else if !errors.Is(checkpointErr, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, fmt.Errorf("load prior compaction checkpoint: %w", checkpointErr)
	}
	if input.SourceEventSequenceEnd <= summarizedThrough {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}
	sourceEnd := input.SourceEventSequenceEnd
	identity := dbsqlc.GetCompactionModelCallContextByIdentityParams{
		ProjectID:              input.ProjectID,
		AgentID:                input.AgentID,
		InputEventSequence:     input.InputEventSequence,
		SourceEventSequenceEnd: &sourceEnd,
	}
	id, err := q.GetCompactionModelCallContextByIdentity(ctx, identity)
	if err == nil {
		row, loadErr := loadModelCallContextByID(ctx, q, input.ProjectID, input.AgentID, id)
		if loadErr == nil && row.AgentConfigID != parent.AgentConfigID {
			return ModelCallContextRecord{}, false, fmt.Errorf(
				"agent config changed after compaction context creation: %w",
				storeerr.ErrStateTransitionConflict,
			)
		}
		return row, false, loadErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, fmt.Errorf("load compaction model call context: %w", err)
	}
	configuredModelRevisionID, err := getAgentConfigRevisionForModelCall(
		ctx,
		q,
		input.ProjectID,
		parent.AgentConfigID,
	)
	if err != nil {
		return ModelCallContextRecord{}, false, err
	}

	id, err = q.InsertTriggeredCompactionModelCallContext(
		ctx,
		dbsqlc.InsertTriggeredCompactionModelCallContextParams{
			SourceEventSequenceEnd:    &sourceEnd,
			ProjectID:                 input.ProjectID,
			AgentID:                   input.AgentID,
			RuntimeLockID:             input.RuntimeLockID,
			ParentModelCallContextID:  input.ParentContextID,
			ConfiguredModelRevisionID: configuredModelRevisionID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, storeerr.ErrAgentNotAdvanceable
	}
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("create compaction model call context: %w", err)
	}
	row, err := loadModelCallContextByID(ctx, q, input.ProjectID, input.AgentID, id)
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("load compaction model call context: %w", err)
	}
	if row.AgentConfigID != parent.AgentConfigID {
		return ModelCallContextRecord{}, false, fmt.Errorf(
			"agent config changed before compaction context creation: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	return row, true, nil
}

func (s *Store) ClaimNextModelCallContext(
	ctx context.Context,
	input ClaimNextModelCallContextInput,
) (ModelCallClaim, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) ||
		isNilID(input.PredecessorModelCallContextID) || isNilID(input.RuntimeLockID) {
		return ModelCallClaim{}, errors.New("project, agent, predecessor context, and runtime are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ModelCallClaim{}, fmt.Errorf("begin claim next model call context: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ModelCallClaim{}, err
	}
	predecessor, err := loadModelCallContextByID(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		input.PredecessorModelCallContextID,
	)
	if err != nil {
		return ModelCallClaim{}, fmt.Errorf("load model call context for retry: %w", err)
	}
	contextRow, created, err := claimNextModelCallContextTx(
		ctx,
		q,
		predecessor,
		input.RuntimeLockID,
	)
	if err != nil {
		return ModelCallClaim{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelCallClaim{}, fmt.Errorf("commit next model call context claim: %w", err)
	}
	return ModelCallClaim{
		Context: contextRow,
		Created: created,
		Claimed: created,
	}, nil
}

func claimNextModelCallContextTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	predecessor ModelCallContextRecord,
	runtimeLockID ID,
) (ModelCallContextRecord, bool, error) {
	if predecessor.State == ModelCallContextFailed {
		configuredModelRevisionID, err := getAgentConfigRevisionForModelCall(
			ctx,
			q,
			predecessor.ProjectID,
			predecessor.AgentConfigID,
		)
		if err != nil {
			return ModelCallContextRecord{}, false, err
		}
		id, err := q.InsertNextModelCallContext(ctx, dbsqlc.InsertNextModelCallContextParams{
			ProjectID:                     predecessor.ProjectID,
			AgentID:                       predecessor.AgentID,
			PredecessorModelCallContextID: predecessor.ID,
			MaxRetries:                    MaxModelCallRetriesPerOperation,
			ConfiguredModelRevisionID:     configuredModelRevisionID,
			RuntimeLockID:                 runtimeLockID,
		})
		if err == nil {
			row, loadErr := loadModelCallContextByID(
				ctx,
				q,
				predecessor.ProjectID,
				predecessor.AgentID,
				id,
			)
			return row, loadErr == nil, loadErr
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ModelCallContextRecord{}, false, fmt.Errorf("claim next model call context: %w", err)
		}
	}

	latest, err := loadLatestModelCallContextForOperation(ctx, q, predecessor)
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf(
			"load latest model call context for operation: %w",
			err,
		)
	}
	return latest, false, nil
}

func getAgentConfigRevisionForModelCall(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentConfigID ID,
) (ID, error) {
	revisionID, err := q.GetAgentConfigRevisionForModelCall(
		ctx,
		dbsqlc.GetAgentConfigRevisionForModelCallParams{
			ProjectID:     projectID,
			AgentConfigID: agentConfigID,
		},
	)
	if err != nil {
		return ID{}, fmt.Errorf("resolve configured model revision for model call: %w", err)
	}
	return revisionID, nil
}

func loadLatestModelCallContextForOperation(
	ctx context.Context,
	q *dbsqlc.Queries,
	contextRow ModelCallContextRecord,
) (ModelCallContextRecord, error) {
	var id ID
	var err error
	switch contextRow.OperationKind {
	case ModelCallOperationNormal:
		id, err = q.GetNormalModelCallContextByIdentity(
			ctx,
			dbsqlc.GetNormalModelCallContextByIdentityParams{
				ProjectID:          contextRow.ProjectID,
				AgentID:            contextRow.AgentID,
				InputEventSequence: contextRow.InputEventSequence,
			},
		)
	case ModelCallOperationCompaction:
		id, err = q.GetCompactionModelCallContextByIdentity(
			ctx,
			dbsqlc.GetCompactionModelCallContextByIdentityParams{
				ProjectID:              contextRow.ProjectID,
				AgentID:                contextRow.AgentID,
				InputEventSequence:     contextRow.InputEventSequence,
				SourceEventSequenceEnd: contextRow.SourceEventSequenceEnd,
			},
		)
	default:
		return ModelCallContextRecord{}, fmt.Errorf(
			"unsupported model call operation %q",
			contextRow.OperationKind,
		)
	}
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	return loadModelCallContextByID(ctx, q, contextRow.ProjectID, contextRow.AgentID, id)
}

func (s *Store) GetModelCallContext(
	ctx context.Context,
	projectID, agentID, id ID,
) (ModelCallContextRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return ModelCallContextRecord{}, false, errors.New("project, agent, and model context are required")
	}
	record, err := loadModelCallContextByID(ctx, s.q, projectID, agentID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, nil
	}
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("get model call context: %w", err)
	}
	return record, true, nil
}

func (s *Store) GetNormalModelCallContextForFrontier(
	ctx context.Context,
	projectID, agentID ID,
	inputEventSequence int64,
) (ModelCallContextRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || inputEventSequence <= 0 {
		return ModelCallContextRecord{}, false, errors.New(
			"project, agent, and positive input event sequence are required",
		)
	}
	id, err := s.q.GetNormalModelCallContextByIdentity(
		ctx,
		dbsqlc.GetNormalModelCallContextByIdentityParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			InputEventSequence: inputEventSequence,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, nil
	}
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("get normal model call context for frontier: %w", err)
	}
	record, err := loadModelCallContextByID(ctx, s.q, projectID, agentID, id)
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("load normal model call context for frontier: %w", err)
	}
	return record, true, nil
}

func loadModelCallContextByID(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, id ID,
) (ModelCallContextRecord, error) {
	row, err := q.GetModelCallContext(ctx, dbsqlc.GetModelCallContextParams{
		ProjectID: projectID,
		AgentID:   agentID,
		ID:        id,
	})
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	return modelCallContextRecordFromSQLC(row), nil
}

func loadModelCallContextByIDTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, id ID,
) (ModelCallContextRecord, error) {
	return loadModelCallContextByID(ctx, dbsqlc.New(tx), projectID, agentID, id)
}
