//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

type defaultReconciliationMachinePoolProviders struct {
	mergingMachinePoolProviders
}

func (defaultReconciliationMachinePoolProviders) ValidatePool(
	_ string,
	policy executionstore.MachinePoolProviderPolicy,
) error {
	if policy.ResourceLimits.MaxTotalCPU != nil && *policy.ResourceLimits.MaxTotalCPU == 99 {
		return errors.New("provider rejects max_total_cpu")
	}
	return nil
}

func TestReconcileDefaults(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(defaultReconciliationMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-defaults@example.com", "Defaults Owner")

	initialPool := defaultMachinePoolTemplateWithDefaultMachineForTest(
		executionstore.DefaultMachinePoolTemplate{
			Name:               "hosted-pool",
			Description:        "old pool",
			Provider:           "blaxel",
			ProviderAuthEnvVar: "RECONCILE_POOL_TOKEN",
			MaxTotalMachines:   1,
			MaxTotalMemoryMB:   intPtrForMachinePoolTest(1024),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        512,
			DefaultMachineEnv:             json.RawMessage(`{"OLD":"value"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"old"}`),
		},
	)
	initialProvider := modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "omnara-openrouter",
		CredentialSecretName: "omnara-openrouter-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://old.example.com/v1",
		EndpointPath:         "/chat/completions",
		AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
		Models: []modelstore.DefaultConfiguredModelTemplate{
			{Name: "update-model", ProviderModelSlug: "example/old", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
			{Name: "remove-model", ProviderModelSlug: "example/remove", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
			{Name: "retained-model", ProviderModelSlug: "example/retain", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
		},
	}
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:              user.ID,
		Name:                "Defaults Org",
		IdempotencyKey:      "defaults-org",
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{initialPool},
		DefaultModelProvider: &modelstore.ProvisionedDefaultModelProvider{
			Template: initialProvider, CredentialValue: "provider-token",
		},
	})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	providerInvalidPool := initialPool
	providerInvalidPool.MaxTotalCPU = intPtrForMachinePoolTest(99)
	if _, err := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{providerInvalidPool},
	}); err == nil || !strings.Contains(err.Error(), "provider rejects max_total_cpu") {
		t.Fatalf("plan with provider-invalid template error = %v", err)
	}
	missingPool := initialPool
	missingPool.Name = "missing-pool"
	if _, err := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{missingPool},
	}); err == nil {
		t.Fatal("plan with missing machine pool succeeded")
	}
	missingProvider := initialProvider
	missingProvider.Name = "missing-provider"
	if _, err := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
		DefaultModelProvider: &missingProvider,
	}); err == nil {
		t.Fatal("plan with missing model provider succeeded")
	}
	providerDriftPool := initialPool
	providerDriftPool.Provider = "unikraft"
	if _, err := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{providerDriftPool},
	}); err == nil {
		t.Fatal("plan with changed machine pool provider succeeded")
	}
	authEnvDriftPool := initialPool
	authEnvDriftPool.ProviderAuthEnvVar = "OTHER_POOL_TOKEN"
	if _, err := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{authEnvDriftPool},
	}); err == nil {
		t.Fatal("plan with changed machine pool provider auth env var succeeded")
	}
	provider, err := store.Models().GetModelProviderConfigByName(ctx, created.Org.ID, initialProvider.Name)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	updateModel, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "update-model")
	if err != nil {
		t.Fatalf("get update model: %v", err)
	}
	updateModelGrant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, created.Org.ID, created.Project.ID, updateModel.ID,
	)
	if err != nil {
		t.Fatalf("get update model default grant: %v", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(
		ctx, created.Org.ID, created.Project.ID, updateModelGrant.ID,
	); err != nil {
		t.Fatalf("delete update model default grant: %v", err)
	}
	removeModel, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "remove-model")
	if err != nil {
		t.Fatalf("get remove model: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_configs(
			org_id, project_id, configured_model_id, definition,
			source_hash, effective_definition_hash, created_at
		) VALUES ($1, $2, $3, '{}'::jsonb, 'historical-remove-model', 'historical-remove-model', statement_timestamp())
	`, created.Org.ID, created.Project.ID, removeModel.ID); err != nil {
		t.Fatalf("create historical agent config: %v", err)
	}
	retainedModel, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "retained-model")
	if err != nil {
		t.Fatalf("get retained model: %v", err)
	}
	manualProject, err := store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID: created.Org.ID, Creator: userPrincipal(user.ID), Name: "Manual", IdempotencyKey: "manual",
	})
	if err != nil {
		t.Fatalf("create manual project: %v", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID: created.Org.ID, ProjectID: manualProject.ID, ConfiguredModelID: retainedModel.ID,
	}); err != nil {
		t.Fatalf("grant retained model: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE machine_pools
		SET max_total_machines = 8,
		    max_total_cpu = 16,
		    max_total_memory_mb = 8192,
		    max_machine_cpu = 4,
		    max_machine_memory_mb = 2048
		WHERE org_id = $1
	`, created.Org.ID); err != nil {
		t.Fatalf("set pool limits: %v", err)
	}
	machineID := testID("default-reconciliation-runtime-mismatch")
	tag, err := pool.Exec(ctx, `
		INSERT INTO machines(
			id, org_id, machine_pool_id, source_kind, display_name, provider,
			lifecycle_state, lifecycle_changed_at, provider_resource_id,
			provider_provision_attempted_at, cpu, memory_mb, cwd, env, secret_env,
			provider_options, provider_runtime_mismatch_since, metadata, created_at, updated_at
		)
		SELECT $1, machine_pool.org_id, machine_pool.id, 'pool', 'runtime mismatch', machine_pool.provider,
			'active', statement_timestamp(), 'default-reconciliation-runtime',
			statement_timestamp(), 1, 512, '', '{}'::jsonb, '{}'::jsonb,
			'{}'::jsonb, statement_timestamp(), '{}'::jsonb, statement_timestamp(), statement_timestamp()
		FROM machine_pools machine_pool
		WHERE machine_pool.org_id = $2 AND machine_pool.name = $3 AND machine_pool.deleted_at IS NULL
	`, machineID, created.Org.ID, initialPool.Name)
	if err != nil {
		t.Fatalf("seed runtime mismatch marker: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("seed runtime mismatch marker rows = %d, want 1", tag.RowsAffected())
	}
	desiredPool := initialPool
	desiredPool.Description = "new pool"
	desiredPool.DefaultMachineEnv = json.RawMessage(`{"NEW":"value"}`)
	desiredPool.DefaultMachineProviderOptions = json.RawMessage(`{"image":"new","sleep_after_ms":30000}`)
	desiredPool.RuntimeProtectionEnabled = true
	desiredPool.MaxTotalMachines = 0
	desiredPool.MaxTotalCPU = intPtrForMachinePoolTest(1)
	desiredPool.MaxTotalMemoryMB = intPtrForMachinePoolTest(0)
	desiredPool.MinMachineCPU = intPtrForMachinePoolTest(1)
	desiredPool.MinMachineMemoryMB = intPtrForMachinePoolTest(512)
	desiredPool.MaxMachineCPU = intPtrForMachinePoolTest(1)
	desiredPool.MaxMachineMemoryMB = intPtrForMachinePoolTest(512)
	desiredProvider := initialProvider
	desiredProvider.BaseURL = "https://new.example.com/v1"
	desiredProvider.RequestTimeoutMS = 120000
	desiredProvider.Models = []modelstore.DefaultConfiguredModelTemplate{
		{Name: "update-model", ProviderModelSlug: "example/new", ContextWindowTokens: 16384, MaxOutputTokens: 2048},
		{Name: "add-model", ProviderModelSlug: "example/add", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
	}
	input := orglifecycle.ReconcileDefaultsInput{
		DefaultMachinePools:  []executionstore.DefaultMachinePoolTemplate{desiredPool},
		DefaultModelProvider: &desiredProvider,
	}
	result, err := store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("plan defaults: %v", err)
	}
	if len(result.Changes) == 0 {
		t.Fatal("plan reported no changes")
	}

	input.Apply = true
	if _, err := store.Organizations().ReconcileDefaults(ctx, input); err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	poolRecord, err := testQueries(store).GetMachinePoolByName(ctx, dbsqlc.GetMachinePoolByNameParams{
		OrgID: created.Org.ID, Name: desiredPool.Name,
	})
	if err != nil {
		t.Fatalf("get machine pool: %v", err)
	}
	if poolRecord.Description != desiredPool.Description ||
		!poolRecord.RuntimeProtectionEnabled ||
		poolRecord.MaxTotalMachines != 0 ||
		poolRecord.MaxTotalCpu == nil || *poolRecord.MaxTotalCpu != 1 ||
		poolRecord.MaxTotalMemoryMb == nil || *poolRecord.MaxTotalMemoryMb != 0 ||
		poolRecord.MinMachineCpu == nil || *poolRecord.MinMachineCpu != 1 ||
		poolRecord.MinMachineMemoryMb == nil || *poolRecord.MinMachineMemoryMb != 512 ||
		poolRecord.MaxMachineCpu == nil || *poolRecord.MaxMachineCpu != 1 ||
		poolRecord.MaxMachineMemoryMb == nil || *poolRecord.MaxMachineMemoryMb != 512 {
		t.Fatalf("unexpected reconciled pool: %+v", poolRecord)
	}
	assertJSONRawEqual(t, poolRecord.DefaultMachineEnv, string(desiredPool.DefaultMachineEnv))
	assertJSONRawEqual(t, poolRecord.DefaultMachineProviderOptions, string(desiredPool.DefaultMachineProviderOptions))
	var hasRuntimeMismatch bool
	if err := pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since IS NOT NULL FROM machines WHERE org_id = $1 AND id = $2`,
		created.Org.ID,
		machineID,
	).Scan(&hasRuntimeMismatch); err != nil {
		t.Fatalf("load runtime mismatch marker: %v", err)
	}
	if hasRuntimeMismatch {
		t.Fatal("runtime mismatch marker survived runtime protection change")
	}

	reconciledProvider, err := store.Models().GetModelProviderConfigByName(ctx, created.Org.ID, desiredProvider.Name)
	if err != nil {
		t.Fatalf("get reconciled provider: %v", err)
	}
	if reconciledProvider.BaseURL != desiredProvider.BaseURL || reconciledProvider.RequestTimeoutMS != 120000 ||
		reconciledProvider.CredentialSecretID != provider.CredentialSecretID {
		t.Fatalf("unexpected reconciled provider: %+v", reconciledProvider)
	}
	updatedModel, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "update-model")
	if err != nil || updatedModel.ProviderModelSlug != "example/new" || updatedModel.MaxOutputTokens != 2048 {
		t.Fatalf("unexpected updated model: %+v, err %v", updatedModel, err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, created.Org.ID, created.Project.ID, updatedModel.ID,
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get update model default grant error = %v, want no rows", err)
	}
	addedModel, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "add-model")
	if err != nil {
		t.Fatalf("get added model: %v", err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, created.Org.ID, created.Project.ID, addedModel.ID,
	); err != nil {
		t.Fatalf("get added model default grant: %v", err)
	}
	if _, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "remove-model"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get removed model error = %v, want no rows", err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, created.Org.ID, created.Project.ID, retainedModel.ID,
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get retained model default grant error = %v, want no rows", err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, created.Org.ID, manualProject.ID, retainedModel.ID,
	); err != nil {
		t.Fatalf("get retained model manual grant: %v", err)
	}

	result, err = store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(result.Changes) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("second apply result = %+v, want only retained-model warning", result)
	}
	if _, err := store.Organizations().DeleteProject(
		ctx,
		created.Org.ID,
		created.Project.ID,
		userPrincipal(user.ID),
	); err != nil {
		t.Fatalf("delete default project: %v", err)
	}
	desiredPool.Description = "pool without default project"
	desiredProvider.Models = []modelstore.DefaultConfiguredModelTemplate{
		{Name: "update-model", ProviderModelSlug: "example/without-project", ContextWindowTokens: 16384, MaxOutputTokens: 2048},
		{Name: "missing-project-model", ProviderModelSlug: "example/missing-project", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
	}
	input.DefaultMachinePools = []executionstore.DefaultMachinePoolTemplate{desiredPool}
	input.DefaultModelProvider = &desiredProvider
	result, err = store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("apply without default project: %v", err)
	}
	if len(result.Changes) != 4 || len(result.Warnings) != 1 {
		t.Fatalf("apply without default project result = %+v, want four changes and one warning", result)
	}
	poolRecord, err = testQueries(store).GetMachinePoolByName(ctx, dbsqlc.GetMachinePoolByNameParams{
		OrgID: created.Org.ID, Name: desiredPool.Name,
	})
	if err != nil {
		t.Fatalf("get machine pool after default project deletion: %v", err)
	}
	if poolRecord.Description != desiredPool.Description {
		t.Fatalf("machine pool description = %q, want %q", poolRecord.Description, desiredPool.Description)
	}
	updatedModel, err = store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "update-model")
	if err != nil || updatedModel.ProviderModelSlug != "example/without-project" {
		t.Fatalf("unexpected model updated without default project: %+v, err %v", updatedModel, err)
	}
	if _, err := store.Models().GetConfiguredModelByName(
		ctx, created.Org.ID, provider.ID, "missing-project-model",
	); err != nil {
		t.Fatalf("get model added without default project: %v", err)
	}
	if _, err := store.Models().GetConfiguredModelByName(
		ctx, created.Org.ID, provider.ID, "add-model",
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get model removed without default project error = %v, want no rows", err)
	}
	result, err = store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("second apply without default project: %v", err)
	}
	if len(result.Changes) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("second apply without default project result = %+v, want only retained-model warning", result)
	}
}

func TestReconcileDefaultsContinuesAfterOrganizationFailure(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-partial@example.com", "Defaults Owner")

	poolTemplate := func(name string) executionstore.DefaultMachinePoolTemplate {
		return defaultMachinePoolTemplateWithDefaultMachineForTest(
			executionstore.DefaultMachinePoolTemplate{
				Name:               name,
				Description:        "old",
				Provider:           "blaxel",
				ProviderAuthEnvVar: "RECONCILE_POOL_TOKEN",
				MaxTotalMachines:   1,
				MaxTotalMemoryMB:   intPtrForMachinePoolTest(4096),
				MaxMachineMemoryMB: intPtrForMachinePoolTest(2048),
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        512,
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"old"}`),
			},
		)
	}
	initialPools := []executionstore.DefaultMachinePoolTemplate{
		poolTemplate("partial-pool-a"),
		poolTemplate("partial-pool-b"),
	}
	createOrg := func(name, key string) identitystore.CreateOrgForUserRecord {
		t.Helper()
		created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
			UserID:              user.ID,
			Name:                name,
			IdempotencyKey:      key,
			DefaultMachinePools: initialPools,
		})
		if err != nil {
			t.Fatalf("create org %q: %v", name, err)
		}
		return created
	}
	orgA := createOrg("Defaults Org A", "defaults-org-a")
	orgB := createOrg("Defaults Org B", "defaults-org-b")
	failingOrg, successfulOrg := orgA, orgB
	if failingOrg.Org.ID.String() > successfulOrg.Org.ID.String() {
		failingOrg, successfulOrg = successfulOrg, failingOrg
	}
	if _, err := pool.Exec(ctx, `
		ALTER TABLE machine_pools DISABLE TRIGGER machine_pools_authority_immutable;
		UPDATE machine_pools
		SET provider_auth_env_var = 'OTHER_POOL_TOKEN'
		WHERE org_id = $1 AND name = $2;
		ALTER TABLE machine_pools ENABLE TRIGGER machine_pools_authority_immutable
	`, pgx.QueryExecModeSimpleProtocol, failingOrg.Org.ID, initialPools[1].Name); err != nil {
		t.Fatalf("change failing org pool auth env var: %v", err)
	}

	desiredPools := append([]executionstore.DefaultMachinePoolTemplate(nil), initialPools...)
	desiredPools[0].Description = "new"
	desiredPools[1].DefaultMachineMemoryMB = intPtrForMachinePoolTest(1024)
	input := orglifecycle.ReconcileDefaultsInput{Apply: true, DefaultMachinePools: desiredPools}
	result, err := store.Organizations().ReconcileDefaults(ctx, input)
	if err == nil || !strings.Contains(err.Error(), failingOrg.Org.ID.String()) {
		t.Fatalf("partial apply error = %v, want failing org ID", err)
	}
	if len(result.Changes) != 2 {
		t.Fatalf("partial apply changes = %v, want two successful-org changes", result.Changes)
	}
	for _, change := range result.Changes {
		if !strings.Contains(change, successfulOrg.Org.ID.String()) {
			t.Fatalf("partial apply change = %q, want successful org only", change)
		}
	}
	queries := testQueries(store)
	successfulPoolA, err := queries.GetMachinePoolByName(ctx, dbsqlc.GetMachinePoolByNameParams{
		OrgID: successfulOrg.Org.ID, Name: initialPools[0].Name,
	})
	if err != nil {
		t.Fatalf("get successful org pool: %v", err)
	}
	failingPoolA, err := queries.GetMachinePoolByName(ctx, dbsqlc.GetMachinePoolByNameParams{
		OrgID: failingOrg.Org.ID, Name: initialPools[0].Name,
	})
	if err != nil {
		t.Fatalf("get failing org pool: %v", err)
	}
	if successfulPoolA.Description != "new" || failingPoolA.Description != "old" {
		t.Fatalf(
			"pool descriptions after partial apply = successful %q, failing %q",
			successfulPoolA.Description,
			failingPoolA.Description,
		)
	}

	if _, err := pool.Exec(ctx, `
		ALTER TABLE machine_pools DISABLE TRIGGER machine_pools_authority_immutable;
		UPDATE machine_pools
		SET provider_auth_env_var = 'RECONCILE_POOL_TOKEN'
		WHERE org_id = $1 AND name = $2;
		ALTER TABLE machine_pools ENABLE TRIGGER machine_pools_authority_immutable
	`, pgx.QueryExecModeSimpleProtocol, failingOrg.Org.ID, initialPools[1].Name); err != nil {
		t.Fatalf("restore failing org pool auth env var: %v", err)
	}
	result, err = store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("retry apply: %v", err)
	}
	if len(result.Changes) != 2 {
		t.Fatalf("retry changes = %v, want two recovered-org changes", result.Changes)
	}
	for _, change := range result.Changes {
		if !strings.Contains(change, failingOrg.Org.ID.String()) {
			t.Fatalf("retry change = %q, want recovered org only", change)
		}
	}
	result, err = store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("idempotent apply changes = %v, want none", result.Changes)
	}
}

