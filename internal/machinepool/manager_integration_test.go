//go:build integration

package machinepool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

type poolMachineProvisioningScenario uint8

func managerUserPrincipal(userID storage.ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

const (
	provisioningFacts poolMachineProvisioningScenario = iota
	provisioningCapacityRejection
	provisioningFactDrift
	provisioningFinalFailure
	provisioningAdmissionFinalFailure
)

func TestPoolMachineManagerPersistsResolvedProviderFacts(t *testing.T) {
	testPoolMachineManagerProvisioningScenario(t, provisioningFacts)
}

func TestPoolMachineManagerFinishesProvisioningAfterManagedWorkAdmissionCloses(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "admitted-provision", now)
	projectID, _ := seedManagerProjectActor(
		t,
		ctx,
		pool,
		store,
		orgID,
		"admitted-provision-project",
		"admitted-provision@example.com",
		now,
	)
	t.Setenv("TEST_ADMITTED_PROVISION_TOKEN", "pool-token")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin default pool provisioning: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.Execution().ProvisionOrganizationDefaultsTx(
		ctx,
		tx,
		orgID,
		projectID,
		[]executionstore.DefaultMachinePoolTemplate{{
			Name:                          "Admitted Provision Pool",
			Provider:                      "capture",
			DefaultMachineCPU:             intPtrForManagerTest(1),
			DefaultMachineMemoryMB:        intPtrForManagerTest(1024),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"admitted"}`),
			ProviderAuthEnvVar:            "TEST_ADMITTED_PROVISION_TOKEN",
			MaxTotalMachines:              1,
			MaxTotalCPU:                   intPtrForManagerTest(1),
			MaxTotalMemoryMB:              intPtrForManagerTest(1024),
			MaxMachineCPU:                 intPtrForManagerTest(1),
			MaxMachineMemoryMB:            intPtrForManagerTest(1024),
		}},
	); err != nil {
		t.Fatalf("provision default pool: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit default pool provisioning: %v", err)
	}
	var machinePoolID storage.ID
	if err := pool.QueryRow(ctx, `
SELECT id
FROM machine_pools
WHERE org_id = $1 AND name = 'Admitted Provision Pool' AND deleted_at IS NULL
`, orgID).Scan(&machinePoolID); err != nil {
		t.Fatalf("load default pool id: %v", err)
	}
	machinePool, err := store.Execution().GetMachinePool(ctx, orgID, machinePoolID)
	if err != nil {
		t.Fatalf("load default pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(
		t,
		ctx,
		pool,
		machinePool,
		"provisioning",
		"",
		now,
	)
	poolGrant, err := store.Execution().GetActiveProjectMachinePoolGrantForMachinePool(
		ctx,
		projectID,
		machinePool.ID,
	)
	if err != nil {
		t.Fatalf("load default pool grant: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO project_machine_grants(
    org_id,
    project_id,
    machine_id,
    source_kind,
    project_machine_pool_grant_id,
    description,
    metadata,
    created_at,
    updated_at
) VALUES ($1, $2, $3, 'pool', $4, '', '{}'::jsonb, $5, $5)
`, orgID, projectID, machineID, poolGrant.ID, now); err != nil {
		t.Fatalf("grant admitted machine to project: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO org_managed_work_admission(org_id, new_managed_work_allowed)
VALUES ($1, false)
`, orgID); err != nil {
		t.Fatalf("close managed work admission: %v", err)
	}
	provider := &captureProvider{provisionResourceID: "admitted-resource"}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(&testProviderDefinition{provider: provider}),
		PublicURL: "https://app.omnara.test",
	}
	if err := manager.ProvisionMachine(ctx, orgID, machineID); err != nil {
		t.Fatalf("finish admitted machine provisioning: %v", err)
	}
	if provider.provisioning == nil {
		t.Fatal("provider was not called for admitted machine")
	}
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("load provisioned machine: %v", err)
	}
	if machine.LifecycleState != executionstore.MachineLifecycleStateActive ||
		machine.ProviderResourceID != "admitted-resource" {
		t.Fatalf("provisioned machine = %+v", machine)
	}
}

func TestPoolMachineManagerRejectsResolvedFactsAbovePoolCapacity(t *testing.T) {
	testPoolMachineManagerProvisioningScenario(t, provisioningCapacityRejection)
}

func TestPoolMachineManagerRejectsResolvedFactDrift(t *testing.T) {
	testPoolMachineManagerProvisioningScenario(t, provisioningFactDrift)
}

func TestPoolMachineManagerCleansUpResourceAfterFinalProvisionFailure(t *testing.T) {
	testPoolMachineManagerProvisioningScenario(t, provisioningFinalFailure)
}

func TestPoolMachineManagerFinalizesCleanupAfterAdmissionExhaustion(t *testing.T) {
	testPoolMachineManagerProvisioningScenario(t, provisioningAdmissionFinalFailure)
}

func testPoolMachineManagerProvisioningScenario(t *testing.T, scenario poolMachineProvisioningScenario) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "provider-config-provision", now)
	projectID, actorID := seedManagerProjectActor(
		t,
		ctx,
		pool,
		store,
		orgID,
		"provider-config-provision-project",
		"provider-config-provision@example.com",
		now,
	)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"provision-provider-auth",
		"pool-token",
	)
	machineSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     orgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provision-runtime-env",
		Material:  secrets.GenericMaterial{Value: "runtime-secret"},
		Actor:     managerUserPrincipal(actorID),
	})
	if err != nil {
		t.Fatalf("create machine secret: %v", err)
	}
	machineSecretID := secretPublicIDForManagerTest(t, machineSecret.ID)
	maxCPU, maxMemoryMB := 100, 1024*1024
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Provider Config Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{"mode":"provision"}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     4,
			MaxTotalCPU:          intPtrForManagerTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForManagerTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForManagerTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForManagerTest(1024),
		},
		1,
		1024,
		map[string]string{"PLAIN": "plain"},
		map[string]string{"API_TOKEN": machineSecretID},
		map[string]any{},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID:           orgID,
		SecretID:        machineSecret.ID,
		TargetProjectID: projectID,
		Actor:           managerUserPrincipal(actorID),
	}); err != nil {
		t.Fatalf("grant machine secret: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:         orgID,
		ProjectID:     projectID,
		MachinePoolID: machinePool.ID,
	})

	if err != nil {
		t.Fatalf("create project pool grant: %v", err)
	}
	intent := defaultMachineProvisioningForManagerTest(t, machinePool)
	intent.CPU = nil
	intent.MemoryMB = nil
	machineID := insertPoolMachineForManagerTestWithFields(
		t,
		ctx,
		pool,
		machinePool,
		"provisioning",
		"",
		intent,
		machinePool.DefaultMachineEnv,
		machinePool.DefaultMachineSecretEnv,
		now,
	)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO project_machine_grants(org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, metadata, created_at, updated_at) VALUES ($1, $2, $3, 'pool', $4, 'secret resolution machine', '{}'::jsonb, $5, $5)`,
		orgID,
		projectID,
		machineID,
		poolGrant.ID,
		now,
	); err != nil {
		t.Fatalf("insert project machine grant: %v", err)
	}
	provider := &captureProvider{
		provisionResourceID: "resource-1",
		prepare: func(provisioning executionstore.MachineProvisioningConfig) (executionstore.MachineResourceFacts, error) {
			if provisioning.CPU != nil || provisioning.MemoryMB != nil {
				t.Fatalf("preparation input resources = cpu %v memory %v, want unresolved", provisioning.CPU, provisioning.MemoryMB)
			}
			return executionstore.MachineResourceFacts{
				CPU:      intPtrForManagerTest(1),
				MemoryMB: intPtrForManagerTest(1024),
			}, nil
		},
	}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://app.omnara.test",
	}
	provisionErr := errors.New("provider provision failed")

	switch scenario {
	case provisioningFacts:
		if err := manager.ProvisionMachine(ctx, orgID, machineID); err != nil {
			t.Fatalf("provision machine: %v", err)
		}
		if got := providerConfigField(t, definition.rawConfig, "mode"); got != "provision" {
			t.Fatalf("provider config = %s, want pool config", definition.rawConfig)
		}
		if definition.authToken != "pool-token" {
			t.Fatalf("provider auth token = %q, want pool-token", definition.authToken)
		}
		provisioning := provider.provisioning
		if provisioning == nil {
			t.Fatal("expected provider to be called with provision config")
		}
		if err := bearertoken.Validate(provider.machineToken, bearertoken.KindDaemon); err != nil {
			t.Fatalf("managed provider machine token is not canonical: %v", err)
		}
		if provisioning.CPU == nil || *provisioning.CPU != 1 ||
			provisioning.MemoryMB == nil || *provisioning.MemoryMB != 1024 {
			t.Fatalf("captured machine provisioning = %+v", provisioning)
		}
		if len(provider.machineEnv) != 2 || provider.machineEnv["PLAIN"] != "plain" ||
			provider.machineEnv["API_TOKEN"] != "runtime-secret" {
			t.Fatalf("captured machine env = %+v, want resolved env and secret env", provider.machineEnv)
		}
		installationID, err := store.Identity().GetInstallationID(ctx)
		if err != nil {
			t.Fatalf("get installation id: %v", err)
		}
		if provider.installationID != installationID || provider.machineID != machineID {
			t.Fatalf(
				"provider identity = %s/%s, want %s/%s",
				provider.installationID,
				provider.machineID,
				installationID,
				machineID,
			)
		}
		machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
		if err != nil {
			t.Fatalf("get machine: %v", err)
		}
		if machine.LifecycleState != "active" || machine.ConnectionState != "offline" ||
			machine.ProviderResourceID != "resource-1" ||
			machine.ProviderProvisionAttemptedAt == nil {
			t.Fatalf("unexpected provisioned machine: %+v", machine)
		}
		if machine.CPU == nil || *machine.CPU != 1 || machine.MemoryMB == nil || *machine.MemoryMB != 1024 {
			t.Fatalf("persisted provider facts = cpu %v memory %v", machine.CPU, machine.MemoryMB)
		}

	case provisioningCapacityRejection:
		secondMachineID := insertPoolMachineForManagerTestWithFields(
			t,
			ctx,
			pool,
			machinePool,
			"provisioning",
			"",
			intent,
			machinePool.DefaultMachineEnv,
			machinePool.DefaultMachineSecretEnv,
			now,
		)
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO project_machine_grants(org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, metadata, created_at, updated_at) VALUES ($1, $2, $3, 'pool', $4, 'over-cap preparation machine', '{}'::jsonb, $5, $5)`,
			orgID,
			projectID,
			secondMachineID,
			poolGrant.ID,
			now,
		); err != nil {
			t.Fatalf("insert second project machine grant: %v", err)
		}
		provider.prepare = func(executionstore.MachineProvisioningConfig) (executionstore.MachineResourceFacts, error) {
			return executionstore.MachineResourceFacts{
				CPU:      intPtrForManagerTest(1),
				MemoryMB: intPtrForManagerTest(2048),
			}, nil
		}
		provider.provisioning = nil
		if err := manager.ProvisionMachine(ctx, orgID, secondMachineID); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
			t.Fatalf("over-cap provisioning error = %v, want state transition conflict", err)
		}
		secondMachine, err := store.Execution().GetMachine(ctx, orgID, secondMachineID)
		if err != nil {
			t.Fatalf("get over-cap machine: %v", err)
		}
		if secondMachine.LifecycleState != "provision_failed" ||
			secondMachine.LifecycleReasonCode != "provisioning_admission_failed" ||
			secondMachine.CPU != nil || secondMachine.MemoryMB != nil {
			t.Fatalf("over-cap machine = %+v", secondMachine)
		}
		if provider.provisioning != nil {
			t.Fatalf("provider provisioned over-cap machine with %+v", provider.provisioning)
		}

	case provisioningFactDrift:
		driftMachineID := insertPoolMachineForManagerTestWithFields(
			t,
			ctx,
			pool,
			machinePool,
			"provisioning",
			"",
			intent,
			machinePool.DefaultMachineEnv,
			machinePool.DefaultMachineSecretEnv,
			now,
		)
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO project_machine_grants(org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, metadata, created_at, updated_at) VALUES ($1, $2, $3, 'pool', $4, 'provider fact drift machine', '{}'::jsonb, $5, $5)`,
			orgID,
			projectID,
			driftMachineID,
			poolGrant.ID,
			now,
		); err != nil {
			t.Fatalf("insert drift project machine grant: %v", err)
		}
		provider.prepare = func(executionstore.MachineProvisioningConfig) (executionstore.MachineResourceFacts, error) {
			return executionstore.MachineResourceFacts{
				CPU:      intPtrForManagerTest(1),
				MemoryMB: intPtrForManagerTest(1024),
			}, nil
		}
		provider.provisionResourceID = ""
		provider.provisionErr = provisionErr
		if err := manager.ProvisionMachine(ctx, orgID, driftMachineID); !errors.Is(err, provisionErr) {
			t.Fatalf("initial provider failure = %v, want provision failure", err)
		}
		provider.prepare = func(executionstore.MachineProvisioningConfig) (executionstore.MachineResourceFacts, error) {
			return executionstore.MachineResourceFacts{
				CPU:      intPtrForManagerTest(2),
				MemoryMB: intPtrForManagerTest(1024),
			}, nil
		}
		provider.provisionErr = nil
		provider.provisioning = nil
		makePoolMachineReadyForManagerReconcile(t, ctx, pool, orgID, driftMachineID)
		if err := manager.ProvisionMachine(ctx, orgID, driftMachineID); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
			t.Fatalf("provider fact drift error = %v, want state transition conflict", err)
		}
		driftMachine, err := store.Execution().GetMachine(ctx, orgID, driftMachineID)
		if err != nil {
			t.Fatalf("get provider fact drift machine: %v", err)
		}
		if driftMachine.LifecycleState != "provision_failed" ||
			driftMachine.LifecycleReasonCode != "provisioning_admission_failed" ||
			driftMachine.CPU == nil || *driftMachine.CPU != 1 ||
			driftMachine.MemoryMB == nil || *driftMachine.MemoryMB != 1024 {
			t.Fatalf("provider fact drift machine = %+v", driftMachine)
		}
		if provider.provisioning != nil {
			t.Fatalf("provider provisioned machine after fact drift with %+v", provider.provisioning)
		}
		provider.inspectResourceID = "resource-from-ambiguous-first-attempt"
		provider.inspectFound = true
		makePoolMachineReadyForManagerReconcile(t, ctx, pool, orgID, driftMachineID)
		if err := manager.ProvisionMachine(ctx, orgID, driftMachineID); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
			t.Fatalf("final provider fact drift error = %v, want state transition conflict", err)
		}
		driftMachine, err = store.Execution().GetMachine(ctx, orgID, driftMachineID)
		if err != nil {
			t.Fatalf("get cleaned-up provider fact drift machine: %v", err)
		}
		if driftMachine.LifecycleState != "deleted" || driftMachine.DeletedAt == nil {
			t.Fatalf("provider fact drift machine was not cleaned up: %+v", driftMachine)
		}
		if len(provider.deletedResourceIDs) != 1 ||
			provider.deletedResourceIDs[0] != "resource-from-ambiguous-first-attempt" {
			t.Fatalf(
				"deleted resources after provider fact drift = %v, want prior ambiguous resource",
				provider.deletedResourceIDs,
			)
		}

	case provisioningFinalFailure:
		cleanupMachineID := insertPoolMachineForManagerTestWithFields(
			t,
			ctx,
			pool,
			machinePool,
			"provisioning",
			"",
			intent,
			machinePool.DefaultMachineEnv,
			machinePool.DefaultMachineSecretEnv,
			now,
		)
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO project_machine_grants(org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, metadata, created_at, updated_at) VALUES ($1, $2, $3, 'pool', $4, 'final attempt cleanup machine', '{}'::jsonb, $5, $5)`,
			orgID,
			projectID,
			cleanupMachineID,
			poolGrant.ID,
			now,
		); err != nil {
			t.Fatalf("insert cleanup project machine grant: %v", err)
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE machines SET provision_attempts = $3 WHERE org_id = $1 AND id = $2`,
			orgID,
			cleanupMachineID,
			executionstore.DefaultPoolMachineProvisionFailureLimit-1,
		); err != nil {
			t.Fatalf("seed final provisioning attempt: %v", err)
		}
		provider.prepare = func(executionstore.MachineProvisioningConfig) (executionstore.MachineResourceFacts, error) {
			return executionstore.MachineResourceFacts{
				CPU:      intPtrForManagerTest(1),
				MemoryMB: intPtrForManagerTest(1024),
			}, nil
		}
		provider.provisionResourceID = "resource-returned-with-provider-error"
		provider.provisionErr = provisionErr
		provider.inspectResourceID = "resource-after-provider-error"
		provider.inspectFound = true
		provider.deletedResourceIDs = nil
		if err := manager.ProvisionMachine(ctx, orgID, cleanupMachineID); !errors.Is(err, provisionErr) {
			t.Fatalf("final provider failure = %v, want provision failure", err)
		}
		cleanupMachine, err := store.Execution().GetMachine(ctx, orgID, cleanupMachineID)
		if err != nil {
			t.Fatalf("get final attempt cleanup machine: %v", err)
		}
		if cleanupMachine.LifecycleState != "deleted" || cleanupMachine.DeletedAt == nil {
			t.Fatalf("final attempt machine was not cleaned up: %+v", cleanupMachine)
		}
		if len(provider.deletedResourceIDs) != 1 ||
			provider.deletedResourceIDs[0] != "resource-returned-with-provider-error" {
			t.Fatalf("deleted resources = %v, want provider-returned resource", provider.deletedResourceIDs)
		}
		if provider.inspectMachineID != storage.NilID {
			t.Fatalf("provider resource was inspected despite checkpointed identity: %s", provider.inspectMachineID)
		}

	case provisioningAdmissionFinalFailure:
		if _, err := pool.Exec(
			ctx,
			`UPDATE machines SET provision_attempts = $3 WHERE org_id = $1 AND id = $2`,
			orgID,
			machineID,
			executionstore.DefaultPoolMachineProvisionFailureLimit-1,
		); err != nil {
			t.Fatalf("seed final admission attempt: %v", err)
		}
		provider.prepare = func(executionstore.MachineProvisioningConfig) (executionstore.MachineResourceFacts, error) {
			return executionstore.MachineResourceFacts{
				CPU:      intPtrForManagerTest(1),
				MemoryMB: intPtrForManagerTest(2048),
			}, nil
		}
		if err := manager.ProvisionMachine(ctx, orgID, machineID); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
			t.Fatalf("final admission error = %v, want state transition conflict", err)
		}
		machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
		if err != nil {
			t.Fatalf("get admission-rejected machine: %v", err)
		}
		if machine.LifecycleState != "deleted" || machine.DeletedAt == nil ||
			machine.ProviderProvisionAttemptedAt != nil {
			t.Fatalf("admission-rejected machine = %+v, want finalized without provider attempt", machine)
		}
		if provider.provisioning != nil || provider.inspectMachineID != storage.NilID ||
			len(provider.deletedResourceIDs) != 0 {
			t.Fatalf(
				"provider was used during admission cleanup: provisioning=%+v inspect=%s deleted=%v",
				provider.provisioning,
				provider.inspectMachineID,
				provider.deletedResourceIDs,
			)
		}
		var grantCount int
		if err := pool.QueryRow(
			ctx,
			`SELECT count(*) FROM project_machine_grants WHERE org_id = $1 AND machine_id = $2`,
			orgID,
			machineID,
		).Scan(&grantCount); err != nil {
			t.Fatalf("load admission-rejected machine grant: %v", err)
		}
		if grantCount != 0 {
			t.Fatalf("admission-rejected machine grants = %d, want 0", grantCount)
		}
	default:
		t.Fatalf("unknown provisioning scenario %d", scenario)
	}
}

