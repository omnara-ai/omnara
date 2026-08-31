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
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
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
			{Name: "active-agent-model", ProviderModelSlug: "example/active", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
			{Name: "profile-model", ProviderModelSlug: "example/profile", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
		},
	}
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:                        user.ID,
		Name:                          "Defaults Org",
		IdempotencyKey:                "defaults-org",
		DefaultMachinePools:           []executionstore.DefaultMachinePoolTemplate{initialPool},
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	mustCompleteDefaultModelProviderProvisioning(
		t, ctx, store, created.Org.ID, initialProvider, "provider-token",
	)
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
	createAgentConfig := func(model modelstore.ConfiguredModelRecord) executionstore.AgentConfigRecord {
		t.Helper()
		source := `
instruction: test
model:
  provider_config: ` + initialProvider.Name + `
  name: ` + model.Name + `
`
		compiled, err := agentconfig.Compile(
			agentconfig.SourceFormatYAML,
			[]byte(source),
			agentconfig.CompileOptions{
				ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
					return resolvedTestModelSelection(model), nil
				},
			},
		)
		if err != nil {
			t.Fatalf("compile %s agent config: %v", model.Name, err)
		}
		config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
			ProjectID:               created.Project.ID,
			Definition:              json.RawMessage(compiled.CanonicalJSON),
			Source:                  source,
			SourceFormat:            string(agentconfig.SourceFormatYAML),
			ConfiguredModelID:       model.ID,
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		})
		if err != nil {
			t.Fatalf("create %s agent config: %v", model.Name, err)
		}
		return config
	}
	removeModel, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "remove-model")
	if err != nil {
		t.Fatalf("get remove model: %v", err)
	}
	historicalConfig := createAgentConfig(removeModel)
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
	tenantModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 created.Org.ID,
		ModelProviderConfigID: provider.ID,
		Name:                  "tenant-model",
		ProviderModelSlug:     "example/tenant",
		ContextWindowTokens:   8192,
		MaxOutputTokens:       1024,
	})
	if err != nil {
		t.Fatalf("create tenant model under default provider: %v", err)
	}
	activeAgentModel, err := store.Models().GetConfiguredModelByName(
		ctx, created.Org.ID, provider.ID, "active-agent-model",
	)
	if err != nil {
		t.Fatalf("get active agent model: %v", err)
	}
	activeAgentConfig := createAgentConfig(activeAgentModel)
	if _, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: created.Project.ID, CurrentConfigID: activeAgentConfig.ID,
	}); err != nil {
		t.Fatalf("create active agent using removed model: %v", err)
	}
	profileModel, err := store.Models().GetConfiguredModelByName(
		ctx, created.Org.ID, provider.ID, "profile-model",
	)
	if err != nil {
		t.Fatalf("get profile model: %v", err)
	}
	profileConfig := createAgentConfig(profileModel)
	preservedProfile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		OrgID:           created.Org.ID,
		ProjectID:       created.Project.ID,
		Name:            "Profile using removed model",
		CurrentConfigID: profileConfig.ID,
		IdempotencyKey:  "profile-using-removed-model",
	})
	if err != nil {
		t.Fatalf("create profile using removed model: %v", err)
	}
	machineSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     created.Org.ID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "default-pool-environment",
		Material:  secrets.GenericMaterial{Value: "organization-value"},
		Actor:     userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create machine environment secret: %v", err)
	}
	organizationSecretEnv := mustTestRawJSON(t, map[string]string{
		"ORG_SECRET": secretPublicIDForTest(t, machineSecret.ID),
	})
	if _, err := pool.Exec(ctx, `
		UPDATE machine_pools
		SET max_total_machines = 8,
		    max_total_cpu = 16,
		    max_total_memory_mb = 8192,
		    default_machine_cpu = 2,
		    default_machine_memory_mb = 1024,
		    default_machine_env = '{"ORG":"value"}'::jsonb,
		    default_machine_secret_env = $2::jsonb,
		    default_machine_provider_options = '{"image":"old","startup_script":"echo org"}'::jsonb,
		    min_machine_cpu = 1,
		    min_machine_memory_mb = 512,
		    max_machine_cpu = 4,
		    max_machine_memory_mb = 2048,
		    delete_after_idle_minutes = 30
		WHERE org_id = $1
	`, created.Org.ID, string(organizationSecretEnv)); err != nil {
		t.Fatalf("set pool organization fields and limits: %v", err)
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
	desiredPool.DefaultMachineSecretEnv = mustTestRawJSON(t, map[string]string{
		"TEMPLATE_SECRET": secretPublicIDForTest(t, machineSecret.ID),
	})
	desiredPool.DefaultMachineProviderOptions = json.RawMessage(`{"image":"new","sleep_after_ms":30000}`)
	desiredPool.RuntimeProtectionEnabled = true
	desiredPool.MaxTotalMachines = 0
	desiredPool.MaxTotalCPU = intPtrForMachinePoolTest(1)
	desiredPool.MaxTotalMemoryMB = intPtrForMachinePoolTest(0)
	desiredPool.MinMachineCPU = intPtrForMachinePoolTest(1)
	desiredPool.MinMachineMemoryMB = intPtrForMachinePoolTest(512)
	desiredPool.MaxMachineCPU = intPtrForMachinePoolTest(1)
	desiredPool.MaxMachineMemoryMB = intPtrForMachinePoolTest(512)
	desiredPool.DeleteAfterIdleMinutes = intPtrForMachinePoolTest(60)
	desiredProvider := initialProvider
	desiredProvider.BaseURL = "https://new.example.com/v1"
	desiredProvider.RequestTimeoutMS = 120000
	desiredProvider.Models = []modelstore.DefaultConfiguredModelTemplate{
		{Name: "update-model", ProviderModelSlug: "example/new", ContextWindowTokens: 16384, MaxOutputTokens: 2048},
		{Name: "add-model", ProviderModelSlug: "example/add", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
		{Name: "tenant-model", ProviderModelSlug: "example/cluster-collision", ContextWindowTokens: 8192, MaxOutputTokens: 1024},
	}
	input := orglifecycle.ReconcileDefaultsInput{
		DefaultMachinePools:  []executionstore.DefaultMachinePoolTemplate{desiredPool},
		DefaultModelProvider: &desiredProvider,
	}
	assertRetainedModelWarnings := func(label string, result orglifecycle.ReconcileDefaultsResult) {
		t.Helper()
		if len(result.Warnings) != 3 {
			t.Fatalf("%s warnings = %v, want tenant collision and two retained-model warnings", label, result.Warnings)
		}
		warnings := strings.Join(result.Warnings, "\n")
		for _, modelName := range []string{activeAgentModel.Name, profileModel.Name} {
			expected := "cannot remove configured model \"" + modelName +
				"\" because an active agent or current agent profile still references it"
			if !strings.Contains(warnings, expected) {
				t.Fatalf("%s warnings = %v, want warning containing %q", label, result.Warnings, expected)
			}
		}
	}
	result, err := store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("plan defaults: %v", err)
	}
	if len(result.Changes) == 0 {
		t.Fatal("plan reported no changes")
	}
	assertRetainedModelWarnings("plan", result)

	input.Apply = true
	result, err = store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	assertRetainedModelWarnings("apply", result)
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
		poolRecord.DefaultMachineCpu == nil || *poolRecord.DefaultMachineCpu != 2 ||
		poolRecord.DefaultMachineMemoryMb == nil || *poolRecord.DefaultMachineMemoryMb != 1024 ||
		poolRecord.MinMachineCpu == nil || *poolRecord.MinMachineCpu != 1 ||
		poolRecord.MinMachineMemoryMb == nil || *poolRecord.MinMachineMemoryMb != 512 ||
		poolRecord.MaxMachineCpu == nil || *poolRecord.MaxMachineCpu != 4 ||
		poolRecord.MaxMachineMemoryMb == nil || *poolRecord.MaxMachineMemoryMb != 2048 ||
		poolRecord.DeleteAfterIdleMinutes == nil || *poolRecord.DeleteAfterIdleMinutes != 30 {
		t.Fatalf("unexpected reconciled pool: %+v", poolRecord)
	}
	assertJSONRawEqual(t, poolRecord.DefaultMachineEnv, `{"ORG":"value"}`)
	assertJSONRawEqual(t, poolRecord.DefaultMachineSecretEnv, string(organizationSecretEnv))
	assertJSONRawEqual(
		t,
		poolRecord.DefaultMachineProviderOptions,
		`{"image":"new","sleep_after_ms":30000}`,
	)
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
	if _, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		OrgID:           created.Org.ID,
		ProjectID:       created.Project.ID,
		Name:            "Deleted model profile",
		CurrentConfigID: historicalConfig.ID,
		IdempotencyKey:  "deleted-model-profile",
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("create profile from deleted model config error = %v, want ErrNotFound", err)
	}
	if _, err := store.Execution().RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
		ProjectID:               created.Project.ID,
		ProfileID:               preservedProfile.ID,
		ExpectedCurrentConfigID: preservedProfile.CurrentConfigID,
		ConfigID:                historicalConfig.ID,
		Reason:                  "deleted model",
		IdempotencyKey:          "deleted-model-retarget",
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("retarget profile to deleted model config error = %v, want ErrNotFound", err)
	}
	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      created.Project.ID,
		AgentConfigID:  historicalConfig.ID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "deleted-model-launch",
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("launch agent from deleted model config error = %v, want ErrNotFound", err)
	}
	if _, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, "retained-model"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get manually granted removed model error = %v, want no rows", err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, created.Org.ID, manualProject.ID, retainedModel.ID,
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get removed model manual grant error = %v, want no rows", err)
	}
	if _, err := store.Models().GetConfiguredModelByName(
		ctx, created.Org.ID, provider.ID, activeAgentModel.Name,
	); err != nil {
		t.Fatalf("get active-agent referenced model: %v", err)
	}
	if _, err := store.Models().GetConfiguredModelByName(
		ctx, created.Org.ID, provider.ID, profileModel.Name,
	); err != nil {
		t.Fatalf("get profile referenced model: %v", err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, created.Org.ID, created.Project.ID, profileModel.ID,
	); err != nil {
		t.Fatalf("get profile referenced model grant: %v", err)
	}
	preservedTenantModel, err := store.Models().GetConfiguredModelByName(
		ctx, created.Org.ID, provider.ID, tenantModel.Name,
	)
	if err != nil || preservedTenantModel.ManagementKind != management.Tenant ||
		preservedTenantModel.ProviderModelSlug != tenantModel.ProviderModelSlug {
		t.Fatalf("get tenant model after reconciliation = %+v, %v", preservedTenantModel, err)
	}
	if _, err := store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID: created.Org.ID, SecretID: machineSecret.ID, Actor: userPrincipal(user.ID),
	}); err != nil {
		t.Fatalf("delete machine environment secret: %v", err)
	}

	result, err = store.Organizations().ReconcileDefaults(ctx, input)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("second apply changes = %v, want none", result.Changes)
	}
	assertRetainedModelWarnings("second apply", result)
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
	if len(result.Changes) != 6 || len(result.Warnings) != 0 {
		t.Fatalf("apply without default project result = %+v, want six changes and no warnings", result)
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
	assertJSONRawEqual(t, poolRecord.DefaultMachineSecretEnv, string(organizationSecretEnv))
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
	if len(result.Changes) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("second apply without default project result = %+v, want no changes or warnings", result)
	}
}

