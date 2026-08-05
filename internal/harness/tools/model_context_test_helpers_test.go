//go:build integration

package tools

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func claimNormalModelCallForToolsTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID, agentID storage.ID,
	runtime executionstore.AgentRuntimeLockRecord,
	openingInputIDs []storage.ID,
	agentConfigID storage.ID,
	inputEventSequence int64,
	sourceModelCallContextID storage.ID,
) executionstore.ModelCallClaim {
	t.Helper()
	sourceModelOutputID := storage.NilID
	if sourceModelCallContextID != storage.NilID {
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

func currentModelGrantForConfig(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID, agentConfigID storage.ID,
) storage.ID {
	t.Helper()
	config, found, err := store.Execution().GetAgentConfig(ctx, projectID, agentConfigID)
	if err != nil || !found {
		t.Fatalf("load agent config %s: found=%v err=%v", agentConfigID, found, err)
	}
	grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		config.OrgID,
		projectID,
		config.ConfiguredModelID,
	)
	if err != nil {
		t.Fatalf("load project model grant for configured model %s: %v", config.ConfiguredModelID, err)
	}
	return grant.ID
}