func TestManagerValidatesPoolPolicyBeforeProvisioning(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 9, 15, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "provider-policy-provision", now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"provider-policy-auth",
		"pool-token",
	)
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Provider Policy Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForManagerTest(100),
			MaxTotalMemoryMB:     intPtrForManagerTest(1024 * 1024),
			MaxMachineCPU:        intPtrForManagerTest(100),
			MaxMachineMemoryMB:   intPtrForManagerTest(1024 * 1024),
		},
		1,
		1024,
		nil,
		nil,
		map[string]any{"image": "default"},
	))
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTestWithFields(
		t,
		ctx,
		pool,
		machinePool,
		"provisioning",
		"",
		machineProvisioningForManagerTest(t,
			1,
			1024,
			map[string]any{"image": "other"},
		),
		json.RawMessage(`{}`),
		json.RawMessage(`{}`),
		now,
	)
	provider := &captureProvider{provisionResourceID: "resource-1"}
	policyErr := errors.New("provider policy rejected machine config")
	definition := &testProviderDefinition{
		provider: provider,
		validate: func(_ executionstore.MachinePoolProviderPolicy, machineProvisioning executionstore.MachineProvisioningConfig) error {
			var image string
			if err := json.Unmarshal(machineProvisioning.ProviderOptions["image"], &image); err != nil {
				return err
			}
			if image != "default" {
				return policyErr
			}
			return nil
		},
	}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://app.omnara.test",
	}

	if err := manager.ProvisionMachine(ctx, orgID, machineID); !errors.Is(err, policyErr) {
		t.Fatalf("provision error = %v, want policy error", err)
	}
	if provider.provisioning != nil {
		t.Fatalf("provider was called with policy-rejected config: %+v", provider.provisioning)
	}
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	if machine.LifecycleState != "provision_failed" || machine.LifecycleReasonCode != "provider_config_invalid" {
		t.Fatalf("unexpected machine failure state: %+v", machine)
	}
}

