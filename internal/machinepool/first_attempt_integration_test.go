//go:build integration

package machinepool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestManagerPassesFirstProvisionAttempt(t *testing.T) {
	for _, test := range []struct {
		name       string
		attempted  bool
		invalidEnv bool
	}{
		{name: "new machine"},
		{name: "restart after ambiguous create", attempted: true},
		{name: "invalid environment", invalidEnv: true},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				ctx := context.Background()
				pool := openManagerIntegrationDB(t, ctx)
				store := storage.NewStore(
					pool,
					storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
					storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
				)
				now := time.Now().Add(-time.Hour)
				orgID := seedManagerOrg(t, ctx, pool, "first-attempt", now)
				projectID, _ := seedManagerProjectActor(
					t,
					ctx,
					pool,
					store,
					orgID,
					"first-attempt-project",
					"first-attempt@example.com",
					now,
				)
				secretID := createProviderAuthSecretForManagerTest(t, ctx, pool, store, orgID, "provider-auth", "token")
				machinePool, err := store.Execution().
					CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(t, executionstore.CreateMachinePoolInput{
						OrgID:                orgID,
						Name:                 "first-attempt",
						Provider:             "capture",
						ProviderAuthSecretID: secretID,
						ProviderConfig:       json.RawMessage(`{}`),
						MaxTotalMachines:     1,
						MaxTotalCPU:          new(2),
						MaxTotalMemoryMB:     new(4096),
						MaxMachineCPU:        new(2),
						MaxMachineMemoryMB:   new(4096),
					}, 2, 4096, nil, nil, map[string]any{}))
				require.NoError(t, err)
				machineID := insertPoolMachineForManagerTest(t, ctx, pool, machinePool, "provisioning", "", now)
				grant, err := store.Execution().
					CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
						OrgID:         orgID,
						ProjectID:     projectID,
						MachinePoolID: machinePool.ID,
					})
				require.NoError(t, err)
				_, err = pool.Exec(
					ctx,
					`INSERT INTO project_machine_grants(org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, metadata, created_at, updated_at) VALUES ($1, $2, $3, 'pool', $4, '', '{}'::jsonb, $5, $5)`,
					orgID,
					projectID,
					machineID,
					grant.ID,
					now,
				)
				require.NoError(t, err)
				if test.attempted {
					_, err := pool.Exec(
						ctx,
						"UPDATE machines SET provider_provision_attempted_at=$3 WHERE org_id=$1 AND id=$2",
						orgID,
						machineID,
						now,
					)
					require.NoError(t, err)
				}
				definition := &testProviderDefinition{provider: &captureProvider{provisionResourceID: "owned-session"}}
				if test.invalidEnv {
					definition.provider.validateConfigErr = errors.New("invalid env")
				}
				manager := Manager{
					Execution:    store.Execution(),
					Identity:     store.Identity(),
					Catalog:      testProviderCatalog(definition),
					PublicAPIURL: "https://api.omnara.test/api/v1",
				}
				if test.invalidEnv {
					require.ErrorIs(t, manager.ProvisionMachine(ctx, orgID, machineID), definition.provider.validateConfigErr)
					require.Nil(t, definition.provider.provisioning)
					machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
					require.NoError(t, err)
					require.Nil(t, machine.ProviderProvisionAttemptedAt)
					require.Equal(t, executionstore.MachineLifecycleStateDeleted, machine.LifecycleState)
					require.NotNil(t, machine.DeletedAt)
					return
				}
				require.NoError(t, manager.ProvisionMachine(ctx, orgID, machineID))
				require.Equal(t, !test.attempted, definition.provider.firstAttempt)
				machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
				require.NoError(t, err)
				require.NotNil(t, machine.ProviderProvisionAttemptedAt)
				require.Equal(t, "owned-session", machine.ProviderResourceID)
			},
		)
	}
}
