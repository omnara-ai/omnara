//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func completeMachinePoolInputForTest(input executionstore.CreateMachinePoolInput) executionstore.CreateMachinePoolInput {
	if input.DefaultMachineCPU == nil {
		input.DefaultMachineCPU = intPtrForMachinePoolTest(1)
	}
	if input.DefaultMachineMemoryMB == nil {
		input.DefaultMachineMemoryMB = intPtrForMachinePoolTest(1024)
	}
	input.DefaultMachineEnv = normalizedJSON(input.DefaultMachineEnv)
	input.DefaultMachineSecretEnv = normalizedJSON(input.DefaultMachineSecretEnv)
	input.DefaultMachineProviderOptions = normalizedJSON(input.DefaultMachineProviderOptions)
	if input.MaxTotalCPU == nil {
		input.MaxTotalCPU = intPtrForMachinePoolTest(32)
	}
	if input.MaxTotalMemoryMB == nil {
		input.MaxTotalMemoryMB = intPtrForMachinePoolTest(65536)
	}
	if input.MaxMachineCPU == nil {
		input.MaxMachineCPU = input.MaxTotalCPU
	}
	if input.MaxMachineMemoryMB == nil {
		input.MaxMachineMemoryMB = input.MaxTotalMemoryMB
	}
	return input
}

type defaultMachineFieldsForTest struct {
	DefaultMachineCPU             int
	DefaultMachineMemoryMB        int
	DefaultMachineEnv             json.RawMessage
	DefaultMachineSecretEnv       json.RawMessage
	DefaultMachineProviderOptions json.RawMessage
}

func machinePoolInputWithDefaultMachineForTest(
	input executionstore.CreateMachinePoolInput,
	fields defaultMachineFieldsForTest,
) executionstore.CreateMachinePoolInput {
	if fields.DefaultMachineCPU != 0 {
		input.DefaultMachineCPU = intPtrForMachinePoolTest(fields.DefaultMachineCPU)
	}
	if fields.DefaultMachineMemoryMB != 0 {
		input.DefaultMachineMemoryMB = intPtrForMachinePoolTest(fields.DefaultMachineMemoryMB)
	}
	input.DefaultMachineEnv = fields.DefaultMachineEnv
	input.DefaultMachineSecretEnv = fields.DefaultMachineSecretEnv
	input.DefaultMachineProviderOptions = fields.DefaultMachineProviderOptions
	return input
}

func defaultMachinePoolTemplateWithDefaultMachineForTest(
	template executionstore.DefaultMachinePoolTemplate,
	fields defaultMachineFieldsForTest,
) executionstore.DefaultMachinePoolTemplate {
	if fields.DefaultMachineCPU != 0 {
		template.DefaultMachineCPU = intPtrForMachinePoolTest(fields.DefaultMachineCPU)
	}
	if fields.DefaultMachineMemoryMB != 0 {
		template.DefaultMachineMemoryMB = intPtrForMachinePoolTest(fields.DefaultMachineMemoryMB)
	}
	template.DefaultMachineEnv = fields.DefaultMachineEnv
	template.DefaultMachineSecretEnv = fields.DefaultMachineSecretEnv
	template.DefaultMachineProviderOptions = fields.DefaultMachineProviderOptions
	return template
}

type defaultMachineUpdateFieldsForTest struct {
	DefaultMachineCPU             *int
	DefaultMachineMemoryMB        *int
	DefaultMachineEnv             json.RawMessage
	DefaultMachineSecretEnv       json.RawMessage
	DefaultMachineProviderOptions json.RawMessage
}

func machinePoolUpdateInputWithDefaultMachineForTest(
	input executionstore.UpdateMachinePoolInput,
	fields defaultMachineUpdateFieldsForTest,
) executionstore.UpdateMachinePoolInput {
	input.DefaultMachineCPU = patch.NullableInt{Set: fields.DefaultMachineCPU != nil, Value: fields.DefaultMachineCPU}
	input.DefaultMachineMemoryMB = patch.NullableInt{
		Set:   fields.DefaultMachineMemoryMB != nil,
		Value: fields.DefaultMachineMemoryMB,
	}
	input.DefaultMachineEnv = fields.DefaultMachineEnv
	input.DefaultMachineSecretEnv = fields.DefaultMachineSecretEnv
	input.DefaultMachineProviderOptions = fields.DefaultMachineProviderOptions
	return input
}

type defaultMachineOverlayFieldsForTest struct {
	DefaultMachineCPU                    *int
	DefaultMachineMemoryMB               *int
	DefaultMachineEnvOverlay             json.RawMessage
	DefaultMachineSecretEnvOverlay       json.RawMessage
	DefaultMachineProviderOptionsOverlay json.RawMessage
}

func projectGrantInputWithDefaultMachineOverlayForTest(
	input executionstore.CreateProjectMachinePoolGrantInput,
	fields defaultMachineOverlayFieldsForTest,
) executionstore.CreateProjectMachinePoolGrantInput {
	input.DefaultMachineCPU = fields.DefaultMachineCPU
	input.DefaultMachineMemoryMB = fields.DefaultMachineMemoryMB
	input.DefaultMachineEnvOverlay = fields.DefaultMachineEnvOverlay
	input.DefaultMachineSecretEnvOverlay = fields.DefaultMachineSecretEnvOverlay
	input.DefaultMachineProviderOptionsOverlay = fields.DefaultMachineProviderOptionsOverlay
	return input
}

func intPtrForMachinePoolTest(value int) *int {
	return &value
}

func boolPtrForMachinePoolTest(value bool) *bool {
	return &value
}

func TestMachinePoolRuntimeProtectionDefaultsOffAndToggleClearsMarkers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))

	unprotected, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Name:             "Unprotected By Default",
				Provider:         "test.provider",
				MaxTotalMachines: 1,
			},
		),
	)
	if err != nil {
		t.Fatalf("create default-unprotected machine pool: %v", err)
	}
	if unprotected.RuntimeProtectionEnabled {
		t.Fatal("omitted runtime protection did not default off")
	}

	protected, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{
				OrgID:                    testOrgID,
				Name:                     "Explicitly Protected",
				Provider:                 "test.provider",
				RuntimeProtectionEnabled: true,
				MaxTotalMachines:         1,
			},
		),
	)
	if err != nil {
		t.Fatalf("create explicitly protected machine pool: %v", err)
	}
	if !protected.RuntimeProtectionEnabled {
		t.Fatal("explicitly enabled runtime protection was disabled")
	}

	machineID := testID("runtime-protection-marker-machine")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
INSERT INTO machines(
    id, org_id, machine_pool_id, source_kind, display_name, provider,
    lifecycle_state, lifecycle_changed_at, provider_resource_id,
    provider_provision_attempted_at, cpu, memory_mb, cwd, env, secret_env,
    provider_options, provider_runtime_mismatch_since, metadata, created_at, updated_at
) VALUES (
    $1, $2, $3, 'pool', 'runtime protection marker', $4,
    'active', $5, 'provider-resource', $5, 1, 1024, '', '{}'::jsonb, '{}'::jsonb,
    '{}'::jsonb, $5, '{}'::jsonb, $5, $5
)
`, machineID, testOrgID, protected.ID, protected.Provider, now); err != nil {
		t.Fatalf("seed runtime mismatch marker: %v", err)
	}
	updated, err := store.Execution().UpdateMachinePool(
		ctx,
		executionstore.UpdateMachinePoolInput{
			OrgID:                    testOrgID,
			ID:                       protected.ID,
			RuntimeProtectionEnabled: boolPtrForMachinePoolTest(false),
		},
	)
	if err != nil {
		t.Fatalf("disable runtime protection: %v", err)
	}
	if updated.RuntimeProtectionEnabled {
		t.Fatal("runtime protection remained enabled")
	}
	var mismatchSince *time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machineID,
	).Scan(&mismatchSince); err != nil {
		t.Fatalf("load runtime mismatch marker: %v", err)
	}
	if mismatchSince != nil {
		t.Fatalf("runtime mismatch marker survived protection disable: %v", mismatchSince)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET provider_runtime_mismatch_since = statement_timestamp() WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machineID,
	); err != nil {
		t.Fatalf("seed stale disabled marker: %v", err)
	}
	if _, err := store.Execution().UpdateMachinePool(
		ctx,
		executionstore.UpdateMachinePoolInput{
			OrgID:                    testOrgID,
			ID:                       protected.ID,
			RuntimeProtectionEnabled: boolPtrForMachinePoolTest(true),
		},
	); err != nil {
		t.Fatalf("re-enable runtime protection: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machineID,
	).Scan(&mismatchSince); err != nil {
		t.Fatalf("reload runtime mismatch marker: %v", err)
	}
	if mismatchSince != nil {
		t.Fatalf("stale mismatch marker survived protection re-enable: %v", mismatchSince)
	}
}

func completeMachinePoolCreateInputForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	input executionstore.CreateMachinePoolInput,
) executionstore.CreateMachinePoolInput {
	t.Helper()
	input = completeMachinePoolInputForTest(input)
	if input.ManagementKind != management.Cluster && isNilID(input.ProviderAuthSecretID) {
		input.ProviderAuthSecretID = createMachinePoolProviderAuthSecretForTest(
			t,
			ctx,
			store,
			"test-token",
		)
	}
	return input
}

func createMachinePoolProviderAuthSecretForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	value string,
) ID {
	t.Helper()
	suffix, err := newSecretUUID()
	if err != nil {
		t.Fatalf("generate machine pool provider auth secret suffix: %v", err)
	}
	name := "machine-pool-auth-" + suffix.String()
	admin := createSecretTestUser(t, ctx, store, name+" admin", "admin")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      name,
		Material:  secrets.GenericMaterial{Value: value},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create machine pool provider auth secret: %v", err)
	}
	return secret.ID
}

func secretPublicIDForTest(t *testing.T, id ID) string {
	t.Helper()
	out, err := publicid.Encode(publicid.KindSecret, id)
	if err != nil {
		t.Fatalf("encode secret public id: %v", err)
	}
	return out
}

func TestUpdateMachinePoolRejectsUnknownDefaultMachineSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	created, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             "Secret Ref Pool",
					Provider:         "test.provider",
					MaxTotalMachines: 1,
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineMemoryMB:        1024,
					DefaultMachineEnv:             json.RawMessage(`{}`),
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"initial"}`),
				},
			),
		),
	)
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	missingSecretID := secretPublicIDForTest(t, testID("missing-default-machine-config-secret"))
	_, err = store.Execution().UpdateMachinePool(ctx, machinePoolUpdateInputWithDefaultMachineForTest(
		executionstore.UpdateMachinePoolInput{
			OrgID: testOrgID,
			ID:    created.ID,
		},
		defaultMachineUpdateFieldsForTest{
			DefaultMachineCPU:             intPtrForMachinePoolTest(1),
			DefaultMachineMemoryMB:        intPtrForMachinePoolTest(1024),
			DefaultMachineSecretEnv:       json.RawMessage(`{"API_TOKEN":"` + missingSecretID + `"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"initial"}`),
		},
	))
	if err == nil || !strings.Contains(err.Error(), "secret is not found or not org-owned") {
		t.Fatalf("update with unknown secret_env = %v, want missing secret rejection", err)
	}
}

type capturingMachinePoolProviders struct {
	mergingMachinePoolProviders
	validatedProvider     string
	validatedPolicy       executionstore.MachinePoolProviderPolicy
	validatedProvisioning executionstore.MachineProvisioningConfig
}

func (p *capturingMachinePoolProviders) ValidatePool(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
) error {
	p.validatedProvider = provider
	p.validatedPolicy = policy
	return nil
}