func TestManagerDeletesMachineWhenMachineEnvIsPermanentlyUnresolvable(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "secret-resolution-failure", now)
	projectID, actorID := seedManagerProjectActor(
		t,
		ctx,
		pool,
		store,
		orgID,
		"secret-resolution-project",
		"secret-resolution@example.com",
		now,
	)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"secret-resolution-provider-auth",
		"pool-token",
	)
	machineSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     orgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "runtime-env-secret",
		Material:  secrets.GenericMaterial{Value: "runtime-secret"},
		Actor:     managerUserPrincipal(actorID),
	})
	if err != nil {
		t.Fatalf("create machine secret: %v", err)
	}
	machineSecretID := secretPublicIDForManagerTest(t, machineSecret.ID)
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Secret Resolution Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{"mode":"provision"}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForManagerTest(100),
			MaxTotalMemoryMB:     intPtrForManagerTest(1024 * 1024),
			MaxMachineCPU:        intPtrForManagerTest(100),
			MaxMachineMemoryMB:   intPtrForManagerTest(1024 * 1024),
		},
		1,
		1024,
		nil,
		map[string]string{"API_TOKEN": machineSecretID},
		map[string]any{},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	secretGrant, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID:           orgID,
		SecretID:        machineSecret.ID,
		TargetProjectID: projectID,
		Actor:           managerUserPrincipal(actorID),
	})
	if err != nil {
		t.Fatalf("grant machine secret: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:         orgID,
		ProjectID:     projectID,
		MachinePoolID: machinePool.ID,
	})

	if err != nil {
		t.Fatalf("create project pool grant: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(t, ctx, pool, machinePool, "provisioning", "", now)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO project_machine_grants(org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, metadata, created_at, updated_at) VALUES ($1, $2, $3, 'pool', $4, 'secret resolution machine', '{}'::jsonb, $5, $5)`,
		orgID,
		projectID,
		machineID,
		poolGrant.ID,
		now,
	); err != nil {
		t.Fatalf("insert project machine grant: %v", err)
	}
	if _, err := store.Secrets().DeleteSecretGrant(
		ctx,
		secretstore.DeleteSecretGrantInput{
			OrgID: orgID, SecretID: secretGrant.SecretID,
			GrantID: secretGrant.ID, Actor: managerUserPrincipal(actorID),
		},
	); err != nil {
		t.Fatalf("revoke machine secret grant: %v", err)
	}
	provider := &captureProvider{provisionResourceID: "resource-1"}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://app.omnara.test",
	}

	err = manager.ProvisionMachine(ctx, orgID, machineID)
	if err == nil || !strings.Contains(err.Error(), "secret_env.API_TOKEN") ||
		!errors.Is(err, storeerr.ErrPermanentEnvironment) {
		t.Fatalf("provision machine error = %v, want permanent unresolvable secret_env failure", err)
	}
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	if machine.LifecycleState != "deleted" || machine.DeletedAt == nil {
		t.Fatalf(
			"machine lifecycle = %q deleted_at %v, want machine deleted without provisioning retries",
			machine.LifecycleState,
			machine.DeletedAt,
		)
	}
	if machine.ProvisionAttempts != 1 {
		t.Fatalf("provision attempts = %d, want 1", machine.ProvisionAttempts)
	}
	if machine.ProviderProvisionAttemptedAt != nil {
		t.Fatalf("provider provision attempted at = %v, want no provider attempt", machine.ProviderProvisionAttemptedAt)
	}
	if provider.provisioning != nil {
		t.Fatalf("provider machine provisioning = %+v, want provider not called", provider.provisioning)
	}
}