func TestReconcileDefaultsLocksModelsBeforeMachinePools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-locks@example.com", "Reconcile Locks")

	poolTemplate := func(name string) executionstore.DefaultMachinePoolTemplate {
		return defaultMachinePoolTemplateWithDefaultMachineForTest(
			executionstore.DefaultMachinePoolTemplate{
				Name:               name,
				Description:        "old",
				Provider:           "blaxel",
				ProviderAuthEnvVar: "RECONCILE_LOCK_TOKEN",
				MaxTotalMachines:   1,
				MaxTotalMemoryMB:   intPtrForMachinePoolTest(4096),
				MaxMachineMemoryMB: intPtrForMachinePoolTest(2048),
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        512,
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"lock-test"}`),
			},
		)
	}
	initialPools := []executionstore.DefaultMachinePoolTemplate{
		poolTemplate("reconcile-lock-pool-a"),
	}
	initialProvider := modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "reconcile-lock-provider",
		CredentialSecretName: "reconcile-lock-provider-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://reconcile-lock.example.com/v1",
		EndpointPath:         "/chat/completions",
		AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
		Models: []modelstore.DefaultConfiguredModelTemplate{{
			Name: "reconcile-lock-model", ProviderModelSlug: "example/lock",
			ContextWindowTokens: 8192, MaxOutputTokens: 1024,
		}},
	}
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:                        user.ID,
		Name:                          "Reconcile Lock Org",
		IdempotencyKey:                "reconcile-lock-org",
		DefaultMachinePools:           initialPools,
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create lock-order org: %v", err)
	}
	mustCompleteDefaultModelProviderProvisioning(
		t, ctx, store, created.Org.ID, initialProvider, "provider-token",
	)
	provider, err := store.Models().GetModelProviderConfigByName(ctx, created.Org.ID, initialProvider.Name)
	if err != nil {
		t.Fatalf("get lock-order provider: %v", err)
	}
	configuredModel, err := store.Models().GetConfiguredModelByName(
		ctx,
		created.Org.ID,
		provider.ID,
		initialProvider.Models[0].Name,
	)
	if err != nil {
		t.Fatalf("get lock-order configured model: %v", err)
	}
	poolRow, err := testQueries(store).GetMachinePoolByName(ctx, dbsqlc.GetMachinePoolByNameParams{
		OrgID: created.Org.ID,
		Name:  initialPools[0].Name,
	})
	if err != nil {
		t.Fatalf("get lock-order machine pool: %v", err)
	}

	t.Run("models before pools", func(t *testing.T) {
		modelBlockerTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin model blocker: %v", err)
		}
		defer func() { _ = modelBlockerTx.Rollback(context.Background()) }()
		if _, err := dbsqlc.New(modelBlockerTx).LockConfiguredModelForUse(
			ctx,
			dbsqlc.LockConfiguredModelForUseParams{OrgID: created.Org.ID, ID: configuredModel.ID},
		); err != nil {
			t.Fatalf("lock configured model for use: %v", err)
		}

		desiredPool := initialPools[0]
		desiredPool.Description = "model-first"
		reconcileDone := make(chan error, 1)
		go func() {
			_, reconcileErr := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
				Apply:                true,
				DefaultMachinePools:  []executionstore.DefaultMachinePoolTemplate{desiredPool},
				DefaultModelProvider: &initialProvider,
			})
			reconcileDone <- reconcileErr
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockConfiguredModelForMutation", 1)

		poolProbeTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin pool probe: %v", err)
		}
		probeCtx, cancelProbe := context.WithTimeout(ctx, time.Second)
		_, probeErr := dbsqlc.New(poolProbeTx).LockMachinePoolForUpdate(
			probeCtx,
			dbsqlc.LockMachinePoolForUpdateParams{OrgID: created.Org.ID, ID: poolRow.ID},
		)
		cancelProbe()
		if rollbackErr := poolProbeTx.Rollback(context.Background()); rollbackErr != nil {
			t.Fatalf("rollback pool probe: %v", rollbackErr)
		}
		if probeErr != nil {
			t.Fatalf("pool locked before blocked configured model: %v", probeErr)
		}
		if err := modelBlockerTx.Rollback(ctx); err != nil {
			t.Fatalf("release configured model blocker: %v", err)
		}
		select {
		case err := <-reconcileDone:
			if err != nil {
				t.Fatalf("reconcile defaults: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for reconciliation")
		}
	})
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
	desiredPools[1].MaxTotalMemoryMB = intPtrForMachinePoolTest(8192)
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
	controlTx := integrationdb.BeginTx(t, ctx, pool)
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
	deleteDone := integrationdb.RunAsyncError(func() error {
		_, deleteErr := store.Organizations().DeleteOrganizationOnceForIntegration(
			ctx,
			created.Org.ID,
			actor,
		)
		return deleteErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMachinePoolForLifecycle", 1)
	desiredPool := poolTemplate
	desiredPool.Description = "must not be applied"
	reconcileDone := integrationdb.RunAsyncError(func() error {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{
				Apply: true, DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{desiredPool},
			},
		)
		return reconcileErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockOrganizationLifecycleShared", 1)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release control transaction: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "organization deletion"); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
	if err := integrationdb.Await(t, reconcileDone, "reconciliation"); !errors.Is(err, storeerr.ErrNotFound) {
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
		UserID:                        user.ID,
		Name:                          "Reconcile Project Deletion",
		IdempotencyKey:                "reconcile-project-delete",
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	mustCompleteDefaultModelProviderProvisioning(
		t, ctx, store, created.Org.ID, providerTemplate, "provider-token",
	)
	provider, err := store.Models().GetModelProviderConfigByName(
		ctx,
		created.Org.ID,
		providerTemplate.Name,
	)
	if err != nil {
		t.Fatalf("get default model provider: %v", err)
	}
	controlTx := integrationdb.BeginTx(t, ctx, pool)
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
	deleteDone := integrationdb.RunAsyncError(func() error {
		_, deleteErr := store.Organizations().DeleteProjectOnceForIntegration(
			ctx,
			created.Org.ID,
			created.Project.ID,
			actor,
		)
		return deleteErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteProjectModelGrantsForProjectDeletion", 1)
	desiredProvider := providerTemplate
	desiredProvider.Models = append(
		append([]modelstore.DefaultConfiguredModelTemplate(nil), providerTemplate.Models...),
		modelstore.DefaultConfiguredModelTemplate{
			Name: "must-not-be-created", ProviderModelSlug: "example/rejected",
			ContextWindowTokens: 8192, MaxOutputTokens: 1024,
		},
	)
	reconcileDone := integrationdb.RunAsyncError(func() error {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{Apply: true, DefaultModelProvider: &desiredProvider},
		)
		return reconcileErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleShared", 1)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release control transaction: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "project deletion"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if err := integrationdb.Await(t, reconcileDone, "reconciliation"); !errors.Is(err, storeerr.ErrNotFound) {
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
	controlTx := integrationdb.BeginTx(t, ctx, pool)
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
	reconcileDone := integrationdb.RunAsyncError(func() error {
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
		return reconcileErr
	})
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
	if err := integrationdb.Await(t, reconcileDone, "reconciliation"); err != nil {
		t.Fatalf("reconcile defaults: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "machine pool deletion"); err != nil {
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

func TestReconcileDefaultsSerializesModelBeforePoolForAgentWorkflows(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "reconcile-launch-order@example.com", "Defaults Owner")
	poolTemplate := defaultReconciliationPoolTemplate("reconcile-launch-order-pool")
	providerTemplate := defaultReconciliationModelProviderTemplate()
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:                        user.ID,
		Name:                          "Reconcile Launch Order",
		IdempotencyKey:                "reconcile-launch-order",
		DefaultMachinePools:           []executionstore.DefaultMachinePoolTemplate{poolTemplate},
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	mustCompleteDefaultModelProviderProvisioning(
		t, ctx, store, created.Org.ID, providerTemplate, "provider-token",
	)
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
	configOrderAgentName := "Reconcile Config Order Agent"
	launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:     created.Project.ID,
		AgentConfigID: config.ID,
		LaunchedBy:    userPrincipal(user.ID),
		Name:          &configOrderAgentName,
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
	nextConfigInput := executionstore.CreateAgentConfigInput{
		ProjectID:               created.Project.ID,
		Definition:              json.RawMessage(nextCompiled.CanonicalJSON),
		Source:                  nextSource,
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(nextCompiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: nextCompiled.Hash,
	}
	nextConfig, err := store.Execution().CreateAgentConfig(ctx, nextConfigInput)
	if err != nil {
		t.Fatalf("create pool-backed config: %v", err)
	}
	controlTx := integrationdb.BeginTx(t, ctx, pool)
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
	reconcileDone := integrationdb.RunAsyncError(func() error {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{
				Apply:                true,
				DefaultMachinePools:  []executionstore.DefaultMachinePoolTemplate{desiredPool},
				DefaultModelProvider: &desiredProvider,
			},
		)
		return reconcileErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMachinePoolForLifecycle", 1)
	changeDone := integrationdb.RunAsyncError(func() error {
		_, changeErr := store.Execution().IntegrationChangeAgentConfigOnce(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: nextConfigInput,
			AgentID:                launched.Agent.ID,
			ActorType:              identitystore.PrincipalTypeUser,
			ActorID:                user.ID,
			IdempotencyKey:         "reconcile-config-order",
		})
		return changeErr
	})
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
	if err := integrationdb.Await(t, reconcileDone, "reconciliation"); err != nil {
		t.Fatalf("reconcile defaults: %v", err)
	}
	if err := integrationdb.Await(t, changeDone, "agent config change"); err != nil {
		t.Fatalf("change agent config: %v", err)
	}

	launchControlTx := integrationdb.BeginTx(t, ctx, pool)
	if _, err := dbsqlc.New(launchControlTx).LockMachinePoolForLifecycle(
		ctx,
		dbsqlc.LockMachinePoolForLifecycleParams{OrgID: created.Org.ID, ID: poolRow.ID},
	); err != nil {
		t.Fatalf("lock machine pool for launch: %v", err)
	}
	desiredPool.Description = "reconciled for launch"
	desiredProvider.BaseURL = "https://launch-reconciled.example.com/v1"
	reconcileDone = integrationdb.RunAsyncError(func() error {
		_, reconcileErr := store.Organizations().ReconcileDefaults(
			ctx,
			orglifecycle.ReconcileDefaultsInput{
				Apply:                true,
				DefaultMachinePools:  []executionstore.DefaultMachinePoolTemplate{desiredPool},
				DefaultModelProvider: &desiredProvider,
			},
		)
		return reconcileErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMachinePoolForLifecycle", 1)
	launchOrderAgentName := "Reconcile Launch Order Agent"
	launchDone := integrationdb.RunAsyncError(func() error {
		_, launchErr := store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
			ProjectID:      created.Project.ID,
			AgentConfigID:  nextConfig.ID,
			LaunchedBy:     userPrincipal(user.ID),
			Name:           &launchOrderAgentName,
			IdempotencyKey: "reconcile-launch-order-agent",
		})
		return launchErr
	})
	waitForDefaultReconciliationLockWaiter(
		t,
		ctx,
		pool,
		"LockConfiguredModelForUse",
		"agent launch",
		launchDone,
	)
	if err := launchControlTx.Commit(ctx); err != nil {
		t.Fatalf("release launch control transaction: %v", err)
	}
	if err := integrationdb.Await(t, reconcileDone, "launch reconciliation"); err != nil {
		t.Fatalf("reconcile defaults for launch: %v", err)
	}
	if err := integrationdb.Await(t, launchDone, "agent launch"); err != nil {
		t.Fatalf("launch agent: %v", err)
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
