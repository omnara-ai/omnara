//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func admitNextAgentInputAndOpenTurnForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentID, runtimeLockID uuid.UUID,
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
	projectID, agentID, runtimeLockID uuid.UUID,
) (executionstore.AdmittedAgentInputTurn, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || runtimeLockID == uuid.Nil {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf(
			"project id, agent id, and runtime lock id are required",
		)
	}
	unit, err := store.Execution().IntegrationBeginUnit(ctx)
	if err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf(
			"begin admit next agent input: %w",
			err,
		)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	if _,
		err := unit.LockAgent(ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID,
			ID: agentID},
		agentexecution.RuntimeAuthority{AgentID: agentID,
			RuntimeLockID: runtimeLockID}); err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, err
	}
	if err := executionstore.IntegrationEnsureRuntimeLockActiveTx(ctx,
		unit,
		projectID,
		agentID,
		runtimeLockID); err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, err
	}
	admitted, err := executionstore.IntegrationAdmitInputs(ctx, unit, projectID, agentID)
	if err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, err
	}
	if err := unit.Commit(ctx, "input admission"); err != nil {
		return executionstore.AdmittedAgentInputTurn{}, false, fmt.Errorf(
			"commit admit next agent input: %w",
			err,
		)
	}
	return admitted, len(admitted.Inputs) > 0, nil
}