func TestManagerUsesArchivedPoolProviderConfigForCleanup(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "provider-config-cleanup", now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"cleanup-provider-auth",
		"cleanup-token",
	)
	maxCPU, maxMemoryMB := 100, 1024*1024
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Cleanup Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{"mode":"cleanup"}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForManagerTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForManagerTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForManagerTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForManagerTest(maxMemoryMB),
		},
		1,
		1024,
		map[string]string{},
		nil,
		map[string]any{},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(t, ctx, pool, machinePool, "active", "resource-cleanup", now)
	if _, err := pool.Exec(
		ctx,
		`UPDATE machine_pools machine_pool
		 SET deleted_at = $3,
		     deletion_provider_auth_secret_version_id = (
		       SELECT current_version_id FROM secrets WHERE id = machine_pool.provider_auth_secret_id
		     ),
		     updated_at = $3
		 WHERE org_id = $1 AND id = $2`,
		orgID,
		machinePool.ID,
		now.Add(time.Second),
	); err != nil {
		t.Fatalf("archive machine pool: %v", err)
	}
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	provider := &captureProvider{}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://app.omnara.test",
	}

	if err := manager.DeleteMachine(
		ctx,
		executionstore.PoolMachineCleanupCandidate{
			Machine:       machine,
			ReasonCode:    "agent_archived_cleanup",
			ReasonMessage: "cleaning up machine after agent archived",
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	if got := providerConfigField(t, definition.rawConfig, "mode"); got != "cleanup" {
		t.Fatalf("provider config = %s, want archived pool config", definition.rawConfig)
	}
	if definition.authToken != "cleanup-token" {
		t.Fatalf("provider auth token = %q, want cleanup-token", definition.authToken)
	}
	if len(provider.deletedResourceIDs) != 1 || provider.deletedResourceIDs[0] != "resource-cleanup" {
		t.Fatalf("deleted resources = %v, want resource-cleanup", provider.deletedResourceIDs)
	}
	installationID, err := store.Identity().GetInstallationID(ctx)
	if err != nil {
		t.Fatalf("get installation id: %v", err)
	}
	if provider.deleteInstallationID != installationID || provider.deleteMachineID != machineID {
		t.Fatalf(
			"deleted identity = %s/%s, want %s/%s",
			provider.deleteInstallationID,
			provider.deleteMachineID,
			installationID,
			machineID,
		)
	}
	machine, err = store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get deleted machine: %v", err)
	}
	if machine.LifecycleState != "deleted" || machine.DeletedAt == nil {
		t.Fatalf("machine was not deleted after cleanup: %+v", machine)
	}
}