func (p *capturingMachinePoolProviders) BuildMachineProvisioningIntent(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := p.ValidatePool(provider, policy); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	p.validatedProvisioning = machineProvisioning
	return machineProvisioning, nil
}

func TestCreateMachinePoolRequiresDefaultMachineProviderOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	providerAuthSecretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"test-token",
	)
	maxCPU, maxMemoryMB := 100, 1024*1024
	input := machinePoolInputWithDefaultMachineForTest(executionstore.CreateMachinePoolInput{
		OrgID:                testOrgID,
		Name:                 "Missing Provider Options",
		Provider:             "test",
		ProviderAuthSecretID: providerAuthSecretID,
		MaxTotalMachines:     1,
		MaxTotalCPU:          intPtrForMachinePoolTest(maxCPU),
		MaxTotalMemoryMB:     intPtrForMachinePoolTest(maxMemoryMB),
		MaxMachineCPU:        intPtrForMachinePoolTest(maxCPU),
		MaxMachineMemoryMB:   intPtrForMachinePoolTest(maxMemoryMB),
	}, defaultMachineFieldsForTest{
		DefaultMachineCPU:      1,
		DefaultMachineMemoryMB: 1024,
		DefaultMachineEnv:      json.RawMessage(`{}`),
	})
	_, err := store.Execution().CreateMachinePool(ctx, input)
	if err == nil || !strings.Contains(err.Error(), "machine pool default_machine fields requires provider_options") {
		t.Fatalf("missing provider options error = %v", err)
	}
}

