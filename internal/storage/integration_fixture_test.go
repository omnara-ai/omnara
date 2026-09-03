//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

var (
	testOrgID                                  = testID("org_test")
	testProjectID                              = testID("project_test")
	testWorkerProcessID                        = testID("worker_process")
	testDefaultProviderAdminUserID             = testID("default_provider_admin_user")
	testDefaultProviderCredentialSecretID      = testID("default_provider_credential_secret")
	testDefaultProviderCredentialSecretVersion = testID("default_provider_credential_secret_version")
)

func testQueries(store *Store) *dbsqlc.Queries {
	return dbsqlc.New(store.pool)
}

const (
	testAgentRuntimeLockLeaseDuration = time.Minute
	testDaemonRuntimeLeaseTimeout     = time.Hour
)

func testID(seed string) ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-storage-integration:"+seed))
}

func isNilID(id ID) bool {
	return id == NilID
}

func testClaimNextAgentWorkInput() executionstore.ClaimNextAgentWorkInput {
	return executionstore.ClaimNextAgentWorkInput{
		WorkerProcessID: testWorkerProcessID,
		LeaseDuration:   testAgentRuntimeLockLeaseDuration,
	}
}

func executeToolCallCommandForTest[T any](
	ctx context.Context,
	store *Store,
	input executionstore.ExecuteToolCallInput,
	command executionstore.ToolCallCommand,
) (T, error) {
	execution, err := store.Execution().ExecuteToolCall(
		ctx,
		input,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return command, nil
		},
	)
	if err != nil {
		var zero T
		return zero, err
	}
	result, ok := execution.CommandResult.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("tool call command returned %T", execution.CommandResult)
	}
	return result, nil
}

func startAsyncToolCallForTest(
	ctx context.Context,
	store *Store,
	input executionstore.ExecuteToolCallInput,
) (executionstore.ExecuteToolCallResult, error) {
	return store.Execution().ExecuteToolCall(
		ctx,
		input,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartToolCallAsync(), nil
		},
	)
}

func expireAgentRuntimeLockForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	runtimeLockID ID,
) {
	t.Helper()
	if _, err := store.pool.Exec(
		ctx,
		`UPDATE agent_runtime_locks
SET started_at = statement_timestamp() - interval '3 minutes',
    renewed_at = statement_timestamp() - interval '2 minutes',
    lease_expires_at = statement_timestamp() - interval '1 minute'
WHERE id = $1`,
		runtimeLockID,
	); err != nil {
		t.Fatalf("expire agent runtime lock: %v", err)
	}
}

func openIntegrationDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
}

func machineProvisioningFromRecordForTest(t *testing.T, machine executionstore.MachineRecord) executionstore.MachineProvisioningConfig {
	t.Helper()
	machineProvisioning, err := executionstore.MachineProvisioningFromRecord(machine)
	if err != nil {
		t.Fatalf("build machine provisioning: %v", err)
	}
	return machineProvisioning
}

func machineEnvironmentFromRecordForTest(
	t *testing.T,
	machine executionstore.MachineRecord,
) executionstore.MachineEnvironment {
	t.Helper()
	environment, err := executionstore.MachineEnvironmentFromColumns(machine.Env, machine.SecretEnv)
	if err != nil {
		t.Fatalf("build machine environment: %v", err)
	}
	return environment
}

func recordPoolMachineProvisioningResourceForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	machineID ID,
	provisionAttempt int32,
	providerResourceID string,
) {
	t.Helper()
	if _, err := store.Execution().RecordPoolMachineProvisioningResource(
		ctx,
		executionstore.RecordPoolMachineProvisioningResourceInput{
			OrgID:              testOrgID,
			MachineID:          machineID,
			ProviderResourceID: providerResourceID,
			ProvisionAttempt:   provisionAttempt,
		},
	); err != nil {
		t.Fatalf("record pool machine provider resource: %v", err)
	}
}

func beginAndRecordPoolMachineProvisioningForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	machineID ID,
	provisionAttempt int32,
	providerResourceID string,
) {
	t.Helper()
	if _, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        machineID,
			ProvisionAttempt: provisionAttempt,
			TokenName:        "test bootstrap",
		},
	); err != nil {
		t.Fatalf("begin pool machine provider provisioning: %v", err)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		machineID,
		provisionAttempt,
		providerResourceID,
	)
}

func seedMigratedDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	seedDefaultProject(t, ctx, newIntegrationStore(pool))
}

