//go:build integration

package appstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func (f profileChoiceFixture) selectionPlan(t *testing.T, appID uuid.UUID, slot string) json.RawMessage {
	t.Helper()
	plan, err := json.Marshal(map[string]any{"chosen": map[string]any{
		"selection": appstore.InboxAppSelection{
			AppID: appID, Address: f.input.Address, Slot: slot,
		},
	}})
	require.NoError(t, err)
	return plan
}

func TestAppProfileChoiceUnplannedHandoffReservesConversation(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"pending", "processing"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceFixture(t)
			choice := f.menu(t)
			late := f.receipt(t, "raw-follow-up", f.input.Payload)
			_, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, choice, "support"))
			require.NoError(t, err)
			decided := f.decidedReceipt(t, choice.ID)
			if state == "processing" {
				decided = f.claim(t)
			}
			f.exec(t, `UPDATE app_states SET expires_at=now()-interval '1 second' WHERE id=$1`, choice.ID)
			nextInput := f.input
			nextInput.SourceKey = "new-mention"
			reused, created, err := f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), nextInput)
			require.NoError(t, err)
			require.False(t, created)
			require.Equal(t, choice.ID, reused.ID)
			require.Equal(t, "support", reused.SelectedKey)
			var reservation *appstore.AppSelectionReservationError
			err = f.store.WithAppInboxLease(f.ctx, late.Lease(),
				func(work *appstore.AppInboxLeaseTx) error {
					if err := work.CheckNoUnsettledAppSelection(f.ctx, f.input.Address); err != nil {
						return err
					}
					return work.FreezePlan(f.ctx, json.RawMessage(`{}`))
				})
			require.ErrorAs(t, err, &reservation)
			require.Equal(t, decided.ID, reservation.ReceiptID)
			require.Equal(t, appstore.AppInboxState(state), reservation.State)
			require.Nil(t, f.read(t, late.ID).Plan, "raw follow-up must retry instead of freezing an empty plan")

			setup := appstore.SaveProjectAppInput{
				OrgID: f.org, ProjectID: f.project, Name: f.app.Name, AppType: f.app.AppType,
				Settings: f.app.Settings,
			}
			setup.Settings.Launcher.Slots = setup.Settings.Launcher.Slots[1:]
			_, err = f.store.UpdateProjectApp(f.ctx, f.app.ID, setup)
			require.NoError(t, err)
			plan := f.selectionPlan(t, f.app.ID, "review")
			err = f.store.WithAppInboxLease(f.ctx, late.Lease(),
				func(work *appstore.AppInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) })
			require.ErrorAs(t, err, &reservation)
			require.Equal(t, decided.ID, reservation.ReceiptID)

			otherApp := f.addApp(t, "another-setup", setup.Settings)
			other := f
			other.appID, other.app = otherApp.ID, otherApp
			otherReceipt := other.receipt(t, "raw-follow-up", f.input.Payload)
			otherMenu := nextInput
			otherMenu.AppID = otherApp.ID
			otherMenu.Options = otherMenu.Options[1:]
			_, _, err = f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), otherMenu)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized, "a receipt cannot create another app's menu")
			independent, created, err := f.store.EnsureAppProfileChoice(f.ctx, otherReceipt.Lease(), otherMenu)
			require.NoError(t, err)
			require.True(t, created, "a distinct app still owns its independent menu")
			require.Equal(t, otherApp.ID, independent.AppID)
			otherPlan := f.selectionPlan(t, otherApp.ID, "review")
			other.mutate(t, otherReceipt, func(work *appstore.AppInboxLeaseTx) error {
				return work.FreezePlan(f.ctx, otherPlan)
			})
		})
	}
}

