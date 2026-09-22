//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func scheduledInboxSnapshot(t *testing.T, f inboxFixture) integrationstore.ScheduledAppEvent {
	t.Helper()
	var profileID uuid.UUID
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT id FROM agent_profiles WHERE project_id=$1 AND deleted_at IS NULL LIMIT 1`, f.project).Scan(&profileID))
	profileRef, err := publicid.Encode(publicid.KindAgentProfile, profileID)
	require.NoError(t, err)
	settings, err := json.Marshal(appdefinition.ThreadScheduleSettings{
		AgentProfileID: profileRef, ChannelID: "C123",
		OpeningMessageTemplate: "Daily review", MessageTemplate: "Review the queue.",
	})
	require.NoError(t, err)
	launch := integrationstore.ScheduledAppEvent{
		TriggerID: uuid.New(), Settings: settings,
		Occurrence: cronschedule.Occurrence{
			Name: "Daily review", DueAt: time.Now().UTC(), FiredAt: time.Now().UTC(), Timezone: "UTC",
		},
	}
	return launch
}

func acceptScheduledInbox(t *testing.T, f inboxFixture, launch integrationstore.ScheduledAppEvent) {
	t.Helper()
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
	require.NoError(t, integrationstore.LockAppsTx(f.ctx, tx, f.project, nil, f.appID))
	_, created, err := f.store.AcceptScheduledAppEventTx(f.ctx, tx, integrationstore.AcceptScheduledAppEventInput{
		ProjectID: f.project, AppID: f.appID, Event: launch,
		ReceiptKey: "cron_trigger:" + launch.TriggerID.String() + ":" + launch.Occurrence.DueAt.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, tx.Commit(f.ctx))
}

func TestScheduledSelectionRequiresTrustedReceiptSource(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	launch := scheduledInboxSnapshot(t, f)
	root := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}
	payload, err := json.Marshal(struct {
		integrationstore.ScheduledAppEvent
		Source string              `json:"source"`
		Root   appdefinition.Scope `json:"root"`
	}{launch, "scheduled", root})
	require.NoError(t, err)
	_, _, err = f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project, AppID: f.appID, ReceiptKey: "forged-scheduled-event", Payload: payload,
	})
	require.NoError(t, err)
	provider := f.claim(t)
	require.Equal(t, integrationstore.IntegrationInboxSourceProvider, provider.Source)
	_, err = provider.ScheduledEvent()
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	plan, err := json.Marshal(map[string]any{"scheduled": map[string]any{
		"scope": root, "launch": scheduledPlanLaunch(t, f, launch, root),
		"selection": integrationstore.InboxAppSelection{
			AppID: f.appID, Slot: "scheduled",
			Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:100.1"},
		},
	}})
	require.NoError(t, err)
	err = f.store.WithIntegrationInboxLease(
		f.ctx,
		provider.Lease(),
		func(w *integrationstore.IntegrationInboxLeaseTx) error {
			return w.FreezePlan(f.ctx, plan)
		},
	)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	require.Empty(t, f.read(t, provider.ID).Plan)
	acceptScheduledInbox(t, f, launch)
	scheduled := f.claim(t)
	f.mutate(t, scheduled, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.FreezePlan(f.ctx, plan)
	})
	require.JSONEq(t, string(plan), string(f.read(t, scheduled.ID).Plan))
}

func TestScheduledSelectionRequiresOneThreadWithinAcceptedParent(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"no scope", "different thread", "wrong parent", "multiple conversations", "empty plan",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			acceptScheduledInbox(t, f, scheduledInboxSnapshot(t, f))
			receipt := f.claim(t)
			root := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}
			selection := integrationstore.InboxAppSelection{
				AppID: f.appID, Slot: "scheduled",
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:100.1"},
			}
			if scenario == "different thread" {
				selection.Address.Ref = "C123:101.1"
			}
			slots := map[string]any{"scheduled": map[string]any{
				"selection": selection, "scope": root,
				"launch": scheduledPlanLaunch(t, f, scheduledInboxSnapshot(t, f), root),
			}}
			if scenario == "no scope" {
				slots["scheduled"] = map[string]any{"selection": selection}
			}
			if scenario == "wrong parent" {
				root.Slack.ChannelID = "C999"
				selection.Address.Ref = "C999:100.1"
				slots["scheduled"] = map[string]any{
					"selection": selection, "scope": root,
					"launch": scheduledPlanLaunch(t, f, scheduledInboxSnapshot(t, f), root),
				}
			}
			if scenario == "empty plan" {
				slots = map[string]any{}
			}
			if scenario == "multiple conversations" {
				selection.Address.Ref = "C123:101.1"
				slots["another"] = map[string]any{"selection": selection}
			}
			plan, err := json.Marshal(slots)
			require.NoError(t, err)
			err = f.store.WithIntegrationInboxLease(
				f.ctx,
				receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.FreezePlan(f.ctx, plan)
				},
			)
			require.Error(t, err)
			require.Empty(t, f.read(t, receipt.ID).Plan)
		})
	}
}

func TestScheduledReceiptReplayRequiresIdenticalSnapshot(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	launch := scheduledInboxSnapshot(t, f)
	acceptScheduledInbox(t, f, launch)
	original := f.claim(t)
	for _, changed := range []bool{false, true} {
		tx := integrationdb.BeginTx(t, f.ctx, f.pool)
		require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
		require.NoError(t, integrationstore.LockAppsTx(f.ctx, tx, f.project, nil, f.appID))
		replayed := launch
		if changed {
			replayed.Occurrence.Name = "Different occurrence settings"
		}
		receipt, created, err := f.store.AcceptScheduledAppEventTx(f.ctx, tx, integrationstore.AcceptScheduledAppEventInput{
			ProjectID: f.project, AppID: f.appID, Event: replayed,
			ReceiptKey: "cron_trigger:" + launch.TriggerID.String() + ":" + launch.Occurrence.DueAt.Format(time.RFC3339),
		})
		require.False(t, created)
		if changed {
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
		} else {
			require.NoError(t, err)
			require.Equal(t, original.ID, receipt.ID)
		}
		require.NoError(t, tx.Rollback(f.ctx))
	}
	require.JSONEq(t, string(original.Payload), string(f.read(t, original.ID).Payload))
}

func scheduledPlanLaunch(
	t *testing.T, f inboxFixture, event integrationstore.ScheduledAppEvent, root appdefinition.Scope,
) map[string]any {
	t.Helper()
	launch, err := appdefinition.PrepareThreadSchedule(appdefinition.SlackThread, event.Settings, event.Occurrence)
	require.NoError(t, err)
	app, err := f.store.GetProjectAppByID(f.ctx, f.appID)
	require.NoError(t, err)
	raw, err := json.Marshal([]map[string]string{{"type": "text", "text": launch.Message}})
	require.NoError(t, err)
	content, err := appdefinition.AppendInputContext(app.Name, root, raw)
	require.NoError(t, err)
	return map[string]any{"ProfileID": launch.ProfileID, "InitialInput": map[string]any{"content_blocks": content}}
}
