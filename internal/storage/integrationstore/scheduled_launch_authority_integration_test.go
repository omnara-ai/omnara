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
	// source and preparation. None of them may become storage authority.
	payload, err := json.Marshal(struct {
		integrationstore.ScheduledAppLaunch
		Source      string                                      `json:"source"`
		Preparation integrationstore.ScheduledLaunchPreparation `json:"preparation"`
	}{launch, "scheduled_launch", integrationstore.ScheduledLaunchPreparation{AttemptedAt: &launch.DueAt, Root: &root}})
	require.NoError(t, err)
	_, _, err = f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project, AppID: f.appID, ReceiptKey: "forged-scheduled-event", Payload: payload,
	})
	require.NoError(t, err)
	provider := f.claim(t)
	require.Equal(t, integrationstore.IntegrationInboxSourceProvider, provider.Source)
	require.Empty(t, provider.Preparation)
	_, err = provider.ScheduledLaunch()
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	plan, err := json.Marshal(map[string]any{"scheduled": map[string]any{
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
	// The same app has no mention launcher. Only the dedicated scheduled intake
	// and confirmed publication grant the authority to reserve this conversation.
	acceptScheduledInbox(t, f, launch)
	scheduled := f.claim(t)
	f.mutate(t, scheduled, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		if _, _, err := w.BeginScheduledPublication(f.ctx); err != nil {
			return err
		}
		if err := w.RecordScheduledRoot(f.ctx, root); err != nil {
			return err
		}
		return w.FreezePlan(f.ctx, plan)
	})
	require.JSONEq(t, string(plan), string(f.read(t, scheduled.ID).Plan))
}

func TestScheduledSelectionCannotReserveUnconfirmedConversations(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"no confirmed root", "different thread", "multiple conversations"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			acceptScheduledInbox(t, f, scheduledInboxSnapshot(t, f))
			receipt := f.claim(t)
			root := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}
			if scenario != "no confirmed root" {
				f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
					if _, _, err := work.BeginScheduledPublication(f.ctx); err != nil {
						return err
					}
					return work.RecordScheduledRoot(f.ctx, root)
				})
			}
			selection := integrationstore.InboxAppSelection{
				AppID: f.appID, Slot: "scheduled",
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:100.1"},
			}
			if scenario == "different thread" {
				selection.Address.Ref = "C123:101.1"
			}
			slots := map[string]any{"scheduled": map[string]any{"selection": selection}}
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
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
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

func TestScheduledPreparationMutationsFenceExpiredLease(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"begin publication", "clear publication attempt", "record root"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			acceptScheduledInbox(t, f, scheduledInboxSnapshot(t, f))
			receipt := f.claim(t)
			f.mutate(t, receipt, func(w *integrationstore.IntegrationInboxLeaseTx) error {
				_, _, err := w.BeginScheduledPublication(f.ctx)
				return err
			})
			before := f.read(t, receipt.ID).Preparation
			tx := integrationdb.BeginTx(t, f.ctx, f.pool)
			work, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, receipt.Lease())
			require.NoError(t, err)
			// Exercise the mutation fence, not just rejection when acquiring a handle.
			_, err = tx.Exec(
				f.ctx,
				`UPDATE integration_inbox SET claim_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`,
				receipt.ID,
			)
			require.NoError(t, err)
			switch operation {
			case "begin publication":
				_, _, err = work.BeginScheduledPublication(f.ctx)
			case "clear publication attempt":
				err = work.RecordScheduledNonDelivery(f.ctx)
			case "record root":
				err = work.RecordScheduledRoot(f.ctx,
					appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}})
			}
			require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
			var after json.RawMessage
			require.NoError(t, tx.QueryRow(f.ctx,
				`SELECT preparation FROM integration_inbox WHERE id=$1`, receipt.ID).Scan(&after))
			require.JSONEq(
				t,
				string(before),
				string(after),
				"expiry must preserve evidence even before caller rollback",
			)
		})
	}
}