func TestAppProfileChoiceFrozenPlanTakesOverReservation(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	choice := f.menu(t)
	late := f.receipt(t, "raw-follow-up", f.input.Payload)
	_, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, choice, "support"))
	require.NoError(t, err)
	decided := f.claim(t)
	plan := f.selectionPlan(t, f.app.ID, "support")
	f.mutate(t, decided, func(work *appstore.AppInboxLeaseTx) error {
		if err := work.CheckNoUnsettledAppSelection(f.ctx, f.input.Address); err != nil {
			return err
		}
		return work.FreezePlan(f.ctx, plan)
	})
	newMention := f.input
	newMention.SourceKey = "while-plan-is-processing"
	retained, made, err := f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), newMention)
	require.NoError(t, err)
	require.False(t, made, "a frozen processing plan still owns this app's menu")
	require.Equal(t, choice.ID, retained.ID)
	f.mutate(t, decided, func(work *appstore.AppInboxLeaseTx) error {
		return work.Retry(f.ctx, time.Second, "retry media preparation")
	})
	retained, made, err = f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), newMention)
	require.NoError(t, err)
	require.False(t, made, "a frozen pending plan also owns this app's menu")
	require.Equal(t, choice.ID, retained.ID)
	f.exec(t, `UPDATE app_inbox SET available_at=now() WHERE id=$1`, decided.ID)
	decided = f.claim(t)
	err = f.store.WithAppInboxLease(f.ctx, late.Lease(),
		func(work *appstore.AppInboxLeaseTx) error {
			return work.CheckNoUnsettledAppSelection(f.ctx, f.input.Address)
		})
	require.ErrorIs(t, err, appstore.ErrAppSelectionReserved)
	f.mutate(t, decided, func(work *appstore.AppInboxLeaseTx) error {
		return work.Fail(f.ctx, "frozen plan failed")
	})
	newInput := f.input
	newInput.SourceKey = "later-mention"
	reused, created, err := f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), newInput)
	require.NoError(t, err)
	require.True(t, created, "terminal work releases the conversation for a new menu")
	require.NotEqual(t, choice.ID, reused.ID)
	f.mutate(t, late, func(work *appstore.AppInboxLeaseTx) error {
		if err := work.CheckNoUnsettledAppSelection(f.ctx, f.input.Address); err != nil {
			return err
		}
		return work.FreezePlan(f.ctx, plan)
	})
	require.Equal(t, appstore.AppInboxFailed, f.read(t, decided.ID).State)
}

func TestAppProfileChoiceFailedUnplannedAllowsNewRequest(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	choice := f.menu(t)
	late := f.receipt(t, "new-mention", f.input.Payload)
	_, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, choice, "support"))
	require.NoError(t, err)
	decided := f.claim(t)
	f.mutate(t, decided, func(work *appstore.AppInboxLeaseTx) error {
		return work.Fail(f.ctx, "expected profile unavailable before planning")
	})
	require.Nil(t, f.read(t, decided.ID).Plan)
	f.mutate(t, late, func(work *appstore.AppInboxLeaseTx) error {
		return work.CheckNoUnsettledAppSelection(f.ctx, f.input.Address)
	})
	newInput := f.input
	newInput.SourceKey = "replacement-message"
	fresh, created, err := f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), newInput)
	require.NoError(t, err)
	require.True(t, created, "failed unplanned receipt must not trap users behind a stale menu")
	require.NotEqual(t, choice.ID, fresh.ID)
	require.NoError(t, f.store.RecordAppProfileChoiceMessage(f.ctx, f.project, f.appID, fresh.ID, "C123", "new-menu"))
	fresh = f.readChoice(t, fresh.ID)
	_, err = f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, fresh, "review"))
	require.NoError(t, err)
	newDecided := f.claim(t)
	newPlan := f.selectionPlan(t, f.app.ID, "review")
	f.mutate(t, newDecided, func(work *appstore.AppInboxLeaseTx) error {
		return work.FreezePlan(f.ctx, newPlan)
	})
	err = f.store.WithAppInboxLease(f.ctx, decided.Lease(),
		func(work *appstore.AppInboxLeaseTx) error {
			return work.FreezePlan(f.ctx, f.selectionPlan(t, f.app.ID, "support"))
		})
	require.ErrorIs(t, err, appstore.ErrAppInboxLeaseLost)
	retained, found, err := f.store.GetAppProfileChoiceBySource(f.ctx, f.project, f.app.ID, choice.SourceKey)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "support", retained.SelectedKey)
}