func TestCreateMachinePoolAllowsOmittedDefaultMachineEnv(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	providerAuthSecretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"test-token",
	)
	maxCPU, maxMemoryMB := 100, 1024*1024

	if _, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:                testOrgID,
			Name:                 "Pool Without Env",
			Provider:             "test",
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForMachinePoolTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForMachinePoolTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForMachinePoolTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForMachinePoolTest(maxMemoryMB),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{}`),
		},
	)); err != nil {
		t.Fatalf("create machine pool without env: %v", err)
	}
}

func TestUpdateMachinePoolMutatesConfigAndKeepsProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	machinePoolProviders := &capturingMachinePoolProviders{}
	store := newIntegrationStore(pool, WithMachinePoolProviders(machinePoolProviders))
	created, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:    testOrgID,
					Name:     "Mutable Pool",
					Provider: "test.provider",
					ProviderConfig: json.RawMessage(
						`{"token":"initial"}`,
					),
					MaxTotalMachines: 2,
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineMemoryMB:        1024,
					DefaultMachineEnv:             json.RawMessage(`{}`),
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"initial"}`),
				},
			),
		),
	)
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	for _, test := range []struct {
		column string
		value  any
	}{
		{column: "id", value: testID("changed-machine-pool-id")},
		{column: "org_id", value: testID("changed-machine-pool-org")},
		{column: "management_kind", value: "cluster"},
		{column: "provider", value: "changed.provider"},
		{column: "provider_auth_env_var", value: "CHANGED_PROVIDER_TOKEN"},
	} {
		_, err := pool.Exec(
			ctx,
			fmt.Sprintf("UPDATE machine_pools SET %s = $1 WHERE org_id = $2 AND id = $3", test.column),
			test.value,
			created.OrgID,
			created.ID,
		)
		if !isPgCode(err, "25006") {
			t.Fatalf("update machine pool %s error = %v, want SQLSTATE 25006", test.column, err)
		}
	}
	if _, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{OrgID: testOrgID, Name: "Taken Pool", Provider: "test.provider", MaxTotalMachines: 1},
		),
	); err != nil {
		t.Fatalf("create duplicate-name target pool: %v", err)
	}
	rotatedProviderAuthSecretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"rotated-token",
	)
	machinePoolProviders.validatedProvider = ""
	machinePoolProviders.validatedPolicy = executionstore.MachinePoolProviderPolicy{}
	machinePoolProviders.validatedProvisioning = executionstore.MachineProvisioningConfig{}
	renamedName := "Renamed Pool"
	updatedDescription := "updated description"
	updatedDefaultCwd := "/updated"
	updatedMaxTotalMachines := int32(3)
	updatedMaxTotalCPU := 8
	updatedMaxTotalMemoryMB := 16384
	updatedMinMachineCPU := 1
	updatedMinMachineMemoryMB := 1024
	updatedMaxMachineCPU := 4
	updatedMaxMachineMemoryMB := 8192
	updatedDeleteAfterIdleMinutes := 30
	updated, err := store.Execution().UpdateMachinePool(ctx, machinePoolUpdateInputWithDefaultMachineForTest(
		executionstore.UpdateMachinePoolInput{
			OrgID:                testOrgID,
			ID:                   created.ID,
			Name:                 &renamedName,
			Description:          &updatedDescription,
			DefaultCwd:           &updatedDefaultCwd,
			ProviderConfig:       json.RawMessage(`{"token":"rotated"}`),
			ProviderAuthSecretID: &rotatedProviderAuthSecretID,
			MaxTotalMachines:     &updatedMaxTotalMachines,
			MaxTotalCPU:          patch.NullableInt{Set: true, Value: &updatedMaxTotalCPU},
			MaxTotalMemoryMB:     patch.NullableInt{Set: true, Value: &updatedMaxTotalMemoryMB},
			MinMachineCPU:        patch.NullableInt{Set: true, Value: &updatedMinMachineCPU},
			MinMachineMemoryMB:   patch.NullableInt{Set: true, Value: &updatedMinMachineMemoryMB},
			MaxMachineCPU:        patch.NullableInt{Set: true, Value: &updatedMaxMachineCPU},
			MaxMachineMemoryMB:   patch.NullableInt{Set: true, Value: &updatedMaxMachineMemoryMB},
			DeleteAfterIdleMinutes: patch.NullableInt{
				Set: true, Value: &updatedDeleteAfterIdleMinutes,
			},
			Metadata: resourcemeta.Metadata{"team": "infra"},
		},
		defaultMachineUpdateFieldsForTest{
			DefaultMachineCPU:             intPtrForMachinePoolTest(2),
			DefaultMachineMemoryMB:        intPtrForMachinePoolTest(2048),
			DefaultMachineEnv:             json.RawMessage(`{"SECRET":"value"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"updated"}`),
		},
	))
	if err != nil {
		t.Fatalf("update machine pool: %v", err)
	}
	if updated.ID != created.ID || updated.OrgID != created.OrgID || updated.Provider != created.Provider || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("update changed immutable fields: created=%+v updated=%+v", created, updated)
	}
	if updated.Name != "Renamed Pool" || updated.Description != "updated description" ||
		updated.DefaultCwd != "/updated" || updated.ProviderAuthSecretID != rotatedProviderAuthSecretID ||
		updated.MaxTotalMachines != 3 || updated.MaxTotalCPU == nil || *updated.MaxTotalCPU != 8 ||
		updated.MaxTotalMemoryMB == nil || *updated.MaxTotalMemoryMB != 16384 ||
		updated.MinMachineCPU == nil || *updated.MinMachineCPU != 1 ||
		updated.MinMachineMemoryMB == nil || *updated.MinMachineMemoryMB != 1024 ||
		updated.MaxMachineCPU == nil || *updated.MaxMachineCPU != 4 ||
		updated.MaxMachineMemoryMB == nil || *updated.MaxMachineMemoryMB != 8192 ||
		updated.DeleteAfterIdleMinutes == nil || *updated.DeleteAfterIdleMinutes != 30 {
		t.Fatalf("update did not apply mutable fields: %+v", updated)
	}
	if machinePoolProviders.validatedProvider != "test.provider" {
		t.Fatalf("provider validator got provider %q, want existing provider", machinePoolProviders.validatedProvider)
	}
	if machinePoolProviders.validatedPolicy.DefaultProvisioning.CPU == nil ||
		*machinePoolProviders.validatedPolicy.DefaultProvisioning.CPU != 2 {
		t.Fatalf("provider validator policy = %+v, want default cpu 2", machinePoolProviders.validatedPolicy)
	}
	if machinePoolProviders.validatedPolicy.ResourceLimits.MaxTotalCPU == nil ||
		*machinePoolProviders.validatedPolicy.ResourceLimits.MaxTotalCPU != 8 ||
		machinePoolProviders.validatedPolicy.ResourceLimits.MinMachineCPU == nil ||
		*machinePoolProviders.validatedPolicy.ResourceLimits.MinMachineCPU != 1 ||
		machinePoolProviders.validatedPolicy.ResourceLimits.MinMachineMemoryMB == nil ||
		*machinePoolProviders.validatedPolicy.ResourceLimits.MinMachineMemoryMB != 1024 ||
		machinePoolProviders.validatedPolicy.ResourceLimits.MaxMachineMemoryMB == nil ||
		*machinePoolProviders.validatedPolicy.ResourceLimits.MaxMachineMemoryMB != 8192 {
		t.Fatalf("provider validator limits = %+v, want updated limits", machinePoolProviders.validatedPolicy.ResourceLimits)
	}
	if !sameJSON(machinePoolProviders.validatedPolicy.ProviderConfig, json.RawMessage(`{"token":"rotated"}`)) ||
		!sameJSON(updated.ProviderConfig, json.RawMessage(`{"token":"rotated"}`)) {
		t.Fatalf(
			"provider config not updated/validated: validated=%s updated=%s",
			machinePoolProviders.validatedPolicy.ProviderConfig,
			updated.ProviderConfig,
		)
	}
	if !sameJSON(updated.Metadata, json.RawMessage(`{"team":"infra"}`)) {
		t.Fatalf("metadata not updated: %s", updated.Metadata)
	}
	patchDescription := "patched description only"
	patched, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{OrgID: testOrgID, ID: created.ID, Description: &patchDescription})
	if err != nil {
		t.Fatalf("patch machine pool: %v", err)
	}
	if patched.Name != updated.Name || patched.Description != patchDescription ||
		patched.DefaultCwd != updated.DefaultCwd || patched.ProviderAuthSecretID != updated.ProviderAuthSecretID ||
		!sameIntPtr(patched.MaxTotalCPU, updated.MaxTotalCPU) ||
		!sameIntPtr(patched.DeleteAfterIdleMinutes, updated.DeleteAfterIdleMinutes) ||
		!sameIntPtr(patched.DefaultMachineCPU, updated.DefaultMachineCPU) ||
		!sameIntPtr(patched.DefaultMachineMemoryMB, updated.DefaultMachineMemoryMB) ||
		!sameJSON(patched.DefaultMachineEnv, updated.DefaultMachineEnv) ||
		!sameJSON(patched.DefaultMachineSecretEnv, updated.DefaultMachineSecretEnv) ||
		!sameJSON(patched.DefaultMachineProviderOptions, updated.DefaultMachineProviderOptions) ||
		!sameJSON(patched.ProviderConfig, updated.ProviderConfig) || !sameJSON(patched.Metadata, updated.Metadata) {
		t.Fatalf("patch did not preserve omitted fields: before=%+v after=%+v", updated, patched)
	}
	cleared, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: testOrgID, ID: created.ID,
		DeleteAfterIdleMinutes: patch.NullableInt{Set: true},
	})
	if err != nil {
		t.Fatalf("clear machine pool idle deletion policy: %v", err)
	}
	if cleared.DeleteAfterIdleMinutes != nil {
		t.Fatalf("cleared machine pool idle deletion policy = %v, want nil", cleared.DeleteAfterIdleMinutes)
	}
	badMaxMachineCPU := 1
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: testOrgID, ID: created.ID,
		MaxMachineCPU: patch.NullableInt{Set: true, Value: &badMaxMachineCPU},
	}); err == nil || !strings.Contains(err.Error(), "cpu exceeds max_machine_cpu") {
		t.Fatalf("bad cap update error = %v, want cpu cap error", err)
	}
	takenName := "Taken Pool"
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{OrgID: testOrgID, ID: created.ID, Name: &takenName}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("duplicate name update error = %v, want ErrConflict", err)
	}
	if _, err := store.Execution().DeleteMachinePool(ctx, testOrgID, created.ID); err != nil {
		t.Fatalf("archive machine pool: %v", err)
	}
	archivedName := "Archived Pool"
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{OrgID: testOrgID, ID: created.ID, Name: &archivedName}); !storeerr.IsNotFound(err) {
		t.Fatalf("archived pool update error = %v, want not found", err)
	}
	defaultPool := createDefaultMachinePoolForTest(
		t,
		ctx,
		store,
		machinePoolInputWithDefaultMachineForTest(
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Name:             "Hosted Default Pool",
				Provider:         "test.provider",
				MaxTotalMachines: 1,
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"cluster","sleep_after_ms":30000}`),
			},
		),
	)
	clusterDefaultCPU := 2
	clusterDefaultMemoryMB := 2048
	clusterMinCPU := 1
	clusterMinMemoryMB := 1024
	clusterMaxCPU := 4
	clusterMaxMemoryMB := 4096
	clusterDeleteAfterIdleMinutes := 30
	clusterSecretEnv := json.RawMessage(fmt.Sprintf(
		`{"TOKEN":%q}`,
		secretPublicIDForTest(t, rotatedProviderAuthSecretID),
	))
	updatedDefaultPool, err := store.Execution().UpdateMachinePool(ctx, machinePoolUpdateInputWithDefaultMachineForTest(
		executionstore.UpdateMachinePoolInput{
			OrgID:                  testOrgID,
			ID:                     defaultPool.ID,
			MinMachineCPU:          patch.NullableInt{Set: true, Value: &clusterMinCPU},
			MinMachineMemoryMB:     patch.NullableInt{Set: true, Value: &clusterMinMemoryMB},
			MaxMachineCPU:          patch.NullableInt{Set: true, Value: &clusterMaxCPU},
			MaxMachineMemoryMB:     patch.NullableInt{Set: true, Value: &clusterMaxMemoryMB},
			DeleteAfterIdleMinutes: patch.NullableInt{Set: true, Value: &clusterDeleteAfterIdleMinutes},
		},
		defaultMachineUpdateFieldsForTest{
			DefaultMachineCPU:       &clusterDefaultCPU,
			DefaultMachineMemoryMB:  &clusterDefaultMemoryMB,
			DefaultMachineEnv:       json.RawMessage(`{"ALLOWED":"yes"}`),
			DefaultMachineSecretEnv: clusterSecretEnv,
		},
	))
	if err != nil {
		t.Fatalf("update cluster-managed machine pool editable fields: %v", err)
	}
	if updatedDefaultPool.DefaultMachineCPU == nil || *updatedDefaultPool.DefaultMachineCPU != clusterDefaultCPU ||
		updatedDefaultPool.DefaultMachineMemoryMB == nil || *updatedDefaultPool.DefaultMachineMemoryMB != clusterDefaultMemoryMB ||
		updatedDefaultPool.MinMachineCPU == nil || *updatedDefaultPool.MinMachineCPU != clusterMinCPU ||
		updatedDefaultPool.MinMachineMemoryMB == nil || *updatedDefaultPool.MinMachineMemoryMB != clusterMinMemoryMB ||
		updatedDefaultPool.MaxMachineCPU == nil || *updatedDefaultPool.MaxMachineCPU != clusterMaxCPU ||
		updatedDefaultPool.MaxMachineMemoryMB == nil || *updatedDefaultPool.MaxMachineMemoryMB != clusterMaxMemoryMB ||
		updatedDefaultPool.DeleteAfterIdleMinutes == nil || *updatedDefaultPool.DeleteAfterIdleMinutes != clusterDeleteAfterIdleMinutes ||
		!sameJSON(updatedDefaultPool.DefaultMachineEnv, json.RawMessage(`{"ALLOWED":"yes"}`)) ||
		!sameJSON(updatedDefaultPool.DefaultMachineSecretEnv, clusterSecretEnv) ||
		!sameJSON(updatedDefaultPool.DefaultMachineProviderOptions, json.RawMessage(`{"image":"cluster","sleep_after_ms":30000}`)) {
		t.Fatalf("cluster-managed editable fields not updated: %+v", updatedDefaultPool)
	}
	defaultDescription := "not allowed"
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{OrgID: testOrgID, ID: defaultPool.ID, Description: &defaultDescription}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("cluster-managed description update error = %v, want ErrStateTransitionConflict", err)
	}
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: testOrgID, ID: defaultPool.ID,
		DefaultMachineProviderOptions: json.RawMessage(`{"image":"cluster","sleep_after_ms":30000,"startup_script":"echo ready"}`),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("cluster-managed provider options update error = %v, want ErrStateTransitionConflict", err)
	}
	clusterMaxTotalMachines := int32(2)
	if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{OrgID: testOrgID, ID: defaultPool.ID, MaxTotalMachines: &clusterMaxTotalMachines}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("cluster-managed quota update error = %v, want ErrStateTransitionConflict", err)
	}
	if _, err := store.Execution().DeleteMachinePool(ctx, testOrgID, defaultPool.ID); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("cluster-managed archive error = %v, want ErrStateTransitionConflict", err)
	}
	preservedDefaultPool, err := store.Execution().GetMachinePool(ctx, testOrgID, defaultPool.ID)
	if err != nil {
		t.Fatalf("get cluster-managed pool after rejected update/archive: %v", err)
	}
	if preservedDefaultPool.Description == defaultDescription ||
		preservedDefaultPool.MaxTotalMachines != defaultPool.MaxTotalMachines ||
		preservedDefaultPool.DeletedAt != nil {
		t.Fatalf("cluster-managed pool changed after rejected update/archive: %+v", preservedDefaultPool)
	}
}

func TestMachineConfigEnvRejectsReservedOmnaraNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	providerAuthSecretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"test-token",
	)

	for _, tc := range []struct {
		name    string
		fields  defaultMachineFieldsForTest
		wantErr string
	}{
		{
			name: "Reserved Pool Env",
			fields: defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineEnv:             json.RawMessage(`{"OMNARA_MACHINE_TOKEN":"bad"}`),
				DefaultMachineProviderOptions: json.RawMessage(`{}`),
			},
			wantErr: "machine pool default_machine fields env cannot set reserved OMNARA_ key OMNARA_MACHINE_TOKEN",
		},
		{
			name: "Reserved Pool Env Null",
			fields: defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineEnv:             json.RawMessage(`{"OMNARA_FUTURE_SETTING":null}`),
				DefaultMachineProviderOptions: json.RawMessage(`{}`),
			},
			wantErr: "machine pool default_machine fields env cannot set reserved OMNARA_ key OMNARA_FUTURE_SETTING",
		},
		{
			name: "Pool Env Object Value",
			fields: defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineEnv:             json.RawMessage(`{"APP_ENV":{"value":"test"}}`),
				DefaultMachineProviderOptions: json.RawMessage(`{}`),
			},
		},
	} {
		_, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForTest(
			executionstore.CreateMachinePoolInput{
				OrgID:                testOrgID,
				Name:                 tc.name,
				Provider:             "test",
				ProviderAuthSecretID: providerAuthSecretID,
				MaxTotalMachines:     1,
				MaxTotalCPU:          intPtrForMachinePoolTest(4),
				MaxTotalMemoryMB:     intPtrForMachinePoolTest(4096),
				MaxMachineCPU:        intPtrForMachinePoolTest(4),
				MaxMachineMemoryMB:   intPtrForMachinePoolTest(4096),
			},
			tc.fields,
		))

		if err == nil {
			t.Fatalf("%s error = nil, want error", tc.name)
		}
		if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s error = %v, want %q", tc.name, err, tc.wantErr)
		}
	}

	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:                testOrgID,
			Name:                 "Reserved Overlay Env Pool",
			Provider:             "test",
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     2,
			MaxTotalCPU:          intPtrForMachinePoolTest(4),
			MaxTotalMemoryMB:     intPtrForMachinePoolTest(4096),
			MaxMachineCPU:        intPtrForMachinePoolTest(4),
			MaxMachineMemoryMB:   intPtrForMachinePoolTest(4096),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineEnv:             json.RawMessage(`{"APP_ENV":"test"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{}`),
		},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}

	_, err = store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-reserved-project-pool-env",
		},
		defaultMachineOverlayFieldsForTest{
			DefaultMachineEnvOverlay: json.RawMessage(`{"OMNARA_FUTURE_SETTING":"bad"}`),
		},
	))

	if err == nil || !strings.Contains(err.Error(), "env cannot set reserved OMNARA_ key OMNARA_FUTURE_SETTING") {
		t.Fatalf("project grant reserved env error = %v", err)
	}
	clearGrant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-project-pool-env-clear",
		},
		defaultMachineOverlayFieldsForTest{
			DefaultMachineEnvOverlay: json.RawMessage(`{"APP_ENV":null}`),
		},
	))

	if err != nil {
		t.Fatalf("project grant env clear should be valid: %v", err)
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		clearGrant.ID,
	); err != nil {
		t.Fatalf("revoke env clear grant: %v", err)
	}

	if _, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachinePoolID:  machinePool.ID,
		IdempotencyKey: "idem-reserved-agent-source-env-grant",
	}); err != nil {
		t.Fatalf("create valid pool grant: %v", err)
	}
	if _, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachinePoolID:  machinePool.ID,
		IdempotencyKey: "idem-duplicate-pool-grant",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("duplicate pool grant error = %v, want ErrConflict", err)
	}
	compiled := mustCompileAgentYAMLWithMachineSourceResolvers(t, ctx, store, `
instruction: Use the pool.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    env_overlay:
      OMNARA_FUTURE_SETTING: bad
tools:
  run_command: {}
`)
	err = store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(compiled.CanonicalJSON),
		agentconfig.CompilerVersion,
		compiled.Hash)

	if err == nil || !strings.Contains(err.Error(), "env cannot set reserved OMNARA_ key OMNARA_FUTURE_SETTING") {
		t.Fatalf("agent source reserved env error = %v", err)
	}
}

func TestUpdateMachinePoolRejectsInvalidStoredName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	created, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             "Stored Pool",
					Provider:         "test",
					MaxTotalMachines: 1,
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineMemoryMB:        1024,
					DefaultMachineProviderOptions: json.RawMessage(`{}`),
				},
			),
		),
	)
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE machine_pools DROP CONSTRAINT machine_pools_name_policy`); err != nil {
		t.Fatalf("drop machine pool name constraint: %v", err)
	}
	const invalidStoredName = " invalid pool "
	var seededName string
	if err := pool.QueryRow(
		ctx,
		`UPDATE machine_pools SET name = $1 WHERE id = $2 RETURNING name`,
		invalidStoredName,
		created.ID,
	).Scan(&seededName); err != nil {
		t.Fatalf("seed invalid machine pool name: %v", err)
	}
	if seededName != invalidStoredName {
		t.Fatalf("seeded machine pool name = %q", seededName)
	}
	stored, err := store.Execution().GetMachinePool(ctx, testOrgID, created.ID)
	if err != nil {
		t.Fatalf("load invalid stored machine pool: %v", err)
	}
	if stored.Name != invalidStoredName {
		t.Fatalf("loaded machine pool name = %q", stored.Name)
	}

	description := "updated without a rename"
	updatedInvalid, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID:       testOrgID,
		ID:          created.ID,
		Description: &description,
	})
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("update with invalid stored machine pool name = %+v, error = %v, want invalid request", updatedInvalid, err)
	}
	repairedName := "Repaired Pool"
	updated, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: testOrgID,
		ID:    created.ID,
		Name:  &repairedName,
	})
	if err != nil {
		t.Fatalf("repair machine pool name: %v", err)
	}
	if updated.Name != repairedName {
		t.Fatalf("repaired machine pool name = %q", updated.Name)
	}
}

