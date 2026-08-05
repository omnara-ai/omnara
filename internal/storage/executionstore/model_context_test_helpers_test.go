//go:build integration

package executionstore_test

import (
	"context"
	"testing"
)

func currentModelRevisionForConfig(t *testing.T, ctx context.Context, store *Store, projectID, agentConfigID ID) ID {
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

func currentModelGrantForConfig(t *testing.T, ctx context.Context, store *Store, projectID, agentConfigID ID) ID {
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

func modelProviderSlugForContext(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentID, modelCallContextID ID,
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
