//go:build integration

package tools

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func claimNormalModelCallForToolsTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID, agentID uuid.UUID,
	runtime executionstore.AgentRuntimeLockRecord,
	openingInputIDs []uuid.UUID,
	agentConfigID uuid.UUID,
	inputEventSequence int64,
	sourceModelCallContextID uuid.UUID,
) executionstore.ModelCallClaim {
	t.Helper()
	sourceModelOutputID := uuid.Nil
	if sourceModelCallContextID != uuid.Nil {
		output, found, err := store.Execution().GetModelOutputForContext(
			ctx,
			projectID,
			agentID,
			sourceModelCallContextID,
		)
		if err != nil {
			t.Fatalf("load source model output: %v", err)
		}
		if !found {
			t.Fatalf("source model context %s has no output", sourceModelCallContextID)
		}
		sourceModelOutputID = output.ID
	}
	claim, err := store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:                projectID,
		AgentID:                  agentID,
		RuntimeLockID:            runtime.ID,
		OpeningInputIDs:          openingInputIDs,
		AgentConfigID:            agentConfigID,
		InputEventSequence:       inputEventSequence,
		SourceModelCallContextID: sourceModelCallContextID,
		SourceModelOutputID:      sourceModelOutputID,
	})
	if err != nil {
		t.Fatalf("claim normal model call: %v", err)
	}
	return claim
}