func TestReconcileDefaultsWaitingBehindOrganizationDeletionRejectsInactiveOrganization(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-org-delete@example.com", "Defaults Owner")
	poolTemplate := defaultReconciliationPoolTemplate("reconcile-org-delete-pool")
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:              user.ID,
		Name:                "Reconcile Organization Deletion",
		IdempotencyKey:      "reconcile-org-delete",
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{poolTemplate},
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	poolRow, err := testQueries(store).GetMachinePoolByName(
		ctx,
		dbsqlc.GetMachinePoolByNameParams{OrgID: created.Org.ID, Name: poolTemplate.Name},
	)
	if err != nil {
		t.Fatalf("get default machine pool: %v", err)
	}
	controlTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(controlTx).LockMachinePoolForLifecycle(
		ctx,
		dbsqlc.LockMachinePoolForLifecycleParams{OrgID: created.Org.ID, ID: poolRow.ID},
	); err != nil {
		t.Fatalf("lock machine pool: %v", err)
	}
	actor, err := executionstore.OmnaraActorParams(created.Org.ID, userPrincipal(user.ID))
	if err != nil {
		t.Fatalf("build deletion actor: %v", err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := store.Organizations().DeleteOrganizationOnceForIntegration(
			ctx,
			created.Org.ID,
			actor,
		)
		deleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMachinePoolForLifecycle", 1)
	desiredPool := poolTemplate
	desiredPool.Description = "must not be applied"
	reconcileDone := make(chan error, 1)
	go func() {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{
				Apply: true, DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{desiredPool},
			},
		)
		reconcileDone <- reconcileErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockOrganizationLifecycleShared", 1)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release control transaction: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "organization deletion", deleteDone); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "reconciliation", reconcileDone); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("reconciliation error = %v, want not found", err)
	}
	var description string
	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT description, deleted_at IS NOT NULL FROM machine_pools WHERE org_id = $1 AND id = $2`,
		created.Org.ID,
		poolRow.ID,
	).Scan(&description, &deleted); err != nil {
		t.Fatalf("load deleted machine pool: %v", err)
	}
	if description != poolTemplate.Description || !deleted {
		t.Fatalf("machine pool after deletion = description %q, deleted %t", description, deleted)
	}
}

func TestReconcileDefaultsWaitingBehindProjectDeletionCreatesNoModel(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-project-delete@example.com", "Defaults Owner")
	providerTemplate := defaultReconciliationModelProviderTemplate()
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:         user.ID,
		Name:           "Reconcile Project Deletion",
		IdempotencyKey: "reconcile-project-delete",
		DefaultModelProvider: &modelstore.ProvisionedDefaultModelProvider{
			Template: providerTemplate, CredentialValue: "provider-token",
		},
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	provider, err := store.Models().GetModelProviderConfigByName(
		ctx,
		created.Org.ID,
		providerTemplate.Name,
	)
	if err != nil {
		t.Fatalf("get default model provider: %v", err)
	}
	controlTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	var grantID ID
	if err := controlTx.QueryRow(
		ctx,
		`SELECT id FROM project_model_grants WHERE org_id = $1 AND project_id = $2 LIMIT 1 FOR UPDATE`,
		created.Org.ID,
		created.Project.ID,
	).Scan(&grantID); err != nil {
		t.Fatalf("lock default project model grant: %v", err)
	}
	actor, err := executionstore.OmnaraActorParams(created.Org.ID, userPrincipal(user.ID))
	if err != nil {
		t.Fatalf("build deletion actor: %v", err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := store.Organizations().DeleteProjectOnceForIntegration(
			ctx,
			created.Org.ID,
			created.Project.ID,
			actor,
		)
		deleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteProjectModelGrantsForProjectDeletion", 1)
	desiredProvider := providerTemplate
	desiredProvider.Models = append(
		append([]modelstore.DefaultConfiguredModelTemplate(nil), providerTemplate.Models...),
		modelstore.DefaultConfiguredModelTemplate{
			Name: "must-not-be-created", ProviderModelSlug: "example/rejected",
			ContextWindowTokens: 8192, MaxOutputTokens: 1024,
		},
	)
	reconcileDone := make(chan error, 1)
	go func() {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{Apply: true, DefaultModelProvider: &desiredProvider},
		)
		reconcileDone <- reconcileErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleShared", 1)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release control transaction: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "project deletion", deleteDone); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "reconciliation", reconcileDone); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("reconciliation error = %v, want not found", err)
	}
	if _, err := store.Models().GetConfiguredModelByName(
		ctx,
		created.Org.ID,
		provider.ID,
		"must-not-be-created",
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get rejected configured model error = %v, want no rows", err)
	}
}

func TestReconcileDefaultsLocksAllPoolsBeforeAffectedMachines(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-pool-order@example.com", "Defaults Owner")
	poolA := defaultReconciliationPoolTemplate("reconcile-pool-order-a")
	poolB := defaultReconciliationPoolTemplate("reconcile-pool-order-b")
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:              user.ID,
		Name:                "Reconcile Pool Order",
		IdempotencyKey:      "reconcile-pool-order",
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{poolA, poolB},
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	queries := testQueries(store)
	poolARow, err := queries.GetMachinePoolByName(
		ctx,
		dbsqlc.GetMachinePoolByNameParams{OrgID: created.Org.ID, Name: poolA.Name},
	)
	if err != nil {
		t.Fatalf("get first default machine pool: %v", err)
	}
	poolBRow, err := queries.GetMachinePoolByName(
		ctx,
		dbsqlc.GetMachinePoolByNameParams{OrgID: created.Org.ID, Name: poolB.Name},
	)
	if err != nil {
		t.Fatalf("get second default machine pool: %v", err)
	}
	machineID := testID("reconcile-pool-order-machine")
	if _, err := pool.Exec(ctx, `
INSERT INTO machines(
    id, org_id, machine_pool_id, source_kind, display_name, provider,
    lifecycle_state, lifecycle_changed_at, provider_resource_id,
    provider_provision_attempted_at, cpu, memory_mb, cwd, env, secret_env,
    provider_options, provider_runtime_mismatch_since, metadata, created_at, updated_at
) VALUES (
    $1, $2, $3, 'pool', 'runtime mismatch', 'blaxel',
    'active', statement_timestamp(), 'reconcile-pool-order-resource',
    statement_timestamp(), 1, 512, '', '{}'::jsonb, '{}'::jsonb,
    '{}'::jsonb, statement_timestamp(), '{}'::jsonb, statement_timestamp(), statement_timestamp()
)
`, machineID, created.Org.ID, poolARow.ID); err != nil {
		t.Fatalf("seed pool machine: %v", err)
	}
	controlTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(controlTx).LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: created.Org.ID, ID: machineID},
	); err != nil {
		t.Fatalf("lock pool machine: %v", err)
	}
	desiredA := poolA
	desiredA.RuntimeProtectionEnabled = true
	desiredB := poolB
	desiredB.Description = "updated before deletion"
	reconcileDone := make(chan error, 1)
	go func() {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{
				Apply: true,
				DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{
					desiredB,
					desiredA,
				},
			},
		)
		reconcileDone <- reconcileErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMachineForLifecycle", 1)
	deleteDone := make(chan error, 1)
	go func() {
		deleteTx, deleteErr := pool.Begin(ctx)
		if deleteErr != nil {
			deleteDone <- deleteErr
			return
		}
		defer func() { _ = deleteTx.Rollback(ctx) }()
		_, deleteErr = store.Execution().DeleteMachinePoolTx(
			ctx,
			deleteTx,
			notifications.NewTxNotifications(),
			created.Org.ID,
			poolBRow.ID,
		)
		if deleteErr == nil {
			deleteErr = deleteTx.Commit(ctx)
		}
		deleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMachinePoolForUpdate", 1)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release control transaction: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "reconciliation", reconcileDone); err != nil {
		t.Fatalf("reconcile defaults: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "machine pool deletion", deleteDone); err != nil {
		t.Fatalf("delete machine pool: %v", err)
	}
	var runtimeProtectionEnabled bool
	var mismatchCleared bool
	if err := pool.QueryRow(
		ctx,
		`SELECT pool.runtime_protection_enabled, machine.provider_runtime_mismatch_since IS NULL
FROM machine_pools pool
JOIN machines machine ON machine.org_id = pool.org_id AND machine.machine_pool_id = pool.id
WHERE pool.org_id = $1 AND pool.id = $2 AND machine.id = $3`,
		created.Org.ID,
		poolARow.ID,
		machineID,
	).Scan(&runtimeProtectionEnabled, &mismatchCleared); err != nil {
		t.Fatalf("load reconciled runtime protection: %v", err)
	}
	if !runtimeProtectionEnabled || !mismatchCleared {
		t.Fatalf(
			"runtime protection after reconciliation = enabled %t, mismatch cleared %t",
			runtimeProtectionEnabled,
			mismatchCleared,
		)
	}
	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL FROM machine_pools WHERE org_id = $1 AND id = $2`,
		created.Org.ID,
		poolBRow.ID,
	).Scan(&deleted); err != nil {
		t.Fatalf("load deleted machine pool: %v", err)
	}
	if !deleted {
		t.Fatal("second machine pool remained active")
	}
}

