//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestPendingProviderLaunchOlderReceiptStillWaitsForAcceptedState(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	older := f.source
	mention := f.receipt(t, "newer-mention", f.input.Payload)
	f.mutate(t, mention, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.MarkIntegrationPendingLaunch(f.ctx, f.input.Address)
	})
	f.mutate(t, older, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address)
	})
	follower := f.receipt(t, "newer-follow-up", f.input.Payload)
	err := f.store.WithIntegrationInboxLease(f.ctx, follower.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address)
		})
	var reserved *integrationstore.IntegrationLaunchClaimError
	require.ErrorAs(t, err, &reserved)
	require.Equal(t, mention.ID, reserved.ReceiptID)

	choice := f.menu(t)
	_, err = f.store.ChooseIntegrationProfile(f.ctx, f.chooseInput(t, choice, "support"))
	require.NoError(t, err)
	selected := f.decidedReceipt(t, choice.ID)
	err = f.store.WithIntegrationInboxLease(f.ctx, older.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address)
		})
	require.ErrorAs(t, err, &reserved)
	require.Equal(t, selected.ID, reserved.ReceiptID, "a newer accepted choice still protects older messages")
	err = f.store.WithIntegrationInboxLease(f.ctx, mention.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.FreezePlan(f.ctx, f.launchClaimPlan(t, f.integration.ID, "support"))
		})
	require.ErrorAs(t, err, &reserved)
	require.Equal(t, selected.ID, reserved.ReceiptID, "a provider marker cannot outrank accepted state work")

	selected = f.claim(t)
	f.mutate(t, selected, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address); err != nil {
			return err
		}
		return work.FreezePlan(f.ctx, f.launchClaimPlan(t, f.integration.ID, "support"))
	})
	err = f.store.WithIntegrationInboxLease(f.ctx, older.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address)
		})
	require.ErrorAs(t, err, &reserved)
	require.Equal(t, selected.ID, reserved.ReceiptID, "the frozen state plan takes over protection at any age")
}

func TestPendingProviderLaunchKeepsFirstFreezeOrdering(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	older := f.source
	newer := f.receipt(t, "newer-mention", f.input.Payload)
	for _, receipt := range []integrationstore.IntegrationInboxRecord{older, newer} {
		f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.MarkIntegrationPendingLaunch(f.ctx, f.input.Address)
		})
	}
	plan := f.launchClaimPlan(t, f.integration.ID, "support")
	f.mutate(t, newer, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.FreezePlan(f.ctx, plan)
	})
	err := f.store.WithIntegrationInboxLease(f.ctx, older.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) })
	var reserved *integrationstore.IntegrationLaunchClaimError
	require.ErrorAs(t, err, &reserved)
	require.Equal(t, newer.ID, reserved.ReceiptID, "first Freeze wins even when an older provider marker exists")
	require.Empty(t, f.read(t, older.ID).Plan)
}

func TestPendingProviderLaunchDeliveryOnlyCannotCycle(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	older := f.source
	newer := f.receipt(t, "newer-mention", f.input.Payload)
	for _, receipt := range []integrationstore.IntegrationInboxRecord{older, newer} {
		f.mutate(t, receipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.MarkIntegrationPendingLaunch(f.ctx, f.input.Address)
		})
	}
	freezeDelivery := func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address); err != nil {
			return err
		}
		return work.FreezePlan(f.ctx, json.RawMessage(`{"recipients":{}}`))
	}
	err := f.store.WithIntegrationInboxLease(f.ctx, newer.Lease(), freezeDelivery)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationLaunchReserved)
	f.mutate(t, older, freezeDelivery)
	f.mutate(t, newer, freezeDelivery)
	for _, receipt := range []integrationstore.IntegrationInboxRecord{older, newer} {
		var kind, ref string
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT reserved_scope_kind, reserved_scope_ref FROM integration_inbox WHERE id=$1`, receipt.ID).
			Scan(&kind, &ref))
		require.Equal(t, f.input.Address.Kind, kind)
		require.Equal(t, f.input.Address.Ref, ref, "freezing ends protection without clearing the marker")
	}
}

func TestPendingProviderLaunchSourceCanWaitForItsChosenState(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	f.mutate(t, f.source, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.MarkIntegrationPendingLaunch(f.ctx, f.input.Address)
	})
	choice := f.menu(t)
	_, err := f.store.ChooseIntegrationProfile(f.ctx, f.chooseInput(t, choice, "support"))
	require.NoError(t, err)
	err = f.store.WithIntegrationInboxLease(f.ctx, f.source.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address)
		})
	require.ErrorIs(t, err, integrationstore.ErrIntegrationLaunchReserved)
	selected := f.claim(t)
	require.ErrorIs(t, f.store.WithIntegrationInboxLease(f.ctx, selected.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.MarkIntegrationPendingLaunch(f.ctx, f.input.Address)
		}), storeerr.ErrUnauthorized, "provider markers cannot replace accepted state reservations")
	f.mutate(t, selected, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address); err != nil {
			return err
		}
		return work.FreezePlan(f.ctx, f.launchClaimPlan(t, f.integration.ID, "support"))
	})
}

func TestPendingProviderLaunchScopeAndLease(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	mark := func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.MarkIntegrationPendingLaunch(f.ctx, f.input.Address)
	}
	f.mutate(t, f.source, mark)
	f.mutate(t, f.source, mark)
	different := f.input.Address
	different.Ref = "C456:789.012"
	err := f.store.WithIntegrationInboxLease(f.ctx, f.source.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.MarkIntegrationPendingLaunch(f.ctx, different)
		})
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.NotErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
	otherAddress := f.receipt(t, "other-address", f.input.Payload)
	f.mutate(t, otherAddress, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.CheckNoUnsettledIntegrationLaunch(f.ctx, different)
	})
	otherIntegration := f.addIntegration(t, "another-integration", f.integration.Settings)
	other := f
	other.integrationID, other.integration = otherIntegration.ID, otherIntegration
	otherReceipt := other.receipt(t, "other-integration", f.input.Payload)
	other.mutate(t, otherReceipt, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.CheckNoUnsettledIntegrationLaunch(f.ctx, f.input.Address)
	})
	lease := f.source.Lease()
	lease.Token = uuid.New()
	require.ErrorIs(t, f.store.WithIntegrationInboxLease(f.ctx, lease, mark),
		integrationstore.ErrIntegrationInboxLeaseLost)
	f.exec(t, `UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, f.source.ID)
	require.ErrorIs(t, f.store.WithIntegrationInboxLease(f.ctx, f.source.Lease(), mark),
		integrationstore.ErrIntegrationInboxLeaseLost)
}
