//go:build integration

package machinepool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

type creationGuardDefinition struct {
	testProviderDefinition
	instances []*creationGuardCapture
}

func (d *creationGuardDefinition) NewProvider(
	json.RawMessage,
	providers.RuntimeConfig,
) (providers.Provider, error) {
	p := &creationGuardCapture{captureProvider: captureProvider{provisionResourceID: "owned-session"}}
	d.instances = append(d.instances, p)
	return p, nil
}

type creationGuardCapture struct {
	captureProvider
	authorized bool
}

func (p *creationGuardCapture) AuthorizeCreation(uuid.UUID, uuid.UUID) { p.authorized = true }

func TestManagerAuthorizesCreationOnlyBeforeFirstDurableAttempt(t *testing.T) {
	for _, attempted := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "new machine", true: "restart after ambiguous create"}[attempted],
			func(t *testing.T) {
				ctx := context.Background()
				pool := openManagerIntegrationDB(t, ctx)
				store := storage.NewStore(
					pool,
					storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
					storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
				)
				now := time.Now().Add(-time.Hour)
				orgID := seedManagerOrg(t, ctx, pool, "guarded-create", now)
				projectID, _ := seedManagerProjectActor(
					t,
					ctx,
					pool,
					store,
					orgID,
					"guarded-project",
					"guarded@example.com",
					now,
				)
				secretID := createProviderAuthSecretForManagerTest(t, ctx, pool, store, orgID, "provider-auth", "token")
				machinePool, err := store.Execution().
					CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(t, executionstore.CreateMachinePoolInput{
						OrgID:                orgID,
						Name:                 "guarded",
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
				if attempted {
					_, err := pool.Exec(
						ctx,
						"UPDATE machines SET provider_provision_attempted_at=$3 WHERE org_id=$1 AND id=$2",
						orgID,
						machineID,
						now,
					)
					require.NoError(t, err)
				}
				definition := &creationGuardDefinition{}
				manager := Manager{
					Execution:    store.Execution(),
					Identity:     store.Identity(),
					Catalog:      testProviderCatalog(definition),
					PublicAPIURL: "https://api.omnara.test/api/v1",
				}
				require.NoError(t, manager.ProvisionMachine(ctx, orgID, machineID))
				require.Len(t, definition.instances, 1)
				require.Equal(t, !attempted, definition.instances[0].authorized)
				machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
				require.NoError(t, err)
				require.NotNil(t, machine.ProviderProvisionAttemptedAt)
				require.Equal(t, "owned-session", machine.ProviderResourceID)
			},
		)
	}
}
