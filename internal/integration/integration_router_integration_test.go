//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { integrationdb.RunTestMain(m) }

func TestIntegrationRouterConcurrentFreezePartialRecoveryAndPinnedConfig(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, integrationID := integrationWorkerFixture(t)
	inbox := store.Integrations()
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: "+strings.Repeat("Review carefully. ", 18000)+"End.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "review", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	integration, err := inbox.UpdateIntegration(ctx, integrationID, integrationstore.SaveIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationKind: integrationdefinition.SlackThread,
		Settings: integrationtest.ChatSettings("", profile.ID),
	})
	require.NoError(t, err)
	existing, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: ids.ProjectID, AgentConfigID: base.ID,
		LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
	})
	require.NoError(t, err)
	createTestIntegrationSubscription(t, store, integration, existing.Agent.ID, `{"channel_id":"C123"}`)
	router := NewIntegrationRouter(store.Execution(), inbox)
	claim := func(key string) integrationstore.IntegrationInboxRecord {
		_, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		return receipt
	}
	receipts := []integrationstore.IntegrationInboxRecord{claim("first"), claim("competing")}
	placeholder := uuid.New()
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: "message", Mentioned: true, Scope: integrationdefinition.Scope{
			Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"},
		}},
		SemanticKey: "message:1", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"},{"type":"media_ref","artifact_id":"` + placeholder.String() + `"}]`),
		Files: []executionstore.InboxPlannedFile{failedIntegrationFile(placeholder, []byte("review"))},
		Actor: integrationTestActor(t, integration, "U123"),
	}
	plans := make([]IntegrationInboxPlan, 2)
	failures := make([]error, 2)
	var wg sync.WaitGroup
	for i := range receipts {
		wg.Go(func() { plans[i], failures[i] = freezeTestIntegrationEvent(ctx, router, receipts[i].Lease(), &event) })
	}
	wg.Wait()
	winner := 0
	if failures[0] != nil {
		winner = 1
	}
	require.NoError(t, failures[winner])
	require.ErrorIs(t, failures[1-winner], integrationstore.ErrIntegrationLaunchReserved)
	plan, receipt := plans[winner], receipts[winner]
	require.Len(t, plan.Recipients, 2)
	var launchKey, inputKey string
	for key, recipient := range plan.Recipients {
		if recipient.Launch != nil {
			launchKey = key
		} else {
			inputKey = key
		}
	}
	require.NotEmpty(t, launchKey)
	require.NotEmpty(t, inputKey)
	frozen, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Less(t, len(frozen.Plan), 16*1024, "full profile configs are not copied into the work plan")
	_, files, err := plan.Message.RecipientContent(plan.Recipients[inputKey].ArtifactIDs)
	require.NoError(t, err)
	results, err := router.Admit(
		ctx, receipt.Lease(), map[string][]artifactstore.PreparedArtifact{inputKey: {*files[0].Expected}},
	)
	require.Error(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Input.Created)
	// Change the live profile config after partial admission: recovery keeps its frozen base/config IDs.
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	compiled.Instruction = "edited profile"
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	next, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID: ids.ProjectID, ConfiguredModelID: base.ConfiguredModelID,
		CompiledDefinition: encoded.CanonicalJSON, EffectiveDefinitionHash: encoded.Hash,
	})
	require.NoError(t, err)
	_, err = store.Execution().RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
		ProjectID: ids.ProjectID, ProfileID: profile.ID, ExpectedCurrentConfigID: base.ID,
		ConfigID: next.ID, IdempotencyKey: "retarget",
	})
	require.NoError(t, err)
	require.NoError(t, inbox.WithIntegrationInboxLease(
		ctx, receipt.Lease(), func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.Retry(ctx, time.Second, "partial upload")
		},
	))
	_, err = pool.Exec(ctx,
		`UPDATE integration_inbox SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	retry, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, receipt.ID, retry.ID)
	require.NotEqual(t, receipt.ClaimToken, retry.ClaimToken)
	recovered, err := router.Freeze(ctx, retry.Lease(), nil)
	require.NoError(t, err)
	require.JSONEq(t, string(githubEventJSON(t, plan)), string(githubEventJSON(t, recovered)))
	uploads := &integrationConsumerUploads{}
	provider := &integrationConsumerProvider{
		file: IntegrationInboxFile{Content: []byte("review"), ContentType: "text/plain"},
	}
	consumer := NewIntegrationInboxConsumer(
		router, inbox, uploads, map[string]IntegrationInboxProvider{"slack": provider},
		nil, testIntegrationLaunchWorkflow(router),
	)
	results, err = consumer.Consume(ctx, retry.Lease())
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, plan.Recipients[launchKey].ArtifactIDs, uploads.probed, "retry prepares only the pending recipient")
	require.Equal(t, 1, provider.downloads)
	require.Equal(t, 1, uploads.uploads)
	require.Zero(t, provider.expansions)
	for _, result := range results {
		if result.Launch != nil {
			require.Equal(t, plan.Recipients[launchKey].AgentID, result.Launch.Agent.ID)
			require.Equal(t, plan.Recipients[launchKey].Launch.AgentConfigID, result.Launch.Agent.CurrentConfigID)
		} else {
			require.False(t, result.Input.Created)
		}
	}
	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_idempotency_key=$2`,
		ids.ProjectID, event.SemanticKey,
	).Scan(&count))
	require.Equal(t, 2, count)
	replay, err := consumer.Consume(ctx, retry.Lease())
	require.NoError(t, err)
	require.Len(t, replay, 2)
	require.Equal(t, 1, uploads.uploads)
}

