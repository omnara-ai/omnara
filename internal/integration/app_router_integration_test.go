//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { integrationdb.RunTestMain(m) }

func TestAppRouterConcurrentFreezePartialRecoveryAndPinnedConfig(t *testing.T) {
	ctx := t.Context()
	_, file, _, _ := runtime.Caller(0)
	pool := integrationdb.OpenMigratedPool(t, ctx, filepath.Join(filepath.Dir(file), "../../migrations"))
	ids := storagefixture.ProjectIDs{
		OrgID:                   uuid.New(),
		ProjectID:               uuid.New(),
		ProviderAdminUserID:     uuid.New(),
		ProviderSecretID:        uuid.New(),
		ProviderSecretVersionID: uuid.New(),
		ProviderConfigID:        uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	store := storage.NewStore(pool)
	integrations := store.Integrations()
	router := NewAppRouter(store.Execution(), integrations)
	base := storagefixture.SeedAgentConfig(
		t,
		ctx,
		store.Models(),
		store.Execution(),
		ids.OrgID,
		ids.ProjectID,
		"instruction: original profile\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
	)
	profile, err := store.Execution().
		CreateAgentProfile(
			ctx,
			executionstore.CreateAgentProfileInput{ProjectID: ids.ProjectID, Name: "review", CurrentConfigID: base.ID},
		)
	require.NoError(t, err)
	connection := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(
		ctx,
		`INSERT INTO integration_connections(id,org_id,project_id,installed_by_user_id,provider,state,provider_tenant_id,provider_account_ref,created_at,updated_at) VALUES($1,$2,$3,$4,'slack','active','T123','app-router',now(),now())`,
		connection,
		ids.OrgID,
		ids.ProjectID,
		ids.ProviderAdminUserID,
	)
	require.NoError(t, err)
	publicConnection, err := publicid.Encode(publicid.KindIntegrationConnection, connection)
	require.NoError(t, err)
	setup := integrationstore.SaveProjectAppInput{
		OrgID:        ids.OrgID,
		ProjectID:    ids.ProjectID,
		Name:         "reviewers",
		DefinitionID: appdefinition.Slack,
		Enabled:      true,
		Settings: integrationstore.ProjectAppSettings{
			Resource: agentconfig.AgentConfigAppResourceSource{
				Definition:         appdefinition.Slack,
				Connection:         publicConnection,
				Listener:           &appdefinition.Listener{Events: []string{"message"}},
				InteractionHandler: &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions},
			},
			Launcher: &integrationstore.AppLauncher{
				Trigger:   "mention",
				ScopeKind: "workspace",
				ScopeRef:  "T123",
				Slots: []integrationstore.AppLaunchSlot{
					{Key: "a", AgentProfileID: &profile.ID},
					{Key: "b", AgentProfileID: &profile.ID},
				},
			},
		},
	}
	app, err := store.Integrations().CreateProjectApp(ctx, setup)
	require.NoError(t, err)
	claim := func(key string) integrationstore.IntegrationInboxRecord {
		_, _, err := store.Integrations().
			AcceptIntegrationReceipt(
				ctx,
				integrationstore.VerifiedIntegrationReceipt{
					ProjectID:    ids.ProjectID,
					ConnectionID: connection,
					ReceiptKey:   key,
					Payload:      []byte(`{"verified":true}`),
				},
			)
		require.NoError(t, err)
		r, found, err := store.Integrations().
			ClaimIntegrationInbox(
				ctx,
				integrationstore.ClaimIntegrationInboxInput{
					ProjectID:     ids.ProjectID,
					ConnectionID:  connection,
					LeaseDuration: time.Minute,
				},
			)
		require.NoError(t, err)
		require.True(t, found)
		return r
	}
	receipts := []integrationstore.IntegrationInboxRecord{claim("first"), claim("second")}
	placeholder := uuid.Must(uuid.NewV7())
	event := AppEvent{
		Event: appdefinition.Event{
			Scope:     appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
			Kind:      "message",
			Mentioned: true,
		},
		SemanticKey: "message:1",
		ContentBlocks: json.RawMessage(
			`[{"type":"text","text":"review"},{"type":"media_ref","artifact_id":"` + placeholder.String() + `"}]`,
		),
		Files: []AppPlannedFile{{ArtifactID: placeholder, ProviderFileID: "F123"}},
		Actor: executionstore.ActorParams{Provider: "slack", ProviderTenantID: "T123", ProviderUserID: "U123"},
	}
	plans := make([]AppInboxPlan, 2)
	failures := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range receipts {
		wg.Go(func() {
			<-start
			plans[i], failures[i] = freezeTestAppEvents(ctx, router, receipts[i].Lease(), []AppEvent{event})
		})
	}
	close(start)
	wg.Wait()
	winner := 0
	if failures[0] != nil {
		winner = 1
	}
	require.NoError(t, failures[winner])
	require.ErrorIs(t, failures[1-winner], integrationstore.ErrAppSelectionReserved)
	plan, receipt := plans[winner], receipts[winner]
	require.Len(t, plan, 2)
	// Disabling a launcher stops future selections; this frozen N-slot
	// membership and its pinned profile remain admitted work.
	setup.Enabled = false
	setup.Settings.Launcher.Slots[1].Key = "c"
	_, err = store.Integrations().UpdateProjectApp(ctx, app.ID, setup)
	require.NoError(t, err)
	var changed agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &changed))
	changed.Instruction = "edited profile"
	encoded, err := agentconfig.EncodeCompiled(changed)
	require.NoError(t, err)
	next, err := store.Execution().
		CreateAgentConfig(
			ctx,
			executionstore.CreateAgentConfigInput{
				ProjectID:               ids.ProjectID,
				ConfiguredModelID:       base.ConfiguredModelID,
				CompiledDefinition:      encoded.CanonicalJSON,
				CompilerVersion:         agentconfig.CompilerVersion,
				EffectiveDefinitionHash: encoded.Hash,
			},
		)
	require.NoError(t, err)
	_, err = store.Execution().
		RetargetAgentProfile(
			ctx,
			executionstore.RetargetAgentProfileInput{
				ProjectID:               ids.ProjectID,
				ProfileID:               profile.ID,
				ExpectedCurrentConfigID: base.ID,
				ConfigID:                next.ID,
				IdempotencyKey:          "retarget",
			},
		)
	require.NoError(t, err)
	prepare := func(key string, slot AppInboxSlot) {
		file := slot.Files[0]
		err := router.Prepare(
			ctx,
			receipt.Lease(),
			key,
			[]artifactstore.PreparedArtifact{
				{
					ID:          file.ArtifactID,
					Filename:    "review.txt",
					ContentType: "text/plain",
					Digest:      blobstore.ContentDigest([]byte("review")),
					SizeBytes:   6,
				},
			},
		)
		require.NoError(t, err)
	}
	for key, slot := range plan {
		if slot.Selection.Slot == "a" {
			prepare(key, slot)
		}
	}
	results, err := router.Admit(ctx, receipt.Lease())
	require.Error(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "a", plan[results[0].Slot].Selection.Slot)
	var count int
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1`, ids.ProjectID).Scan(&count),
	)
	require.Equal(t, 1, count)
	// Once A exists, an ordinary follow-up may continue A without waiting for B
	// or promising retrospective delivery to B when it eventually launches.
	partialReceipt := claim("partial-follow")
	partialEvent := event
	partialEvent.SemanticKey, partialEvent.Event.Mentioned = "message:partial-follow", false
	partialEvent.ContentBlocks, partialEvent.Files = json.RawMessage(`[{"type":"text","text":"continue A"}]`), nil
	partialPlan, err := freezeTestAppEvents(ctx, router, partialReceipt.Lease(), []AppEvent{partialEvent})
	require.NoError(t, err)
	require.Len(t, partialPlan, 1)
	_, err = router.Admit(ctx, partialReceipt.Lease())
	require.NoError(t, err)
	// Recovery ignores replacement input/profile/app settings and preserves B.
	recovered, err := freezeTestAppEvents(ctx, router, receipt.Lease(), nil)
	require.NoError(t, err)
	originalJSON, err := json.Marshal(plan)
	require.NoError(t, err)
	recoveredJSON, err := json.Marshal(recovered)
	require.NoError(t, err)
	require.JSONEq(t, string(originalJSON), string(recoveredJSON))
	for key, slot := range recovered {
		if slot.Selection.Slot == "b" {
			prepare(key, slot)
		}
	}
	results, err = router.Admit(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, result := range results {
		config, _, err := store.Execution().GetAgentConfig(ctx, ids.ProjectID, result.Launch.Agent.CurrentConfigID)
		require.NoError(t, err)
		var compiled agentconfig.Compiled
		require.NoError(t, json.Unmarshal(config.CompiledDefinition, &compiled))
		require.Equal(t, "original profile", compiled.Instruction)
	}
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1`, ids.ProjectID).Scan(&count),
	)
	require.Equal(t, 2, count)
	// The second receipt follows current listeners; edited slot C cannot replace B.
	event.SemanticKey = "message:2"
	event.ContentBlocks = json.RawMessage(`[{"type":"text","text":"follow up"}]`)
	event.Files = nil
	follow, err := freezeTestAppEvents(ctx, router, receipts[1-winner].Lease(), []AppEvent{event})
	require.NoError(t, err)
	require.Len(t, follow, 2)
	for _, slot := range follow {
		require.Nil(t, slot.Selection)
		require.NotNil(t, slot.Listener)
	}
	_, err = router.Admit(ctx, receipts[1-winner].Lease())
	require.NoError(t, err)
	// The consumer owns no receipt schedule and never expands a frozen replay.
	// Its result retains kernel cancellation/provisioning outcomes for the worker.
	consumerReceipt := claim("consumer")
	event.SemanticKey = "message:consumer"
	provider := &appConsumerProvider{events: []AppEvent{event}}
	consumer := NewAppInboxConsumer(
		router, integrations, nil, map[string]AppInboxProvider{"slack": provider}, nil, testAppLaunchWorkflow(router),
	)
	consumed, err := consumer.Consume(ctx, consumerReceipt.Lease())
	require.NoError(t, err)
	require.Len(t, consumed, 2)
	require.Equal(t, 1, provider.expansions)
	provider.events = nil
	consumed, err = consumer.Consume(ctx, consumerReceipt.Lease())
	require.NoError(t, err)
	require.Len(t, consumed, 2)
	require.Equal(t, 1, provider.expansions)
	for _, result := range consumed {
		require.False(t, result.Input.Created)
	}
	// Completed replay needs neither active connection nor fresh handler writes.
	_, err = pool.Exec(ctx, `UPDATE integration_connections SET state='disabled' WHERE id=$1`, connection)
	require.NoError(t, err)
	results, err = router.Admit(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, result := range results {
		require.False(t, result.Launch.Created)
	}
}

