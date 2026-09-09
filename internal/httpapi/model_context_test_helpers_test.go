//go:build integration

package httpapi

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func claimNormalModelCallForHTTPTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID, agentID storage.ID,
	runtime executionstore.AgentRuntimeLockRecord,
	openingInputIDs []storage.ID,
	agentConfigID storage.ID,
	inputEventSequence int64,
) executionstore.ModelCallClaim {
	t.Helper()
	claim, err := store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          projectID,
		AgentID:            agentID,
		RuntimeLockID:      runtime.ID,
		OpeningInputIDs:    openingInputIDs,
		AgentConfigID:      agentConfigID,
		InputEventSequence: inputEventSequence,
	})
	if err != nil {
		t.Fatalf("claim normal model call: %v", err)
	}
	return claim
}

type modelCallProviderIdentityForHTTPTest struct {
	Slug       string
	APIFormat  modelprotocol.APIFormat
	APIVariant modelprotocol.APIVariant
}

func loadModelCallProviderIdentityForHTTPTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID storage.ID,
	modelCallContext executionstore.ModelCallContextRecord,
) modelCallProviderIdentityForHTTPTest {
	t.Helper()
	config, found, err := store.Execution().GetAgentConfig(ctx, projectID, modelCallContext.AgentConfigID)
	if err != nil || !found {
		t.Fatalf("load agent config %s: found=%v err=%v", modelCallContext.AgentConfigID, found, err)
	}
	revision, err := store.Models().GetConfiguredModelRevisionDisplay(
		ctx,
		config.OrgID,
		modelCallContext.ConfiguredModelRevisionID,
	)
	if err != nil {
		t.Fatalf("load configured model revision %s: %v", modelCallContext.ConfiguredModelRevisionID, err)
	}
	return modelCallProviderIdentityForHTTPTest{
		Slug:       revision.ProviderModelSlug,
		APIFormat:  revision.APIFormat,
		APIVariant: revision.APIVariant,
	}
}