func TestOrganizationDeletionRetainsTenantProviderCredentialThroughMachineCleanup(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "organization-provider-cleanup", now)
	_, actorID := seedManagerProjectActor(
		t,
		ctx,
		pool,
		store,
		orgID,
		"organization-provider-cleanup-project",
		"organization-provider-cleanup@example.com",
		now,
	)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"organization-provider-cleanup-key",
		"organization-cleanup-token",
	)
	maxCPU, maxMemoryMB := 4, 4096
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		machinePoolInputWithDefaultMachineForManagerTest(
			t,
			executionstore.CreateMachinePoolInput{
				OrgID:                orgID,
				Name:                 "Organization Provider Cleanup",
				Provider:             "capture",
				ProviderConfig:       json.RawMessage(`{"mode":"organization-cleanup"}`),
				ProviderAuthSecretID: providerAuthSecretID,
				MaxTotalMachines:     1,
				MaxTotalCPU:          intPtrForManagerTest(maxCPU),
				MaxTotalMemoryMB:     intPtrForManagerTest(maxMemoryMB),
				MaxMachineCPU:        intPtrForManagerTest(maxCPU),
				MaxMachineMemoryMB:   intPtrForManagerTest(maxMemoryMB),
			},
			1,
			1024,
			map[string]string{},
			nil,
			map[string]any{},
		),
	)
	if err != nil {
		t.Fatalf("create organization cleanup machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(
		t,
		ctx,
		pool,
		machinePool,
		"active",
		"resource-organization-cleanup",
		now,
	)

	machines, err := store.Organizations().DeleteOrganization(
		ctx,
		orgID,
		managerUserPrincipal(actorID),
	)
	if err != nil {
		t.Fatalf("delete organization with active provider machine: %v", err)
	}
	if len(machines) != 1 || machines[0].ID != machineID || machines[0].LifecycleState != "deleting" {
		t.Fatalf("organization deletion machines = %+v, want deleting machine %s", machines, machineID)
	}

	var retainedSecretID, deletionVersionID, currentVersionID *storage.ID
	var secretDeletedAt *time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT pool.provider_auth_secret_id,
		        pool.deletion_provider_auth_secret_version_id,
		        secret.current_version_id,
		        secret.deleted_at
		 FROM machine_pools pool
		 JOIN secrets secret ON secret.id = pool.provider_auth_secret_id
		 WHERE pool.org_id = $1 AND pool.id = $2`,
		orgID,
		machinePool.ID,
	).Scan(&retainedSecretID, &deletionVersionID, &currentVersionID, &secretDeletedAt); err != nil {
		t.Fatalf("load retained provider credential: %v", err)
	}
	if retainedSecretID == nil || *retainedSecretID != providerAuthSecretID ||
		deletionVersionID == nil || currentVersionID != nil || secretDeletedAt == nil {
		t.Fatalf(
			"retained credential state = secret %v deletion version %v current %v deleted %v",
			retainedSecretID,
			deletionVersionID,
			currentVersionID,
			secretDeletedAt,
		)
	}
	var retainedVersionCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)::integer FROM secret_versions WHERE secret_id = $1 AND id = $2`,
		providerAuthSecretID,
		*deletionVersionID,
	).Scan(&retainedVersionCount); err != nil {
		t.Fatalf("count retained provider credential version: %v", err)
	}
	if retainedVersionCount != 1 {
		t.Fatalf("retained provider credential versions = %d, want 1", retainedVersionCount)
	}

	provider := &captureProvider{}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://app.omnara.test",
	}
	deleted, err := manager.DeleteMachines(ctx, machines)
	if err != nil {
		t.Fatalf("delete organization machines through provider: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted organization machines = %d, want 1", deleted)
	}
	if definition.authToken != "organization-cleanup-token" {
		t.Fatalf("provider auth token = %q, want retained organization credential", definition.authToken)
	}
	if len(provider.deletedResourceIDs) != 1 || provider.deletedResourceIDs[0] != "resource-organization-cleanup" {
		t.Fatalf("provider deleted resources = %v", provider.deletedResourceIDs)
	}

	var credentialReleased bool
	var remainingVersionCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT provider_auth_secret_id IS NULL
		          AND deletion_provider_auth_secret_version_id IS NULL,
		        (SELECT count(*)::integer FROM secret_versions WHERE secret_id = $2)
		 FROM machine_pools
		 WHERE org_id = $1 AND id = $3`,
		orgID,
		providerAuthSecretID,
		machinePool.ID,
	).Scan(&credentialReleased, &remainingVersionCount); err != nil {
		t.Fatalf("load released provider credential: %v", err)
	}
	if !credentialReleased || remainingVersionCount != 0 {
		t.Fatalf(
			"provider credential after teardown = released %v versions %d",
			credentialReleased,
			remainingVersionCount,
		)
	}
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("load organization machine after provider teardown: %v", err)
	}
	if machine.LifecycleState != "deleted" || machine.DeletedAt == nil {
		t.Fatalf("organization machine after provider teardown = %+v", machine)
	}
}

func TestManagerWakeMachineRetriesProviderWake(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "provider-wake", now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"provider-wake-auth",
		"pool-token",
	)
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		machinePoolInputWithDefaultMachineForManagerTest(
			t,
			executionstore.CreateMachinePoolInput{
				OrgID:                orgID,
				Name:                 "Provider Wake Pool",
				Provider:             "capture",
				ProviderConfig:       json.RawMessage(`{}`),
				ProviderAuthSecretID: providerAuthSecretID,
				MaxTotalMachines:     1,
				MaxTotalCPU:          intPtrForManagerTest(1),
				MaxTotalMemoryMB:     intPtrForManagerTest(1024),
				MaxMachineCPU:        intPtrForManagerTest(1),
				MaxMachineMemoryMB:   intPtrForManagerTest(1024),
			},
			1,
			1024,
			nil,
			nil,
			map[string]any{},
		),
	)
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(
		t,
		ctx,
		pool,
		machinePool,
		"active",
		"resource-1",
		now,
	)
	sandboxURL := "https://sandbox.example.test"
	wakeErr := errors.New("provider wake failed")
	provider := &captureProvider{wakeErrors: []error{wakeErr, wakeErr}}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(&testProviderDefinition{provider: provider}),
		PublicURL: "https://app.omnara.test",
	}

	shouldRetry, err := manager.WakeMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("wake non-asleep machine: %v", err)
	}
	if shouldRetry {
		t.Fatal("should retry = true for offline machine")
	}
	if len(provider.wakeInputs) != 0 {
		t.Fatalf("offline wake attempts = %d, want 0", len(provider.wakeInputs))
	}

	tokenID := uuid.New()
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO machine_daemon_tokens(id, org_id, machine_id, name, token_hash, created_at)
		 VALUES ($1, $2, $3, 'provider-wake', $4, $5)`,
		tokenID,
		orgID,
		machineID,
		uuid.NewString(),
		now,
	); err != nil {
		t.Fatalf("insert daemon token: %v", err)
	}
	registration, err := store.Execution().RegisterDaemonRuntimeWithReconciliation(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            orgID,
		MachineID:        machineID,
		DaemonTokenID:    tokenID,
		DaemonInstanceID: uuid.New(),
		DaemonVersion:    "1.0.0",
	})
	if err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}
	runtime := registration.Runtime
	shouldRetry, err = manager.WakeMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("wake online machine: %v", err)
	}
	if !shouldRetry {
		t.Fatal("should retry = false for online machine")
	}
	if len(provider.wakeInputs) != 0 {
		t.Fatalf("online wake attempts = %d, want 0", len(provider.wakeInputs))
	}
	if _, err := store.Execution().EndDaemonRuntime(ctx, executionstore.DaemonRuntimeAuthority{
		OrgID:           orgID,
		MachineID:       machineID,
		DaemonRuntimeID: runtime.ID,
		DaemonTokenID:   tokenID,
	}); err != nil {
		t.Fatalf("end daemon runtime: %v", err)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET sandbox_url = $1, asleep_since = $2 WHERE org_id = $3 AND id = $4`,
		sandboxURL,
		now,
		orgID,
		machineID,
	); err != nil {
		t.Fatalf("mark machine asleep: %v", err)
	}
	shouldRetry, err = manager.WakeMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("wake machine: %v", err)
	}
	if !shouldRetry {
		t.Fatal("should retry = false after successful wake")
	}
	if len(provider.wakeInputs) != machineWakeAttempts {
		t.Fatalf("wake attempts = %d, want %d", len(provider.wakeInputs), machineWakeAttempts)
	}
	for _, input := range provider.wakeInputs {
		if input.ProviderResourceID != "resource-1" || input.SandboxURL != sandboxURL {
			t.Fatalf("wake input = %+v", input)
		}
	}
	shouldRetry, err = manager.WakeMachine(ctx, orgID, machineID)
	if err != nil || !shouldRetry {
		t.Fatalf("repeat pending wake = (%t, %v), want true/nil", shouldRetry, err)
	}
	if len(provider.wakeInputs) != machineWakeAttempts {
		t.Fatalf("pending wake made another provider call: %d attempts", len(provider.wakeInputs))
	}

	runtimeProtectionEnabled := true
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID:                    orgID,
		ID:                       machinePool.ID,
		RuntimeProtectionEnabled: &runtimeProtectionEnabled,
	}); err != nil {
		t.Fatalf("enable runtime protection: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE machines
SET wake_attempt_expires_at = statement_timestamp() - interval '1 millisecond'
WHERE org_id = $1 AND id = $2
`, orgID, machineID); err != nil {
		t.Fatalf("expire protected wake attempt: %v", err)
	}
	shouldRetry, err = manager.WakeMachine(ctx, orgID, machineID)
	if !errors.Is(err, storeerr.ErrMachineWakeUnresolved) || shouldRetry {
		t.Fatalf("unresolved protected wake = (%t, %v), want false/unresolved", shouldRetry, err)
	}
	if len(provider.wakeInputs) != machineWakeAttempts {
		t.Fatalf("unresolved protected wake made another provider call: %d attempts", len(provider.wakeInputs))
	}
	runtimeProtectionEnabled = false
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID:                    orgID,
		ID:                       machinePool.ID,
		RuntimeProtectionEnabled: &runtimeProtectionEnabled,
	}); err != nil {
		t.Fatalf("disable runtime protection: %v", err)
	}

	provider.wakeInputs = nil
	provider.wakeErrors = []error{wakeErr, wakeErr, wakeErr}
	if _, err := pool.Exec(ctx, `
UPDATE machines
SET wake_attempt_expires_at = statement_timestamp() - interval '5 minutes'
WHERE org_id = $1 AND id = $2
`, orgID, machineID); err != nil {
		t.Fatalf("expire unprotected wake intent: %v", err)
	}
	shouldRetry, err = manager.WakeMachine(ctx, orgID, machineID)
	if !errors.Is(err, wakeErr) {
		t.Fatalf("wake error = %v, want %v", err, wakeErr)
	}
	if shouldRetry {
		t.Fatal("should retry = true after failed wake")
	}
	if len(provider.wakeInputs) != machineWakeAttempts {
		t.Fatalf("failed wake attempts = %d, want %d", len(provider.wakeInputs), machineWakeAttempts)
	}
}