func TestAppRouterPlainFollowupWaitsForReservedConversation(t *testing.T) {
	for _, ownerState := range []integrationstore.IntegrationInboxState{
		integrationstore.IntegrationInboxProcessing,
		integrationstore.IntegrationInboxPending,
		integrationstore.IntegrationInboxFailed,
	} {
		t.Run(string(ownerState), func(t *testing.T) {
			pool, store, ids, connection := appWorkerFixture(t)
			ctx := t.Context()
			inbox := store.Integrations()
			router := NewAppRouter(store.Execution(), inbox)
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
			publicConnection, err := publicid.Encode(publicid.KindIntegrationConnection, connection)
			require.NoError(t, err)
			_, err = inbox.CreateProjectApp(
				ctx,
				integrationstore.SaveProjectAppInput{
					OrgID:        ids.OrgID,
					ProjectID:    ids.ProjectID,
					Name:         "reviewers",
					DefinitionID: appdefinition.Slack,
					Enabled:      true,
					Settings: integrationstore.ProjectAppSettings{
						Resource: agentconfig.AgentConfigAppResourceSource{
							Definition: appdefinition.Slack,
							Connection: publicConnection,
							Listener:   &appdefinition.Listener{Events: []string{"message"}},
						},
						Launcher: &integrationstore.AppLauncher{
							Trigger:   "mention",
							ScopeKind: "workspace",
							ScopeRef:  "T123",
							Slots:     []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
						},
					},
				},
			)
			require.NoError(t, err)
			claim := func() integrationstore.IntegrationInboxRecord {
				r, found, err := inbox.ClaimIntegrationInbox(
					ctx,
					integrationstore.ClaimIntegrationInboxInput{
						ProjectID:     ids.ProjectID,
						ConnectionID:  connection,
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
						ProjectID:    ids.ProjectID,
						ConnectionID: connection,
						ReceiptKey:   key,
						Payload:      []byte(`{}`),
					},
				)
				require.NoError(t, err)
				return claim()
			}
			owner := capture("mention")
			event := AppEvent{
				Event: appdefinition.Event{
					Scope: appdefinition.Scope{
						Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"},
					},
					Kind:      "message",
					Mentioned: true,
				},
				SemanticKey:   "message:initial",
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
				Actor: executionstore.ActorParams{
					Provider:         "slack",
					ProviderTenantID: "T123",
					ProviderUserID:   "U123",
				},
			}
			plan, err := freezeTestAppEvents(ctx, router, owner.Lease(), []AppEvent{event})
			require.NoError(t, err)
			require.Len(t, plan, 1)
			if ownerState != integrationstore.IntegrationInboxProcessing {
				require.NoError(
					t,
					inbox.WithIntegrationInboxLease(
						ctx,
						owner.Lease(),
						func(work *integrationstore.IntegrationInboxLeaseTx) error {
							if ownerState == integrationstore.IntegrationInboxPending {
								return work.Retry(ctx, time.Hour, "media preparation retry")
							}
							return work.Fail(ctx, "launch needs operator recovery")
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
					empty, err := freezeTestAppEvents(ctx, router, follow.Lease(), []AppEvent{event})
					require.NoError(t, err)
					require.Empty(t, empty)
					_, err = router.Admit(ctx, follow.Lease())
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
				_, err = freezeTestAppEvents(ctx, router, mention.Lease(), []AppEvent{mentioned})
				require.ErrorIs(t, err, integrationstore.ErrAppSelectionReserved)
			} else {
				_, err = freezeTestAppEvents(ctx, router, follow.Lease(), []AppEvent{event})
				var reservation *integrationstore.AppSelectionReservationError
				require.ErrorAs(t, err, &reservation)
				require.Equal(t, owner.ID, reservation.ReceiptID)
				require.Equal(t, ownerState, reservation.State)
				unplanned, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, follow.ID)
				require.NoError(t, err)
				require.Empty(t, unplanned.Plan, "plain follow-up must not freeze empty")
				require.Equal(t, integrationstore.IntegrationInboxProcessing, unplanned.State)
			}
			// The reservation covers only the exact conversation, not its workspace.
			other := capture("unrelated")
			unrelated := event
			unrelated.Event.Scope = appdefinition.Scope{
				Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "2.0"},
			}
			empty, err := freezeTestAppEvents(ctx, router, other.Lease(), []AppEvent{unrelated})
			require.NoError(t, err)
			require.Empty(t, empty)
			_, err = router.Admit(ctx, other.Lease())
			require.NoError(t, err)
			if ownerState == integrationstore.IntegrationInboxFailed {
				require.NoError(t, inbox.RetryFailedIntegrationInbox(ctx, ids.ProjectID, owner.ID))
			}
			if ownerState == integrationstore.IntegrationInboxPending {
				_, err := pool.Exec(ctx, `UPDATE integration_inbox SET available_at=now() WHERE id=$1`, owner.ID)
				require.NoError(t, err)
			}
			if ownerState != integrationstore.IntegrationInboxProcessing {
				owner = claim()
			}
			_, err = router.Admit(ctx, owner.Lease())
			require.NoError(t, err)
			if ownerState == integrationstore.IntegrationInboxFailed {
				follow = capture("follow-after-recovery")
			}
			continued, err := freezeTestAppEvents(ctx, router, follow.Lease(), []AppEvent{event})
			require.NoError(t, err)
			require.Len(t, continued, 1)
			results, err := router.Admit(ctx, follow.Lease())
			require.NoError(t, err)
			require.True(t, results[0].Input.Created)
			results, err = router.Admit(ctx, follow.Lease())
			require.NoError(t, err)
			require.False(t, results[0].Input.Created)
		})
	}
}