func TestAppProfileChoiceRechecksSettledSelectionAfterRoutingSnapshot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                          string
		pending, retired, unpublished bool
	}{
		{name: "active"},
		{name: "retired", retired: true},
		{name: "pending-menu", pending: true},
		{name: "unpublished-menu", pending: true, unpublished: true},
		{name: "retired-pending-menu", pending: true, retired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceFixture(t)
			var original appstore.AppProfileChoiceRecord
			if tc.unpublished {
				var err error
				original, _, err = f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
				require.NoError(t, err)
			} else if tc.pending {
				original = f.menu(t)
			}
			var snapshot appstore.AppRoutingCandidates
			f.mutate(t, f.source, func(work *appstore.AppInboxLeaseTx) error {
				var err error
				snapshot, err = f.store.AppRoutingCandidatesForInbox(f.ctx, work, f.input.Address,
					[]appstore.ConversationAddress{f.input.Address}, "message")
				return err
			})
			require.Empty(t, snapshot.Selections)
			execution := executionstore.New(f.pool, executionstore.Config{})
			profile, err := execution.GetAgentProfile(f.ctx, f.project, f.input.Options[0].ProfileID)
			require.NoError(t, err)
			launch, err := execution.LaunchAgent(f.ctx,
				executionstore.LaunchAgentInput{
					ProjectID: f.project, AgentConfigID: profile.CurrentConfig.ID,
					LaunchedBy: identitystore.NewUserPrincipal(f.user),
				})
			require.NoError(t, err)
			tx, err := f.pool.Begin(f.ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(f.ctx) }()
			require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
			require.NoError(
				t,
				dbsqlc.New(tx).LockProjectAppLifecycleShared(f.ctx, dbsqlc.LockProjectAppLifecycleSharedParams{AppID: f.appID}),
			)
			require.NoError(t, appstore.LockConversationTx(f.ctx, tx, f.project, f.appID, f.input.Address))
			require.NoError(t, lifecyclelock.Agents(f.ctx, tx,
				[]lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: launch.Agent.ID}}))
			target, err := f.store.EnsureConversationTargetTx(f.ctx, tx, appstore.EnsureConversationTargetInput{
				ProjectID: f.project, AgentID: launch.Agent.ID, AppID: f.appID,
				Address: f.input.Address, SelectionSlot: "support",
			})
			require.NoError(t, err)
			if tc.retired {
				_, err = tx.Exec(f.ctx, `UPDATE app_targets SET deleted_at=now() WHERE id=$1`, target.ID)
				require.NoError(t, err)
			}
			require.NoError(t, tx.Commit(f.ctx))
			input := f.input
			input.SourceKey = "stale-new-mention"
			choice, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
			require.ErrorIs(t, err, appstore.ErrAppSelectionSettled)
			require.False(t, created)
			require.Zero(t, choice)
			_, found, err := f.store.GetAppProfileChoiceBySource(f.ctx, f.project, f.app.ID, input.SourceKey)
			require.NoError(t, err)
			require.False(t, found, "a stale snapshot must not create a menu after admission")
			if tc.pending {
				replayed, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
				if tc.unpublished {
					require.ErrorIs(t, err, appstore.ErrAppSelectionSettled,
						"an old publisher cannot recreate its menu after another request launched")
				} else {
					require.NoError(t, err)
					require.Equal(t, original, replayed, "published source bookkeeping remains intact")
				}
				require.False(t, created)
				require.Equal(t, original, f.readChoice(t, original.ID))
			}
		})
	}
}