func TestIntegrationRouterPlainFollowupWaitsForReservedConversation(t *testing.T) {
	for _, ownerState := range []integrationstore.IntegrationInboxState{
		integrationstore.IntegrationInboxProcessing,
		integrationstore.IntegrationInboxQueued,
		integrationstore.IntegrationInboxFailed,
	} {
		t.Run(string(ownerState), func(t *testing.T) {
			pool, store, ids, integrationSetup := integrationWorkerFixture(t)
			ctx := t.Context()
			inbox := store.Integrations()
			router := NewIntegrationRouter(store.Execution(), inbox)
			base := storagefixture.SeedAgentConfig(
				t,
				ctx,
				store.Models(),
				store.Execution(),
				ids.OrgID,
				ids.ProjectID,
				"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
			)
			profile, err := store.Execution().
				CreateAgentProfile(
					ctx,
					executionstore.CreateAgentProfileInput{
						ProjectID:       ids.ProjectID,
						Name:            "review",
						CurrentConfigID: base.ID,
					},
				)
			require.NoError(t, err)
			integration, err := inbox.UpdateIntegration(
				ctx, integrationSetup,
				integrationstore.SaveIntegrationInput{
					OrgID:           ids.OrgID,
					ProjectID:       ids.ProjectID,
					Name:            "chat",
					IntegrationKind: integrationdefinition.SlackThread,
					Settings:        integrationtest.ChatSettings("", profile.ID),
				},
			)
			require.NoError(t, err)
			claim := func() integrationstore.IntegrationInboxRecord {
				r, found, err := inbox.ClaimIntegrationInbox(
					ctx,
					integrationstore.ClaimIntegrationInboxInput{
						ProjectID:     ids.ProjectID,
						IntegrationID: integrationSetup,
						LeaseDuration: time.Minute,
					},
				)
				require.NoError(t, err)
				require.True(t, found)
				return r
			}
			capture := func(key string) integrationstore.IntegrationInboxRecord {
				_, _, err := inbox.AcceptIntegrationReceipt(
					ctx,
					integrationstore.VerifiedIntegrationReceipt{
						ProjectID:     ids.ProjectID,
						IntegrationID: integrationSetup,
						ReceiptKey:    key,
						Payload:       []byte(`{}`),
					},
				)
				require.NoError(t, err)
				return claim()
			}
			owner := capture("mention")
			event := IntegrationEvent{
				Event: integrationdefinition.Event{
					Scope: integrationdefinition.Scope{
						Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"},
					},
					Kind:      "message",
					Mentioned: true,
				},
				SemanticKey:   "message:initial",
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
				Actor:         integrationTestActor(t, integration, "U123"),
			}
			plan, err := freezeTestIntegrationEvent(ctx, router, owner.Lease(), &event)
			require.NoError(t, err)
			require.Len(t, plan.Recipients, 1)
			if ownerState != integrationstore.IntegrationInboxProcessing {
				require.NoError(
					t,
					inbox.WithIntegrationInboxLease(
						ctx,
						owner.Lease(),
						func(work *integrationstore.IntegrationInboxLeaseTx) error {
							if ownerState == integrationstore.IntegrationInboxQueued {
								return work.Retry(ctx, time.Hour, "media preparation retry")
							}
							return work.Fail(ctx, "launch failed permanently")
						},
					),
				)
			}
			follow := capture("plain-follow")
			event.SemanticKey, event.Event.Mentioned = "message:follow", false
			if ownerState == integrationstore.IntegrationInboxFailed {
				frozen, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, owner.ID)
				require.NoError(t, err)
				for i := range 12 {
					if i > 0 {
						follow = capture(fmt.Sprintf("plain-follow-%d", i))
					}
					empty, err := freezeTestIntegrationEvent(ctx, router, follow.Lease(), &event)
					require.NoError(t, err)
					require.Empty(t, empty.Recipients)
					_, err = router.Admit(ctx, follow.Lease(), nil)
					require.NoError(t, err)
					completed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, follow.ID)
					require.NoError(t, err)
					require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
					require.Equal(t, 1, completed.AttemptCount, "failed owner must not exhaust each follow-up's budget")
				}
				unchanged, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, owner.ID)
				require.NoError(t, err)
				require.Equal(t, frozen, unchanged)
				mention := capture("replacement-mention")
				mentioned := event
				mentioned.Event.Mentioned = true
				fresh, err := freezeTestIntegrationEvent(ctx, router, mention.Lease(), &mentioned)
				require.NoError(t, err)
				require.Len(t, fresh.Recipients, 1)
				_, err = router.Admit(ctx, mention.Lease(), nil)
				require.NoError(t, err)
			} else {
				_, err = freezeTestIntegrationEvent(ctx, router, follow.Lease(), &event)
				var reservation *integrationstore.IntegrationLaunchClaimError
				require.ErrorAs(t, err, &reservation)
				require.Equal(t, owner.ID, reservation.ReceiptID)
				require.Equal(t, ownerState, reservation.State)
				unplanned, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, follow.ID)
				require.NoError(t, err)
				require.Empty(t, unplanned.Plan, "plain follow-up must not freeze empty")
				require.Equal(t, integrationstore.IntegrationInboxProcessing, unplanned.State)
			}
			other := capture("unrelated")
			unrelated := event
			unrelated.Event.Scope = integrationdefinition.Scope{
				Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "2.0"},
			}
			empty, err := freezeTestIntegrationEvent(ctx, router, other.Lease(), &unrelated)
			require.NoError(t, err)
			require.Empty(t, empty.Recipients)
			_, err = router.Admit(ctx, other.Lease(), nil)
			require.NoError(t, err)
			if ownerState == integrationstore.IntegrationInboxFailed {
				_, err = router.Admit(ctx, owner.Lease(), nil)
				require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
				return
			}
			if ownerState == integrationstore.IntegrationInboxQueued {
				_, err := pool.Exec(ctx, `UPDATE integration_inbox SET next_attempt_at=now() WHERE id=$1`, owner.ID)
				require.NoError(t, err)
			}
			if ownerState != integrationstore.IntegrationInboxProcessing {
				owner = claim()
			}
			_, err = router.Admit(ctx, owner.Lease(), nil)
			require.NoError(t, err)
			continued, err := freezeTestIntegrationEvent(ctx, router, follow.Lease(), &event)
			require.NoError(t, err)
			require.Len(t, continued.Recipients, 1)
			results, err := router.Admit(ctx, follow.Lease(), nil)
			require.NoError(t, err)
			require.True(t, results[0].Input.Created)
			results, err = router.Admit(ctx, follow.Lease(), nil)
			require.NoError(t, err)
			require.False(t, results[0].Input.Created)
		})
	}
}
