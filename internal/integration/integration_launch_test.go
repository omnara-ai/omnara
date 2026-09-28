package integration

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"testing"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func testIntegrationLaunchWorkflow(router *IntegrationRouter) *IntegrationLaunchWorkflow {
	return NewIntegrationLaunchWorkflow(router, map[integrationdefinition.Kind]IntegrationLauncher{
		integrationdefinition.SlackThread:   testProfileIntegrationLauncher,
		integrationdefinition.GitHubPR:      testProfileIntegrationLauncher,
		integrationdefinition.DiscordThread: testProfileIntegrationLauncher,
	}, nil)
}

func freezeTestIntegrationEvent(
	ctx context.Context,
	router *IntegrationRouter,
	lease integrationstore.IntegrationInboxLease,
	event *IntegrationEvent,
) (IntegrationInboxPlan, error) {
	receipt, err := router.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return IntegrationInboxPlan{}, err
	}
	if len(receipt.Plan) != 0 {
		return decodeIntegrationInboxPlan(receipt.Plan)
	}
	integrationSetup, err := router.integrations.GetIntegrationByID(ctx, receipt.IntegrationID)
	if err != nil {
		return IntegrationInboxPlan{}, err
	}
	if event != nil {
		event, err = testIntegrationLaunchWorkflow(router).Decide(ctx, lease, receipt, integrationSetup, *event)
		if err != nil {
			return IntegrationInboxPlan{}, err
		}
	}
	return router.Freeze(ctx, lease, event)
}

func applyTestIntegrationLaunchPolicy(
	t *testing.T, receipt integrationstore.IntegrationInboxRecord,
	integrationSetup integrationstore.IntegrationRecord, request *integrationEventCandidates,
) {
	t.Helper()
	request.event.Launches = nil
	if integration := request.candidates.Launcher; integration != nil {
		intents, err := testProfileIntegrationLauncher(t.Context(), IntegrationLaunchContext{
			Receipt: receipt, Integration: *integration, Event: request.event,
			Address: request.address, Candidates: request.candidates,
		})
		require.NoError(t, err)
		request.event.Launches = intents
	}
}

func testLaunchSettings(kind integrationdefinition.Kind, trigger string) json.RawMessage {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	if kind == integrationdefinition.GitHubPR {
		return integrationtest.GitHubSettings(id, trigger, "")
	}
	return integrationtest.ChatSettings("", id)
}
func testProfileIntegrationLauncher(
	ctx context.Context, input IntegrationLaunchContext,
) ([]IntegrationLaunchIntent, error) {
	if input.Integration.IntegrationKind == integrationdefinition.GitHubPR {
		return GitHubIntegrationLauncher(ctx, input)
	}
	if !integrationLaunchEligible(input) {
		return nil, nil
	}
	profiles, err := integrationdefinition.ChatLaunchProfiles(input.Integration.Settings)
	if err != nil {
		return nil, err
	}
	if len(profiles) != 1 {
		return nil, nil
	} // Real chat menus are exercised by the choice journey.
	return []IntegrationLaunchIntent{{
		IntegrationID: input.Integration.ID, LaunchKey: integrationdefinition.ProfileLaunchKey, ProfileID: profiles[0],
	}}, nil
}
