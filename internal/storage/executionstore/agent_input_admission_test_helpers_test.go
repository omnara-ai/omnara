//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func admitNextAgentInputAndOpenTurnForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentID, runtimeLockID ID,
) (executionstore.AdmittedAgentInputTurn, bool) {
	t.Helper()
	admitted, found, err := admitNextAgentInputAndOpenTurnForTestErr(
		ctx,
		store,
		projectID,
		agentID,
		runtimeLockID,
	)
	if err != nil {
		t.Fatalf("admit next agent input and open turn: %v", err)
	}
	return admitted, found
}

func admitNextAgentInputAndOpenTurnForTestErr(
	ctx context.Context,
	store *Store,
	projectID, agentID, runtimeLockID ID,
) (executionstore.AdmittedAgentInputTurn, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(runtimeLockID) {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf(
			"project id, agent id, and runtime lock id are required",
		)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf("begin admit next agent input: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID}); err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf("lock agent for input admission: %w", err)
	}
	if err := executionstore.IntegrationEnsureRuntimeLockActiveTx(ctx, tx, projectID, agentID, runtimeLockID); err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, err
	}
	if incomplete, err := qtx.AgentHasIncompleteToolBatch(
		ctx,
		dbsqlc.AgentHasIncompleteToolBatchParams{
			ProjectID: projectID,
			AgentID:   agentID,
		},
	); err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf("check incomplete tool batch: %w", err)
	} else if incomplete {
		return executionstore.AdmittedAgentInputTurn{}, false, nil
	}
	selected, err := executionstore.IntegrationSelectLockedSteeringAgentInputsForAdmissionTx(ctx, qtx, projectID, agentID)
	if err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, err
	}
	if len(selected) == 0 {
		selected, err = executionstore.IntegrationSelectLockedQueuedAgentInputForAdmissionTx(ctx, qtx, projectID, agentID)
		if err != nil {
			return executionstore.AdmittedAgentInputTurn{}, false, err
		}
	}
	if len(selected) == 0 {
		return executionstore.AdmittedAgentInputTurn{}, false, nil
	}
	admitted, err := executionstore.IntegrationAdmitLockedAgentInputsAndOpenTurnTx(
		ctx,
		notifications.NewTxNotifications(),
		tx,
		qtx,
		executionstore.IntegrationAdmitAgentInputAndOpenTurnInput{
			ProjectID: projectID,
			AgentID:   agentID,
		},
		selected,
	)
	if err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf("commit admit next agent input: %w", err)
	}
	return admitted, true, nil
}