func TestMachinePoolSecretEnvValidatesAndMaterializes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 18, 13, 20, 0, 0, time.UTC)
	user := createSecretTestUser(t, ctx, store, "pool-secret-env-admin", "admin")
	providerAuthSecretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"test-token",
	)

	orgSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "pool-org-env",
		Material:  secrets.GenericMaterial{Value: "org-secret-value"},
		Actor:     userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create org secret: %v", err)
	}
	ungrantedOrgSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "pool-ungranted-org-env",
		Material:  secrets.GenericMaterial{Value: "ungranted-org-secret-value"},
		Actor:     userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create ungranted org secret: %v", err)
	}
	projectSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "pool-project-env",
		Material:       secrets.GenericMaterial{Value: "project-secret-value"},
		Actor:          userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create project secret: %v", err)
	}
	orgSecretID := secretPublicIDForTest(t, orgSecret.ID)
	ungrantedOrgSecretID := secretPublicIDForTest(t, ungrantedOrgSecret.ID)
	projectSecretID := secretPublicIDForTest(t, projectSecret.ID)

	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:                testOrgID,
			Name:                 "Secret Env Pool",
			Provider:             "test",
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     2,
			MaxTotalCPU:          intPtrForMachinePoolTest(4),
			MaxTotalMemoryMB:     intPtrForMachinePoolTest(4096),
			MaxMachineCPU:        intPtrForMachinePoolTest(4),
			MaxMachineMemoryMB:   intPtrForMachinePoolTest(4096),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineEnv:             json.RawMessage(`{"PLAIN":"plain"}`),
			DefaultMachineSecretEnv:       json.RawMessage(`{"ORG_SECRET":"` + orgSecretID + `"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{}`),
		},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}

	_, err = store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachinePoolID:  machinePool.ID,
		IdempotencyKey: "idem-pool-secret-env-default-unavailable",
	})

	if err == nil || !strings.Contains(err.Error(), "secret_env.ORG_SECRET secret is not available to the project") {
		t.Fatalf("project grant unavailable default secret error = %v", err)
	}

	if _, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID:           testOrgID,
		SecretID:        orgSecret.ID,
		TargetProjectID: testProjectID,
		Actor:           userPrincipal(user.ID),
	}); err != nil {
		t.Fatalf("grant pool org secret to project: %v", err)
	}

	_, err = store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pool-secret-env-invalid",
		},
		defaultMachineOverlayFieldsForTest{
			DefaultMachineSecretEnvOverlay: json.RawMessage(`{"PROJECT_SECRET":"` + ungrantedOrgSecretID + `"}`),
		},
	))

	if err == nil || !strings.Contains(err.Error(), "secret_env.PROJECT_SECRET secret is not available to the project") {
		t.Fatalf("project grant unavailable secret error = %v", err)
	}

	_, err = store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pool-secret-env-valid",
		},
		defaultMachineOverlayFieldsForTest{
			DefaultMachineSecretEnvOverlay: json.RawMessage(`{"PROJECT_SECRET":"` + projectSecretID + `"}`),
		},
	))

	if err != nil {
		t.Fatalf("create project grant with secret_env: %v", err)
	}

	machineRef := "mchr-secr3t"
	agentID := mustCreateAgent(t, ctx, store, now.Add(6*time.Second))
	poolGrant, err := store.q.GetActiveProjectMachinePoolGrantForLaunch(
		ctx,
		dbsqlc.GetActiveProjectMachinePoolGrantForLaunchParams{
			OrgID:         testOrgID,
			ProjectID:     testProjectID,
			MachinePoolID: machinePool.ID,
		},
	)
	if err != nil {
		t.Fatalf("get launch pool grant: %v", err)
	}
	agentPlainValue := "agent-plain"
	agentSecretRef := orgSecretID
	resolvedMachine, err := store.Execution().ResolvePoolMachineTx(
		ctx,
		store.q,
		poolGrant,
		agentconfig.RuntimeMachine{
			EnvOverlay:       map[string]*string{"AGENT_PLAIN": &agentPlainValue},
			SecretEnvOverlay: map[string]*string{"AGENT_SECRET": &agentSecretRef},
		},
	)
	if err != nil {
		t.Fatalf("resolve pool machine: %v", err)
	}
	if _, err := executionstore.IntegrationCreatePoolMachineBindingTx(ctx, store.q, executionstore.IntegrationPoolMachineBindingInput{
		OrgID:            testOrgID,
		ProjectID:        testProjectID,
		AgentID:          agentID,
		Description:      "secret env machine",
		PoolGrant:        poolGrant,
		ResolvedMachine:  resolvedMachine,
		MachineRef:       machineRef,
		CreateToolCallID: NilID,
	}); err != nil {
		t.Fatalf("create pool machine binding: %v", err)
	}
	record, err := executionstore.IntegrationPoolMachineByRefTx(ctx, store.q, testProjectID, agentID, machineRef)
	if err != nil {
		t.Fatalf("load pool machine: %v", err)
	}
	var storedSecretEnv map[string]string
	if err := json.Unmarshal(record.Machine.SecretEnv, &storedSecretEnv); err != nil {
		t.Fatalf("decode stored machine secret_env: %v", err)
	}
	for key, value := range storedSecretEnv {
		if value == "org-secret-value" || value == "project-secret-value" {
			t.Fatalf("stored binding secret_env[%s] contains secret payload", key)
		}
	}
	storedProvisioning := machineProvisioningFromRecordForTest(t, record.Machine)
	for key, value := range storedProvisioning.ProviderOptions {
		if strings.Contains(string(value), "org-secret-value") || strings.Contains(string(value), "project-secret-value") {
			t.Fatalf("stored machine config provider_options[%s] contains secret payload", key)
		}
	}
	resolvedEnv, err := store.Execution().ResolveEnvironmentSecrets(
		ctx,
		record.Binding.OrgID,
		record.Binding.ProjectID,
		record.Machine.Env,
		record.Machine.SecretEnv,
	)
	if err != nil {
		t.Fatalf("resolve binding environment: %v", err)
	}
	wantEnv := map[string]string{
		"PLAIN":          "plain",
		"ORG_SECRET":     "org-secret-value",
		"PROJECT_SECRET": "project-secret-value",
	}
	for key, want := range wantEnv {
		if got := resolvedEnv[key]; got != want {
			t.Fatalf("resolved env[%s] = %q, want %q; env=%+v", key, got, want, resolvedEnv)
		}
	}
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(ctx, testOrgID, record.Machine.ID)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	if claim.GrantProjectID != testProjectID {
		t.Fatalf("claim grant project = %s, want %s", claim.GrantProjectID, testProjectID)
	}
	if got := claim.BindingEnvironmentOverlay.Env["AGENT_PLAIN"]; got == nil || *got != agentPlainValue {
		t.Fatalf("claim binding env overlay = %+v, want AGENT_PLAIN", claim.BindingEnvironmentOverlay)
	}
	if got := claim.BindingEnvironmentOverlay.SecretEnv["AGENT_SECRET"]; got == nil || *got != agentSecretRef {
		t.Fatalf("claim binding secret env overlay = %+v, want AGENT_SECRET", claim.BindingEnvironmentOverlay)
	}
	provisioningEnv, err := store.Execution().ResolvePoolMachineProvisioningEnv(ctx, claim)
	if err != nil {
		t.Fatalf("resolve provisioning env: %v", err)
	}
	wantProvisioningEnv := map[string]string{
		"PLAIN":          "plain",
		"ORG_SECRET":     "org-secret-value",
		"PROJECT_SECRET": "project-secret-value",
		"AGENT_PLAIN":    "agent-plain",
		"AGENT_SECRET":   "org-secret-value",
	}
	if len(provisioningEnv) != len(wantProvisioningEnv) {
		t.Fatalf("provisioning env = %+v, want %+v", provisioningEnv, wantProvisioningEnv)
	}
	for key, want := range wantProvisioningEnv {
		if got := provisioningEnv[key]; got != want {
			t.Fatalf("provisioning env[%s] = %q, want %q; env=%+v", key, got, want, provisioningEnv)
		}
	}

	secondProjectID := testID("pool_secret_env_second_project")
	if _, err := pool.Exec(
		ctx,
		`
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Second Secret Env Project', 'idem-pool-secret-env-second-project', $3, $3)
`,
		secondProjectID,
		testOrgID,
		now.Add(8*time.Second),
	); err != nil {
		t.Fatalf("seed second project: %v", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        orgSecret.ID,
			TargetProjectID: secondProjectID,
			Actor:           userPrincipal(user.ID),
		},
	); err != nil {
		t.Fatalf("grant pool org secret to second project: %v", err)
	}
	secondPoolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      secondProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pool-secret-env-second-project-grant",
		})

	if err != nil {
		t.Fatalf("create second project pool grant: %v", err)
	}
	_, err = store.q.UpsertProjectMachineGrant(
		ctx,
		dbsqlc.UpsertProjectMachineGrantParams{
			OrgID:                     testOrgID,
			ProjectID:                 secondProjectID,
			MachineID:                 record.Machine.ID,
			SourceKind:                "pool",
			ProjectMachinePoolGrantID: &secondPoolGrant.ID,
			Description:               secondPoolGrant.Description,
			Metadata:                  json.RawMessage(`{}`),
		},
	)
	if err == nil || !storeutil.IsUniqueViolation(err) {
		t.Fatalf("second active generated pool machine grant error = %v, want unique violation", err)
	}

	if _, err := pool.Exec(
		ctx,
		`DELETE FROM project_machine_grants WHERE org_id = $1 AND machine_id = $2`,
		testOrgID,
		record.Machine.ID,
	); err != nil {
		t.Fatalf("delete project machine grant: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET next_reconcile_after = statement_timestamp() - interval '1 second' WHERE org_id = $1 AND id = $2`,
		testOrgID,
		record.Machine.ID,
	); err != nil {
		t.Fatalf("make ungranted provisioning claim due: %v", err)
	}
	ungrantedClaim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		record.Machine.ID,
	)
	if err != nil || !ok {
		t.Fatalf("claim ungranted pool machine for provisioning ok=%v err=%v", ok, err)
	}
	if ungrantedClaim.GrantProjectID != NilID {
		t.Fatalf("ungranted claim grant project = %s, want nil", ungrantedClaim.GrantProjectID)
	}
	if _, err := store.Execution().ResolvePoolMachineProvisioningEnv(ctx, ungrantedClaim); err == nil ||
		!strings.Contains(err.Error(), "secret_env.PROJECT_SECRET") {
		t.Fatalf("ungranted provisioning env error = %v, want PROJECT_SECRET rejection", err)
	}
	orgEnv, err := store.Execution().ResolveEnvironmentSecrets(
		ctx,
		testOrgID,
		NilID,
		json.RawMessage(`{"PLAIN":"plain"}`),
		json.RawMessage(`{"ORG_SECRET":"`+orgSecretID+`"}`),
	)
	if err != nil {
		t.Fatalf("resolve org-scoped environment: %v", err)
	}
	if len(orgEnv) != 2 || orgEnv["PLAIN"] != "plain" || orgEnv["ORG_SECRET"] != "org-secret-value" {
		t.Fatalf("org-scoped env = %+v", orgEnv)
	}
}