func TestAppProfileChoiceStaleOfferedProfileExpiresMenu(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	choice := f.menu(t)
	click := f.chooseInput(t, choice, "support")
	setup := appstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: f.app.Name, AppType: f.app.AppType,
		Settings: f.app.Settings,
	}
	setup.Settings.Launcher.Slots[0].AgentProfileID = &f.input.Options[1].ProfileID
	_, err := f.store.UpdateProjectApp(f.ctx, f.app.ID, setup)
	require.NoError(t, err)
	wrongMenu := click
	wrongMenu.MessageID = "forged"
	_, err = f.store.ChooseAppProfile(f.ctx, wrongMenu)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	unknown := click
	unknown.Key = "never-offered"
	unchanged, err := f.store.ChooseAppProfile(f.ctx, unknown)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.Equal(t, choice, unchanged, "unknown key does not authorize menu dismissal")
	require.Equal(t, choice, f.readChoice(t, choice.ID))
	staleRevision := click
	staleRevision.SourceChoiceRevision = choice.Revision - 1
	_, err = f.store.ChooseAppProfile(f.ctx, staleRevision)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.Equal(t, choice, f.readChoice(t, choice.ID))
	retired, err := f.store.ChooseAppProfile(f.ctx, click)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	expired := f.readChoice(t, choice.ID)
	require.Equal(t, expired, retired, "caller can distinguish retired menus from invalid selections")
	require.True(t, expired.ExpiresAt.Before(choice.ExpiresAt))
	require.Greater(t, expired.Revision, choice.Revision)
	require.Empty(t, expired.SelectedKey)

	replay, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, expired, replay)
	newInput := f.input
	newInput.SourceKey = "new-after-edit"
	newInput.Options = append([]appstore.AppProfileChoiceOption(nil), f.input.Options...)
	newInput.Options[0].ProfileID = f.input.Options[1].ProfileID
	fresh, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), newInput)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, choice.ID, fresh.ID)
	require.Equal(t, newInput.Options, fresh.Options)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM app_inbox WHERE project_id=$1`,
		f.project).Scan(&count))
	require.Equal(t, 1, count, "a stale click must not hand off any launch")
}

func TestAppProfileChoiceUnpublishedMenuFollowsOwnerRecovery(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"pending", "processing", "failed", "completed", "deleted"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceFixture(t)
			choice, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
			require.NoError(t, err)
			require.True(t, created)
			require.Empty(t, choice.MessageID)
			late := f.receipt(t, "later-message", f.input.Payload)
			switch state {
			case "pending":
				f.mutate(t, f.source, func(work *appstore.AppInboxLeaseTx) error {
					return work.Retry(f.ctx, time.Hour, "retry menu publication")
				})
			case "failed":
				f.mutate(t, f.source, func(work *appstore.AppInboxLeaseTx) error {
					return work.Fail(f.ctx, "menu publication exhausted")
				})
			case "completed", "deleted":
				f.mutate(t, f.source, func(work *appstore.AppInboxLeaseTx) error {
					if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
						return err
					}
					return work.Complete(f.ctx)
				})
				if state == "deleted" {
					f.exec(t, `DELETE FROM app_inbox WHERE id=$1`, f.source.ID)
				}
			}
			input := f.input
			input.SourceKey = "later-message"
			fresh, created, err := f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), input)
			require.NoError(t, err)
			canPublish := state == "pending" || state == "processing"
			require.Equal(t, !canPublish, created, "only recoverable unpublished menus hold new sources")
			if canPublish {
				require.Equal(t, choice, fresh)
			} else {
				require.NotEqual(t, choice.ID, fresh.ID)
				require.Equal(t, late.ID, fresh.OwnerReceiptID)
			}
			replayed, created, err := f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), f.input)
			require.NoError(t, err)
			require.False(t, created)
			require.Equal(t, choice, replayed)
		})
	}
}

func TestAppProfileChoicePublishedMenuSurvivesOwnerCompletion(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	choice := f.menu(t)
	f.mutate(t, f.source, func(work *appstore.AppInboxLeaseTx) error {
		if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
			return err
		}
		return work.Complete(f.ctx)
	})
	late := f.receipt(t, "later-message", f.input.Payload)
	input := f.input
	input.SourceKey = "later-message"
	retained, created, err := f.store.EnsureAppProfileChoice(f.ctx, late.Lease(), input)
	require.NoError(t, err)
	require.False(t, created, "a published menu remains available after its owner completes")
	require.Equal(t, choice, retained)
}

func TestAppProfileChoiceExplicitExpiryPreservesAcceptedWork(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	choice := f.menu(t)
	require.ErrorIs(t, f.store.ExpireAppProfileChoice(f.ctx, uuid.New(), f.appID, choice.ID), storeerr.ErrNotFound)
	require.ErrorIs(t, f.store.ExpireAppProfileChoice(f.ctx, f.project, uuid.New(), choice.ID), storeerr.ErrNotFound)
	require.NoError(t, f.store.ExpireAppProfileChoice(f.ctx, f.project, f.appID, choice.ID))
	expired := f.readChoice(t, choice.ID)
	require.True(t, expired.ExpiresAt.Before(choice.ExpiresAt))
	require.NoError(t, f.store.ExpireAppProfileChoice(f.ctx, f.project, f.appID, choice.ID))
	require.Equal(t, expired, f.readChoice(t, choice.ID), "repeated expiry does not change the revision")
	input := f.input
	input.SourceKey = "another-source"
	fresh, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, f.store.RecordAppProfileChoiceMessage(
		f.ctx, f.project, f.appID, fresh.ID, "C123", "fresh-menu"))
	fresh = f.readChoice(t, fresh.ID)
	selected, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, fresh, "review"))
	require.NoError(t, err)
	receipt := f.decidedReceipt(t, selected.ID)
	require.NoError(t, f.store.ExpireAppProfileChoice(f.ctx, f.project, f.appID, selected.ID))
	require.Equal(t, selected, f.readChoice(t, selected.ID))
	require.Equal(t, receipt, f.decidedReceipt(t, selected.ID), "expiration cannot change accepted work")
}

func TestAppProfileChoiceInboxRetentionProtectsOldSource(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"pending", "processing", "completed", "failed"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceFixture(t)
			choice := f.menu(t)
			_, found, err := f.store.GetAppProfileChoiceInbox(f.ctx, f.project, f.appID, choice.ID)
			require.NoError(t, err)
			require.False(t, found, "an unselected menu has no accepted receipt")
			_, err = f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, choice, "support"))
			require.NoError(t, err)
			receipt := f.decidedReceipt(t, choice.ID)
			if state != "pending" {
				receipt = f.claim(t)
			}
			if state == "completed" || state == "failed" {
				f.mutate(t, receipt, func(work *appstore.AppInboxLeaseTx) error {
					if state == "failed" {
						return work.Fail(f.ctx, "failed before planning")
					}
					if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
						return err
					}
					return work.Complete(f.ctx)
				})
			}
			receipt = f.read(t, receipt.ID)
			got, found, err := f.store.GetAppProfileChoiceInbox(f.ctx, f.project, f.appID, choice.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, receipt, got)
			for _, scope := range [][2]uuid.UUID{{uuid.New(), f.appID}, {f.project, uuid.New()}} {
				_, found, err = f.store.GetAppProfileChoiceInbox(f.ctx, scope[0], scope[1], choice.ID)
				require.NoError(t, err)
				require.False(t, found)
			}
			f.exec(t, `UPDATE app_states SET expires_at=now()-interval '30 days' WHERE id=$1`, choice.ID)
			for range 2 {
				count, err := f.store.CleanupAppStates(f.ctx, appstore.AppProfileChoiceMinRetention, 1)
				require.NoError(t, err)
				require.Zero(t, count)
				count, err = f.store.CleanupTerminalAppInbox(f.ctx, 7*24*time.Hour, 1)
				require.NoError(t, err)
				require.Zero(t, count)
			}
			if state == "pending" || state == "processing" {
				return
			}
			f.exec(t, `UPDATE app_inbox SET completed_at=now()-interval '8 days' WHERE id=$1`, receipt.ID)
			count, err := f.store.CleanupTerminalAppInbox(f.ctx, 7*24*time.Hour, 1)
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			_, found, err = f.store.GetAppProfileChoiceInbox(f.ctx, f.project, f.appID, choice.ID)
			require.NoError(t, err)
			require.False(t, found)
			count, err = f.store.CleanupAppStates(f.ctx, appstore.AppProfileChoiceMinRetention, 1)
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.appID, choice.ID)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		})
	}
}