// getProjectMachineGrantByMachineForTest reads the newest grant row for a
// machine directly from the table.
func getProjectMachineGrantByMachineForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	orgID, projectID, machineID ID,
) executionstore.ProjectMachineGrantRecord {
	t.Helper()
	var grant executionstore.ProjectMachineGrantRecord
	var poolGrantID *ID
	if err := store.pool.QueryRow(ctx, `
SELECT id, org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id,
       description, coalesce(idempotency_key, ''), metadata, created_at, updated_at
FROM project_machine_grants
WHERE org_id = $1 AND project_id = $2 AND machine_id = $3
ORDER BY created_at DESC, id DESC
LIMIT 1
`, orgID, projectID, machineID).Scan(
		&grant.ID, &grant.OrgID, &grant.ProjectID, &grant.MachineID,
		&grant.SourceKind, &poolGrantID, &grant.Description,
		&grant.IdempotencyKey,
		&grant.Metadata, &grant.CreatedAt, &grant.UpdatedAt,
	); err != nil {
		t.Fatalf("load project machine grant for machine %s: %v", machineID, err)
	}
	if poolGrantID != nil {
		grant.ProjectMachinePoolGrantID = *poolGrantID
	}
	return grant
}

// countProjectMachineGrantsForMachineForTest counts grant rows for a machine
// directly; deletion flows hard-delete them.
func countProjectMachineGrantsForMachineForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	orgID, projectID, machineID ID,
) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(ctx, `
SELECT count(*) FROM project_machine_grants
WHERE org_id = $1 AND project_id = $2 AND machine_id = $3
`, orgID, projectID, machineID).Scan(&count); err != nil {
		t.Fatalf("count project machine grants for machine %s: %v", machineID, err)
	}
	return count
}

func seedDefaultProject(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	now := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	if _, err := store.pool.Exec(
		ctx,
		`INSERT INTO users(id, display_name, created_at, updated_at)
		 VALUES ($1, 'Default Provider Admin', $2, $2)`,
		testDefaultProviderAdminUserID,
		now,
	); err != nil {
		t.Fatalf("seed default-provider admin: %v", err)
	}
	if _, err := store.pool.Exec(
		ctx,
		`
INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
VALUES ($1, 'Test Org', 'idem-test-org', $2, $2)
`,
		testOrgID,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := store.pool.Exec(
		ctx,
		`
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Test Project', 'idem-test-project', $3, $3)
`,
		testProjectID,
		testOrgID,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed default model provider config: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO secrets(id, org_id, management_kind, owner_kind, name, kind, metadata, current_version_id, created_at, updated_at)
VALUES ($1, $2, 'tenant', 'org', 'default-provider-key', 'generic', '{}'::jsonb, $3, $4, $4)
ON CONFLICT (id) DO NOTHING`, testDefaultProviderCredentialSecretID, testOrgID,
		testDefaultProviderCredentialSecretVersion, now); err != nil {
		t.Fatalf("seed default provider secret: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO secret_versions(id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at)
VALUES ($1, $2, $3, 1, ARRAY['value'], 'aes-256-gcm-envelope-v1', 'test-key', 'local', decode(repeat('01', 48), 'hex'), decode(repeat('02', 12), 'hex'), decode(repeat('03', 12), 'hex'), decode(repeat('04', 32), 'hex'), $4)
ON CONFLICT (id) DO NOTHING`, testDefaultProviderCredentialSecretVersion, testOrgID,
		testDefaultProviderCredentialSecretID, now); err != nil {
		t.Fatalf("seed default provider secret version: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO model_provider_configs(id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path, auth_kind, credential_secret_id, created_at, updated_at)
VALUES ($1, $2, 'tenant', 'openai-prod', 'openai-responses', 'default', 'https://api.openai.com/v1', '/responses', 'bearer_token', $3, $4, $4)
ON CONFLICT (id) DO NOTHING`, testDefaultProviderConfigID(), testOrgID,
		testDefaultProviderCredentialSecretID, now); err != nil {
		t.Fatalf("seed default model provider config: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit default model provider config seed: %v", err)
	}
}

func seedAdditionalProjectForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	seed string,
) ID {
	t.Helper()
	projectID := testID("project_" + seed)
	now := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)`,
		projectID,
		testOrgID,
		"Test Project "+seed,
		"idem-test-project-"+seed,
		now,
	); err != nil {
		t.Fatalf("seed additional project %q: %v", seed, err)
	}
	return projectID
}

func testDefaultProviderConfigID() ID {
	return testID("default_provider_config")
}

func ensureTestConfiguredModel(
	t *testing.T,
	ctx context.Context,
	store *Store,
	providerConfigName, configuredModelName string,
	now time.Time,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	if providerConfigName == "" {
		providerConfigName = "openai-prod"
	}
	if configuredModelName == "" {
		configuredModelName = "gpt-test"
	}
	providerConfig, err := store.Models().GetModelProviderConfigByName(ctx, testOrgID, providerConfigName)
	if err != nil {
		t.Fatalf("load test provider config %q: %v", providerConfigName, err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  providerConfig.ID,
		Name:                   configuredModelName,
		ProviderModelSlug:      configuredModelName,
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
	})
	if err != nil {
		t.Fatalf("create test configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant test configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	return configuredModel
}

func testAgentModelSource(t *testing.T, sourceYAML string) agentconfig.AgentConfigModelSource {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse agent config source model: %v", err)
	}
	return source.Model
}

func ensureTestConfiguredModelForSource(
	t *testing.T,
	ctx context.Context,
	store *Store,
	sourceYAML string,
	now time.Time,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	modelSource := testAgentModelSource(t, sourceYAML)
	return ensureTestConfiguredModel(t, ctx, store, modelSource.ProviderConfig, modelSource.Name, now)
}

func parseConfiguredModelID(t *testing.T, compiled agentconfig.Result) ID {
	t.Helper()
	id, err := ParseID(compiled.Compiled.Model.ConfiguredModelID)
	if err != nil {
		t.Fatalf("parse compiled configured model id: %v", err)
	}
	return id
}

func configuredModelIDForRevision(t *testing.T, ctx context.Context, store *Store, revisionID ID) ID {
	t.Helper()
	revision, err := store.Models().GetConfiguredModelRevisionForUse(ctx, testOrgID, revisionID)
	if err != nil {
		t.Fatalf("load configured model revision %s: %v", revisionID, err)
	}
	return revision.ConfiguredModelID
}

func resolvedTestModelSelection(configuredModel modelstore.ConfiguredModelRecord) agentconfig.ResolvedModelSelection {
	supportsTools := configuredModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID.String(),
		SupportsTools:     &supportsTools,
	}
}

func mustCompileAgentYAMLResolved(
	t *testing.T,
	ctx context.Context,
	store *Store,
	sourceYAML string,
	now time.Time,
) agentconfig.Result {
	t.Helper()
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, sourceYAML, now)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
		ResolveMachineName: func(machineName string) (string, error) {
			machineID, err := store.Execution().ResolveAgentConfigMachineName(ctx, testProjectID, machineName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachine, machineID)
		},
		ResolveMachinePoolName: func(machinePoolName string) (string, error) {
			machinePoolID, err := store.Execution().ResolveAgentConfigMachinePoolName(ctx, testOrgID, testProjectID, machinePoolName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachinePool, machinePoolID)
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent yaml: %v", err)
	}
	return compiled
}

func assertProjectAllowed(
	t *testing.T,
	ctx context.Context,
	store *Store,
	principal identitystore.PrincipalRecord,
	action string,
	want bool,
) {
	t.Helper()
	allowed, err := store.Identity().AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
		Principal: principal,
		OrgID:     testOrgID,
		ProjectID: testProjectID,
		Action:    action,
	})
	if err != nil {
		t.Fatalf("authorize project action %s for %+v: %v", action, principal, err)
	}
	if allowed != want {
		t.Fatalf("authorize project action %s for %+v: expected %v, got %v", action, principal, want, allowed)
	}
}