func TestCreateProjectMachinePoolGrantAppliesOnlyPerMachineLimitsToResolvedResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	providerAuthSecretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"test-token",
	)
	maxCPU, maxMemoryMB := 16, 32768
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:                testOrgID,
			Name:                 "Cap Fit Pool",
			Provider:             "test.provider",
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     5,
			MaxTotalCPU:          intPtrForMachinePoolTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForMachinePoolTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForMachinePoolTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForMachinePoolTest(maxMemoryMB),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             4,
			DefaultMachineMemoryMB:        8192,
			DefaultMachineEnv:             json.RawMessage(`{}`),
			DefaultMachineProviderOptions: json.RawMessage(`{}`),
		},
	))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	machines0 := 0
	machines9 := 9
	cpu2 := 2
	cpu8 := 8
	memory4096 := 4096
	cases := []struct {
		name        string
		input       executionstore.CreateProjectMachinePoolGrantInput
		wantErr     string
		wantCreated bool
	}{
		{
			name:        "Total CPU Below Inherited Default",
			input:       executionstore.CreateProjectMachinePoolGrantInput{MaxTotalCPU: &cpu2},
			wantCreated: true,
		},
		{
			name:        "Zero Total Machines Cap",
			input:       executionstore.CreateProjectMachinePoolGrantInput{MaxTotalMachines: &machines0},
			wantCreated: true,
		},
		{
			name:        "Total Machines Cap Above Pool Budget",
			input:       executionstore.CreateProjectMachinePoolGrantInput{MaxTotalMachines: &machines9},
			wantCreated: true,
		},
		{
			name:        "Total Memory Below Inherited Default",
			input:       executionstore.CreateProjectMachinePoolGrantInput{MaxTotalMemoryMB: &memory4096},
			wantCreated: true,
		},
		{
			name:    "Per Machine CPU Cap Below Inherited Default",
			input:   executionstore.CreateProjectMachinePoolGrantInput{MaxMachineCPU: &cpu2},
			wantErr: "resolved project machine pool grant config: cpu exceeds max_machine_cpu",
		},
		{
			name: "Per Machine CPU Cap With Lower Project Default",
			input: projectGrantInputWithDefaultMachineOverlayForTest(
				executionstore.CreateProjectMachinePoolGrantInput{MaxMachineCPU: &cpu2},
				defaultMachineOverlayFieldsForTest{DefaultMachineCPU: &cpu2},
			),
			wantCreated: true,
		},
		{
			name:    "Per Machine CPU Minimum Above Inherited Default",
			input:   executionstore.CreateProjectMachinePoolGrantInput{MinMachineCPU: &cpu8},
			wantErr: "resolved project machine pool grant config: cpu is below min_machine_cpu",
		},
		{
			name: "Per Machine CPU Minimum With Higher Project Default",
			input: projectGrantInputWithDefaultMachineOverlayForTest(
				executionstore.CreateProjectMachinePoolGrantInput{MinMachineCPU: &cpu8},
				defaultMachineOverlayFieldsForTest{DefaultMachineCPU: &cpu8},
			),
			wantCreated: true,
		},
		{
			name:        "No Default Machine Overlay",
			input:       executionstore.CreateProjectMachinePoolGrantInput{},
			wantCreated: true,
		},
	}
	for index, tc := range cases {
		input := tc.input
		input.OrgID = testOrgID
		input.ProjectID = testProjectID
		input.MachinePoolID = machinePool.ID
		input.IdempotencyKey = fmt.Sprintf("idem-project-pool-grant-cap-fit-%d", index)
		grant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, input)
		if tc.wantCreated {
			if err != nil {
				t.Fatalf("%s create grant: %v", tc.name, err)
			}
			if !grant.Created {
				t.Fatalf("%s grant replayed unexpectedly: %+v", tc.name, grant)
			}
			if _, err := store.Execution().DeleteProjectMachinePoolGrant(
				ctx,
				testOrgID,
				testProjectID,
				grant.ID,
			); err != nil {
				t.Fatalf("%s revoke grant: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s error = %v, want %q", tc.name, err, tc.wantErr)
		}
	}

	agentGrant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			MinMachineCPU:  &cpu8,
			IdempotencyKey: "idem-agent-config-machine-minimum",
		},
		defaultMachineOverlayFieldsForTest{DefaultMachineCPU: &cpu8},
	))
	if err != nil {
		t.Fatalf("create agent config minimum grant: %v", err)
	}
	compiled := mustCompileAgentYAMLWithMachineSourceResolvers(t, ctx, store, `
instruction: Use the pool.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    machine_cpu: 4
tools:
  run_command: {}
`)
	err = store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(compiled.CanonicalJSON),
		agentconfig.CompilerVersion,
		compiled.Hash,
	)
	if err == nil || !strings.Contains(err.Error(), "cpu is below min_machine_cpu") {
		t.Fatalf("agent config minimum error = %v, want cpu minimum error", err)
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		agentGrant.ID,
	); err != nil {
		t.Fatalf("revoke agent config minimum grant: %v", err)
	}
}

