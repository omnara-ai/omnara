//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestProviderInboxLaunchCannotClaimCronActor(t *testing.T) {
	t.Parallel()
	// A mention launcher may legitimately name a slot "scheduled". Its provider
	// receipt still cannot grant Omnara actor authority, even to a system launch.
	f := newInboxLaunchFixture(t, false, time.Minute, "scheduled")
	require.Equal(t, integrationstore.IntegrationInboxSourceProvider, f.receipt.Source)
	slot := f.slots["scheduled"]
	providerActor := slot.Launch.InitialInput.Actor
	triggerID := uuid.New()
	tenant, err := publicid.Encode(publicid.KindOrganization, testOrgID)
	require.NoError(t, err)
	trigger, err := publicid.Encode(publicid.KindCronTrigger, triggerID)
	require.NoError(t, err)
	slot.Launch.LaunchedBy = identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeSystem, ID: triggerID}
	slot.Launch.InitialInput.Actor = &executionstore.ActorParams{
		Provider: executionstore.ActorProviderOmnara, ProviderTenantID: tenant, ProviderUserID: trigger,
		DisplayName: new("Daily review"),
	}
	f.slots["scheduled"] = slot
	plan, err := json.Marshal(f.slots)
	require.NoError(t, err)
	// Seed a bad worker plan at the admission boundary; its valid app selection
	// is already reserved. Frozen plan data must not substitute for receipt source.
	_, err = f.store.pool.Exec(f.ctx, `UPDATE integration_inbox SET plan=$2 WHERE id=$1`, f.receipt.ID, plan)
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "scheduled")
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	f.assertAbsent(t, "scheduled")

	// Changing only the actor back to the verified provider makes this same
	// system launch admissible: rejection above was not a malformed plan/config.
	slot.Launch.InitialInput.Actor = providerActor
	f.slots["scheduled"] = slot
	plan, err = json.Marshal(f.slots)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE integration_inbox SET plan=$2 WHERE id=$1`, f.receipt.ID, plan)
	require.NoError(t, err)
	launch, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "scheduled")
	require.NoError(t, err)
	require.True(t, launch.Created)
	require.Equal(t, slot.AgentID, launch.Agent.ID)
}