func assertOrgAllowed(
	t *testing.T,
	ctx context.Context,
	store *Store,
	principal identitystore.PrincipalRecord,
	action string,
	want bool,
) {
	t.Helper()
	allowed, err := store.Identity().AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
		Principal: principal,
		OrgID:     testOrgID,
		Action:    action,
	})
	if err != nil {
		t.Fatalf("authorize org action %s for %+v: %v", action, principal, err)
	}
	if allowed != want {
		t.Fatalf("authorize org action %s for %+v: expected %v, got %v", action, principal, want, allowed)
	}
}

func assertMachineAllowed(
	t *testing.T,
	ctx context.Context,
	store *Store,
	principal identitystore.PrincipalRecord,
	machineID ID,
	action string,
	want bool,
) {
	t.Helper()
	allowed, err := store.Execution().AuthorizeMachine(ctx, executionstore.AuthorizeMachineInput{
		Principal: principal,
		OrgID:     testOrgID,
		MachineID: machineID,
		Action:    action,
	})
	if err != nil {
		t.Fatalf("authorize machine action %s for %+v: %v", action, principal, err)
	}
	if allowed != want {
		t.Fatalf("authorize machine action %s for %+v: expected %v, got %v", action, principal, want, allowed)
	}
}

func mustCreateAgent(t *testing.T, ctx context.Context, store *Store, now time.Time) ID {
	t.Helper()
	configID := mustCreateAgentConfig(t, ctx, store, testProjectID, "default", now)
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: configID})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	sourceYAML := testAgentConfigYAML()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	if _, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Definition:              json.RawMessage(compiled.CanonicalJSON),
			Source:                  sourceYAML,
			ConfiguredModelID:       parseConfiguredModelID(t, compiled),
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:        agent.ID,
		ActorType:      identitystore.PrincipalTypeSystem,
		Reason:         "test_create",
		IdempotencyKey: "test-agent-config-change-" + agent.ID.String(),
	}); err != nil {
		t.Fatalf("activate agent config: %v", err)
	}
	return agent.ID
}