func TestManagerDeletesMachineWithoutProviderProvisionAttempt(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 10, 30, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "unattempted-deleting-no-provider", now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"unattempted-delete-provider-auth",
		"must-not-load",
	)
	maxCPU, maxMemoryMB := 100, 1024*1024
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Unattempted Deleting Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{"api_token":"must-not-load"}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForManagerTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForManagerTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForManagerTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForManagerTest(maxMemoryMB),
		},
		1,
		1024,
		map[string]string{},
		nil,
		map[string]any{},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTestWithFields(
		t,
		ctx,
		pool,
		machinePool,
		"deleting",
		"",
		executionstore.MachineProvisioningConfig{ProviderOptions: map[string]json.RawMessage{}},
		machinePool.DefaultMachineEnv,
		machinePool.DefaultMachineSecretEnv,
		now,
	)
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET provision_attempts = $3 WHERE org_id = $1 AND id = $2`,
		orgID,
		machineID,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
	); err != nil {
		t.Fatalf("seed exhausted pre-provider attempts: %v", err)
	}
	bindingID, grantID := seedPoolMachineCleanupBindingForManagerTest(
		t,
		ctx,
		pool,
		orgID,
		machinePool.ID,
		machineID,
		now,
	)
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get deleting machine: %v", err)
	}
	if machine.ProvisionAttempts != executionstore.DefaultPoolMachineProvisionFailureLimit ||
		machine.ProviderResourceID != "" ||
		machine.ProviderProvisionAttemptedAt != nil {
		t.Fatalf("seeded machine = %+v, want no provider work", machine)
	}
	provider := &captureProvider{}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://api.omnara.test",
	}

	if err := manager.DeleteMachine(
		ctx,
		executionstore.PoolMachineCleanupCandidate{
			Machine:       machine,
			ReasonCode:    "machine_tool_delete",
			ReasonMessage: "deleted by machine tool",
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	if len(definition.rawConfig) != 0 {
		t.Fatalf("provider config was loaded for unattempted delete: %s", definition.rawConfig)
	}
	if len(provider.deletedResourceIDs) != 0 {
		t.Fatalf("deleted resources = %v, want none", provider.deletedResourceIDs)
	}
	machine, err = store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get deleted machine: %v", err)
	}
	if machine.LifecycleState != "deleted" || machine.DeletedAt == nil {
		t.Fatalf("machine was not deleted after cleanup: %+v", machine)
	}
	var bindingState string
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_machine_bindings WHERE org_id = $1 AND id = $2`, orgID, bindingID).
		Scan(&bindingState); err != nil {
		t.Fatalf("load binding state: %v", err)
	}
	if bindingState != "released" {
		t.Fatalf("binding state = %q, want released", bindingState)
	}
	var grantRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_machine_grants WHERE org_id = $1 AND id = $2`, orgID, grantID).
		Scan(&grantRows); err != nil {
		t.Fatalf("count generated grant rows: %v", err)
	}
	if grantRows != 0 {
		t.Fatalf("generated grant rows after teardown = %d, want 0", grantRows)
	}
}

func TestManagerRetriesDeletingAttemptedMachineWithoutResource(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 11, 0, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "attempted-deleting-missing-resource", now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"attempted-delete-provider-auth",
		"attempted-delete-token",
	)
	maxCPU, maxMemoryMB := 100, 1024*1024
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Attempted Cleanup Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForManagerTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForManagerTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForManagerTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForManagerTest(maxMemoryMB),
		},
		1,
		1024,
		map[string]string{},
		nil,
		map[string]any{},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(t, ctx, pool, machinePool, "deleting", "", now)
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET lifecycle_reason_code = 'provider_not_configured', lifecycle_reason_message = 'machine provider is not configured', provision_attempts = 1, provider_provision_attempted_at = $3 WHERE org_id = $1 AND id = $2`,
		orgID,
		machineID,
		now,
	); err != nil {
		t.Fatalf("mark machine provider not configured: %v", err)
	}
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	provider := &captureProvider{}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://api.omnara.test",
	}

	if err := manager.DeleteMachine(
		ctx,
		executionstore.PoolMachineCleanupCandidate{
			Machine:       machine,
			ReasonCode:    "machine_tool_delete",
			ReasonMessage: "deleted by machine tool",
		},
	); err == nil {
		t.Fatal("expected delete machine to retry missing provider resource")
	}
	installationID, err := store.Identity().GetInstallationID(ctx)
	if err != nil {
		t.Fatalf("get installation id: %v", err)
	}
	if provider.inspectInstallationID != installationID || provider.inspectMachineID != machineID {
		t.Fatalf(
			"inspected identity = %s/%s, want %s/%s",
			provider.inspectInstallationID,
			provider.inspectMachineID,
			installationID,
			machineID,
		)
	}
	machine, err = store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get delete failed machine: %v", err)
	}
	if machine.LifecycleState != "delete_failed" || machine.LifecycleReasonCode != "provider_resource_not_found" ||
		machine.DeleteAttempts != 1 {
		t.Fatalf("machine did not enter provider missing-resource retry: %+v", machine)
	}
	if len(provider.deletedResourceIDs) != 0 {
		t.Fatalf("deleted resources = %v, want none", provider.deletedResourceIDs)
	}
}

func TestManagerFinalizesStaleMissingProviderResource(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 11, 30, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "stale-missing-provider-resource", now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"stale-missing-provider-auth",
		"stale-missing-token",
	)
	maxCPU, maxMemoryMB := 100, 1024*1024
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Stale Missing Provider Resource Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForManagerTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForManagerTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForManagerTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForManagerTest(maxMemoryMB),
		},
		1,
		1024,
		map[string]string{},
		nil,
		map[string]any{},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(t, ctx, pool, machinePool, "deleting", "", now)
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines
		 SET lifecycle_state = 'delete_failed',
		     lifecycle_reason_code = 'provider_resource_not_found',
		     lifecycle_reason_message = 'provider resource was not found by allocation name',
		     provision_attempts = 1,
		     delete_attempts = 3,
		     provider_provision_attempted_at = statement_timestamp() - interval '25 hours',
		     next_reconcile_after = statement_timestamp() - interval '1 second',
		     updated_at = statement_timestamp()
		 WHERE org_id = $1 AND id = $2`,
		orgID,
		machineID,
	); err != nil {
		t.Fatalf("mark stale missing provider resource: %v", err)
	}
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get stale delete failed machine: %v", err)
	}
	provider := &captureProvider{}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://api.omnara.test",
	}

	if err := manager.DeleteMachine(
		ctx,
		executionstore.PoolMachineCleanupCandidate{
			Machine:       machine,
			ReasonCode:    machine.LifecycleReasonCode,
			ReasonMessage: machine.LifecycleReasonMessage,
		},
	); err != nil {
		t.Fatalf("delete stale missing provider resource: %v", err)
	}
	machine, err = store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get finalized machine: %v", err)
	}
	if machine.LifecycleState != "deleted" || machine.DeletedAt == nil {
		t.Fatalf("machine was not finalized after stale missing provider resource: %+v", machine)
	}
	if len(provider.deletedResourceIDs) != 0 {
		t.Fatalf("deleted resources = %v, want none", provider.deletedResourceIDs)
	}
}

func TestManagerReconcilesDeletingMachineWithProviderResource(t *testing.T) {
	ctx := context.Background()
	pool := openManagerIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(managerIntegrationKeyWrapper(t)),
		storage.WithMachinePoolProviders(machinePoolProviderTestResolvers{}),
	)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	orgID := seedManagerOrg(t, ctx, pool, "deleting-provider-resource", now)
	providerAuthSecretID := createProviderAuthSecretForManagerTest(
		t,
		ctx,
		pool,
		store,
		orgID,
		"deleting-resource-provider-auth",
		"deleting-resource-token",
	)
	maxCPU, maxMemoryMB := 100, 1024*1024
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForManagerTest(
		t,
		executionstore.CreateMachinePoolInput{
			OrgID:                orgID,
			Name:                 "Deleting Resource Pool",
			Provider:             "capture",
			ProviderConfig:       json.RawMessage(`{"api_token":"deleting-token"}`),
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForManagerTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForManagerTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForManagerTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForManagerTest(maxMemoryMB),
		},
		1,
		1024,
		map[string]string{},
		nil,
		map[string]any{},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machineID := insertPoolMachineForManagerTest(t, ctx, pool, machinePool, "deleting", "resource-deleting", now)
	bindingID, grantID := seedPoolMachineCleanupBindingForManagerTest(
		t,
		ctx,
		pool,
		orgID,
		machinePool.ID,
		machineID,
		now,
	)
	machine, err := store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get deleting machine: %v", err)
	}
	if machine.LifecycleState != "deleting" || machine.ProviderResourceID != "resource-deleting" {
		t.Fatalf("seeded machine = %+v, want deleting with provider resource", machine)
	}
	provider := &captureProvider{}
	definition := &testProviderDefinition{provider: provider}
	manager := Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   testProviderCatalog(definition),
		PublicURL: "https://api.omnara.test",
	}

	reconciled, err := manager.ReconcileCleanup(ctx, 10)
	if err != nil {
		t.Fatalf("reconcile cleanup: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled cleanup count = %d, want 1", reconciled)
	}
	if got := providerConfigField(t, definition.rawConfig, "api_token"); got != "deleting-token" {
		t.Fatalf("provider config = %s, want deleting pool config", definition.rawConfig)
	}
	if len(provider.deletedResourceIDs) != 1 || provider.deletedResourceIDs[0] != "resource-deleting" {
		t.Fatalf("deleted resources = %v, want resource-deleting", provider.deletedResourceIDs)
	}
	machine, err = store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		t.Fatalf("get deleted machine: %v", err)
	}
	if machine.LifecycleState != "deleted" || machine.DeletedAt == nil {
		t.Fatalf("machine was not deleted after reconcile cleanup: %+v", machine)
	}
	var bindingState string
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_machine_bindings WHERE org_id = $1 AND id = $2`, orgID, bindingID).
		Scan(&bindingState); err != nil {
		t.Fatalf("load binding state: %v", err)
	}
	if bindingState != "released" {
		t.Fatalf("binding state = %q, want released", bindingState)
	}
	var grantRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_machine_grants WHERE org_id = $1 AND id = $2`, orgID, grantID).
		Scan(&grantRows); err != nil {
		t.Fatalf("count generated grant rows: %v", err)
	}
	if grantRows != 0 {
		t.Fatalf("generated grant rows after teardown = %d, want 0", grantRows)
	}
}

func openManagerIntegrationDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
}

func seedManagerOrg(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	seed string,
	now time.Time,
) storage.ID {
	t.Helper()
	orgID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-machinepool-integration:"+seed))
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)`,
		orgID,
		seed,
		"idem-"+seed,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	return orgID
}

