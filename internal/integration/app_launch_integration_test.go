//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestAppLaunchWorkflowRejectsUntrustedRecipients(t *testing.T) {
	// Both cases exercise the same receipt lease without admitting any work.
	f := newChoiceJourney(t, 1, true)
	ctx := t.Context()
	_, _, err := f.store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, ConnectionID: f.connection.ID, ReceiptKey: "untrusted-launch", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	receipt := f.claim()
	for _, test := range []struct {
		name            string
		foreignLauncher bool
	}{{"provider directives", false}, {"foreign app intent", true}} {
		t.Run(test.name, func(t *testing.T) {
			event := f.event
			event.Directed = true
			event.Launches = []AppLaunchIntent{{AppID: uuid.New(), AgentID: uuid.New()}}
			calls := 0
			workflow := NewAppLaunchWorkflow(f.consumer.router, map[string]AppLauncher{
				appdefinition.Slack: func(_ context.Context, input AppLaunchContext) ([]AppLaunchIntent, error) {
					calls++
					require.Equal(t, f.app.ID, input.App.ID)
					if test.foreignLauncher {
						return []AppLaunchIntent{{AppID: uuid.New(), ProfileID: f.profiles[0].ID}}, nil
					}
					return nil, nil
				},
			})
			result, err := workflow.Decide(ctx, receipt.Lease(), receipt, f.connection, []AppEvent{event})
			require.Equal(t, 1, calls)
			if test.foreignLauncher {
				require.ErrorContains(t, err, "returned an intent for another app")
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.Len(t, result, 1)
				require.False(t, result[0].Directed)
				require.Empty(t, result[0].Launches)
				require.Equal(t, event.ContentBlocks, result[0].ContentBlocks)
			}
		})
	}
	var agents int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Zero(t, agents)
}
