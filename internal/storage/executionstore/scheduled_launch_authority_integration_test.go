//go:build integration

package executionstore_test

import (
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
	f := newInboxLaunchFixture(t, false, time.Minute, "default")
	require.Equal(t, integrationstore.IntegrationInboxSourceProvider, f.receipt.Source)
	recipient := f.recipients["default"]
	providerActor := recipient.InitialInput.Actor
	triggerID := uuid.New()
	tenant, err := publicid.Encode(publicid.KindOrganization, testOrgID)
	require.NoError(t, err)
	trigger, err := publicid.Encode(publicid.KindCronTrigger, triggerID)
	require.NoError(t, err)
	recipient.Launch.LaunchedBy = executionstore.InboxLaunchPrincipal{
		Type: identitystore.PrincipalTypeSystem, ID: triggerID,
	}
	recipient.InitialInput.Actor = &executionstore.ActorParams{
		Provider: executionstore.ActorProviderOmnara, ProviderTenantID: tenant, ProviderUserID: trigger,
		DisplayName: new("Daily review"),
	}
	f.recipients["default"] = recipient
	plan, err := marshalInboxLaunchPlan(f.recipients)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE integration_inbox SET plan=$2 WHERE id=$1`, f.receipt.ID, plan)
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxLaunchRecipient(f.ctx, f.receipt.Lease(), "default", nil)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	f.assertAbsent(t, "default")

	recipient.InitialInput.Actor = providerActor
	f.recipients["default"] = recipient
	plan, err = marshalInboxLaunchPlan(f.recipients)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE integration_inbox SET plan=$2 WHERE id=$1`, f.receipt.ID, plan)
	require.NoError(t, err)
	launch, err := f.store.Execution().AdmitInboxLaunchRecipient(f.ctx, f.receipt.Lease(), "default", nil)
	require.NoError(t, err)
	require.True(t, launch.Created)
	require.Equal(t, recipient.AgentID, launch.Agent.ID)
}
