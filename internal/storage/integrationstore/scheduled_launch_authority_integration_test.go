//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func scheduledInboxSnapshot(t *testing.T, f inboxFixture) integrationstore.ScheduledAppLaunch {
	t.Helper()
	launch := integrationstore.ScheduledAppLaunch{
		TriggerID:   uuid.New(),
		TriggerName: "Daily review",
		DueAt:       time.Now().UTC(),
		Destination: json.RawMessage(
			`{"channel_id":"C123"}`,
		),
		OpeningMessage: "Daily review",
		Message:        "Review the queue.",
	}
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT profile.id,version.agent_config_id FROM agent_profiles profile
 JOIN agent_profile_versions version ON version.id=profile.current_version_id
 WHERE profile.project_id=$1 AND profile.deleted_at IS NULL LIMIT 1`,
		f.project).Scan(&launch.ProfileID, &launch.ConfigID))
	return launch
}

func acceptScheduledInbox(t *testing.T, f inboxFixture, launch integrationstore.ScheduledAppLaunch) {
	t.Helper()
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
	require.NoError(t, integrationstore.LockAppsTx(f.ctx, tx, f.project, nil, f.appID))
	var profile uuid.UUID
	require.NoError(t, tx.QueryRow(f.ctx,
		`SELECT id FROM agent_profiles WHERE project_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`,
		f.project, launch.ProfileID).Scan(&profile))
	_, created, err := f.store.AcceptScheduledAppLaunchTx(f.ctx, tx, integrationstore.AcceptScheduledAppLaunchInput{
		ProjectID: f.project, AppID: f.appID, Launch: launch,
		ReceiptKey: "cron_trigger:" + launch.TriggerID.String() + ":" + launch.DueAt.Format(time.RFC3339),
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
	// A verified provider body can contain arbitrary fields, including a forged
	// source and a thread. None of them may become storage authority.
	payload, err := json.Marshal(struct {
		integrationstore.ScheduledAppLaunch
		Source string              `json:"source"`
		Root   appdefinition.Scope `json:"root"`
	}{launch, "scheduled_launch", root})
	require.NoError(t, err)
	_, _, err = f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project, AppID: f.appID, ReceiptKey: "forged-scheduled-event", Payload: payload,
	})
	require.NoError(t, err)
	provider := f.claim(t)
	require.Equal(t, integrationstore.IntegrationInboxSourceProvider, provider.Source)
	_, err = provider.ScheduledLaunch()
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	plan, err := json.Marshal(map[string]any{"scheduled": map[string]any{
		"scope": root,
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
	// The same app has no mention launcher. Only the trusted scheduled receipt
	// and a plan within its accepted parent authorize this reservation.
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
			slots := map[string]any{"scheduled": map[string]any{"selection": selection, "scope": root}}
			if scenario == "no scope" {
				slots["scheduled"] = map[string]any{"selection": selection}
			}
			if scenario == "wrong parent" {
				root.Slack.ChannelID = "C999"
				selection.Address.Ref = "C999:100.1"
				slots["scheduled"] = map[string]any{"selection": selection, "scope": root}
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
			if scenario == "no scope" || scenario == "wrong parent" {
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			} else {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			}
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
		var profile uuid.UUID
		require.NoError(t, tx.QueryRow(f.ctx,
			`SELECT id FROM agent_profiles WHERE project_id=$1 AND id=$2 FOR UPDATE`,
			f.project, launch.ProfileID).Scan(&profile))
		replayed := launch
		if changed {
			replayed.Message = "A different task must not replace the accepted occurrence."
		}
		receipt, created, err := f.store.AcceptScheduledAppLaunchTx(f.ctx, tx, integrationstore.AcceptScheduledAppLaunchInput{
			ProjectID: f.project, AppID: f.appID, Launch: replayed,
			ReceiptKey: "cron_trigger:" + launch.TriggerID.String() + ":" + launch.DueAt.Format(time.RFC3339),
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