func TestReconcileDefaultsSerializesModelBeforePoolWithAgentConfigChange(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-launch-order@example.com", "Defaults Owner")
	poolTemplate := defaultReconciliationPoolTemplate("reconcile-launch-order-pool")
	providerTemplate := defaultReconciliationModelProviderTemplate()
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:              user.ID,
		Name:                "Reconcile Launch Order",
		IdempotencyKey:      "reconcile-launch-order",
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{poolTemplate},
		DefaultModelProvider: &modelstore.ProvisionedDefaultModelProvider{
			Template: providerTemplate, CredentialValue: "provider-token",
		},
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	poolRow, err := testQueries(store).GetMachinePoolByName(
		ctx,
		dbsqlc.GetMachinePoolByNameParams{OrgID: created.Org.ID, Name: poolTemplate.Name},
	)
	if err != nil {
		t.Fatalf("get default machine pool: %v", err)
	}
	provider, err := store.Models().GetModelProviderConfigByName(ctx, created.Org.ID, providerTemplate.Name)
	if err != nil {
		t.Fatalf("get default model provider: %v", err)
	}
	configuredModel, err := store.Models().GetConfiguredModelByName(
		ctx,
		created.Org.ID,
		provider.ID,
		providerTemplate.Models[0].Name,
	)
	if err != nil {
		t.Fatalf("get default configured model: %v", err)
	}
	compileConfig := func(source string) agentconfig.Result {
		t.Helper()
		compiled, compileErr := agentconfig.Compile(
			agentconfig.SourceFormatYAML,
			[]byte(source),
			agentconfig.CompileOptions{
				ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
					return resolvedTestModelSelection(configuredModel), nil
				},
				ResolveMachinePoolName: func(string) (string, error) {
					return publicid.Encode(publicid.KindMachinePool, poolRow.ID)
				},
			},
		)
		if compileErr != nil {
			t.Fatalf("compile agent config: %v", compileErr)
		}
		return compiled
	}
	initialSource := `
instruction: Exercise model and pool lock ordering.
model:
  provider_config: reconcile-openrouter
  name: existing-model
`
	initialCompiled := compileConfig(initialSource)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               created.Project.ID,
		Definition:              json.RawMessage(initialCompiled.CanonicalJSON),
		Source:                  initialSource,
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(initialCompiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: initialCompiled.Hash,
	})
	if err != nil {
		t.Fatalf("create launch config: %v", err)
	}
	launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:     created.Project.ID,
		AgentConfigID: config.ID,
		LaunchedBy:    userPrincipal(user.ID),
		Name:          "Reconcile Config Order Agent",
	})
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	nextSource := `
instruction: Exercise model and pool lock ordering.
model:
  provider_config: reconcile-openrouter
  name: existing-model
machine_sources:
  - machine_pool_name: reconcile-launch-order-pool
`
	nextCompiled := compileConfig(nextSource)
	controlTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(controlTx).LockMachinePoolForLifecycle(
		ctx,
		dbsqlc.LockMachinePoolForLifecycleParams{OrgID: created.Org.ID, ID: poolRow.ID},
	); err != nil {
		t.Fatalf("lock machine pool: %v", err)
	}
	desiredPool := poolTemplate
	desiredPool.Description = "reconciled"
	desiredProvider := providerTemplate
	desiredProvider.BaseURL = "https://reconciled.example.com/v1"
	reconcileDone := make(chan error, 1)
	go func() {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{
				Apply:                true,
				DefaultMachinePools:  []executionstore.DefaultMachinePoolTemplate{desiredPool},
				DefaultModelProvider: &desiredProvider,
			},
		)
		reconcileDone <- reconcileErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMachinePoolForLifecycle", 1)
	changeDone := make(chan error, 1)
	go func() {
		_, changeErr := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
				ProjectID:               created.Project.ID,
				Definition:              json.RawMessage(nextCompiled.CanonicalJSON),
				Source:                  nextSource,
				ConfiguredModelID:       configuredModel.ID,
				CompiledDefinition:      json.RawMessage(nextCompiled.CanonicalJSON),
				CompilerVersion:         agentconfig.CompilerVersion,
				EffectiveDefinitionHash: nextCompiled.Hash,
			},
			AgentID:        launched.Agent.ID,
			ActorType:      identitystore.PrincipalTypeUser,
			ActorID:        user.ID,
			IdempotencyKey: "reconcile-config-order",
		})
		changeDone <- changeErr
	}()
	waitForDefaultReconciliationLockWaiter(
		t,
		ctx,
		pool,
		"LockConfiguredModelForUse",
		"agent config change",
		changeDone,
	)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release control transaction: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "reconciliation", reconcileDone); err != nil {
		t.Fatalf("reconcile defaults: %v", err)
	}
	if err := waitForDefaultReconciliationOperation(t, "agent config change", changeDone); err != nil {
		t.Fatalf("change agent config: %v", err)
	}
}

