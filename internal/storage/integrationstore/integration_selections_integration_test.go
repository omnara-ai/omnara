//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSelectionReservationFreezesEntireRecipientSet(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	store := integrationstore.New(f.pool, executionstore.IntegrationAccess{})
	var profileID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_profiles WHERE project_id=$1 LIMIT 1`, f.project).Scan(&profileID),
	)
	f.exec(t, `UPDATE project_integrations SET provider_tenant_id='T123' WHERE id=$1`, f.integrationID)
	setup := integrationstore.SaveProjectIntegrationInput{
		OrgID:           f.org,
		ProjectID:       f.project,
		Name:            "inbox-integration",
		IntegrationType: integrationdefinition.SlackThread,
		Settings: integrationstore.ProjectIntegrationSettings{
			Launcher: &integrationstore.IntegrationLauncher{
				Trigger:   "mention",
				ScopeKind: "workspace",
				ScopeRef:  "T123",
				Slots: []integrationstore.IntegrationLaunchSlot{
					{Key: "a", AgentProfileID: &profileID},
					{Key: "b", AgentProfileID: &profileID},
				},
			},
		},
	}
	integration, err := store.UpdateProjectIntegration(f.ctx, f.integrationID, setup)
	require.NoError(t, err)
	plan := func(keys ...string) json.RawMessage {
		t.Helper()
		slots := map[string]any{}
		for _, key := range keys {
			id, err := uuid.NewV7()
			require.NoError(t, err)
			slots[key] = map[string]any{"agent_id": id,
				"selection": integrationstore.InboxIntegrationSelection{IntegrationID: integration.ID,
					Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"}, Slot: key}}
		}
		raw, err := json.Marshal(slots)
		require.NoError(t, err)
		return raw
	}
	f.accept(t, "first")
	first := f.claim(t)
	f.accept(t, "second")
	second := f.claim(t)
	plans := []json.RawMessage{plan("a", "b"), plan("a", "b")}
	receipts := []integrationstore.IntegrationInboxRecord{first, second}
	results := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Go(func() {
			<-start
			results[i] = store.WithIntegrationInboxLease(
				f.ctx,
				receipts[i].Lease(),
				func(lease *integrationstore.IntegrationInboxLeaseTx) error {
					return lease.FreezePlan(f.ctx, plans[i])
				},
			)
		})
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range results {
		if err == nil {
			require.Equal(t, -1, winner, "only one receipt may freeze initial selection before media preparation")
			winner = i
		} else {
			require.ErrorIs(t, err, integrationstore.ErrIntegrationSelectionReserved)
		}
	}
	require.NotEqual(t, -1, winner)
	require.JSONEq(t, string(plans[winner]), string(f.read(t, receipts[winner].ID).Plan))
	require.Empty(t, f.read(t, receipts[1-winner].ID).Plan)
	setup.Settings.Launcher.Slots[1].Key = "c"
	_, err = store.UpdateProjectIntegration(f.ctx, integration.ID, setup)
	require.NoError(t, err)
	blockedPlan := plan("c")
	err = store.WithIntegrationInboxLease(
		f.ctx,
		receipts[1-winner].Lease(),
		func(lease *integrationstore.IntegrationInboxLeaseTx) error {
			return lease.FreezePlan(f.ctx, blockedPlan)
		},
	)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationSelectionReserved)
	f.mutate(t, receipts[winner], func(lease *integrationstore.IntegrationInboxLeaseTx) error {
		return lease.Fail(f.ctx, "profile b unavailable")
	})
	f.mutate(t, receipts[1-winner], func(lease *integrationstore.IntegrationInboxLeaseTx) error {
		return lease.FreezePlan(f.ctx, blockedPlan)
	})
	require.JSONEq(t, string(blockedPlan), string(f.read(t, receipts[1-winner].ID).Plan))
	err = store.WithIntegrationInboxLease(f.ctx, receipts[winner].Lease(),
		func(lease *integrationstore.IntegrationInboxLeaseTx) error {
			return lease.Complete(f.ctx)
		})
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
}
