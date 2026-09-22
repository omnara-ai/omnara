package integration

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func testAppLaunchWorkflow(router *AppRouter) *AppLaunchWorkflow {
	return NewAppLaunchWorkflow(router, map[appdefinition.Type]AppLauncher{
		appdefinition.SlackThread:   EverySlotAppLauncher,
		appdefinition.GitHubPR:      EverySlotAppLauncher,
		appdefinition.DiscordThread: EverySlotAppLauncher,
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
	appSetup, err := router.integrations.GetProjectAppByID(ctx, receipt.AppID)
	if err != nil {
		return nil, err
	}
	events, err = testAppLaunchWorkflow(router).Decide(ctx, lease, receipt, appSetup, events)
	if err != nil {
		return nil, err
	}
	return router.Freeze(ctx, lease, events)
}

func applyTestAppLaunchPolicy(
	t *testing.T,
	receipt integrationstore.IntegrationInboxRecord,
	appSetup integrationstore.ProjectAppRecord,
	requests []appEventCandidates,
) {
	t.Helper()
	for i := range requests {
		request := &requests[i]
		request.event.Launches = nil
		if app := request.candidates.Launcher; app != nil {
			intents, err := EverySlotAppLauncher(t.Context(), AppLaunchContext{
				Receipt: receipt, App: *app,
				Event: request.event, Address: request.address, Candidates: request.candidates,
			})
			require.NoError(t, err)
			request.event.Launches = append(request.event.Launches, intents...)
		}
	}
}
