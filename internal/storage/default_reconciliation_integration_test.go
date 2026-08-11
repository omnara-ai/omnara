//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
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
	invalidPool := initialPool
	invalidPool.DefaultMachineMemoryMB = intPtrForMachinePoolTest(4096)
	if _, err := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{invalidPool},
	}); err == nil || !strings.Contains(err.Error(), "org "+created.Org.ID.String()+":") {
		t.Fatalf("plan with incompatible pool limits error = %v, want org ID", err)
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
		poolRecord.MaxTotalMachines != 8 ||
		poolRecord.MaxTotalCpu == nil || *poolRecord.MaxTotalCpu != 16 ||
		poolRecord.MaxTotalMemoryMb == nil || *poolRecord.MaxTotalMemoryMb != 8192 ||
		poolRecord.MinMachineCpu == nil || *poolRecord.MinMachineCpu != 1 ||
		poolRecord.MinMachineMemoryMb == nil || *poolRecord.MinMachineMemoryMb != 512 ||
		poolRecord.MaxMachineCpu == nil || *poolRecord.MaxMachineCpu != 4 ||
		poolRecord.MaxMachineMemoryMb == nil || *poolRecord.MaxMachineMemoryMb != 2048 {
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
		UPDATE machine_pools
		SET max_machine_memory_mb = 512
		WHERE org_id = $1 AND name = $2
	`, failingOrg.Org.ID, initialPools[1].Name); err != nil {
		t.Fatalf("restrict failing org pool: %v", err)
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
		UPDATE machine_pools
		SET max_machine_memory_mb = 2048
		WHERE org_id = $1 AND name = $2
	`, failingOrg.Org.ID, initialPools[1].Name); err != nil {
		t.Fatalf("restore failing org pool limit: %v", err)
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
