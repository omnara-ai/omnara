//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func currentModelRevisionForConfig(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentConfigID uuid.UUID,
) uuid.UUID {
	t.Helper()
	config, found, err := store.Execution().GetAgentConfig(ctx, projectID, agentConfigID)
	if err != nil || !found {
		t.Fatalf("load agent config %s: found=%v err=%v", agentConfigID, found, err)
	}
	configuredModel, err := store.Models().GetConfiguredModel(ctx, config.OrgID, config.ConfiguredModelID)
	if err != nil {
		t.Fatalf("load configured model %s: %v", config.ConfiguredModelID, err)
	}
	return configuredModel.CurrentRevisionID
}

func modelProviderSlugForContext(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentID, modelCallContextID uuid.UUID,
) string {
	t.Helper()
	modelContext, found, err := store.Execution().GetModelCallContext(
		ctx,
		projectID,
		agentID,
		modelCallContextID,
	)
	if err != nil || !found {
		t.Fatalf("load model call context %s: found=%v err=%v", modelCallContextID, found, err)
	}
	revision, err := store.Models().GetConfiguredModelRevisionForUse(
		ctx,
		modelContext.OrgID,
		modelContext.ConfiguredModelRevisionID,
	)
	if err != nil {
		t.Fatalf("load configured model revision: %v", err)
	}
	return revision.ProviderModelSlug
}
