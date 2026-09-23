//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestProviderInboxLaunchCannotClaimCronActor(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, false, time.Minute, "scheduled")
	require.Equal(t, appstore.AppInboxSourceProvider, f.receipt.Source)
	slot := f.slots["scheduled"]
	providerActor := slot.Launch.InitialInput.Actor
	triggerID := uuid.New()
	tenant, err := publicid.Encode(publicid.KindOrganization, testOrgID)
	require.NoError(t, err)
	trigger, err := publicid.Encode(publicid.KindCronTrigger, triggerID)
	require.NoError(t, err)
	slot.Launch.LaunchedBy = executionstore.InboxLaunchPrincipal{Type: identitystore.PrincipalTypeSystem, ID: triggerID}
	slot.Launch.InitialInput.Actor = &executionstore.ActorParams{
		Provider: executionstore.ActorProviderOmnara, ProviderTenantID: tenant, ProviderUserID: trigger,
		DisplayName: new("Daily review"),
	}
	f.slots["scheduled"] = slot
	plan, err := json.Marshal(f.slots)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE app_inbox SET plan=$2 WHERE id=$1`, f.receipt.ID, plan)
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "scheduled")
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	f.assertAbsent(t, "scheduled")

	slot.Launch.InitialInput.Actor = providerActor
	f.slots["scheduled"] = slot
	plan, err = json.Marshal(f.slots)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE app_inbox SET plan=$2 WHERE id=$1`, f.receipt.ID, plan)
	require.NoError(t, err)
	launch, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "scheduled")
	require.NoError(t, err)
	require.True(t, launch.Created)
	require.Equal(t, slot.AgentID, launch.Agent.ID)
}
