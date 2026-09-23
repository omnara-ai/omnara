//go:build integration

package apps

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestAppLaunchWorkflowRejectsUntrustedRecipients(t *testing.T) {
	f := newChoiceJourney(t, 1)
	ctx := t.Context()
	_, _, err := f.store.Apps().AcceptAppReceipt(ctx, appstore.VerifiedAppReceipt{
		ProjectID: f.ids.ProjectID, AppID: f.appSetup.ID, ReceiptKey: "untrusted-launch", Payload: []byte(`{}`),
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
			workflow := NewAppLaunchWorkflow(f.consumer.router, map[appdefinition.Type]AppLauncher{
				appdefinition.SlackThread: func(_ context.Context, input AppLaunchContext) ([]AppLaunchIntent, error) {
					calls++
					require.Equal(t, f.app.ID, input.App.ID)
					if test.foreignLauncher {
						return []AppLaunchIntent{{AppID: uuid.New(), ProfileID: f.profiles[0].ID}}, nil
					}
					return nil, nil
				},
			})
			result, err := workflow.Decide(ctx, receipt.Lease(), receipt, f.appSetup, []AppEvent{event})
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

func TestAppLaunchExistingRecipientsArchiveBeforeDecisionOrAdmission(t *testing.T) {
	for _, archiveBeforeDecision := range []bool{true, false} {
		name := "after freeze"
		if archiveBeforeDecision {
			name = "before decision"
		}
		t.Run(name, func(t *testing.T) {
			pool, store, ids, appID := appWorkerFixture(t)
			ctx := t.Context()
			_, err := pool.Exec(ctx, `INSERT INTO org_memberships(org_id,user_id,role,created_at)
				VALUES($1,$2,'owner',now()) ON CONFLICT DO NOTHING`, ids.OrgID, ids.ProviderAdminUserID)
			require.NoError(t, err)
			principal := identitystore.NewUserPrincipal(ids.ProviderAdminUserID)
			base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			agentIDs := make([]uuid.UUID, 2)
			var slots []appstore.AppLaunchSlot
			for i, key := range []string{"archived", "active"} {
				launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
					ProjectID: ids.ProjectID, AgentConfigID: base.ID, LaunchedBy: principal,
				})
				require.NoError(t, err)
				agentIDs[i] = launched.Agent.ID
				slots = append(slots, appstore.AppLaunchSlot{Key: key, AgentID: &agentIDs[i]})
			}
			inbox := store.Apps()
			_, err = inbox.UpdateProjectApp(ctx, appID, appstore.SaveProjectAppInput{
				OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", AppType: appdefinition.SlackThread,
				Settings: appstore.ProjectAppSettings{Launcher: &appstore.AppLauncher{
					Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123", Slots: slots,
				}},
			})
			require.NoError(t, err)
			archive := func() {
				_, _, err := store.Execution().ArchiveAgent(ctx, ids.ProjectID, agentIDs[0], principal)
				require.NoError(t, err)
			}
			if archiveBeforeDecision {
				archive()
			}
			_, _, err = inbox.AcceptAppReceipt(ctx, appstore.VerifiedAppReceipt{
				ProjectID: ids.ProjectID, AppID: appID, ReceiptKey: "archived-recipient", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			receipt, found, err := inbox.ClaimAppInbox(ctx, appstore.ClaimAppInboxInput{
				ProjectID: ids.ProjectID, AppID: appID, LeaseDuration: time.Minute,
			})
			require.NoError(t, err)
			require.True(t, found)
			event := AppEvent{
				Event: appdefinition.Event{
					Scope: appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
					Kind:  "message", Mentioned: true,
				},
				SemanticKey: "message:archive", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
				Actor: appTestActor(t, appID, "U123"),
			}
			router := NewAppRouter(store.Execution(), inbox)
			plan, err := freezeTestAppEvents(ctx, router, receipt.Lease(), []AppEvent{event})
			require.NoError(t, err)
			if archiveBeforeDecision {
				require.Len(t, plan, 1, "known archived destinations are omitted")
				for _, slot := range plan {
					require.Equal(t, agentIDs[1], slot.AgentID)
				}
			} else {
				require.Len(t, plan, 2)
				archive()
			}
			admitted, err := router.Admit(ctx, receipt.Lease())
			require.NoError(t, err)
			require.Len(t, admitted, len(plan))
			var delivered uuid.UUID
			for _, result := range admitted {
				require.NotNil(t, result.Input)
				if plan[result.Slot].AgentID == agentIDs[0] {
					require.Equal(t, executionstore.InboxInputSkipAgentArchived, result.Input.Skipped)
					require.False(t, result.Input.Created)
				} else {
					require.True(t, result.Input.Created)
					delivered = result.Input.AgentInput.ID
				}
			}
			require.NotEqual(t, uuid.Nil, delivered)
			saved, err := inbox.GetAppInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, appstore.AppInboxCompleted, saved.State)
			replayed, err := router.Admit(ctx, receipt.Lease())
			require.NoError(t, err)
			for _, result := range replayed {
				require.False(t, result.Input.Created)
				if plan[result.Slot].AgentID == agentIDs[1] {
					require.Equal(t, delivered, result.Input.AgentInput.ID)
				}
			}
			var count int
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content'`, ids.ProjectID).Scan(&count))
			require.Equal(t, 1, count, "the live sibling receives exactly one input across replay")
		})
	}
}