func TestCreateProjectMachinePoolGrantAllowsDefaultPool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	machinePool := createDefaultMachinePoolForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:            testOrgID,
			Provider:         "test",
			MaxTotalMachines: 1,
		},
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
	))
	grant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-default-pool-grant",
		},
		defaultMachineOverlayFieldsForTest{
			DefaultMachineProviderOptionsOverlay: json.RawMessage(`{"startup_script":"echo ready"}`),
		},
	))

	if err != nil {
		t.Fatalf("create default project machine pool grant: %v", err)
	}
	if !grant.Created || grant.MachinePoolID != machinePool.ID {
		t.Fatalf("unexpected default project machine pool grant: %+v", grant)
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		grant.ID,
	); err != nil {
		t.Fatalf("revoke default project machine pool grant: %v", err)
	}
	for _, tt := range []struct {
		name                   string
		providerOptionsOverlay json.RawMessage
	}{
		{name: "image", providerOptionsOverlay: json.RawMessage(`{"image":"attacker/img"}`)},
		{name: "metro", providerOptionsOverlay: json.RawMessage(`{"metro":"iad"}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			grant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
				executionstore.CreateProjectMachinePoolGrantInput{
					OrgID:          testOrgID,
					ProjectID:      testProjectID,
					MachinePoolID:  machinePool.ID,
					IdempotencyKey: "idem-default-pool-grant-" + tt.name,
				},
				defaultMachineOverlayFieldsForTest{
					DefaultMachineProviderOptionsOverlay: tt.providerOptionsOverlay,
				},
			))

			if err != nil {
				t.Fatalf("create default project machine pool grant with %s override: %v", tt.name, err)
			}
			if _, err := store.Execution().DeleteProjectMachinePoolGrant(
				ctx,
				testOrgID,
				testProjectID,
				grant.ID,
			); err != nil {
				t.Fatalf("revoke default project machine pool grant with %s override: %v", tt.name, err)
			}
		})
	}
}

func TestProjectMachinePoolGrantDoesNotMaterializeMachineGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{OrgID: testOrgID, Name: "Pool", Provider: "test", MaxTotalMachines: 2},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-pool-grants",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	if poolGrant.ID == NilID || !poolGrant.Created {
		t.Fatalf("pool grant should be created with server id: %+v", poolGrant)
	}
	grantPage, err := store.Execution().ListProjectMachineGrants(
		ctx,
		executionstore.ListProjectMachineGrantsInput{OrgID: testOrgID, ProjectID: testProjectID, Limit: 10},
	)
	if err != nil {
		t.Fatalf("list project machine grants: %v", err)
	}
	grants := grantPage.Grants
	if len(grants) != 0 {
		t.Fatalf("pool grant should not broadly materialize machine grants: %+v", grants)
	}
	replayed, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-pool-grants",
		})

	if err != nil {
		t.Fatalf("replay pool grant: %v", err)
	}
	if replayed.ID != poolGrant.ID || replayed.Created {
		t.Fatalf("pool grant replay = %+v, want existing record", replayed)
	}
	if _, err := store.Execution().DeleteMachinePool(ctx, testOrgID, machinePool.ID); err != nil {
		t.Fatalf("archive machine pool: %v", err)
	}
	// The pool grant was hard-deleted with the pool, freeing its idempotency
	// key; recreating against the deleted pool fails on the pool itself.
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-pool-grants",
		}); !storeerr.IsNotFound(err) {
		t.Fatalf("recreate pool grant after pool delete error = %v, want not found", err)
	}
}

func TestProjectMachinePoolGrantKeyFreedAfterDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Name:             "Replay Revoked Pool",
				Provider:         "test",
				MaxTotalMachines: 2,
			},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	createInput := executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachinePoolID:  machinePool.ID,
		IdempotencyKey: "idem-pmpg-replay-revoked",
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, createInput)
	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	if !poolGrant.Created {
		t.Fatalf("pool grant created = false")
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		poolGrant.ID,
	); err != nil {
		t.Fatalf("revoke pool grant: %v", err)
	}
	recreated, err := store.Execution().CreateProjectMachinePoolGrant(ctx, createInput)
	if err != nil {
		t.Fatalf("recreate pool grant after delete: %v", err)
	}
	if !recreated.Created || recreated.ID == poolGrant.ID {
		t.Fatalf("hard-deleted grant should free its idempotency key, got %+v (original %s)", recreated, poolGrant.ID)
	}
}

func TestDeleteMachinePoolRevokesGrantsAndMarksMachinesDeleting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 18, 15, 15, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "pool-archive-tester@example.com",
			DisplayName: "Pool Archive Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{OrgID: testOrgID, Name: "Archive Pool", Provider: "test", MaxTotalMachines: 2},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-pool-archive",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	agent := createLaunchTestAgent(t, ctx, store, "idem-pool-archive-agent", `
instruction: Use a pool machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
tools:
  run_command: {}
`)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-pool-archive-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch pool agent: %v", err)
	}
	generated := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, launch.MachineBindings[0].MachineID)
	agentID := launch.Agent.ID
	machineID := launch.MachineBindings[0].MachineID
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(ctx, testOrgID, machineID)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        machineID,
			ProvisionAttempt: claimed.ProvisionAttempts,
			TokenName:        "test bootstrap",
		},
	)
	if err != nil {
		t.Fatalf("create system token: %v", err)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		machineID,
		claimed.ProvisionAttempts,
		"archive-pool-resource",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		machineID,
		"archive-pool-resource",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete pool machine provisioning: %v", err)
	}
	if _, err := store.Execution().BootstrapMachineDaemon(
		ctx,
		executionstore.MachineDaemonBootstrapInput{
			OrgID:         testOrgID,
			MachineID:     machineID,
			DaemonTokenID: providerProvisioning.DaemonToken.Record.ID,
		},
	); err != nil {
		t.Fatalf("bootstrap pool daemon: %v", err)
	}
	runtime, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machineID,
			DaemonTokenID:    providerProvisioning.DaemonToken.Record.ID,
			DaemonInstanceID: testID("daemon-pool-archive"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}
	binding := getAgentMachineBindingForTest(t, ctx, store, testProjectID, agentID, launch.MachineBindings[0].ID)
	if binding.State != "attached" {
		t.Fatalf("binding after runtime registration = %+v, want attached", binding)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agentID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	fixture := processDaemonFixture{
		Store:     store,
		OrgID:     testOrgID,
		AgentID:   agentID,
		MachineID: machineID,
		BindingID: binding.ID,
		TokenID:   providerProvisioning.DaemonToken.Record.ID,
		RuntimeID: runtime.ID,
		DaemonID:  testID("daemon-pool-archive"),
		UserID:    user.ID,
		Lock:      lock,
		GrantID:   generated.ID,
		Now:       now,
	}
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "pool_archive_process", "run_command")
	process, err := startProcessForTest(
		ctx, store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       agentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: lock.ID,
		}, executionstore.CreateProcessInput{
			AgentMachineBindingID: binding.ID,
			Command:               "echo archive",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("start process before archive: %v", err)
	}
	orphanCPU := int32(1)
	orphanMemoryMB := int32(1024)
	orphanCwd := ""
	orphanEnv := json.RawMessage(`{}`)
	orphanSecretEnv := json.RawMessage(`{}`)
	orphanProviderOptions := json.RawMessage(`{}`)
	orphan, err := store.q.InsertMachine(ctx, dbsqlc.InsertMachineParams{
		OrgID:                  testOrgID,
		MachinePoolID:          &machinePool.ID,
		SourceKind:             "pool",
		DisplayName:            "orphan pool machine",
		Provider:               "test",
		LifecycleState:         "provisioning",
		Cpu:                    &orphanCPU,
		MemoryMb:               &orphanMemoryMB,
		Cwd:                    orphanCwd,
		Env:                    orphanEnv,
		SecretEnv:              orphanSecretEnv,
		ProviderOptions:        &orphanProviderOptions,
		LifecycleReasonMessage: "",
		ProvisionAttempts:      1,
		Metadata:               json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("insert orphan pool machine: %v", err)
	}
	beginAndRecordPoolMachineProvisioningForTest(
		t,
		ctx,
		store,
		orphan.ID,
		1,
		"orphan-pool-resource",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		orphan.ID,
		"orphan-pool-resource",
		"",
		1,
	); err != nil {
		t.Fatalf("complete orphan pool machine provisioning: %v", err)
	}
	seedProviderRuntimeMismatchForTest(t, ctx, pool, machineID, orphan.ID)
	archivedMachines, err := store.Execution().DeleteMachinePool(ctx, testOrgID, machinePool.ID)
	if err != nil {
		t.Fatalf("archive machine pool: %v", err)
	}
	markedMachineIDs := make(map[ID]bool, len(archivedMachines))
	for _, machine := range archivedMachines {
		markedMachineIDs[machine.ID] = true
	}
	if len(markedMachineIDs) != 2 || !markedMachineIDs[launch.MachineBindings[0].MachineID] || !markedMachineIDs[orphan.ID] {
		t.Fatalf("archived machines = %+v", archivedMachines)
	}
	assertProviderRuntimeMismatchClearedForTest(t, ctx, pool, machineID, orphan.ID)
	if _, err := store.Execution().GetMachinePool(ctx, testOrgID, machinePool.ID); !storeerr.IsNotFound(err) {
		t.Fatalf("get archived machine pool error = %v, want not found", err)
	}
	pools, err := store.Execution().ListMachinePools(ctx, executionstore.ListMachinePoolsInput{OrgID: testOrgID, Limit: 10})
	if err != nil {
		t.Fatalf("list machine pools after archive: %v", err)
	}
	for _, pool := range pools.Pools {
		if pool.ID == machinePool.ID {
			t.Fatalf("archived pool still listed: %+v", pools)
		}
	}
	if _, err := store.Execution().GetProjectMachinePoolGrant(ctx, testOrgID, testProjectID, poolGrant.ID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted pool grant lookup error = %v, want not found", err)
	}
	if _, err := store.Execution().GetProjectMachineGrant(ctx, testOrgID, testProjectID, generated.ID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted generated grant lookup error = %v, want not found", err)
	}
	current, err := store.Execution().GetProcess(ctx, testProjectID, agentID, process.ID)
	if err != nil {
		t.Fatalf("get archived pool process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed || current.StateReasonCode != "project_machine_grant_revoked" ||
		current.StateChangedAt.IsZero() {
		t.Fatalf("archived pool process = %+v, want failed/project_machine_grant_revoked", current)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, testProjectID, agentID, toolCallID)
	if err != nil {
		t.Fatalf("get archived pool tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, store, agentID, toolCall, "project_machine_grant_revoked")
	machine, err := store.Execution().GetMachine(ctx, testOrgID, launch.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get pool machine after pool archive: %v", err)
	}
	if machine.LifecycleState != "deleting" || machine.LifecycleReasonCode != "machine_pool_deleted" {
		t.Fatalf("machine after pool archive = %+v", machine)
	}
	orphanAfterArchive, err := store.Execution().GetMachine(ctx, testOrgID, orphan.ID)
	if err != nil {
		t.Fatalf("get orphan pool machine after pool archive: %v", err)
	}
	if orphanAfterArchive.LifecycleState != "deleting" ||
		orphanAfterArchive.LifecycleReasonCode != "machine_pool_deleted" {
		t.Fatalf("orphan pool machine after pool archive = %+v", orphanAfterArchive)
	}
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-pool-archive-after-archive",
		}); !storeerr.IsNotFound(err) {
		t.Fatalf("create pool grant after pool archive error = %v, want not found", err)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list pool machines for cleanup after pool archive: %v", err)
	}
	cleanupMachineIDs := make(map[ID]bool, len(cleanup))
	for _, candidate := range cleanup {
		if candidate.ReasonCode != "deleting_retry" {
			t.Fatalf("cleanup after pool archive = %+v", cleanup)
		}
		cleanupMachineIDs[candidate.Machine.ID] = true
	}
	if len(cleanupMachineIDs) != 2 || !cleanupMachineIDs[machine.ID] || !cleanupMachineIDs[orphan.ID] {
		t.Fatalf("cleanup after pool archive = %+v", cleanup)
	}
}

func TestRevokeProjectMachinePoolGrantWaitsForMachinePoolLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Name:             "Revoke Lock Pool",
				Provider:         "test",
				MaxTotalMachines: 1,
			},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-revoke-lock",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin pool lock tx: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM machine_pools WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		machinePool.ID,
	); err != nil {
		t.Fatalf("lock machine pool row: %v", err)
	}
	revokeDone := make(chan error, 1)
	go func() {
		_, revokeErr := store.Execution().DeleteProjectMachinePoolGrant(
			ctx,
			testOrgID,
			testProjectID,
			poolGrant.ID,
		)
		revokeDone <- revokeErr
	}()
	integrationdb.WaitForLockWaiters(t, ctx, pool, "FROM machine_pools", 1)
	select {
	case revokeErr := <-revokeDone:
		t.Fatalf("revoke completed before waiting on machine pool row lock: %v", revokeErr)
	default:
	}
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("release machine pool row lock: %v", err)
	}
	select {
	case revokeErr := <-revokeDone:
		if revokeErr != nil {
			t.Fatalf("revoke after machine pool lock release: %v", revokeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pool grant revoke after lock release")
	}
}

func TestProjectMachinePoolGrantRevocationRevokesGeneratedGrantsAndRequestsMachineDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "pool-revoke-tester@example.com", DisplayName: "Pool Revoke Tester"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{OrgID: testOrgID, Name: "Pool", Provider: "test", MaxTotalMachines: 1},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-pool-revoke",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	agent := createLaunchTestAgent(t, ctx, store, "idem-pool-revoke-agent", `
instruction: Use a agent pool machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
tools:
  run_command: {}
`)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-pool-revoke-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch pool agent: %v", err)
	}
	generated := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, launch.MachineBindings[0].MachineID)
	if generated.SourceKind != "pool" || generated.ProjectMachinePoolGrantID != poolGrant.ID {
		t.Fatalf("unexpected generated grant: %+v", generated)
	}
	if _, err := store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		generated.ID,
	); !errors.Is(
		err,
		storeerr.ErrStateTransitionConflict,
	) {
		t.Fatalf("direct revoke of generated pool grant error = %v, want ErrStateTransitionConflict", err)
	}
	seedProviderRuntimeMismatchForTest(t, ctx, pool, launch.MachineBindings[0].MachineID)
	revokedResult, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		poolGrant.ID,
	)
	if err != nil {
		t.Fatalf("revoke pool grant: %v", err)
	}
	if len(revokedResult.Machines) != 1 || revokedResult.Machines[0].ID != launch.MachineBindings[0].MachineID {
		t.Fatalf("revoked result machines = %+v", revokedResult.Machines)
	}
	if _, err := store.Execution().GetProjectMachineGrant(ctx, testOrgID, testProjectID, generated.ID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted generated grant lookup error = %v, want not found", err)
	}
	machine, err := store.Execution().GetMachine(ctx, testOrgID, launch.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get pool machine after pool revoke: %v", err)
	}
	if machine.LifecycleState != "deleting" || machine.LifecycleReasonCode != "pool_grant_revoked" {
		t.Fatalf("machine after pool grant revoke = %+v", machine)
	}
	assertProviderRuntimeMismatchClearedForTest(t, ctx, pool, machine.ID)
	if _, claimed, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		machine.ID,
	); err != nil ||
		claimed {
		t.Fatalf("claim revoked pool machine for provisioning claimed=%v err=%v, want false/nil", claimed, err)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list pool machines for cleanup after pool revoke: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != machine.ID || cleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("cleanup after pool grant revoke = %+v", cleanup)
	}
	staleCleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list stale pool machines for cleanup after pool revoke: %v", err)
	}
	if len(staleCleanup) != 1 || staleCleanup[0].Machine.ID != machine.ID ||
		staleCleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("stale cleanup after pool grant revoke = %+v", staleCleanup)
	}
	replayed, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-pool-revoke-launch",
		},
	)
	if err != nil {
		t.Fatalf("replay pool launch after pool grant revoke: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, launch.Agent)
	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-pool-revoke-launch-after-revoke",
		},
	); !storeerr.IsNotFound(
		err,
	) {
		t.Fatalf("expected revoked pool grant to be unavailable for future launch, got %v", err)
	}
}

func TestProjectMachinePoolGrantRevocationFencesInFlightProvisioning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "pool-revoke-provisioning@example.com",
			DisplayName: "Pool Revoke Provisioning Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Name:             "Provisioning Pool",
				Provider:         "test",
				MaxTotalMachines: 1,
			},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-provisioning-revoke",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	agent := createLaunchTestAgent(t, ctx, store, "idem-pool-revoke-provisioning-agent", `
instruction: Use a agent pool machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
tools:
  run_command: {}
`)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-pool-revoke-provisioning-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch pool agent: %v", err)
	}
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		launch.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	if claimed.LifecycleState != "provisioning" {
		t.Fatalf("claimed machine state = %s, want provisioning", claimed.LifecycleState)
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		poolGrant.ID,
	); err != nil {
		t.Fatalf("revoke pool grant: %v", err)
	}
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		claimed.ID,
		"late-provider-resource",
		"",
		claimed.ProvisionAttempts,
	); err == nil {
		t.Fatal("expected provisioning completion after pool grant revoke to fail")
	}
	current, err := store.Execution().GetMachine(ctx, testOrgID, claimed.ID)
	if err != nil {
		t.Fatalf("get revoked provisioning machine: %v", err)
	}
	if current.LifecycleState != "deleting" || current.LifecycleReasonCode != "pool_grant_revoked" ||
		current.ProviderResourceID != "" {
		t.Fatalf("machine after revoked provisioning completion = %+v", current)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup after provisioning revoke: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != claimed.ID || cleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("cleanup after provisioning revoke = %+v", cleanup)
	}
	staleCleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list stale cleanup after provisioning revoke: %v", err)
	}
	if len(staleCleanup) != 1 || staleCleanup[0].Machine.ID != claimed.ID ||
		staleCleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("stale cleanup after provisioning revoke = %+v", staleCleanup)
	}
}

func TestProjectMachinePoolGrantRevocationOverwritesProviderNotConfiguredReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "pool-revoke-provider-not-configured@example.com",
			DisplayName: "Pool Revoke Provider Config Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Name:             "Provider Not Configured Pool",
				Provider:         "test",
				MaxTotalMachines: 1,
			},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-provider-not-configured-revoke",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	agent := createLaunchTestAgent(t, ctx, store, "idem-pool-revoke-provider-not-configured-agent", `
instruction: Use a pool machine with missing provider config.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
tools:
  run_command: {}
`)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-pool-revoke-provider-not-configured-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch pool agent: %v", err)
	}
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		launch.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	if err := store.Execution().MarkPoolMachineProvisionFailed(
		ctx,
		executionstore.PoolMachineProvisionFailureInput{
			OrgID:                  testOrgID,
			MachineID:              claimed.ID,
			ProvisionAttempt:       claimed.ProvisionAttempts,
			LifecycleReasonCode:    "provider_not_configured",
			LifecycleReasonMessage: "machine provider is not configured",
			RetryDelay:             10 * time.Second,
		},
	); err != nil {
		t.Fatalf("mark pool machine provision failed: %v", err)
	}
	failed, err := store.Execution().GetMachine(ctx, testOrgID, claimed.ID)
	if err != nil {
		t.Fatalf("get failed pool machine: %v", err)
	}
	if failed.LifecycleState != "provision_failed" || failed.LifecycleReasonCode != "provider_not_configured" ||
		failed.ProvisionAttempts == 0 {
		t.Fatalf("failed provider-not-configured machine = %+v", failed)
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		poolGrant.ID,
	); err != nil {
		t.Fatalf("revoke pool grant: %v", err)
	}
	current, err := store.Execution().GetMachine(ctx, testOrgID, claimed.ID)
	if err != nil {
		t.Fatalf("get revoked provider-not-configured machine: %v", err)
	}
	if current.LifecycleState != "deleting" || current.LifecycleReasonCode != "pool_grant_revoked" ||
		current.ProviderResourceID != "" {
		t.Fatalf("machine after provider-not-configured pool revoke = %+v", current)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup after provider-not-configured revoke: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != claimed.ID || cleanup[0].ReasonCode != "deleting_retry" ||
		cleanup[0].Machine.LifecycleReasonCode != "pool_grant_revoked" {
		t.Fatalf("cleanup after provider-not-configured revoke = %+v", cleanup)
	}
	staleCleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list stale cleanup after provider-not-configured revoke: %v", err)
	}
	if len(staleCleanup) != 1 || staleCleanup[0].Machine.ID != claimed.ID ||
		staleCleanup[0].ReasonCode != "deleting_retry" ||
		staleCleanup[0].Machine.LifecycleReasonCode != "pool_grant_revoked" {
		t.Fatalf("stale cleanup after provider-not-configured revoke = %+v", staleCleanup)
	}
}

func TestProjectMachinePoolGrantRevocationStopsActivePoolMachineExecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 18, 16, 0, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "pool-revoke-active@example.com",
			DisplayName: "Pool Revoke Active Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             "Active Pool",
					Provider:         "test",
					DefaultCwd:       "/pool",
					MaxTotalMachines: 1,
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineMemoryMB:        1024,
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"active"}`),
				},
			),
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-pmpg-active-revoke",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	agent := createLaunchTestAgent(t, ctx, store, "idem-pool-revoke-active-agent", `
instruction: Use an active pool machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
tools:
  run_command: {}
