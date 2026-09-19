package integration

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

// Tests of admission use an explicit app policy rather than teaching the generic
// planner to launch every matching setup. Provider choice tests supply their own.
func testAppLaunchWorkflow(router *AppRouter) *AppLaunchWorkflow {
	return NewAppLaunchWorkflow(router, map[string]AppLauncher{
		appdefinition.Slack:   EverySlotAppLauncher,
		appdefinition.GitHub:  EverySlotAppLauncher,
		appdefinition.Discord: EverySlotAppLauncher,
	})
}

func freezeTestAppEvents(
	ctx context.Context,
	router *AppRouter,
	lease integrationstore.IntegrationInboxLease,
	events []AppEvent,
) (AppInboxPlan, error) {
	receipt, err := router.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	if len(receipt.Plan) != 0 {
		return decodeAppInboxPlan(receipt.Plan)
	}
	connection, err := router.integrations.GetIntegrationConnectionByID(ctx, receipt.ConnectionID)
	if err != nil {
		return nil, err
	}
	events, err = testAppLaunchWorkflow(router).Decide(ctx, lease, receipt, connection, events)
	if err != nil {
		return nil, err
	}
	return router.Freeze(ctx, lease, events)
}

func applyTestAppLaunchPolicy(
	t *testing.T,
	receipt integrationstore.IntegrationInboxRecord,
	connection integrationstore.IntegrationConnectionRecord,
	requests []appEventCandidates,
) {
	t.Helper()
	for i := range requests {
		request := &requests[i]
		request.event.Launches = nil
		for _, app := range request.candidates.Launchers {
			intents, err := EverySlotAppLauncher(t.Context(), AppLaunchContext{
				Receipt: receipt, Connection: connection, App: app,
				Event: request.event, Address: request.address, Candidates: request.candidates,
			})
			require.NoError(t, err)
			request.event.Launches = append(request.event.Launches, intents...)
		}
	}
}