// Follow the consumer's one-handle-per-transaction usage. After recovery,
// the new worker may replay confirmed evidence but cannot clear or replace it.
func TestScheduledPreparationRetainsConfirmedRootAcrossRecovery(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	acceptScheduledInbox(t, f, scheduledInboxSnapshot(t, f))
	receipt := f.claim(t)
	root := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}
	f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if _, _, err := work.BeginScheduledPublication(f.ctx); err != nil {
			return err
		}
		return work.RecordScheduledRoot(f.ctx, root)
	})
	before := f.read(t, receipt.ID).Preparation
	f.exec(
		t,
		`UPDATE integration_inbox SET claim_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`,
		receipt.ID,
	)
	recovered, err := f.store.RecoverIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	resumed := f.claim(t)
	require.NotEqual(t, receipt.ClaimToken, resumed.ClaimToken)
	require.JSONEq(t, string(before), string(resumed.Preparation))
	f.mutate(t, resumed, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.RecordScheduledRoot(f.ctx, root)
	})
	err = f.store.WithIntegrationInboxLease(
		f.ctx,
		resumed.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.RecordScheduledNonDelivery(f.ctx)
		},
	)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	err = f.store.WithIntegrationInboxLease(
		f.ctx,
		resumed.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.RecordScheduledRoot(f.ctx,
				appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "101.1"}})
		},
	)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.JSONEq(t, string(before), string(f.read(t, receipt.ID).Preparation))
}

// Like prepared inbox stages, scheduled preparation must compose across handles
// sharing a caller-owned transaction. This exercises cache refresh, not takeover.
func TestScheduledPreparationMultipleHandlesPreserveEvidence(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	acceptScheduledInbox(t, f, scheduledInboxSnapshot(t, f))
	receipt := f.claim(t)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	first, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, receipt.Lease())
	require.NoError(t, err)
	second, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, receipt.Lease())
	require.NoError(t, err)
	initial, fresh, err := first.BeginScheduledPublication(f.ctx)
	require.NoError(t, err)
	require.True(t, fresh)
	require.NotNil(t, initial.AttemptedAt)
	replay, fresh, err := second.BeginScheduledPublication(f.ctx)
	require.NoError(t, err)
	require.False(t, fresh, "only one handle may report a new publication attempt")
	require.Equal(t, initial.AttemptedAt, replay.AttemptedAt)
	root := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}
	require.NoError(t, first.RecordScheduledRoot(f.ctx, root))
	replay, fresh, err = second.BeginScheduledPublication(f.ctx)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, &root, replay.Root, "begin must return the refreshed confirmed root")
	require.NoError(t, tx.Commit(f.ctx))
	retained, err := f.read(t, receipt.ID).ScheduledPreparation()
	require.NoError(t, err)
	require.Equal(t, replay, retained)
}

func TestScheduledPreparationStaleHandleCannotOverwriteRoot(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"clear publication evidence", "replace confirmed root"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			acceptScheduledInbox(t, f, scheduledInboxSnapshot(t, f))
			receipt := f.claim(t)
			f.mutate(t, receipt, func(w *integrationstore.IntegrationInboxLeaseTx) error {
				_, _, err := w.BeginScheduledPublication(f.ctx)
				return err
			})
			tx := integrationdb.BeginTx(t, f.ctx, f.pool)
			first, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, receipt.Lease())
			require.NoError(t, err)
			stale, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, receipt.Lease())
			require.NoError(t, err)
			require.NoError(t, first.RecordScheduledRoot(f.ctx,
				appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}))
			var before, after json.RawMessage
			require.NoError(t, tx.QueryRow(f.ctx,
				`SELECT preparation FROM integration_inbox WHERE id=$1`, receipt.ID).Scan(&before))
			if operation == "clear publication evidence" {
				require.ErrorIs(t, stale.RecordScheduledNonDelivery(f.ctx), storeerr.ErrStateTransitionConflict)
			} else {
				err := stale.RecordScheduledRoot(f.ctx,
					appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "101.1"}})
				require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			}
			require.NoError(t, tx.QueryRow(f.ctx,
				`SELECT preparation FROM integration_inbox WHERE id=$1`, receipt.ID).Scan(&after))
			require.JSONEq(
				t,
				string(before),
				string(after),
				"a cached handle must not erase a newer confirmed provider root",
			)
		})
	}
}
