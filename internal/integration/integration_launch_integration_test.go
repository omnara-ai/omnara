//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestIntegrationLaunchWorkflowRejectsUntrustedRecipients(t *testing.T) {
	f := newChoiceJourney(t, 1)
	ctx := t.Context()
	_, _, err := f.store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID:     f.ids.ProjectID,
		IntegrationID: f.integrationSetup.ID,
		ReceiptKey:    "untrusted-launch",
		Payload:       []byte(`{}`),
	})
	require.NoError(t, err)
	receipt := f.claim()
	for _, test := range []struct {
		name            string
		foreignLauncher bool
	}{{"provider directives", false}, {"foreign integration intent", true}} {
		t.Run(test.name, func(t *testing.T) {
			event := f.event
			event.Directed = true
			event.Launches = []IntegrationLaunchIntent{{IntegrationID: uuid.New(), ProfileID: uuid.New()}}
			calls := 0
			workflow := NewIntegrationLaunchWorkflow(f.consumer.router, map[integrationdefinition.Kind]IntegrationLauncher{
				integrationdefinition.SlackThread: func(
					_ context.Context,
					input IntegrationLaunchContext,
				) ([]IntegrationLaunchIntent, error) {
					calls++
					require.Equal(t, f.integration.ID, input.Integration.ID)
					if test.foreignLauncher {
						return []IntegrationLaunchIntent{{IntegrationID: uuid.New(), ProfileID: f.profiles[0].ID}}, nil
					}
					return nil, nil
				},
			}, nil)
			result, _, err := workflow.Decide(ctx, receipt.Lease(), receipt, f.integrationSetup, event)
			require.Equal(t, 1, calls)
			if test.foreignLauncher {
				require.ErrorContains(t, err, "returned an intent for another integration")
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.False(t, result.Directed)
				require.Empty(t, result.Launches)
				require.Equal(t, event.ContentBlocks, result.ContentBlocks)
			}
		})
	}
	var agents int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Zero(t, agents)
}

func TestIntegrationLaunchExistingRecipientsArchiveBeforeDecisionOrAdmission(t *testing.T) {
	for _, archiveBeforeDecision := range []bool{true, false} {
		name := "after freeze"
		if archiveBeforeDecision {
			name = "before decision"
		}
		t.Run(name, func(t *testing.T) {
			pool, store, ids, integrationID := integrationWorkerFixture(t)
			ctx := t.Context()
			principal := identitystore.NewUserPrincipal(ids.ProviderAdminUserID)
			base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			agentIDs := make([]uuid.UUID, 2)
			for i := range agentIDs {
				launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
					ProjectID: ids.ProjectID, AgentConfigID: base.ID, LaunchedBy: principal,
				})
				require.NoError(t, err)
				agentIDs[i] = launched.Agent.ID
			}
			inbox := store.Integrations()
			integration, err := inbox.UpdateIntegration(ctx, integrationID, integrationstore.SaveIntegrationInput{
				OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationKind: integrationdefinition.SlackThread,
				Settings: integrationstore.IntegrationSettings(`{}`),
			})
			require.NoError(t, err)
			for _, agentID := range agentIDs {
				createTestIntegrationSubscription(t, store, integration, agentID, `{"channel_id":"C123","thread_ts":"1.2"}`)
			}
			archive := func() {
				_, _, err := store.Execution().ArchiveAgent(ctx, ids.ProjectID, agentIDs[0], principal)
				require.NoError(t, err)
			}
			if archiveBeforeDecision {
				archive()
			}
			_, _, err = inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "archived-recipient", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
			})
			require.NoError(t, err)
			require.True(t, found)
			event := IntegrationEvent{
				Event: integrationdefinition.Event{
					Scope: integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
					Kind:  "message", Mentioned: true,
				},
				SemanticKey: "message:archive", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
				Actor: integrationTestActor(t, integration, "U123"),
			}
			router := NewIntegrationRouter(store.Execution(), inbox)
			plan, err := freezeTestIntegrationEvent(ctx, router, receipt.Lease(), &event)
			require.NoError(t, err)
			if archiveBeforeDecision {
				require.Len(t, plan.Recipients, 1, "known archived destinations are omitted")
				for _, recipient := range plan.Recipients {
					require.Equal(t, agentIDs[1], recipient.AgentID)
				}
			} else {
				require.Len(t, plan.Recipients, 2)
				archive()
			}
			admitted, err := router.Admit(ctx, receipt.Lease(), nil)
			require.NoError(t, err)
			require.Len(t, admitted, len(plan.Recipients))
			var delivered uuid.UUID
			for _, result := range admitted {
				require.NotNil(t, result.Input)
				if plan.Recipients[result.Recipient].AgentID == agentIDs[0] {
					require.Equal(t, executionstore.InboxInputSkipAgentArchived, result.Input.Skipped)
					require.False(t, result.Input.Created)
				} else {
					require.True(t, result.Input.Created)
					delivered = result.Input.AgentInput.ID
				}
			}
			require.NotEqual(t, uuid.Nil, delivered)
			saved, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, saved.State)
			replayed, err := router.Admit(ctx, receipt.Lease(), nil)
			require.NoError(t, err)
			for _, result := range replayed {
				require.False(t, result.Input.Created)
				if plan.Recipients[result.Recipient].AgentID == agentIDs[1] {
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
