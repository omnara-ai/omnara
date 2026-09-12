//go:build integration

package storage

import (
	"context"
	"encoding/json"
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
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
)

var (
	testOrgID                                  = testID("org_test")
	testProjectID                              = testID("project_test")
	testDefaultProviderAdminUserID             = testID("default_provider_admin_user")
	testDefaultProviderCredentialSecretID      = testID("default_provider_credential_secret")
	testDefaultProviderCredentialSecretVersion = testID("default_provider_credential_secret_version")
)

func testQueries(store *Store) *dbsqlc.Queries {
	return dbsqlc.New(store.pool)
}

func testID(seed string) ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-storage-integration:"+seed))
}

func isNilID(id ID) bool {
	return id == NilID
}

func openIntegrationDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
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

func seedMigratedDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	seedDefaultProject(t, ctx, newIntegrationStore(pool))
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
	storagefixture.InsertProject(t, ctx, pool, testOrgID, projectID,
		"Test Project "+seed, "idem-test-project-"+seed, time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC))
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

func mustCreateAgent(t *testing.T, ctx context.Context, store *Store) ID {
	t.Helper()
	configID := mustCreateAgentConfig(t, ctx, store, testProjectID)
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID:       testProjectID,
		CurrentConfigID: configID,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return agent.ID
}

func mustCreateAgentConfig(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID ID,
) ID {
	t.Helper()
	config := storagefixture.SeedAgentConfig(
		t, ctx, store.Models(), store.Execution(), testOrgID, projectID, testAgentConfigYAML(),
	)
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
	config := storagefixture.SeedAgentConfig(
		t, ctx, store.Models(), store.Execution(), testOrgID, testProjectID, sourceYAML,
	)
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

func completeMachinePoolInputForTest(
	input executionstore.CreateMachinePoolInput,
) executionstore.CreateMachinePoolInput {
	if input.DefaultMachineCPU == nil {
		input.DefaultMachineCPU = new(1)
	}
	if input.DefaultMachineMemoryMB == nil {
		input.DefaultMachineMemoryMB = new(1024)
	}
	input.DefaultMachineEnv = normalizedJSON(input.DefaultMachineEnv)
	input.DefaultMachineSecretEnv = normalizedJSON(input.DefaultMachineSecretEnv)
	input.DefaultMachineProviderOptions = normalizedJSON(input.DefaultMachineProviderOptions)
	if input.MaxTotalCPU == nil {
		input.MaxTotalCPU = new(32)
	}
	if input.MaxTotalMemoryMB == nil {
		input.MaxTotalMemoryMB = new(65536)
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
		input.DefaultMachineCPU = new(fields.DefaultMachineCPU)
	}
	if fields.DefaultMachineMemoryMB != 0 {
		input.DefaultMachineMemoryMB = new(fields.DefaultMachineMemoryMB)
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
		template.DefaultMachineCPU = new(fields.DefaultMachineCPU)
	}
	if fields.DefaultMachineMemoryMB != 0 {
		template.DefaultMachineMemoryMB = new(fields.DefaultMachineMemoryMB)
	}
	template.DefaultMachineEnv = fields.DefaultMachineEnv
	template.DefaultMachineSecretEnv = fields.DefaultMachineSecretEnv
	template.DefaultMachineProviderOptions = fields.DefaultMachineProviderOptions
	return template
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
) executionstore.AgentProfileRecord {
	t.Helper()
	config := storagefixture.SeedAgentConfig(
		t, ctx, store.Models(), store.Execution(), testOrgID, testProjectID, sourceYAML,
	)
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

func mustCreateAgentConfigFromYAML(
	t *testing.T,
	ctx context.Context,
	store *Store,
	sourceYAML string,
) executionstore.AgentConfigRecord {
	t.Helper()
	return storagefixture.SeedAgentConfig(
		t, ctx, store.Models(), store.Execution(), testOrgID, testProjectID, sourceYAML,
	)
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