`)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-pool-revoke-active-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch pool agent: %v", err)
	}
	agentID := launch.Agent.ID
	machineID := launch.MachineBindings[0].MachineID
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(ctx, testOrgID, machineID)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        machineID,
			ProvisionAttempt: claimed.ProvisionAttempts,
			TokenName:        "test bootstrap",
		},
	)
	if err != nil {
		t.Fatalf("create system token: %v", err)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		machineID,
		claimed.ProvisionAttempts,
		"active-pool-resource",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		machineID,
		"active-pool-resource",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete pool machine provisioning: %v", err)
	}
	if _, err := store.Execution().BootstrapMachineDaemon(
		ctx,
		executionstore.MachineDaemonBootstrapInput{
			OrgID:         testOrgID,
			MachineID:     machineID,
			DaemonTokenID: providerProvisioning.DaemonToken.Record.ID,
		},
	); err != nil {
		t.Fatalf("bootstrap pool daemon: %v", err)
	}
	runtime, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machineID,
			DaemonTokenID:    providerProvisioning.DaemonToken.Record.ID,
			DaemonInstanceID: testID("daemon-pool-revoke-active"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}
	authority := executionstore.DaemonRuntimeAuthority{
		OrgID:           testOrgID,
		MachineID:       machineID,
		DaemonRuntimeID: runtime.ID,
		DaemonTokenID:   providerProvisioning.DaemonToken.Record.ID,
	}
	binding := getAgentMachineBindingForTest(t, ctx, store, testProjectID, agentID, launch.MachineBindings[0].ID)
	if binding.State != "attached" {
		t.Fatalf("binding after runtime registration = %+v, want attached", binding)
	}
	generated := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, launch.MachineBindings[0].MachineID)
	executable, err := store.Execution().ListExecutableAgentMachineBindings(ctx, testProjectID, agentID)
	if err != nil {
		t.Fatalf("list executable bindings before revoke: %v", err)
	}
	if len(executable) != 1 || executable[0].ID != binding.ID || executable[0].Cwd != "/pool" {
		t.Fatalf("executable bindings before revoke = %+v", executable)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agentID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	fixture := processDaemonFixture{
		Store:     store,
		OrgID:     testOrgID,
		AgentID:   agentID,
		MachineID: machineID,
		BindingID: binding.ID,
		TokenID:   providerProvisioning.DaemonToken.Record.ID,
		RuntimeID: runtime.ID,
		DaemonID:  testID("daemon-pool-revoke-active"),
		UserID:    user.ID,
		Lock:      lock,
		GrantID:   generated.ID,
		Now:       now,
	}
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "pool_revoke_active_process", "run_command")
	process, err := startProcessForTest(
		ctx, store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       agentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: lock.ID,
		}, executionstore.CreateProcessInput{
			AgentMachineBindingID: binding.ID,
			Command:               "echo active",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("start process before revoke: %v", err)
	}
	offers, err := store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: authority,
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list daemon process offers before revoke: %v", err)
	}
	if len(offers) != 1 || offers[0].Process.ID != process.ID {
		t.Fatalf("daemon process offers before revoke = %+v", offers)
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		poolGrant.ID,
	); err != nil {
		t.Fatalf("revoke pool grant: %v", err)
	}
	current, err := store.Execution().GetProcess(ctx, testProjectID, agentID, process.ID)
	if err != nil {
		t.Fatalf("get pool revoked process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed || current.StateReasonCode != "project_machine_grant_revoked" ||
		current.StateChangedAt.IsZero() {
		t.Fatalf("pool revoked process = %+v, want failed/project_machine_grant_revoked", current)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, testProjectID, agentID, toolCallID)
	if err != nil {
		t.Fatalf("get pool revoked tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, store, agentID, toolCall, "project_machine_grant_revoked")
	machine, err := store.Execution().GetMachine(ctx, testOrgID, machineID)
	if err != nil {
		t.Fatalf("get revoked active pool machine: %v", err)
	}
	if machine.LifecycleState != "deleting" || machine.ConnectionState != "offline" {
		t.Fatalf("machine after active pool revoke = %+v", machine)
	}
	executable, err = store.Execution().ListExecutableAgentMachineBindings(ctx, testProjectID, agentID)
	if err != nil {
		t.Fatalf("list executable bindings after revoke: %v", err)
	}
	if len(executable) != 0 {
		t.Fatalf("executable bindings after revoke = %+v, want none", executable)
	}
	offers, err = store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: authority,
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list daemon process offers after revoke: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("daemon process offers after revoke = %+v, want none", offers)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		store,
		testOrgID,
		machineID,
		runtime.ID,
		process.ID); err != nil ||
		found {
		t.Fatalf("accept daemon process after revoke found=%v err=%v, want false/nil", found, err)
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	ended, err := store.Execution().EndExpiredDaemonRuntimes(ctx, 10)
	if err != nil {
		t.Fatalf("end expired daemon runtime after pool revoke: %v", err)
	}
	if len(ended) != 1 || ended[0].ID != runtime.ID {
		t.Fatalf("ended expired daemon runtimes after pool revoke = %+v, want %s", ended, runtime.ID)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup after active pool revoke: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != machineID || cleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("cleanup after active pool revoke = %+v", cleanup)
	}
	staleCleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list stale cleanup after active pool revoke: %v", err)
	}
	if len(staleCleanup) != 1 || staleCleanup[0].Machine.ID != machineID ||
		staleCleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("stale cleanup after active pool revoke = %+v", staleCleanup)
	}
}

func TestListProjectMachinePoolGrantsSearchSortAndEmbeddedPool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	betaPool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{OrgID: testOrgID, Name: "beta-workers", Provider: "test", MaxTotalMachines: 2},
		))
	if err != nil {
		t.Fatalf("create beta pool: %v", err)
	}
	alphaPool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{OrgID: testOrgID, Name: "alpha-pool", Provider: "test", MaxTotalMachines: 2},
		))
	if err != nil {
		t.Fatalf("create alpha pool: %v", err)
	}
	for i, poolID := range []ID{betaPool.ID, alphaPool.ID} {
		if _, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:         testOrgID,
			ProjectID:     testProjectID,
			MachinePoolID: poolID,
		}); err != nil {
			t.Fatalf("grant pool %d: %v", i, err)
		}
	}

	page, err := store.Execution().ListProjectMachinePoolGrants(ctx, executionstore.ListProjectMachinePoolGrantsInput{
		OrgID: testOrgID, ProjectID: testProjectID, Limit: 10,
		List: listing.Options{SortField: "name"},
	})
	if err != nil {
		t.Fatalf("list pool grants: %v", err)
	}
	if len(page.Grants) != 2 || page.HasMore {
		t.Fatalf("pool grant list = %+v, want 2 rows without more", page)
	}
	if page.Grants[0].MachinePool.Name != "alpha-pool" || page.Grants[1].MachinePool.Name != "beta-workers" {
		t.Fatalf("pool grants not sorted by pool name: %+v", page.Grants)
	}
	first := page.Grants[0]
	if first.Grant.MachinePoolID != alphaPool.ID || first.MachinePool.ID != alphaPool.ID ||
		first.MachinePool.Provider != "test" || first.MachinePool.ManagementKind != management.Tenant {
		t.Fatalf("embedded pool summary mismatch: %+v", first)
	}

	filtered, err := store.Execution().ListProjectMachinePoolGrants(ctx, executionstore.ListProjectMachinePoolGrantsInput{
		OrgID: testOrgID, ProjectID: testProjectID, Limit: 10,
		List: listing.Options{SortField: "name", NamePattern: "%beta%"},
	})
	if err != nil {
		t.Fatalf("list filtered pool grants: %v", err)
	}
	if len(filtered.Grants) != 1 || filtered.Grants[0].MachinePool.Name != "beta-workers" {
		t.Fatalf("filtered pool grants = %+v, want only beta-workers", filtered.Grants)
	}
}

func TestMachinePoolListSupportsServerSideSearchSortAndPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	machinePools := make(map[string]executionstore.MachinePoolRecord)
	for _, name := range []string{"list-beta", "list-alpha", "list-gamma"} {
		created, err := store.Execution().CreateMachinePool(
			ctx,
			completeMachinePoolCreateInputForTest(
				t,
				ctx,
				store,
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             name,
					Provider:         "test",
					MaxTotalMachines: 2,
				},
			),
		)
		if err != nil {
			t.Fatalf("create machine pool %q: %v", name, err)
		}
		machinePools[name] = created
	}
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
INSERT INTO machines(
    id, org_id, machine_pool_id, source_kind, provider, display_name, lifecycle_state,
    lifecycle_changed_at, provider_resource_id, cpu, memory_mb, provider_options,
    deleted_at, created_at, updated_at
) VALUES
    ($1, $2, $3, 'pool', 'test', 'list-alpha-active', 'active', $4, 'list-alpha-active', 3, 2048, '{}'::jsonb, NULL, $4, $4),
    ($5, $2, $3, 'pool', 'test', 'list-alpha-deleted', 'deleted', $4, 'list-alpha-deleted', 8, 8192, '{}'::jsonb, $4, $4, $4),
    ($6, $2, $7, 'pool', 'test', 'list-beta-known', 'active', $4, 'list-beta-known', 2, 2048, '{}'::jsonb, NULL, $4, $4),
    ($8, $2, $7, 'pool', 'test', 'list-beta-resolved', 'active', $4, 'list-beta-resolved', NULL, 1024, '{}'::jsonb, NULL, $4, $4)
`,
		testID("list_alpha_active"),
		testOrgID,
		machinePools["list-alpha"].ID,
		now,
		testID("list_alpha_deleted"),
		testID("list_beta_known"),
		machinePools["list-beta"].ID,
		testID("list_beta_resolved"),
	); err != nil {
		t.Fatalf("seed machine pool usage: %v", err)
	}

	first, err := store.Execution().ListMachinePools(ctx, executionstore.ListMachinePoolsInput{
		OrgID: testOrgID,
		Limit: 2,
		List:  listing.Options{SortField: "name", NamePattern: "list-%"},
	})
	if err != nil {
		t.Fatalf("list first machine pool page: %v", err)
	}
	if len(first.Pools) != 2 || !first.HasMore ||
		first.Pools[0].Name != "list-alpha" || first.Pools[1].Name != "list-beta" {
		t.Fatalf("first machine pool page = %+v, want alpha and beta with more", first)
	}
	if usage := first.Pools[0].Usage; usage.Machines != 1 || usage.CPU != 3 || usage.MemoryMB != 2048 {
		t.Fatalf("list-alpha usage = %+v, want 1 machine, 3 CPU, 2048 MB", usage)
	}
	if usage := first.Pools[1].Usage; usage.Machines != 2 || usage.CPU != 2 || usage.MemoryMB != 3072 {
		t.Fatalf("list-beta usage = %+v, want 2 machines, 2 CPU, 3072 MB", usage)
	}
	second, err := store.Execution().ListMachinePools(ctx, executionstore.ListMachinePoolsInput{
		OrgID: testOrgID,
		Limit: 2,
		List: listing.Options{
			SortField:   "name",
			NamePattern: "list-%",
			After:       first.Next,
		},
	})
	if err != nil {
		t.Fatalf("list second machine pool page: %v", err)
	}
	if len(second.Pools) != 1 || second.HasMore || second.Pools[0].Name != "list-gamma" {
		t.Fatalf("second machine pool page = %+v, want gamma without more", second)
	}
	if usage := second.Pools[0].Usage; usage.Machines != 0 || usage.CPU != 0 || usage.MemoryMB != 0 {
		t.Fatalf("list-gamma usage = %+v, want zero usage", usage)
	}

	descending, err := store.Execution().ListMachinePools(ctx, executionstore.ListMachinePoolsInput{
		OrgID: testOrgID,
		Limit: 10,
		List: listing.Options{
			SortField:   "name",
			SortDesc:    true,
			NamePattern: "list-%",
		},
	})
	if err != nil {
		t.Fatalf("list machine pools descending: %v", err)
	}
	if len(descending.Pools) != 3 || descending.Pools[0].Name != "list-gamma" ||
		descending.Pools[1].Name != "list-beta" || descending.Pools[2].Name != "list-alpha" {
		t.Fatalf("descending machine pools = %+v, want gamma, beta, alpha", descending.Pools)
	}

	filtered, err := store.Execution().ListMachinePools(ctx, executionstore.ListMachinePoolsInput{
		OrgID: testOrgID,
		Limit: 10,
		List: listing.Options{
			SortField:   "name",
			NamePattern: "%list-b%",
		},
	})
	if err != nil {
		t.Fatalf("list filtered machine pools: %v", err)
	}
	if len(filtered.Pools) != 1 || filtered.Pools[0].Name != "list-beta" {
		t.Fatalf("filtered machine pools = %+v, want only list-beta", filtered.Pools)
	}
}

