//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

var (
	testOrgID                                  = testID("org_test")
	testProjectID                              = testID("project_test")
	testWorkerProcessID                        = testID("worker_process")
	testDefaultProviderAdminUserID             = testID("default_provider_admin_user")
	testDefaultProviderCredentialSecretID      = testID("default_provider_credential_secret")
	testDefaultProviderCredentialSecretVersion = testID("default_provider_credential_secret_version")
)

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
	return storagetest.ExecuteToolCallCommand[T](ctx, store.Execution(), input, command)
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
	return integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
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
	storagefixture.SeedProject(t, ctx, store.pool, storagefixture.ProjectIDs{
		OrgID:                   testOrgID,
		ProjectID:               testProjectID,
		ProviderAdminUserID:     testDefaultProviderAdminUserID,
		ProviderSecretID:        testDefaultProviderCredentialSecretID,
		ProviderSecretVersionID: testDefaultProviderCredentialSecretVersion,
		ProviderConfigID:        testDefaultProviderConfigID(),
	}, time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC))
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

func ensureTestConfiguredModelForSource(
	t *testing.T,
	ctx context.Context,
	store *Store,
	sourceYAML string,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	return storagefixture.SeedModelForAgentYAML(t, ctx, store.Models(), testOrgID, testProjectID, sourceYAML)
}

func parseConfiguredModelID(t *testing.T, compiled agentconfig.Result) ID {
	t.Helper()
	id, err := ParseID(compiled.Compiled.Model.ConfiguredModelID)
	if err != nil {
		t.Fatalf("parse compiled configured model id: %v", err)
	}
	return id
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
) agentconfig.Result {
	t.Helper()
	return storagefixture.SeedModelAndCompileAgentYAML(
		t, ctx, store.Models(), store.Execution(), testOrgID, testProjectID, sourceYAML,
	)
}

func mustCreateAgent(t *testing.T, ctx context.Context, store *Store, now time.Time) ID {
	t.Helper()
	configID := mustCreateAgentConfig(t, ctx, store, testProjectID, "default", now)
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: configID})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	sourceYAML := testAgentConfigYAML()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML)
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
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML)
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
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML)
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