func seedManagerProjectActor(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	orgID storage.ID,
	seed, email string,
	now time.Time,
) (storage.ID, storage.ID) {
	t.Helper()
	projectID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-machinepool-integration:"+seed))
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $5)`,
		projectID,
		orgID,
		seed,
		"idem-"+seed,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	actor, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: email, DisplayName: seed})
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: orgID, UserID: actor.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add actor org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{OrgID: orgID, ProjectID: projectID, UserID: actor.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add actor project membership: %v", err)
	}
	return projectID, actor.ID
}

func managerIntegrationKeyWrapper(t *testing.T) secrets.KeyWrapper {
	t.Helper()
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"machine-pool-provider-test-key",
		map[string][]byte{"machine-pool-provider-test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create key wrapper: %v", err)
	}
	return keyWrapper
}

func createProviderAuthSecretForManagerTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	orgID storage.ID,
	name, value string,
) storage.ID {
	t.Helper()
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: name + "@example.com", DisplayName: name})
	if err != nil {
		t.Fatalf("create provider auth user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: orgID, UserID: user.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add provider auth user org membership: %v", err)
	}
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     orgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      name,
		Material:  secrets.GenericMaterial{Value: value},
		Actor:     managerUserPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create provider auth secret: %v", err)
	}
	return secret.ID
}

func insertPoolMachineForManagerTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	machinePool executionstore.MachinePoolRecord,
	lifecycleState, providerResourceID string,
	now time.Time,
) storage.ID {
	t.Helper()
	return insertPoolMachineForManagerTestWithFields(
		t,
		ctx,
		pool,
		machinePool,
		lifecycleState,
		providerResourceID,
		defaultMachineProvisioningForManagerTest(t, machinePool),
		machinePool.DefaultMachineEnv,
		machinePool.DefaultMachineSecretEnv,
		now,
	)
}

func machinePoolInputWithDefaultMachineForManagerTest(
	t *testing.T,
	input executionstore.CreateMachinePoolInput,
	cpu, memoryMB int,
	env, secretEnv map[string]string,
	providerOptions map[string]any,
) executionstore.CreateMachinePoolInput {
	t.Helper()
	input.DefaultMachineCPU = intPtrForManagerTest(cpu)
	input.DefaultMachineMemoryMB = intPtrForManagerTest(memoryMB)
	input.DefaultMachineEnv = rawJSONForManagerTest(t, env)
	input.DefaultMachineSecretEnv = rawJSONForManagerTest(t, secretEnv)
	input.DefaultMachineProviderOptions = rawJSONForManagerTest(t, providerOptions)
	return input
}

func machineProvisioningForManagerTest(
	t *testing.T,
	cpu, memoryMB int,
	providerOptions map[string]any,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	var rawProviderOptions map[string]json.RawMessage
	if providerOptions != nil {
		rawProviderOptions = make(map[string]json.RawMessage, len(providerOptions))
		for key, value := range providerOptions {
			rawProviderOptions[key] = rawJSONForManagerTest(t, value)
		}
	}
	return executionstore.MachineProvisioningConfig{
		CPU:             intPtrForManagerTest(cpu),
		MemoryMB:        intPtrForManagerTest(memoryMB),
		ProviderOptions: rawProviderOptions,
	}
}

func intPtrForManagerTest(value int) *int {
	return &value
}

func makePoolMachineReadyForManagerReconcile(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgID, machineID storage.ID,
) {
	t.Helper()
	tag, err := pool.Exec(
		ctx,
		`UPDATE machines
		 SET next_reconcile_after = statement_timestamp() - interval '1 millisecond'
		 WHERE org_id = $1 AND id = $2`,
		orgID,
		machineID,
	)
	if err != nil {
		t.Fatalf("make pool machine ready for reconcile: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("make pool machine ready for reconcile affected %d rows, want 1", tag.RowsAffected())
	}
}

func defaultMachineProvisioningForManagerTest(
	t *testing.T,
	machinePool executionstore.MachinePoolRecord,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	defaultMachineProvisioning, err := executionstore.MachineProvisioningFromDefaults(
		machinePool.DefaultMachineCPU,
		machinePool.DefaultMachineMemoryMB,
		machinePool.DefaultMachineProviderOptions,
	)
	if err != nil {
		t.Fatalf("default machine provisioning: %v", err)
	}
	return defaultMachineProvisioning
}

func insertPoolMachineForManagerTestWithFields(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	machinePool executionstore.MachinePoolRecord,
	lifecycleState, providerResourceID string,
	machineProvisioning executionstore.MachineProvisioningConfig,
	env, secretEnv json.RawMessage,
	now time.Time,
) storage.ID {
	t.Helper()
	var machineID storage.ID
	if err := pool.QueryRow(ctx, `
			INSERT INTO machines(
				org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
				lifecycle_changed_at, provider_resource_id, provider_provision_attempted_at,
				cpu, memory_mb, cwd, env, secret_env, provider_options, next_reconcile_after,
				metadata, created_at, updated_at
			)
			VALUES (
				$1, $2, 'pool', 'pool machine', $3, $4, $12, nullif($5, ''),
				CASE WHEN nullif($5, '') IS NULL THEN NULL::timestamptz ELSE $12::timestamptz END,
				$6, $7, $8,
				coalesce(nullif($9::jsonb, 'null'::jsonb), '{}'::jsonb),
				coalesce(nullif($10::jsonb, 'null'::jsonb), '{}'::jsonb),
				coalesce(nullif($11::jsonb, 'null'::jsonb), '{}'::jsonb),
				CASE WHEN $4 IN ('active', 'deleted') THEN NULL::timestamptz ELSE $12::timestamptz END,
				'{}'::jsonb, $12, $12
			)
			RETURNING id
			`,
		machinePool.OrgID,
		machinePool.ID,
		machinePool.Provider,
		lifecycleState,
		providerResourceID,
		machineProvisioning.CPU,
		machineProvisioning.MemoryMB,
		machinePool.DefaultCwd,
		env,
		secretEnv,
		rawJSONForManagerTest(t, machineProvisioning.ProviderOptions),
		now,
	).Scan(&machineID); err != nil {
		t.Fatalf("insert pool machine: %v", err)
	}
	return machineID
}

func rawJSONForManagerTest(t *testing.T, value any) json.RawMessage {
	t.Helper()
	if value == nil {
		return json.RawMessage(`{}`)
	}
	switch typed := value.(type) {
	case map[string]string:
		if typed == nil {
			return json.RawMessage(`{}`)
		}
	case map[string]json.RawMessage:
		if typed == nil {
			return json.RawMessage(`{}`)
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return raw
}

func providerConfigField(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode provider config: %v", err)
	}
	return decoded[key]
}

func seedPoolMachineCleanupBindingForManagerTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgID, machinePoolID, machineID storage.ID,
	now time.Time,
) (storage.ID, storage.ID) {
	t.Helper()
	userID := uuid.New()
	projectID := uuid.New()
	secretID := uuid.New()
	secretVersionID := uuid.New()
	providerConfigID := uuid.New()
	configuredModelID := uuid.New()
	configuredModelRevisionID := uuid.New()
	configID := uuid.New()
	agentID := uuid.New()
	poolGrantID := uuid.New()
	grantID := uuid.New()
	bindingID := uuid.New()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed pool machine cleanup binding: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(label, query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}

	exec("insert cleanup user", `
INSERT INTO users(id, display_name, created_at, updated_at)
VALUES ($1, 'Manager Cleanup User', $2, $2)
`, userID, now)

	exec("insert cleanup project", `
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Manager Cleanup Project', $3, $4, $4)
`, projectID, orgID, "idem-manager-cleanup-project-"+projectID.String(), now)

	exec("insert cleanup provider secret", `
INSERT INTO secrets(id, org_id, management_kind, owner_kind, name, kind, metadata, current_version_id, created_at, updated_at)
VALUES ($1, $2, 'tenant', 'org', 'manager-cleanup-provider-key', 'generic', '{}'::jsonb, $3, $4, $4)
`, secretID, orgID, secretVersionID, now)

	exec("insert cleanup provider secret version", `
