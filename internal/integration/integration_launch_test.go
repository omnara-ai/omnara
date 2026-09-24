package integration

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func testIntegrationLaunchWorkflow(router *IntegrationRouter) *IntegrationLaunchWorkflow {
	return NewIntegrationLaunchWorkflow(router, map[integrationdefinition.Type]IntegrationLauncher{
		integrationdefinition.SlackThread:   EverySlotIntegrationLauncher,
		integrationdefinition.GitHubPR:      EverySlotIntegrationLauncher,
		integrationdefinition.DiscordThread: EverySlotIntegrationLauncher,
	})
}

func freezeTestIntegrationEvents(
	ctx context.Context,
	router *IntegrationRouter,
	lease integrationstore.IntegrationInboxLease,
	events []IntegrationEvent,
) (IntegrationInboxPlan, error) {
	receipt, err := router.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	if len(receipt.Plan) != 0 {
		return decodeIntegrationInboxPlan(receipt.Plan)
	}
	integrationSetup, err := router.integrations.GetProjectIntegrationByID(ctx, receipt.IntegrationID)
	if err != nil {
		return nil, err
	}
	events, err = testIntegrationLaunchWorkflow(router).Decide(ctx, lease, receipt, integrationSetup, events)
	if err != nil {
		return nil, err
	}
	return router.Freeze(ctx, lease, events)
}

func applyTestIntegrationLaunchPolicy(
	t *testing.T,
	receipt integrationstore.IntegrationInboxRecord,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	requests []integrationEventCandidates,
) {
	t.Helper()
	for i := range requests {
		request := &requests[i]
		request.event.Launches = nil
		if integration := request.candidates.Launcher; integration != nil {
			intents, err := EverySlotIntegrationLauncher(t.Context(), IntegrationLaunchContext{
				Receipt: receipt, Integration: *integration,
				Event: request.event, Address: request.address, Candidates: request.candidates,
			})
			require.NoError(t, err)
			request.event.Launches = append(request.event.Launches, intents...)
		}
	}
}