func mustCreateAgentConfig(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID ID,
	key string,
	now time.Time,
) ID {
	t.Helper()
	sourceYAML := testAgentConfigYAML()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	configuredModelID := parseConfiguredModelID(t, compiled)
	if _, err := store.Models().CreateProjectModelGrant(
		ctx,
		modelstore.CreateProjectModelGrantInput{
			OrgID:             testOrgID,
			ProjectID:         projectID,
			ConfiguredModelID: configuredModelID,
		},
	); err != nil {
		t.Fatalf("grant configured model for agent config %s: %v", key, err)
	}
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               projectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		ConfiguredModelID:       configuredModelID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create agent config %s: %v", key, err)
	}
	return config.ID
}

func testAgentConfigYAML() string {
	return `
instruction: test
model:
  provider_config: openai-prod
  name: test
`
}

func createLaunchTestAgent(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key string,
	sourceYAML string,
) executionstore.AgentProfileRecord {
	t.Helper()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, time.Date(2026, 5, 21, 8, 0, 0, 0, time.UTC))
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create launch agent config: %v", err)
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            "Launch Agent",
		CurrentConfigID: config.ID,
		IdempotencyKey:  key,
	})
	if err != nil {
		t.Fatalf("create launch agent profile: %v", err)
	}
	return profile
}

func assertEventTypes(t *testing.T, ctx context.Context, store *Store, agentID ID, want []string) {
	t.Helper()
	events, err := store.Execution().ListAgentEventsForRead(ctx, testProjectID, agentID, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.EventKind)
	}
	if len(got) != len(want) {
		t.Fatalf("event count mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d mismatch: got %v want %v", i, got, want)
		}
	}
}

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

func intPtrForMachinePoolTest(value int) *int {
	return &value
}

func secretPublicIDForTest(t *testing.T, id ID) string {
	t.Helper()
	encoded, err := publicid.Encode(publicid.KindSecret, id)
	if err != nil {
		t.Fatalf("encode secret public id: %v", err)
	}
	return encoded
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

func mustCreateProjectOperatorUser(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email, displayName string,
) identitystore.UserRecord {
	t.Helper()
	return mustCreateProjectRoleUser(t, ctx, store, email, displayName, "operator")
}

func mustCreateProjectDeveloperUser(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email, displayName string,
) identitystore.UserRecord {
	t.Helper()
	return mustCreateProjectRoleUser(t, ctx, store, email, displayName, "developer")
}

func mustCreateProjectRoleUser(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email, displayName, projectRole string,
) identitystore.UserRecord {
	t.Helper()
	user, err := store.CreateVerifiedUser(ctx, CreateVerifiedUserInput{Email: email, DisplayName: displayName})
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add org membership for %s: %v", email, err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			UserID:    user.ID,
			Role:      projectRole,
		},
	); err != nil {
		t.Fatalf("add project %s membership for %s: %v", projectRole, email, err)
	}
	return user
}

func mustCreateConfigAndProfileBookmarkFromYAML(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key, name, sourceYAML string,
	now time.Time,
) executionstore.AgentProfileRecord {
	t.Helper()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	config := mustCreateAgentConfigFromCompiled(t, ctx, store, key, sourceYAML, compiled)
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            name,
		CurrentConfigID: config.ID,
		IdempotencyKey:  "profile-" + key,
	})
	if err != nil {
		t.Fatalf("create agent profile %s: %v", key, err)
	}
	return profile
}

func mustCreateAgentConfigFromCompiled(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key, source string,
	compiled agentconfig.Result,
) executionstore.AgentConfigRecord {
	t.Helper()
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  source,
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create agent config %s: %v", key, err)
	}
	return config
}

func mustCreateAgentConfigFromYAML(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key, sourceYAML string,
	now time.Time,
) executionstore.AgentConfigRecord {
	t.Helper()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	return mustCreateAgentConfigFromCompiled(t, ctx, store, key, sourceYAML, compiled)
}

func changeInputFromRecord(record executionstore.AgentConfigRecord) executionstore.CreateAgentConfigInput {
	return executionstore.CreateAgentConfigInput{
		ProjectID:               record.ProjectID,
		Definition:              record.Definition,
		Source:                  record.Source,
		ConfiguredModelID:       record.ConfiguredModelID,
		CompiledDefinition:      record.CompiledDefinition,
		CompilerVersion:         record.CompilerVersion,
		EffectiveDefinitionHash: record.EffectiveDefinitionHash,
	}
}