INSERT INTO secret_versions(id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at)
VALUES ($1, $2, $3, 1, ARRAY['value'], 'aes-256-gcm-envelope-v1', 'test-key', 'local', decode(repeat('01', 48), 'hex'), decode(repeat('02', 12), 'hex'), decode(repeat('03', 12), 'hex'), decode(repeat('04', 32), 'hex'), $4)
`, secretVersionID, orgID, secretID, now)

	exec("insert cleanup model provider config", `
INSERT INTO model_provider_configs(id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path, auth_kind, credential_secret_id, created_at, updated_at)
VALUES ($1, $2, 'tenant', 'manager-cleanup-provider', 'openai-responses', 'default', 'https://api.openai.com/v1', '/responses', 'bearer_token', $3, $4, $4)
`, providerConfigID, orgID, secretID, now)

	exec("insert cleanup configured model", `
WITH configured_model AS (
INSERT INTO configured_models(id, org_id, model_provider_config_id, name, current_revision_id, created_at, updated_at)
VALUES ($1, $2, $3, 'manager-cleanup-model', $4, $5, $5)
RETURNING id, org_id, model_provider_config_id, current_revision_id
)
INSERT INTO configured_model_revisions(id, org_id, configured_model_id, model_provider_config_id, provider_model_slug, context_window_tokens, max_output_tokens, created_at)
SELECT current_revision_id, org_id, id, model_provider_config_id, 'manager-cleanup-model', 128000, 8192, $5
FROM configured_model
`, configuredModelID, orgID, providerConfigID, configuredModelRevisionID, now)

	exec("insert cleanup agent config", `
INSERT INTO agent_configs(id, org_id, project_id, configured_model_id, definition, source, source_hash, compiled_definition, compiler_version, effective_definition_hash, created_at)
VALUES ($1, $2, $3, $4, '{"name":"manager cleanup","model":{"provider_config":"manager-cleanup-provider","name":"manager-cleanup-model"}}'::jsonb, 'name: manager cleanup', 'manager-cleanup-source-hash', '{"name":"manager cleanup","model":{"provider_config":"manager-cleanup-provider","name":"manager-cleanup-model"}}'::jsonb, 'test', 'manager-cleanup-effective-hash', $5)
`, configID, orgID, projectID, configuredModelID, now)

	exec("insert cleanup agent", `
INSERT INTO agents(id, org_id, project_id, state, name, current_config_id, created_at, updated_at)
VALUES ($1, $2, $3, 'active', 'Manager Cleanup Agent', $4, $5, $5)
`, agentID, orgID, projectID, configID, now)

	exec("insert cleanup pool grant", `
INSERT INTO project_machine_pool_grants(id, org_id, project_id, machine_pool_id, metadata, created_at, updated_at)
VALUES ($1, $2, $3, $4, '{}'::jsonb, $5, $5)
`, poolGrantID, orgID, projectID, machinePoolID, now)

	exec("insert cleanup machine grant", `
INSERT INTO project_machine_grants(id, org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, metadata, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'pool', $5, '{}'::jsonb, $6, $6)
`, grantID, orgID, projectID, machineID, poolGrantID, now)

	exec("insert cleanup binding", `
INSERT INTO agent_machine_bindings(id, org_id, project_id, agent_id, machine_id, machine_ref, binding_kind, state, metadata, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'mchr-abcd23', 'pool', 'attached', '{}'::jsonb, $6, $6)
`, bindingID, orgID, projectID, agentID, machineID, now)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed pool machine cleanup binding: %v", err)
	}
	return bindingID, grantID
}

func secretPublicIDForManagerTest(t *testing.T, id storage.ID) string {
	t.Helper()
	value, err := publicid.Encode(publicid.KindSecret, id)
	if err != nil {
		t.Fatalf("encode secret public id: %v", err)
	}
	return value
}

type machinePoolProviderTestResolvers struct{}

func (machinePoolProviderTestResolvers) ResolveMachineProviderOptions(
	_ string,
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	return mergeTestProviderOptions(defaultOptions, projectOptions, agentOptions), nil
}

func (machinePoolProviderTestResolvers) ValidatePool(
	_ string,
	_ executionstore.MachinePoolProviderPolicy,
) error {
	return nil
}

func (resolvers machinePoolProviderTestResolvers) BuildMachineProvisioningIntent(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := resolvers.ValidatePool(provider, policy); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

func mergeTestProviderOptions(overlays ...map[string]json.RawMessage) map[string]json.RawMessage {
	var merged map[string]json.RawMessage
	for _, overlay := range overlays {
		if overlay != nil && merged == nil {
			merged = map[string]json.RawMessage{}
		}
		for key, value := range overlay {
			merged[key] = append(json.RawMessage(nil), value...)
		}
	}
	return merged
}

type testProviderDefinition struct {
	rawConfig json.RawMessage
	authToken string
	provider  *captureProvider
	validate  func(policy executionstore.MachinePoolProviderPolicy, machineProvisioning executionstore.MachineProvisioningConfig) error
}

func (d *testProviderDefinition) NewProvider(
	providerConfig json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (providers.Provider, error) {
	d.rawConfig = append(json.RawMessage(nil), providerConfig...)
	d.authToken = runtimeConfig.ProviderAuthToken
	return d.provider, nil
}

func (d *testProviderDefinition) ResolveMachineProviderOptions(
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	return mergeTestProviderOptions(defaultOptions, projectOptions, agentOptions)
}

func (d *testProviderDefinition) ValidatePool(
	policy executionstore.MachinePoolProviderPolicy,
) error {
	return nil
}

func (d *testProviderDefinition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if d.validate == nil {
		return nil
	}
	return d.validate(policy, machineProvisioning)
}

func (d *testProviderDefinition) BuildMachineProvisioningIntent(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := d.ValidateMachineProvisioning(policy, machineProvisioning); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

func testProviderCatalog(definition providers.Definition) Catalog {
	return Catalog{definitions: map[string]providers.Definition{"capture": definition}}
}

type captureProvider struct {
	provisionResourceID   string
	provisioning          *executionstore.MachineProvisioningConfig
	machineEnv            map[string]string
	machineToken          string
	provisionErr          error
	prepare               func(executionstore.MachineProvisioningConfig) (executionstore.MachineResourceFacts, error)
	installationID        storage.ID
	machineID             storage.ID
	inspectResourceID     string
	inspectFound          bool
	inspectErr            error
	inspectInstallationID storage.ID
	inspectMachineID      storage.ID
	deleteInstallationID  storage.ID
	deleteMachineID       storage.ID
	deletedResourceIDs    []string
	wakeInputs            []providers.WakeMachineInput
	wakeErrors            []error
}

func (*captureProvider) ProvisioningTimeout() time.Duration {
	return 5 * time.Second
}

func (p *captureProvider) PrepareProvisioning(
	_ context.Context,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	if p.prepare != nil {
		return p.prepare(machineProvisioning)
	}
	return executionstore.MachineResourceFacts{
		CPU:      machineProvisioning.CPU,
		MemoryMB: machineProvisioning.MemoryMB,
	}, nil
}

func (p *captureProvider) ProvisionMachine(
	_ context.Context,
	installationID storage.ID,
	machineID storage.ID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
) (providers.ProvisionMachineResult, error) {
	p.installationID = installationID
	p.machineID = machineID
	p.provisioning = &machineProvisioning
	p.machineEnv = machineEnv
	p.machineToken = machineToken
	return providers.ProvisionMachineResult{
		ProviderResourceID: p.provisionResourceID,
	}, p.provisionErr
}

func (p *captureProvider) WakeMachine(
	_ context.Context,
	input providers.WakeMachineInput,
) error {
	p.wakeInputs = append(p.wakeInputs, input)
	attempt := len(p.wakeInputs) - 1
	if attempt < len(p.wakeErrors) {
		return p.wakeErrors[attempt]
	}
	return nil
}

func (p *captureProvider) InspectMachine(
	_ context.Context,
	installationID storage.ID,
	machineID storage.ID,
	_ executionstore.MachineProvisioningConfig,
	_ string,
) (string, bool, error) {
	p.inspectInstallationID = installationID
	p.inspectMachineID = machineID
	return p.inspectResourceID, p.inspectFound, p.inspectErr
}

func (p *captureProvider) DeleteMachine(
	_ context.Context,
	installationID storage.ID,
	machineID storage.ID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	p.deleteInstallationID = installationID
	p.deleteMachineID = machineID
	p.deletedResourceIDs = append(p.deletedResourceIDs, providerResourceID)
	return nil
}