func defaultReconciliationPoolTemplate(name string) executionstore.DefaultMachinePoolTemplate {
	return defaultMachinePoolTemplateWithDefaultMachineForTest(
		executionstore.DefaultMachinePoolTemplate{
			Name:               name,
			Description:        "initial",
			Provider:           "blaxel",
			ProviderAuthEnvVar: "RECONCILE_POOL_TOKEN",
			MaxTotalMachines:   2,
			MaxTotalMemoryMB:   intPtrForMachinePoolTest(4096),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        512,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"default"}`),
		},
	)
}

func defaultReconciliationModelProviderTemplate() modelstore.DefaultModelProviderTemplate {
	return modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "reconcile-openrouter",
		CredentialSecretName: "reconcile-openrouter-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://example.com/v1",
		EndpointPath:         "/chat/completions",
		AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
		Models: []modelstore.DefaultConfiguredModelTemplate{
			{
				Name: "existing-model", ProviderModelSlug: "example/existing",
				ContextWindowTokens: 8192, MaxOutputTokens: 1024,
			},
		},
	}
}

func waitForDefaultReconciliationOperation(
	t *testing.T,
	label string,
	done <-chan error,
) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func waitForDefaultReconciliationLockWaiter(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryName, operation string,
	done <-chan error,
) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s completed before waiting on %s: %v", operation, queryName, err)
	default:
	}
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, queryName, 1)
	select {
	case err := <-done:
		t.Fatalf("%s completed while waiting on %s: %v", operation, queryName, err)
	default:
	}
}