func TestUpdateProjectMachinePoolGrantAppliesPatchSemantics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	providerAuthSecretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"test-token",
	)
	maxCPU, maxMemoryMB := 16, 32768
	machinePool, err := store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:                testOrgID,
			Name:                 "Grant Update Pool",
			Provider:             "test.provider",
			ProviderAuthSecretID: providerAuthSecretID,
			MaxTotalMachines:     5,
			MaxTotalCPU:          intPtrForMachinePoolTest(maxCPU),
			MaxTotalMemoryMB:     intPtrForMachinePoolTest(maxMemoryMB),
			MaxMachineCPU:        intPtrForMachinePoolTest(maxCPU),
			MaxMachineMemoryMB:   intPtrForMachinePoolTest(maxMemoryMB),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             4,
			DefaultMachineMemoryMB:        8192,
			DefaultMachineEnv:             json.RawMessage(`{}`),
			DefaultMachineProviderOptions: json.RawMessage(`{}`),
		},
	))
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	grant, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:                    testOrgID,
		ProjectID:                testProjectID,
		MachinePoolID:            machinePool.ID,
		Description:              "before",
		DefaultMachineCPU:        intPtrForMachinePoolTest(2),
		DefaultMachineEnvOverlay: json.RawMessage(`{"KEEP":"yes"}`),
		DefaultCwd:               "/before",
		MaxTotalCPU:              intPtrForMachinePoolTest(8),
		MinMachineCPU:            intPtrForMachinePoolTest(0),
		MaxMachineCPU:            intPtrForMachinePoolTest(4),
		DeleteAfterIdleMinutes:   intPtrForMachinePoolTest(15),
	})
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}

	description := "after"
	memory4096 := 4096
	deleteAfterIdleMinutes := 30
	envOverlay := json.RawMessage(`{"NEW":"value"}`)
	updated, err := store.Execution().UpdateProjectMachinePoolGrant(ctx, executionstore.UpdateProjectMachinePoolGrantInput{
		OrgID:                    testOrgID,
		ProjectID:                testProjectID,
		ID:                       grant.ID,
		Description:              &description,
		DefaultMachineMemoryMB:   patch.NullableInt{Set: true, Value: &memory4096},
		DefaultMachineEnvOverlay: &envOverlay,
		MaxTotalCPU:              patch.NullableInt{Set: true},
		DeleteAfterIdleMinutes:   patch.NullableInt{Set: true, Value: &deleteAfterIdleMinutes},
	})
	if err != nil {
		t.Fatalf("update grant: %v", err)
	}
	if updated.Description != "after" ||
		updated.DefaultMachineCPU == nil || *updated.DefaultMachineCPU != 2 ||
		updated.DefaultMachineMemoryMB == nil || *updated.DefaultMachineMemoryMB != 4096 ||
		!sameJSON(updated.DefaultMachineEnvOverlay, envOverlay) ||
		updated.DefaultCwd != "/before" ||
		updated.MaxTotalCPU != nil ||
		updated.MinMachineCPU == nil || *updated.MinMachineCPU != 0 ||
		updated.MaxMachineCPU == nil || *updated.MaxMachineCPU != 4 ||
		updated.DeleteAfterIdleMinutes == nil || *updated.DeleteAfterIdleMinutes != 30 ||
		updated.MachinePoolID != machinePool.ID {
		t.Fatalf("updated grant patch mismatch: %+v", updated)
	}
	if !updated.CreatedAt.Equal(grant.CreatedAt) || updated.UpdatedAt.Before(grant.UpdatedAt) {
		t.Fatalf("updated grant timestamps mismatch: %+v", updated)
	}
	fetched, err := store.Execution().GetProjectMachinePoolGrant(ctx, testOrgID, testProjectID, grant.ID)
	if err != nil {
		t.Fatalf("get updated grant: %v", err)
	}
	if fetched.Description != "after" || fetched.MaxTotalCPU != nil ||
		fetched.DeleteAfterIdleMinutes == nil || *fetched.DeleteAfterIdleMinutes != 30 ||
		fetched.MinMachineCPU == nil || *fetched.MinMachineCPU != 0 {
		t.Fatalf("fetched grant did not persist patch: %+v", fetched)
	}
	cleared, err := store.Execution().UpdateProjectMachinePoolGrant(ctx, executionstore.UpdateProjectMachinePoolGrantInput{
		OrgID:                  testOrgID,
		ProjectID:              testProjectID,
		ID:                     grant.ID,
		MinMachineCPU:          patch.NullableInt{Set: true},
		DeleteAfterIdleMinutes: patch.NullableInt{Set: true},
	})
	if err != nil {
		t.Fatalf("clear grant minimum: %v", err)
	}
	if cleared.MinMachineCPU != nil || cleared.DeleteAfterIdleMinutes != nil {
		t.Fatalf("cleared grant overrides = %+v, want nil minimum and idle deletion policy", cleared)
	}

	two := 2
	if _, err := store.Execution().UpdateProjectMachinePoolGrant(ctx, executionstore.UpdateProjectMachinePoolGrantInput{
		OrgID:         testOrgID,
		ProjectID:     testProjectID,
		ID:            grant.ID,
		MaxMachineCPU: patch.NullableInt{Set: true, Value: &two},
	}); err != nil {
		t.Fatalf("lower per-machine cap to grant default: %v", err)
	}
	if _, err := store.Execution().UpdateProjectMachinePoolGrant(ctx, executionstore.UpdateProjectMachinePoolGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ID:                grant.ID,
		DefaultMachineCPU: patch.NullableInt{Set: true},
	}); err == nil || !strings.Contains(err.Error(), "cpu exceeds max_machine_cpu") {
		t.Fatalf("clearing default cpu should fail against merged per-machine cap, got %v", err)
	}

	tooLarge := maxCPU * 2
	if _, err := store.Execution().UpdateProjectMachinePoolGrant(ctx, executionstore.UpdateProjectMachinePoolGrantInput{
		OrgID:         testOrgID,
		ProjectID:     testProjectID,
		ID:            grant.ID,
		MaxMachineCPU: patch.NullableInt{Set: true, Value: &tooLarge},
	}); err == nil || !strings.Contains(err.Error(), "cannot exceed machine pool max_machine_cpu") {
		t.Fatalf("raising per-machine cap above pool cap should fail, got %v", err)
	}

	if _, err := store.Execution().UpdateProjectMachinePoolGrant(ctx, executionstore.UpdateProjectMachinePoolGrantInput{
		OrgID:       testOrgID,
		ProjectID:   testProjectID,
		ID:          uuid.New(),
		Description: &description,
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("update missing grant error = %v, want storeerr.ErrNotFound", err)
	}
}
