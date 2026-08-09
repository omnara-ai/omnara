//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type rejectingMachinePoolProviders struct {
	mergingMachinePoolProviders
	reject bool
}

func requireCurrentAgentLaunchReplay(
	t *testing.T,
	replayed executionstore.LaunchAgentResult,
	current executionstore.AgentRecord,
) {
	t.Helper()
	sameArchivedAt := replayed.Agent.ArchivedAt == nil && current.ArchivedAt == nil
	if replayed.Agent.ArchivedAt != nil && current.ArchivedAt != nil {
		sameArchivedAt = replayed.Agent.ArchivedAt.Equal(*current.ArchivedAt)
	}
	if replayed.Created ||
		replayed.Agent.ID != current.ID ||
		replayed.Agent.State != current.State ||
		replayed.Agent.CurrentConfigID != current.CurrentConfigID ||
		!sameArchivedAt {
		t.Fatalf("launch replay agent = %+v, want current agent %+v", replayed.Agent, current)
	}
	if replayed.AgentConfig.ID != NilID ||
		replayed.ConfigChange.AgentInput.ID != NilID ||
		replayed.ConfigChange.Event.ID != NilID ||
		len(replayed.MCPServers) != 0 ||
		len(replayed.MCPConnections) != 0 ||
		len(replayed.MachineBindings) != 0 ||
		len(replayed.ProvisionMachineIDs) != 0 ||
		replayed.AgentInput.ID != NilID ||
		len(replayed.InputContentBlocks) != 0 {
		t.Fatalf("launch replay included launch-only data: %+v", replayed)
	}
}

func (p *rejectingMachinePoolProviders) ValidatePool(
	_ string,
	_ executionstore.MachinePoolProviderPolicy,
) error {
	if p.reject {
		return errors.New("provider validation rejected")
	}
	return nil
}

func (p *rejectingMachinePoolProviders) BuildMachineProvisioningIntent(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := p.ValidatePool(provider, policy); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

func createLaunchTestMachinePool(
	t *testing.T,
	ctx context.Context,
	store *Store,
	name, provider string,
	defaultMachineFields defaultMachineFieldsForTest,
	maxActiveMachines int32,
	now time.Time,
) executionstore.MachinePoolRecord {
	t.Helper()
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:              testOrgID,
					Name:               name,
					Provider:           provider,
					MaxTotalMachines:   maxActiveMachines,
					MaxTotalCPU:        intPtrForMachinePoolTest(32),
					MaxTotalMemoryMB:   intPtrForMachinePoolTest(65536),
					MaxMachineCPU:      intPtrForMachinePoolTest(32),
					MaxMachineMemoryMB: intPtrForMachinePoolTest(65536),
				},
				defaultMachineFields,
			),
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	return machinePool
}

func createDefaultMachinePoolForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	input executionstore.CreateMachinePoolInput,
) executionstore.MachinePoolRecord {
	t.Helper()
	input = completeMachinePoolInputForTest(input)
	if input.Name == "" {
		input.Name = "test-default-pool"
	}
	if input.ProviderAuthEnvVar == "" {
		input.ProviderAuthEnvVar = "TEST_DEFAULT_POOL_TOKEN"
	}
	input.DefaultMachineEnv = normalizedJSON(input.DefaultMachineEnv)
	input.DefaultMachineSecretEnv = normalizedJSON(input.DefaultMachineSecretEnv)
	input.DefaultMachineProviderOptions = normalizedJSON(input.DefaultMachineProviderOptions)
	input.ProviderConfig = normalizedJSON(input.ProviderConfig)
	input.Metadata = normalizedJSON(input.Metadata)
	row, err := store.q.InsertMachinePool(ctx, dbsqlc.InsertMachinePoolParams{
		OrgID:                         input.OrgID,
		Name:                          input.Name,
		ManagementKind:                string(management.Cluster),
		Description:                   input.Description,
		Provider:                      input.Provider,
		DefaultMachineCpu:             sqlcInt32Ptr(input.DefaultMachineCPU),
		DefaultMachineMemoryMb:        sqlcInt32Ptr(input.DefaultMachineMemoryMB),
		DefaultMachineEnv:             input.DefaultMachineEnv,
		DefaultMachineSecretEnv:       input.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: input.DefaultMachineProviderOptions,
		DefaultCwd:                    input.DefaultCwd,
		ProviderConfig:                input.ProviderConfig,
		ProviderAuthSecretID:          sqlcIDFromNil(input.ProviderAuthSecretID),
		ProviderAuthEnvVar:            input.ProviderAuthEnvVar,
		MaxTotalMachines:              input.MaxTotalMachines,
		MaxTotalCpu:                   sqlcInt32Ptr(input.MaxTotalCPU),
		MaxTotalMemoryMb:              sqlcInt32Ptr(input.MaxTotalMemoryMB),
		MaxMachineCpu:                 sqlcInt32Ptr(input.MaxMachineCPU),
		MaxMachineMemoryMb:            sqlcInt32Ptr(input.MaxMachineMemoryMB),
		Metadata:                      input.Metadata,
	})
	if err != nil {
		t.Fatalf("create default machine pool: %v", err)
	}
	record, err := store.Execution().GetMachinePool(ctx, input.OrgID, row.ID)
	if err != nil {
		t.Fatalf("load default machine pool: %v", err)
	}
	return record
}

func getAgentMachineBindingForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentID, bindingID ID,
) executionstore.AgentMachineBindingRecord {
	t.Helper()
	var row dbsqlc.AgentMachineBinding
	err := store.pool.QueryRow(ctx, `
		SELECT id, org_id, project_id, agent_id, create_tool_call_id, delete_tool_call_id,
		       machine_id, machine_ref, binding_kind, state, description, cwd, env_overlay,
		       secret_env_overlay, metadata, created_at, updated_at
		FROM agent_machine_bindings
		WHERE project_id = $1 AND agent_id = $2 AND id = $3
	`, projectID, agentID, bindingID).Scan(
		&row.ID,
		&row.OrgID,
		&row.ProjectID,
		&row.AgentID,
		&row.CreateToolCallID,
		&row.DeleteToolCallID,
		&row.MachineID,
		&row.MachineRef,
		&row.BindingKind,
		&row.State,
		&row.Description,
		&row.Cwd,
		&row.EnvOverlay,
		&row.SecretEnvOverlay,
		&row.Metadata,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if err != nil {
		t.Fatalf("get agent machine binding: %v", err)
	}
	return executionstore.AgentMachineBindingRecord{
		ID:               row.ID,
		OrgID:            row.OrgID,
		ProjectID:        row.ProjectID,
		AgentID:          row.AgentID,
		CreateToolCallID: idFromSQLCPtrForTest(row.CreateToolCallID),
		DeleteToolCallID: idFromSQLCPtrForTest(row.DeleteToolCallID),
		MachineID:        row.MachineID,
		MachineRef:       row.MachineRef,
		BindingKind:      executionstore.AgentMachineBindingKind(row.BindingKind),
		State:            executionstore.AgentMachineBindingState(row.State),
		Description:      row.Description,
		Cwd:              row.Cwd,
		EnvOverlay:       row.EnvOverlay,
		SecretEnvOverlay: row.SecretEnvOverlay,
		Metadata:         row.Metadata,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func launchPoolAgentForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	userID ID,
	machinePool executionstore.MachinePoolRecord,
	profileKey, profileName, idempotencyKey string,
	now time.Time,
) executionstore.LaunchAgentResult {
	t.Helper()
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, profileKey, profileName, `
name: `+profileName+`
instruction: Use a project machine pool.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    cwd: /workspace
tools:
  run_command: {}
`, now)
	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(userID),
			IdempotencyKey: idempotencyKey,
		},
	)
	if err != nil {
		t.Fatalf("launch pool agent: %v", err)
	}
	if len(result.MachineBindings) != 1 || len(result.ProvisionMachineIDs) != 1 ||
		result.ProvisionMachineIDs[0] != result.MachineBindings[0].MachineID {
		t.Fatalf("pool launch should create one attached binding and provisioning request, result=%+v", result)
	}
	return result
}

func TestLaunchAgentValidatesProviderPoolConfigAtLaunch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	providers := &rejectingMachinePoolProviders{}
	store := newIntegrationStore(pool, WithMachinePoolProviders(providers))
	now := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"launch-provider-validation@example.com",
		"Launch Provider Validation",
	)
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Provider Validation Pool",
		"test",
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"launch-provider-validation"}`),
		},
		1,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-provider-validation-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "launch-provider-validation", "Launch Provider Validation Agent", `
name: Launch Provider Validation Agent
instruction: Trigger provider validation at launch.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 1
    initial_num_machines: 1
tools:
  run_command: {}
`, now.Add(2*time.Second))

	providers.reject = true
	_, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-provider-validation-agent",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "provider validation rejected") {
		t.Fatalf("launch provider validation error = %v", err)
	}
}

func TestConcurrentSameKeyLaunchIgnoresLosingBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"concurrent-launch-replay@example.com",
		"Concurrent Launch Replay",
	)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"concurrent-launch-replay",
		"Concurrent Launch Replay",
		`
name: Concurrent Launch Replay
instruction: Test same-key launch serialization.
model:
  provider_config: openai-prod
  name: gpt-test
tools: {}
`,
		time.Date(2026, 6, 16, 10, 10, 0, 0, time.UTC),
	)

	const idempotencyKey = "idem-concurrent-same-key-launch"
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin idempotency lock blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if err := dbsqlc.New(blocker).LockAgentLaunchIdempotencyKey(
		ctx,
		dbsqlc.LockAgentLaunchIdempotencyKeyParams{
			ProjectID:      testProjectID,
			IdempotencyKey: idempotencyKey,
		},
	); err != nil {
		t.Fatalf("lock launch idempotency key: %v", err)
	}
	var blockerPID int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatalf("load idempotency lock blocker pid: %v", err)
	}

	type launchOutcome struct {
		result executionstore.LaunchAgentResult
		err    error
	}
	winnerDone := make(chan launchOutcome, 1)
	go func() {
		result, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: idempotencyKey,
		})
		winnerDone <- launchOutcome{result: result, err: err}
	}()
	waitForDatabaseLockWait(t, ctx, pool, "-- name: LockAgentLaunchIdempotencyKey", blockerPID)

	replayDone := make(chan launchOutcome, 1)
	go func() {
		result, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			AgentConfigID:  testID("missing-concurrent-retry-config"),
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: idempotencyKey,
		})
		replayDone <- launchOutcome{result: result, err: err}
	}()
	waitForDatabaseLockWaitCount(t, ctx, pool, "-- name: LockAgentLaunchIdempotencyKey", 2)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release launch idempotency key: %v", err)
	}

	winner := <-winnerDone
	if winner.err != nil || !winner.result.Created {
		t.Fatalf("same-key launch winner = %+v err=%v", winner.result, winner.err)
	}
	replayed := <-replayDone
	if replayed.err != nil {
		t.Fatalf("same-key launch replay: %v", replayed.err)
	}
	requireCurrentAgentLaunchReplay(t, replayed.result, winner.result.Agent)
}

func TestLaunchAgentPersistsProviderIntentWithoutExternalResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	providers := &externalMachinePoolProviders{}
	store := newIntegrationStore(pool, WithMachinePoolProviders(providers))
	now := time.Date(2026, 6, 16, 10, 15, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"launch-provider-intent@example.com",
		"Launch Provider Intent",
	)
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Provider Intent Pool",
		"daytona",
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             8,
			DefaultMachineMemoryMB:        8192,
			DefaultMachineProviderOptions: json.RawMessage(`{"snapshot":"configured"}`),
		},
		1,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-provider-intent-grant",
		},
	); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-provider-intent",
		"Launch Provider Intent Agent",
		`
name: Launch Provider Intent Agent
instruction: Persist a machine intent.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    initial_num_machines: 1
tools:
  run_command: {}
`,
		now.Add(2*time.Second),
	)
	result, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-launch-provider-intent-agent",
	})
	if err != nil {
		t.Fatalf("launch provider intent agent: %v", err)
	}
	if len(result.ProvisionMachineIDs) != 1 {
		t.Fatalf("provision machine ids = %+v, want one", result.ProvisionMachineIDs)
	}
	machine, err := store.Execution().GetMachine(ctx, testOrgID, result.ProvisionMachineIDs[0])
	if err != nil {
		t.Fatalf("get provider intent machine: %v", err)
	}
	if machine.CPU != nil || machine.MemoryMB != nil {
		t.Fatalf("provider-owned resources were persisted before preparation: cpu %v memory %v", machine.CPU, machine.MemoryMB)
	}
}
func TestLaunchAgentWithDefaultPool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	now := time.Date(2026, 5, 21, 8, 30, 0, 0, time.UTC)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	defaultPool := createDefaultMachinePoolForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:            testOrgID,
			Provider:         "test",
			DefaultCwd:       "/workspace",
			MaxTotalMachines: 2,
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"default"}`),
		},
	))
	user := mustCreateProjectDeveloperUser(t, ctx, store, "launch-default-pool@example.com", "Launch Cluster Pool")
	if err := provisionDefaultMachinePoolGrantsForProject(
		ctx,
		store,
		testOrgID,
		testProjectID,
	); err != nil {
		t.Fatalf("create default project pool grant: %v", err)
	}
	overCapacity := mustCompileAgentYAMLWithMachineSourceResolvers(t, ctx, store, `
name: Cluster Pool Over Capacity
instruction: Request too many default pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+defaultPool.Name+`
    max_machines: 3
tools:
  run_command: {}
`)
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(overCapacity.CanonicalJSON),
		agentconfig.CompilerVersion,
		overCapacity.Hash); err != nil {
		t.Fatalf("validate config with max_machines above the pool budget: %v", err)
	}
	imageOverride := mustCompileAgentYAMLWithMachineSourceResolvers(t, ctx, store, `
name: Cluster Pool Image Override
instruction: Override the default pool image.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+defaultPool.Name+`
    machine_provider_options_overlay:
      image: registry.example.com/other:latest
tools:
  run_command: {}
`)
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(imageOverride.CanonicalJSON),
		agentconfig.CompilerVersion,
		imageOverride.Hash); err != nil {
		t.Fatalf("default pool image override validation error = %v, want allowed overlay", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "default-pool", "Cluster Pool", `
name: Cluster Pool
instruction: Use the default pool.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+defaultPool.Name+`
    max_machines: 2
    initial_num_machines: 2
    cwd: /work
    description: Cluster pool machine
    machine_cpu: 1
    machine_memory_mb: 1024
    env_overlay:
      APP_ENV: test
tools:
  run_command: {}
`, now)
	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-default-pool",
		},
	)
	if err != nil {
		t.Fatalf("launch default pool agent: %v", err)
	}
	if len(result.MachineBindings) != 2 || len(result.ProvisionMachineIDs) != 2 {
		t.Fatalf("default pool launch should create two bindings and provisioning requests: %+v", result)
	}
	var poolGrantCount int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM project_machine_pool_grants WHERE project_id = $1`, testProjectID).
		Scan(&poolGrantCount); err != nil {
		t.Fatalf("count project machine pool grants: %v", err)
	}
	if poolGrantCount != 1 {
		t.Fatalf("default pool project machine pool grants = %d, want 1", poolGrantCount)
	}
	var defaultPoolGrantID ID
	if err := pool.QueryRow(ctx, `SELECT id FROM project_machine_pool_grants WHERE project_id = $1`, testProjectID).
		Scan(&defaultPoolGrantID); err != nil {
		t.Fatalf("load default project machine pool grant: %v", err)
	}
	var generatedGrants int
	for _, binding := range result.MachineBindings {
		generatedGrant := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, binding.MachineID)
		if generatedGrant.SourceKind != "pool" || generatedGrant.ProjectMachinePoolGrantID != defaultPoolGrantID {
			t.Fatalf(
				"generated grant source_kind=%q project_machine_pool_grant_id=%s, want pool %s",
				generatedGrant.SourceKind,
				generatedGrant.ProjectMachinePoolGrantID,
				defaultPoolGrantID,
			)
		}
		generatedGrants++
	}
	if generatedGrants != 2 {
		t.Fatalf("generated default pool grants = %d, want 2", generatedGrants)
	}
	replayed, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-default-pool",
		},
	)
	if err != nil {
		t.Fatalf("replay default pool launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, result.Agent)
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		defaultPoolGrantID,
	); err != nil {
		t.Fatalf("delete default project machine pool grant: %v", err)
	}
	if _, err := store.Execution().GetProjectMachinePoolGrant(ctx, testOrgID, testProjectID, defaultPoolGrantID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted pool grant lookup error = %v, want not found", err)
	}
	replayedAfterRevoke, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-default-pool",
		},
	)
	if err != nil {
		t.Fatalf("replay default pool launch after grant revoke: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayedAfterRevoke, result.Agent)
	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-default-pool-after-revoke",
		},
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("launch default pool agent after grant revoke error = %v, want ErrNotFound", err)
	}
}

func TestArchiveAgentMarksPoolMachinesDeletingAndStopsExecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 47, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "archive-agent@example.com", "Archive Agent User")
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Archive Agent Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		2,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-archive-agent-pool-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"archive-agent-pool",
		"Archive Agent Pool Agent",
		"idem-archive-agent",
		now.Add(2*time.Second),
	)

	backlogInput, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        result.Agent.ID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"queued before archive"}]`),
		IdempotencyKey: "idem-archive-agent-backlog-input",
	})
	if err != nil {
		t.Fatalf("create queued backlog input: %v", err)
	}
	backlogBeforeArchive, err := store.Execution().ListQueuedBacklogInputs(
		ctx,
		executionstore.ListQueuedBacklogInputsInput{ProjectID: testProjectID, AgentID: result.Agent.ID, Limit: 10},
	)
	if err != nil {
		t.Fatalf("list queued backlog before archive: %v", err)
	}
	var foundBacklogInput bool
	for _, input := range backlogBeforeArchive.Inputs {
		if input.ID == backlogInput.ID {
			foundBacklogInput = true
			break
		}
	}
	if !foundBacklogInput {
		t.Fatalf("queued backlog before archive = %+v, missing %s", backlogBeforeArchive, backlogInput.ID)
	}

	directMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Archive Agent Direct Machine",
			IdempotencyKey: "idem-archive-agent-direct-machine",
		},
	)
	if err != nil {
		t.Fatalf("create direct machine: %v", err)
	}
	directGrant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      directMachine.ID,
			IdempotencyKey: "idem-archive-agent-direct-grant",
		},
	)
	if err != nil {
		t.Fatalf("create direct grant: %v", err)
	}
	directBinding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               result.Agent.ID,
			ProjectMachineGrantID: directGrant.ID,
			MachineRef:            "mchr-arch01",
			BindingKind:           executionstore.MachineBindingKindExplicit,
		},
	)
	if err != nil {
		t.Fatalf("bind direct machine: %v", err)
	}
	runtime, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		result.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	seedProviderRuntimeMismatchForTest(
		t,
		ctx,
		pool,
		result.MachineBindings[0].MachineID,
	)
	archivedAgent, archivedMachines, err := store.Execution().ArchiveAgent(
		ctx,
		testProjectID,
		result.Agent.ID,
		userPrincipal(user.ID),
	)
	if err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if archivedAgent.State != executionstore.AgentStateArchived || archivedAgent.ArchivedAt == nil ||
		!archivedAgent.ArchivedAt.Equal(archivedAgent.UpdatedAt) {
		t.Fatalf("archived agent = %+v", archivedAgent)
	}
	archivedAt := *archivedAgent.ArchivedAt
	loaded, err := store.Execution().GetAgentInProject(ctx, testProjectID, result.Agent.ID)
	if err != nil {
		t.Fatalf("archived agent should stay readable: %v", err)
	}
	if loaded.State != executionstore.AgentStateArchived {
		t.Fatalf("archived agent state = %v, want archived", loaded.State)
	}
	replayed, _, err := store.Execution().ArchiveAgent(
		ctx,
		testProjectID,
		result.Agent.ID,
		userPrincipal(user.ID),
	)
	if err != nil {
		t.Fatalf("replay archive agent: %v", err)
	}
	if replayed.ArchivedAt == nil || !replayed.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("replayed archive should keep the original archived_at, got %+v", replayed.ArchivedAt)
	}
	if len(archivedMachines) != 1 || archivedMachines[0].ID != result.MachineBindings[0].MachineID {
		t.Fatalf("archived machines = %+v", archivedMachines)
	}
	machine, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get archived pool machine: %v", err)
	}
	if machine.LifecycleState != "deleting" || machine.LifecycleReasonCode != "agent_archived_cleanup" ||
		machine.LifecycleReasonMessage != "cleaning up machine after agent archived" {
		t.Fatalf("pool machine after archive = %+v", machine)
	}
	if machine.NextReconcileAfter == nil || !machine.NextReconcileAfter.Equal(archivedAt) {
		t.Fatalf("pool machine next reconcile = %v, want %v", machine.NextReconcileAfter, archivedAt)
	}
	assertProviderRuntimeMismatchClearedForTest(t, ctx, pool, machine.ID)
	directAfter, err := store.Execution().GetMachine(ctx, testOrgID, directMachine.ID)
	if err != nil {
		t.Fatalf("get direct machine after archive: %v", err)
	}
	if directAfter.LifecycleState != "active" {
		t.Fatalf("direct machine lifecycle after archive = %+v", directAfter)
	}
	directBindingAfter := getAgentMachineBindingForTest(
		t,
		ctx,
		store,
		testProjectID,
		result.Agent.ID,
		directBinding.ID,
	)
	if directBindingAfter.State != "released" || !directBindingAfter.UpdatedAt.Equal(archivedAt) {
		t.Fatalf("direct binding after archive = %+v, want released at %v", directBindingAfter, archivedAt)
	}
	var runtimeCancelRequested bool
	if err := pool.QueryRow(ctx, `
SELECT runtime_lock.cancel_requested_at IS NOT NULL
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = $1
  AND runtime_lock.agent_id = $2
  AND runtime_lock.id = $3
`, testProjectID, result.Agent.ID, runtime.ID).
		Scan(&runtimeCancelRequested); err != nil {
		t.Fatalf("query runtime cancel: %v", err)
	}
	if !runtimeCancelRequested {
		t.Fatal("archive did not request runtime cancellation")
	}
	backlogAfterArchive, err := store.Execution().ListQueuedBacklogInputs(
		ctx,
		executionstore.ListQueuedBacklogInputsInput{ProjectID: testProjectID, AgentID: result.Agent.ID, Limit: 10},
	)
	if err != nil {
		t.Fatalf("list queued backlog after archive: %v", err)
	}
	if len(backlogAfterArchive.Inputs) != 0 {
		t.Fatalf("queued backlog after archive = %+v, want empty", backlogAfterArchive)
	}
	var backlogState string
	var backlogCanceledAt time.Time
	if err := pool.QueryRow(ctx, `SELECT state, canceled_at FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND id = $3`, testProjectID, result.Agent.ID, backlogInput.ID).
		Scan(&backlogState, &backlogCanceledAt); err != nil {
		t.Fatalf("query archived backlog input: %v", err)
	}
	if backlogState != "canceled" || backlogCanceledAt.Before(archivedAt) {
		t.Fatalf(
			"archived backlog input state=%s canceled_at=%v, want canceled no earlier than %v",
			backlogState,
			backlogCanceledAt,
			archivedAt,
		)
	}
	if err := store.Execution().MarkAgentWakeup(
		ctx,
		testProjectID,
		result.Agent.ID,
		json.RawMessage(`{"reason":"after_archive"}`),
	); err != nil {
		t.Fatalf("mark archived wakeup: %v", err)
	}
	var wakeups int
	if err := pool.QueryRow(ctx, `SELECT count(*)::integer FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, testProjectID, result.Agent.ID).
		Scan(&wakeups); err != nil {
		t.Fatalf("count archived wakeups: %v", err)
	}
	if wakeups != 0 {
		t.Fatalf("archived agent wakeups = %d, want 0", wakeups)
	}
	if _, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		result.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("acquire archived runtime lock error = %v, want ErrAgentNotAdvanceable", err)
	}
}

func TestChangeAgentConfigReconcilesPoolMachineSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 48, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "live-pool-sources@example.com", "Live Pool Sources")
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Live Source Pool",
		"test.provider",
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"baseline"}`),
		},
		5,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachinePoolID:  machinePool.ID,
		IdempotencyKey: "idem-live-pool-grant",
	}); err != nil {
		t.Fatalf("create live pool grant: %v", err)
	}
	emptyYAML := `
name: Live Pool Sources
instruction: Use pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command: {}
`
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"live-pool-sources",
		"Live Pool Sources",
		emptyYAML,
		now,
	)
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-live-pool-launch",
	})
	if err != nil {
		t.Fatalf("launch agent without pool source: %v", err)
	}
	addedYAML := `
name: Live Pool Sources
instruction: Use pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: ` + machinePool.Name + `
    max_machines: 3
    initial_num_machines: 1
    cwd: /initial
tools:
  create_machine:
    type: built_in
`
	added := changeAgentConfigFromYAMLForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		"live-pool-added",
		addedYAML,
		"idem-live-pool-added",
		now.Add(2*time.Second),
	)
	if len(added.DeleteMachines) != 0 {
		t.Fatalf("added pool source deleted machines: %+v", added.DeleteMachines)
	}
	machines, err := executionstore.IntegrationListPoolMachinesTx(ctx, store.q, testProjectID, launch.Agent.ID)
	if err != nil {
		t.Fatalf("list added pool machines: %v", err)
	}
	if len(machines) != 0 {
		t.Fatalf("live pool source addition created machines: %+v", machines)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launch.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire live pool runtime lock: %v", err)
	}
	initialToolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		added.AgentConfig.ID,
		lock,
		"live-pool-initial",
		[]poolMachineToolCallSpec{{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)}},
	)
	initial, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       launch.Agent.ID,
		ToolCallID:    initialToolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	})
	if err != nil {
		t.Fatalf("create initial pool machine explicitly: %v", err)
	}
	if initial.Machine.Binding.Cwd != "/initial" {
		t.Fatalf("explicitly created initial pool machine = %+v", initial.Machine)
	}
	originalMachine := initial.Machine
	changedYAML := `
name: Live Pool Sources
instruction: Use pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: ` + machinePool.Name + `
    max_machines: 3
    initial_num_machines: 2
    cwd: /changed
    machine_cpu: 2
    machine_memory_mb: 2048
    machine_provider_options_overlay:
      image: changed
tools:
  create_machine:
    type: built_in
`
	changed := changeAgentConfigFromYAMLForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		"live-pool-changed",
		changedYAML,
		"idem-live-pool-changed",
		now.Add(3*time.Second),
	)
	if len(changed.DeleteMachines) != 0 {
		t.Fatalf("changed pool source deleted machines: %+v", changed.DeleteMachines)
	}
	machines, err = executionstore.IntegrationListPoolMachinesTx(ctx, store.q, testProjectID, launch.Agent.ID)
	if err != nil {
		t.Fatalf("list changed pool machines: %v", err)
	}
	if len(machines) != 1 || machines[0].Machine.ID != originalMachine.Machine.ID ||
		machines[0].Binding.Cwd != "/changed" || machines[0].Machine.CPU == nil || *machines[0].Machine.CPU != 1 ||
		machines[0].Machine.MemoryMB == nil || *machines[0].Machine.MemoryMB != 1024 ||
		!sameJSON(machines[0].Machine.ProviderOptions, json.RawMessage(`{"image":"baseline"}`)) {
		t.Fatalf("existing pool machine changed provisioning: %+v", machines)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		changed.AgentConfig.ID,
		lock,
		"live-pool-future",
		[]poolMachineToolCallSpec{{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)}},
	)
	future, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       launch.Agent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	})
	if err != nil {
		t.Fatalf("create future pool machine: %v", err)
	}
	if future.Machine.Machine.CPU == nil || *future.Machine.Machine.CPU != 2 ||
		future.Machine.Machine.MemoryMB == nil || *future.Machine.Machine.MemoryMB != 2048 ||
		!sameJSON(future.Machine.Machine.ProviderOptions, json.RawMessage(`{"image":"changed"}`)) {
		t.Fatalf("future pool machine provisioning = %+v", future.Machine.Machine)
	}
	loweredYAML := strings.ReplaceAll(
		changedYAML,
		"max_machines: 3\n    initial_num_machines: 2",
		"max_machines: 0\n    initial_num_machines: 0",
	)
	loweredConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"live-pool-lowered",
		loweredYAML,
		now.Add(4*time.Second),
	)
	type configChangeResult struct {
		result executionstore.ChangeAgentConfigResult
		err    error
	}
	lockOrderTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin pool lock transaction: %v", err)
	}
	defer func() { _ = lockOrderTx.Rollback(ctx) }()
	lockOrderQ := dbsqlc.New(lockOrderTx)
	if _, err := lockOrderQ.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: testOrgID, ID: machinePool.ID},
	); err != nil {
		t.Fatalf("lock machine pool before config change: %v", err)
	}
	loweredDone := make(chan configChangeResult, 1)
	go func() {
		result, changeErr := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: changeInputFromRecord(loweredConfig),
			AgentID:                launch.Agent.ID,
			ActorType:              identitystore.PrincipalTypeUser,
			ActorID:                user.ID,
			IdempotencyKey:         "idem-live-pool-lowered",
		})
		loweredDone <- configChangeResult{result: result, err: changeErr}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case change := <-loweredDone:
			t.Fatalf("config change completed before waiting on pool lock: %v", change.err)
		default:
		}
		var waiters int
		if err := pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query LIKE '%LockMachinePoolForUpdate%'
`).Scan(&waiters); err != nil {
			t.Fatalf("count config change pool lock waiters: %v", err)
		}
		if waiters > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for config change to lock machine pool")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := lockOrderQ.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{
			OrgID: testOrgID,
			ID:    originalMachine.Machine.ID,
		},
	); err != nil {
		t.Fatalf("lock machine after pool while config change waits: %v", err)
	}
	poolAgentLockCtx, cancelPoolAgentLock := context.WithTimeout(ctx, time.Second)
	defer cancelPoolAgentLock()
	if _, err := lockOrderQ.LockAgentInProject(
		poolAgentLockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: launch.Agent.ID},
	); err != nil {
		t.Fatalf("lock agent after pool and machine while config change waits: %v", err)
	}
	if err := lockOrderTx.Commit(ctx); err != nil {
		t.Fatalf("commit pool-first transaction: %v", err)
	}
	var lowered executionstore.ChangeAgentConfigResult
	select {
	case change := <-loweredDone:
		if change.err != nil {
			t.Fatalf("lower pool maximum after pool lock release: %v", change.err)
		}
		lowered = change.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lowered pool maximum")
	}
	if len(lowered.DeleteMachines) != 0 {
		t.Fatalf("lowered pool maximum deleted machines: %+v", lowered.DeleteMachines)
	}
	current, err := store.Execution().GetMachine(ctx, testOrgID, originalMachine.Machine.ID)
	if err != nil {
		t.Fatalf("load machine after lowering maximum: %v", err)
	}
	if current.LifecycleState == "deleting" {
		t.Fatalf("lowering max_machines marked machine deleting: %+v", current)
	}
	removedConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"live-pool-removed",
		emptyYAML,
		now.Add(5*time.Second),
	)
	seedProviderRuntimeMismatchForTest(
		t,
		ctx,
		pool,
		originalMachine.Machine.ID,
		future.Machine.Machine.ID,
	)
	machineTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin machine lock transaction: %v", err)
	}
	defer func() { _ = machineTx.Rollback(ctx) }()
	machineQ := dbsqlc.New(machineTx)
	if _, err := machineQ.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{
			OrgID: testOrgID,
			ID:    originalMachine.Machine.ID,
		},
	); err != nil {
		t.Fatalf("lock pool machine before config removal: %v", err)
	}
	removalDone := make(chan configChangeResult, 1)
	go func() {
		result, changeErr := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: changeInputFromRecord(removedConfig),
			AgentID:                launch.Agent.ID,
			ActorType:              identitystore.PrincipalTypeUser,
			ActorID:                user.ID,
			IdempotencyKey:         "idem-live-pool-removed",
		})
		removalDone <- configChangeResult{result: result, err: changeErr}
	}()
	deadline = time.Now().Add(5 * time.Second)
	for {
		select {
		case change := <-removalDone:
			t.Fatalf("config removal completed before waiting on machine lock: %v", change.err)
		default:
		}
		var waiters int
		if err := pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query LIKE '%LockAttachedAgentPoolMachines%'
`).Scan(&waiters); err != nil {
			t.Fatalf("count config removal machine lock waiters: %v", err)
		}
		if waiters > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for config removal to lock pool machines")
		}
		time.Sleep(10 * time.Millisecond)
	}
	machineAgentLockCtx, cancelMachineAgentLock := context.WithTimeout(ctx, time.Second)
	defer cancelMachineAgentLock()
	if _, err := machineQ.LockAgentInProject(
		machineAgentLockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: launch.Agent.ID},
	); err != nil {
		t.Fatalf("lock agent after machine while config removal waits: %v", err)
	}
	if err := machineTx.Commit(ctx); err != nil {
		t.Fatalf("commit machine-first transaction: %v", err)
	}
	var removed executionstore.ChangeAgentConfigResult
	select {
	case change := <-removalDone:
		if change.err != nil {
			t.Fatalf("remove pool source after machine lock release: %v", change.err)
		}
		removed = change.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pool source removal")
	}
	if len(removed.DeleteMachines) != 2 {
		t.Fatalf("removed pool source deletions = %+v", removed.DeleteMachines)
	}
	removedIDs := map[ID]bool{}
	for _, machine := range removed.DeleteMachines {
		removedIDs[machine.ID] = true
	}
	if !removedIDs[originalMachine.Machine.ID] || !removedIDs[future.Machine.Machine.ID] {
		t.Fatalf("removed pool source machines = %+v", removed.DeleteMachines)
	}
	deleted, err := store.Execution().GetMachine(ctx, testOrgID, originalMachine.Machine.ID)
	if err != nil {
		t.Fatalf("load removed pool machine: %v", err)
	}
	if deleted.LifecycleState != "deleting" || deleted.LifecycleReasonCode != "agent_config_machine_source_removed" {
		t.Fatalf("removed pool machine = %+v", deleted)
	}
	assertProviderRuntimeMismatchClearedForTest(
		t,
		ctx,
		pool,
		originalMachine.Machine.ID,
		future.Machine.Machine.ID,
	)
	if _, err := store.Execution().DeleteMachinePool(ctx, testOrgID, machinePool.ID); err != nil {
		t.Fatalf("delete removed machine pool: %v", err)
	}
	replayed, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(lowered.AgentConfig),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                user.ID,
		IdempotencyKey:         "idem-live-pool-lowered",
	})
	if err != nil {
		t.Fatalf("replay old pool config after pool archive: %v", err)
	}
	if replayed.ConfigChange.AgentInput.ID != lowered.ConfigChange.AgentInput.ID ||
		replayed.ConfigChange.Event.ID != lowered.ConfigChange.Event.ID {
		t.Fatalf("old pool config replay = %+v, want %+v", replayed.ConfigChange, lowered.ConfigChange)
	}
	currentAgent, err := store.Execution().GetAgentInProject(ctx, testProjectID, launch.Agent.ID)
	if err != nil {
		t.Fatalf("load agent after old pool config replay: %v", err)
	}
	if currentAgent.CurrentConfigID != removed.AgentConfig.ID {
		t.Fatalf("old pool config replay changed current config: %+v", currentAgent)
	}
}

func TestDefaultPoolGrantAllowsSecretEnvBeforeProjectSecretGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := createSecretTestUser(t, ctx, store, "default-pool-secret-env-admin", "admin")
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			UserID:    user.ID,
			Role:      "admin",
		},
	); err != nil {
		t.Fatalf("add project membership: %v", err)
	}
	orgSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "default-pool-env",
		Material:  secrets.GenericMaterial{Value: "default-secret-value"},
		Actor:     userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create org secret: %v", err)
	}
	defaultPool := createDefaultMachinePoolForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:            testOrgID,
			Name:             "default-pool-secret-env",
			Provider:         "test",
			MaxTotalMachines: 2,
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineSecretEnv:       json.RawMessage(`{"API_TOKEN":"` + secretPublicIDForTest(t, orgSecret.ID) + `"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"default"}`),
		},
	))
	if err := provisionDefaultMachinePoolGrantsForProject(
		ctx,
		store,
		testOrgID,
		testProjectID,
	); err != nil {
		t.Fatalf("create default project pool grant: %v", err)
	}
	compiled := mustCompileAgentYAMLWithMachineSourceResolvers(t, ctx, store, `
name: Cluster Pool Secret Env
instruction: Use default pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+defaultPool.Name+`
tools:
  run_command: {}
`)
	err = store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(compiled.CanonicalJSON),
		agentconfig.CompilerVersion,
		compiled.Hash)

	if err == nil || !strings.Contains(err.Error(), "secret_env.API_TOKEN secret is not available to the project") {
		t.Fatalf("default pool secret_env validation error = %v", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID:           testOrgID,
		SecretID:        orgSecret.ID,
		TargetProjectID: testProjectID,
		Actor:           userPrincipal(user.ID),
	}); err != nil {
		t.Fatalf("grant default pool secret to project: %v", err)
	}
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(compiled.CanonicalJSON),
		agentconfig.CompilerVersion,
		compiled.Hash); err != nil {
		t.Fatalf("validate default pool after secret grant: %v", err)
	}
}

func TestLaunchAgentCreatesMachineBindingsInputAndConfigChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "launch@example.com", "Launch User")
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Launch Machine",
			IdempotencyKey: "idem-launch-machine",
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	grant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "idem-launch-grant",
		},
	)
	if err != nil {
		t.Fatalf("create project machine grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "launch-machine", "Launch Agent", `
name: Launch Agent
instruction: Inspect the repo and report progress.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: `+machine.DisplayName+`
    cwd: /workspace
    description: Primary launch machine
    env_overlay:
      APP_MODE: initial
tools:
  run_command: {}
`, now)

	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "start here",
			IdempotencyKey: "idem-launch-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	if !result.Created || result.Agent.CurrentConfigID != profile.CurrentConfigID {
		t.Fatalf("unexpected launched agent: %+v", result.Agent)
	}
	if result.ConfigChange.AgentInput.InputKind != "config_change" ||
		result.ConfigChange.AgentInput.AgentConfigID != result.AgentConfig.ID {
		t.Fatalf("unexpected config change: %+v", result.ConfigChange)
	}
	if len(result.MachineBindings) != 1 || result.MachineBindings[0].MachineID != machine.ID ||
		result.MachineBindings[0].Cwd != "/workspace" {
		t.Fatalf("unexpected machine binding: %+v", result.MachineBindings)
	}
	if !sameJSON(result.MachineBindings[0].EnvOverlay, json.RawMessage(`{"APP_MODE":"initial"}`)) {
		t.Fatalf("initial machine binding env overlay = %s", result.MachineBindings[0].EnvOverlay)
	}
	launchActorID := mustEnsureOmnaraActor(
		t,
		ctx,
		store,
		testOrgID,
		testProjectID,
		user.ID,
	)
	if result.AgentInput.ID == NilID || result.AgentInput.ActorID != launchActorID ||
		string(result.InputContentBlocks) == "" {
		t.Fatalf(
			"expected initial content input, got input=%+v content=%s",
			result.AgentInput,
			result.InputContentBlocks,
		)
	}
	events, err := store.Execution().ListAgentEventsForRead(ctx, testProjectID, result.Agent.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].EventKind != "agent_input" {
		t.Fatalf("launch should append initial config_change event only, got %+v", events)
	}

	replayed, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "start here",
			IdempotencyKey: "idem-launch-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, result.Agent)
	updatedYAML := `
name: Launch Agent
instruction: Inspect the repo and report progress with extra detail.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: ` + machine.DisplayName + `
    cwd: /workspace
    description: Primary launch machine
    env_overlay:
      APP_MODE: initial
tools:
  run_command: {}
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, updatedYAML, now.Add(1500*time.Millisecond))
	change, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Definition:              json.RawMessage(compiled.CanonicalJSON),
			Source:                  updatedYAML,
			ConfiguredModelID:       parseConfiguredModelID(t, compiled),
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:        result.Agent.ID,
		ActorType:      identitystore.PrincipalTypeUser,
		ActorID:        user.ID,
		Reason:         "test",
		IdempotencyKey: "idem-launch-agent-config-change",
	})
	if err != nil {
		t.Fatalf("change launched agent config: %v", err)
	}
	replayedAfterChange, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "start here",
			IdempotencyKey: "idem-launch-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay launch after config change: %v", err)
	}
	loadedAfterReplay, err := store.Execution().GetAgentInProject(ctx, testProjectID, result.Agent.ID)
	if err != nil {
		t.Fatalf("load agent after launch replay: %v", err)
	}
	if loadedAfterReplay.CurrentConfigID != change.AgentConfig.ID {
		t.Fatalf(
			"launch replay rolled back current config: loaded=%+v change=%+v",
			loadedAfterReplay,
			change.AgentConfig,
		)
	}
	requireCurrentAgentLaunchReplay(t, replayedAfterChange, loadedAfterReplay)
	changedReplay, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  change.AgentConfig.ID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "different",
			IdempotencyKey: "idem-launch-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay launch with changed body: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, changedReplay, loadedAfterReplay)
	archived, _, err := store.Execution().ArchiveAgent(
		ctx,
		testProjectID,
		result.Agent.ID,
		userPrincipal(user.ID),
	)
	if err != nil {
		t.Fatalf("archive launched agent: %v", err)
	}
	if archived.State != executionstore.AgentStateArchived || archived.ArchivedAt == nil {
		t.Fatalf("archived agent = %+v", archived)
	}
	replayedAfterArchive, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  change.AgentConfig.ID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "ignored after archive",
			IdempotencyKey: "idem-launch-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay launch after archive: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayedAfterArchive, archived)
	loadedAfterArchiveReplay, err := store.Execution().GetAgentInProject(
		ctx,
		testProjectID,
		result.Agent.ID,
	)
	if err != nil {
		t.Fatalf("load archived agent after launch replay: %v", err)
	}
	if loadedAfterArchiveReplay.State != executionstore.AgentStateArchived ||
		loadedAfterArchiveReplay.ArchivedAt == nil {
		t.Fatalf("launch replay resurrected archived agent: %+v", loadedAfterArchiveReplay)
	}
	if _, err := store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		grant.ID,
	); err != nil {
		t.Fatalf("revoke project machine grant: %v", err)
	}
	replayedAfterGrantRevoke, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "start here",
			IdempotencyKey: "idem-launch-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay launch after project machine grant revoke: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayedAfterGrantRevoke, loadedAfterArchiveReplay)
	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-after-revoke",
		},
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("expected fresh launch after grant revoke to fail, got %v", err)
	}
	var failedAgents int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agents WHERE project_id = $1 AND idempotency_key = 'idem-launch-after-revoke'`, testProjectID).
		Scan(&failedAgents); err != nil {
		t.Fatalf("count failed launch agents: %v", err)
	}
	if failedAgents != 0 {
		t.Fatalf("failed launch after revoke left agent rows behind: %d", failedAgents)
	}
}

func TestLaunchAgentCreatesMultipleMachineBindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 10, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "launch-multi@example.com", DisplayName: "Launch Multi User"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	firstMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "First Launch Machine",
			IdempotencyKey: "idem-launch-multi-machine-1",
		},
	)
	if err != nil {
		t.Fatalf("create first machine: %v", err)
	}
	secondMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Second Launch Machine",
			IdempotencyKey: "idem-launch-multi-machine-2",
		},
	)
	if err != nil {
		t.Fatalf("create second machine: %v", err)
	}
	_, _, err = store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      firstMachine.ID,
			IdempotencyKey: "idem-launch-multi-grant-1",
		},
	)
	if err != nil {
		t.Fatalf("create first grant: %v", err)
	}
	_, _, err = store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      secondMachine.ID,
			IdempotencyKey: "idem-launch-multi-grant-2",
		},
	)
	if err != nil {
		t.Fatalf("create second grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "launch-multi", "Multi Launch Agent", `
name: Multi Launch Agent
instruction: Use the selected machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: `+firstMachine.DisplayName+`
    cwd: /workspace/a
    description: First machine
  - machine_name: `+secondMachine.DisplayName+`
    cwd: /workspace/b
    description: Second machine
tools:
  run_command: {}
`, now)

	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-multi-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch multi-machine agent: %v", err)
	}
	if len(result.MachineBindings) != 2 {
		t.Fatalf("expected two machine bindings, got %+v", result.MachineBindings)
	}
	if result.MachineBindings[0].MachineID != firstMachine.ID ||
		result.MachineBindings[0].Cwd != "/workspace/a" ||
		result.MachineBindings[0].Description != "First machine" {
		t.Fatalf("unexpected first binding: %+v", result.MachineBindings[0])
	}
	if result.MachineBindings[1].MachineID != secondMachine.ID ||
		result.MachineBindings[1].Cwd != "/workspace/b" ||
		result.MachineBindings[1].Description != "Second machine" {
		t.Fatalf("unexpected second binding: %+v", result.MachineBindings[1])
	}
	if result.MachineBindings[0].MachineRef == "" ||
		result.MachineBindings[0].MachineRef == result.MachineBindings[1].MachineRef {
		t.Fatalf("expected distinct machine refs, got %+v", result.MachineBindings)
	}
	replayed, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-multi-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay multi-machine launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, result.Agent)
	for index, binding := range result.MachineBindings {
		token, err := store.Execution().CreateBYOMachineDaemonToken(
			ctx,
			executionstore.CreateBYOMachineDaemonTokenInput{
				OrgID:     testOrgID,
				MachineID: binding.MachineID,
				Name:      "multi daemon",
				Token:     "token-launch-multi-" + binding.MachineRef,
			},
		)
		if err != nil {
			t.Fatalf("create daemon token %d: %v", index, err)
		}
		if _, err := store.Execution().RegisterDaemonRuntime(
			ctx,
			executionstore.RegisterDaemonRuntimeInput{
				OrgID:            testOrgID,
				MachineID:        binding.MachineID,
				DaemonTokenID:    token.ID,
				DaemonInstanceID: testID("daemon-launch-multi-" + binding.MachineRef),
				DaemonVersion:    "1.0.0",
				LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			},
		); err != nil {
			t.Fatalf("register daemon runtime %d: %v", index, err)
		}
	}
	executable, err := store.Execution().ListExecutableAgentMachineBindings(ctx, testProjectID, result.Agent.ID)
	if err != nil {
		t.Fatalf("list executable bindings: %v", err)
	}
	if len(executable) != 2 || executable[0].MachineRef != result.MachineBindings[0].MachineRef ||
		executable[1].MachineRef != result.MachineBindings[1].MachineRef {
		t.Fatalf("unexpected executable bindings: %+v", executable)
	}
}

func TestLaunchAgentExpandsPoolInitialMachinesInStableOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 12, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-initial@example.com",
			DisplayName: "Launch Pool Initial User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Explicit Initial Machine",
			IdempotencyKey: "idem-launch-initial-explicit-machine",
		},
	)
	if err != nil {
		t.Fatalf("create explicit machine: %v", err)
	}
	_, _, err = store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "idem-launch-initial-explicit-grant",
		},
	)
	if err != nil {
		t.Fatalf("create explicit grant: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Initial Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"initial"}`)},
		5,
		now,
	)
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-initial-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-initial-pool",
		"Launch Initial Pool Agent",
		`
name: Launch Initial Pool Agent
instruction: Use explicit and pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: `+machine.DisplayName+`
    cwd: /workspace/explicit
    description: Explicit machine
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 5
    initial_num_machines: 3
    cwd: /workspace/pool
    description: Pool machine
tools:
  run_command: {}
`,
		now,
	)

	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-initial-pool-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch initial pool agent: %v", err)
	}
	if len(result.MachineBindings) != 4 {
		t.Fatalf("machine bindings = %+v, want explicit plus three pool slots", result.MachineBindings)
	}
	if len(result.ProvisionMachineIDs) != 3 {
		t.Fatalf("provision ids = %v, want three pool machines", result.ProvisionMachineIDs)
	}
	if result.MachineBindings[0].MachineID != machine.ID ||
		result.MachineBindings[0].State != "attached" ||
		result.MachineBindings[0].Cwd != "/workspace/explicit" {
		t.Fatalf("unexpected explicit binding: %+v", result.MachineBindings[0])
	}
	for slotIndex := 0; slotIndex < 3; slotIndex++ {
		binding := result.MachineBindings[slotIndex+1]
		if binding.State != "attached" || binding.Cwd != "/workspace/pool" ||
			binding.Description != "Pool machine" {
			t.Fatalf("unexpected pool slot %d binding: %+v", slotIndex, binding)
		}
		if binding.BindingKind != executionstore.MachineBindingKindPool {
			t.Fatalf(
				"pool slot %d binding kind = %s, want %s",
				slotIndex,
				binding.BindingKind,
				executionstore.MachineBindingKindPool,
			)
		}
		if result.ProvisionMachineIDs[slotIndex] != binding.MachineID {
			t.Fatalf(
				"provision id order mismatch at slot %d: ids=%v binding=%+v",
				slotIndex,
				result.ProvisionMachineIDs,
				binding,
			)
		}
		if binding.MachineRef == "" || binding.MachineRef == result.MachineBindings[0].MachineRef {
			t.Fatalf("unexpected pool machine ref at slot %d: %+v", slotIndex, binding)
		}
		if !sameJSON(binding.Metadata, json.RawMessage(`{}`)) {
			t.Fatalf("pool binding metadata = %s, want empty object", binding.Metadata)
		}
		generatedGrant := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, binding.MachineID)
		if generatedGrant.ProjectMachinePoolGrantID != poolGrant.ID || generatedGrant.SourceKind != "pool" {
			t.Fatalf("unexpected generated grant %d: %+v binding=%+v", slotIndex, generatedGrant, binding)
		}
	}
	poolMachines, err := executionstore.IntegrationListPoolMachinesTx(ctx, store.q, testProjectID, result.Agent.ID)
	if err != nil {
		t.Fatalf("list launched pool machines: %v", err)
	}
	if len(poolMachines) != 3 {
		t.Fatalf("listed launched pool machines = %+v, want three", poolMachines)
	}

	replayed, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-initial-pool-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay initial pool launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, result.Agent)
}

func TestLaunchAgentPoolGrantReplacementUsesNewResolvedConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 12, 15, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-config-replace@example.com",
			DisplayName: "Launch Pool Config Replace User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Config Replace Pool",
		"test",
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             4,
			DefaultMachineMemoryMB:        8192,
			DefaultMachineEnv:             json.RawMessage(`{"POOL":"base","SHARED":"pool"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"pool-base"}`),
		},
		5,
		now,
	)
	firstGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		projectGrantInputWithDefaultMachineOverlayForTest(
			executionstore.CreateProjectMachinePoolGrantInput{
				OrgID:          testOrgID,
				ProjectID:      testProjectID,
				MachinePoolID:  machinePool.ID,
				IdempotencyKey: "idem-launch-config-replace-grant-1",
			},
			defaultMachineOverlayFieldsForTest{
				DefaultMachineEnvOverlay:             json.RawMessage(`{"GRANT":"one","SHARED":"grant-one"}`),
				DefaultMachineProviderOptionsOverlay: json.RawMessage(`{"image":"grant-one"}`),
			},
		))

	if err != nil {
		t.Fatalf("create first pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-config-replace",
		"Launch Config Replace Agent",
		`
name: Launch Config Replace Agent
instruction: Use a mutable project machine pool grant.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    env_overlay:
      SOURCE: overlay
tools:
  run_command: {}
`,
		now,
	)

	firstLaunch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-config-replace-1",
		},
	)
	if err != nil {
		t.Fatalf("launch first agent: %v", err)
	}
	if len(firstLaunch.MachineBindings) != 1 {
		t.Fatalf("first launch bindings = %+v, want one", firstLaunch.MachineBindings)
	}
	firstMachine, err := store.Execution().GetMachine(ctx, testOrgID, firstLaunch.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get first generated machine: %v", err)
	}
	firstProvisioning := testMachineProvisioning(t, 4, 8192, map[string]any{"image": "grant-one"})
	firstEnvironment := executionstore.MachineEnvironment{
		Env: map[string]string{"POOL": "base", "GRANT": "one", "SHARED": "grant-one"},
	}
	requireMachineProvisioningForTest(
		t,
		machineProvisioningFromRecordForTest(t, firstMachine),
		firstProvisioning,
	)
	requireMachineEnvironmentForTest(
		t,
		machineEnvironmentFromRecordForTest(t, firstMachine),
		firstEnvironment,
	)
	if !sameJSON(
		firstLaunch.MachineBindings[0].EnvOverlay,
		json.RawMessage(`{"SOURCE":"overlay"}`),
	) {
		t.Fatalf("first binding env overlay = %s", firstLaunch.MachineBindings[0].EnvOverlay)
	}

	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		firstGrant.ID,
	); err != nil {
		t.Fatalf("revoke first pool grant: %v", err)
	}
	secondGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		projectGrantInputWithDefaultMachineOverlayForTest(
			executionstore.CreateProjectMachinePoolGrantInput{
				OrgID:          testOrgID,
				ProjectID:      testProjectID,
				MachinePoolID:  machinePool.ID,
				IdempotencyKey: "idem-launch-config-replace-grant-2",
			},
			defaultMachineOverlayFieldsForTest{
				DefaultMachineEnvOverlay:             json.RawMessage(`{"GRANT":"two","SHARED":"grant-two"}`),
				DefaultMachineProviderOptionsOverlay: json.RawMessage(`{"image":"grant-two"}`),
			},
		))

	if err != nil {
		t.Fatalf("create second pool grant: %v", err)
	}

	secondLaunch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-config-replace-2",
		},
	)
	if err != nil {
		t.Fatalf("launch second agent: %v", err)
	}
	if len(secondLaunch.MachineBindings) != 1 {
		t.Fatalf("second launch bindings = %+v, want one", secondLaunch.MachineBindings)
	}
	secondGeneratedGrant := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, secondLaunch.MachineBindings[0].MachineID)
	if secondGeneratedGrant.ProjectMachinePoolGrantID != secondGrant.ID {
		t.Fatalf(
			"second generated grant pool grant = %s, want %s",
			secondGeneratedGrant.ProjectMachinePoolGrantID,
			secondGrant.ID,
		)
	}
	secondMachine, err := store.Execution().GetMachine(ctx, testOrgID, secondLaunch.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get second generated machine: %v", err)
	}
	secondProvisioning := testMachineProvisioning(t, 4, 8192, map[string]any{"image": "grant-two"})
	secondEnvironment := executionstore.MachineEnvironment{
		Env: map[string]string{"POOL": "base", "GRANT": "two", "SHARED": "grant-two"},
	}
	requireMachineProvisioningForTest(
		t,
		machineProvisioningFromRecordForTest(t, secondMachine),
		secondProvisioning,
	)
	requireMachineEnvironmentForTest(
		t,
		machineEnvironmentFromRecordForTest(t, secondMachine),
		secondEnvironment,
	)
	if !sameJSON(
		secondLaunch.MachineBindings[0].EnvOverlay,
		json.RawMessage(`{"SOURCE":"overlay"}`),
	) {
		t.Fatalf("second binding env overlay = %s", secondLaunch.MachineBindings[0].EnvOverlay)
	}
	reloadedFirstMachine, err := store.Execution().GetMachine(ctx, testOrgID, firstMachine.ID)
	if err != nil {
		t.Fatalf("reload first generated machine: %v", err)
	}
	requireMachineProvisioningForTest(
		t,
		machineProvisioningFromRecordForTest(t, reloadedFirstMachine),
		firstProvisioning,
	)
	requireMachineEnvironmentForTest(
		t,
		machineEnvironmentFromRecordForTest(t, reloadedFirstMachine),
		firstEnvironment,
	)
}

func TestLaunchAgentMultiplePoolSourcesKeepSourceOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 12, 30, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "launch-two-pools@example.com", DisplayName: "Launch Two Pools User"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	firstPool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Two Pools A",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"pool-a"}`)},
		5,
		now,
	)
	secondPool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Two Pools B",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"pool-b"}`)},
		5,
		now,
	)
	if firstPool.ID.String() < secondPool.ID.String() {
		firstPool, secondPool = secondPool, firstPool
	}
	firstGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  firstPool.ID,
			IdempotencyKey: "idem-launch-two-pools-grant-1",
		})

	if err != nil {
		t.Fatalf("create first pool grant: %v", err)
	}
	secondGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  secondPool.ID,
			IdempotencyKey: "idem-launch-two-pools-grant-2",
		})

	if err != nil {
		t.Fatalf("create second pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "launch-two-pools", "Launch Two Pools Agent", `
name: Launch Two Pools Agent
instruction: Use independent pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+firstPool.Name+`
    max_machines: 3
    initial_num_machines: 2
    cwd: /workspace/first-pool
    description: First pool machine
  - machine_pool_name: `+secondPool.Name+`
    max_machines: 3
    initial_num_machines: 2
    cwd: /workspace/second-pool
    description: Second pool machine
tools:
  run_command: {}
`, now)

	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-two-pools-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch two-pool agent: %v", err)
	}
	if len(result.MachineBindings) != 4 || len(result.ProvisionMachineIDs) != 4 {
		t.Fatalf("two-pool launch result bindings=%+v provision=%v", result.MachineBindings, result.ProvisionMachineIDs)
	}
	want := []struct {
		cwd     string
		grantID ID
	}{
		{cwd: "/workspace/first-pool", grantID: firstGrant.ID},
		{cwd: "/workspace/first-pool", grantID: firstGrant.ID},
		{cwd: "/workspace/second-pool", grantID: secondGrant.ID},
		{cwd: "/workspace/second-pool", grantID: secondGrant.ID},
	}
	seenRefs := map[string]bool{}
	for index, binding := range result.MachineBindings {
		if binding.Cwd != want[index].cwd || binding.State != "attached" ||
			result.ProvisionMachineIDs[index] != binding.MachineID {
			t.Fatalf(
				"binding %d order/provision mismatch: binding=%+v provision=%v want=%+v",
				index,
				binding,
				result.ProvisionMachineIDs,
				want[index],
			)
		}
		if binding.MachineRef == "" || seenRefs[binding.MachineRef] {
			t.Fatalf("binding %d has missing or duplicate machine ref: %+v", index, result.MachineBindings)
		}
		seenRefs[binding.MachineRef] = true
		generatedGrant := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, binding.MachineID)
		if generatedGrant.ProjectMachinePoolGrantID != want[index].grantID {
			t.Fatalf(
				"binding %d generated grant pool mismatch: grant=%+v want pool grant %s",
				index,
				generatedGrant,
				want[index].grantID,
			)
		}
		if !sameJSON(binding.Metadata, json.RawMessage(`{}`)) {
			t.Fatalf("binding %d metadata = %s, want empty object", index, binding.Metadata)
		}
	}

	replayed, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-two-pools-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay two-pool launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, result.Agent)
}

func TestLaunchAgentZeroInitialPoolValidatesGrantWithoutCreatingMachines(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 13, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "launch-pool-zero@example.com", DisplayName: "Launch Pool Zero User"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Zero Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"zero"}`)},
		1,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-zero-pool-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "launch-zero-pool", "Launch Zero Pool Agent", `
name: Launch Zero Pool Agent
instruction: Use a pool later.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 0
tools:
  run_command: {}
`, now)

	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-zero-pool-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch zero-initial pool agent: %v", err)
	}
	if len(result.MachineBindings) != 0 || len(result.ProvisionMachineIDs) != 0 {
		t.Fatalf(
			"zero-initial pool created rows: bindings=%+v provision=%v",
			result.MachineBindings,
			result.ProvisionMachineIDs,
		)
	}
	executable, err := store.Execution().ListExecutableAgentMachineBindings(ctx, testProjectID, result.Agent.ID)
	if err != nil {
		t.Fatalf("list zero-initial executable bindings: %v", err)
	}
	if len(executable) != 0 {
		t.Fatalf("zero-initial pool should not create executable bindings: %+v", executable)
	}
	replayed, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-zero-pool-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay zero-initial pool launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, result.Agent)

	ungrantedPool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Zero Ungranted Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"ungranted"}`)},
		1,
		now,
	)
	ungrantedPoolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  ungrantedPool.ID,
			IdempotencyKey: "idem-launch-zero-ungranted-pool-grant",
		})

	if err != nil {
		t.Fatalf("create ungranted pool grant: %v", err)
	}
	ungrantedProfile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-zero-ungranted-pool",
		"Launch Zero Ungranted Pool Agent",
		`
name: Launch Zero Ungranted Pool Agent
instruction: Use a pool later.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+ungrantedPool.Name+`
    max_machines: 1
    initial_num_machines: 0
tools:
  run_command: {}
`,
		now,
	)
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		ungrantedPoolGrant.ID,
	); err != nil {
		t.Fatalf("revoke ungranted pool grant: %v", err)
	}
	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      ungrantedProfile.ID,
			AgentConfigID:  ungrantedProfile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-zero-ungranted-pool-agent",
		},
	); !storeerr.IsNotFound(
		err,
	) {
		t.Fatalf("expected zero-initial ungranted pool to fail, got %v", err)
	}
	var failedAgents int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agents WHERE project_id = $1 AND idempotency_key = 'idem-launch-zero-ungranted-pool-agent'`, testProjectID).
		Scan(&failedAgents); err != nil {
		t.Fatalf("count failed zero-initial agents: %v", err)
	}
	if failedAgents != 0 {
		t.Fatalf("failed zero-initial ungranted launch left agent rows behind: %d", failedAgents)
	}
}

func TestLaunchAgentZeroInitialPoolSkipsCapacityCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 14, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-zero-capacity@example.com",
			DisplayName: "Launch Pool Zero Capacity User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Zero Capacity Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"zero-capacity"}`)},
		1,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-zero-capacity-pool-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	if result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-zero-capacity-fill",
		"Launch Zero Capacity Fill Agent",
		"idem-launch-zero-capacity-fill-agent",
		now,
	); len(
		result.MachineBindings,
	) != 1 {
		t.Fatalf("fill pool capacity result = %+v", result)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-zero-capacity-pool",
		"Launch Zero Capacity Pool Agent",
		`
name: Launch Zero Capacity Pool Agent
instruction: Use a pool later.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 1
    initial_num_machines: 0
tools:
  run_command: {}
`,
		now,
	)

	result, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-zero-capacity-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch zero-initial pool at capacity: %v", err)
	}
	if len(result.MachineBindings) != 0 || len(result.ProvisionMachineIDs) != 0 {
		t.Fatalf(
			"zero-initial pool created rows at capacity: bindings=%+v provision=%v",
			result.MachineBindings,
			result.ProvisionMachineIDs,
		)
	}
}

func TestLaunchAgentInitialPoolCapacityRollsBackAllRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 14, 30, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-initial-capacity@example.com",
			DisplayName: "Launch Pool Initial Capacity User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Initial Capacity Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"initial-capacity"}`)},
		1,
		now,
	)
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-initial-capacity-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-initial-capacity-pool",
		"Launch Initial Capacity Pool Agent",
		`
name: Launch Initial Capacity Pool Agent
instruction: Use too many pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 2
    initial_num_machines: 2
tools:
  run_command: {}
`,
		now,
	)

	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-initial-capacity-agent",
		},
	); !errors.Is(
		err,
		storeerr.ErrStateTransitionConflict,
	) {
		t.Fatalf("launch initial capacity error = %v, want ErrStateTransitionConflict", err)
	}
	var agents, machines, grants, bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agents WHERE project_id = $1 AND idempotency_key = 'idem-launch-initial-capacity-agent'`, testProjectID).
		Scan(&agents); err != nil {
		t.Fatalf("count rolled back agents: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM machines WHERE org_id = $1 AND machine_pool_id = $2`, testOrgID, machinePool.ID).
		Scan(&machines); err != nil {
		t.Fatalf("count pool machines: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM project_machine_grants WHERE project_id = $1 AND project_machine_pool_grant_id = $2`, testProjectID, poolGrant.ID).
		Scan(&grants); err != nil {
		t.Fatalf("count generated grants: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agent_machine_bindings WHERE project_id = $1`, testProjectID).
		Scan(&bindings); err != nil {
		t.Fatalf("count machine bindings: %v", err)
	}
	if agents != 0 || machines != 0 || grants != 0 || bindings != 0 {
		t.Fatalf(
			"initial capacity rollback counts agents=%d machines=%d grants=%d bindings=%d, want all zero",
			agents,
			machines,
			grants,
			bindings,
		)
	}
}

func TestLaunchAgentPoolCPUCapacityRollsBackAllRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 14, 45, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-cpu-capacity@example.com",
			DisplayName: "Launch Pool CPU Capacity User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	grantMaxTotalCPU := 4
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             "Launch CPU Capacity Pool",
					Provider:         "test",
					MaxTotalMachines: 5,
					MaxTotalCPU:      &grantMaxTotalCPU,
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             2,
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"cpu-capacity"}`),
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
			MaxTotalCPU:    &grantMaxTotalCPU,
			IdempotencyKey: "idem-launch-cpu-capacity-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	maxTotalCPU := 3
	maxMachineCPU := 3
	if _, err := store.Execution().UpdateMachinePool(
		ctx,
		executionstore.UpdateMachinePoolInput{
			OrgID:         testOrgID,
			ID:            machinePool.ID,
			MaxTotalCPU:   patch.NullableInt{Set: true, Value: &maxTotalCPU},
			MaxMachineCPU: patch.NullableInt{Set: true, Value: &maxMachineCPU},
		},
	); err != nil {
		t.Fatalf("lower machine pool cpu cap: %v", err)
	}
	sourceYAML := `
name: Launch CPU Capacity Pool Agent
instruction: Use too much cpu.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: ` + machinePool.Name + `
    max_machines: 2
    initial_num_machines: 2
tools:
  run_command: {}
`
	compiled := mustCompileAgentYAMLWithMachineSourceResolvers(t, ctx, store, sourceYAML)
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(compiled.CanonicalJSON),
		agentconfig.CompilerVersion,
		compiled.Hash,
	); err != nil {
		t.Fatalf("validate agent config over the pool cpu budget: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-cpu-capacity-pool",
		"Launch CPU Capacity Pool Agent",
		sourceYAML,
		now.Add(2*time.Second),
	)

	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-cpu-capacity-agent",
		},
	); !errors.Is(
		err,
		storeerr.ErrStateTransitionConflict,
	) {
		t.Fatalf("launch cpu capacity error = %v, want ErrStateTransitionConflict", err)
	}
	var agents, machines, grants, bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agents WHERE project_id = $1 AND idempotency_key = 'idem-launch-cpu-capacity-agent'`, testProjectID).
		Scan(&agents); err != nil {
		t.Fatalf("count rolled back agents: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM machines WHERE org_id = $1 AND machine_pool_id = $2`, testOrgID, machinePool.ID).
		Scan(&machines); err != nil {
		t.Fatalf("count pool machines: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM project_machine_grants WHERE project_id = $1 AND project_machine_pool_grant_id = $2`, testProjectID, poolGrant.ID).
		Scan(&grants); err != nil {
		t.Fatalf("count generated grants: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agent_machine_bindings WHERE project_id = $1`, testProjectID).
		Scan(&bindings); err != nil {
		t.Fatalf("count machine bindings: %v", err)
	}
	if agents != 0 || machines != 0 || grants != 0 || bindings != 0 {
		t.Fatalf(
			"cpu capacity rollback counts agents=%d machines=%d grants=%d bindings=%d, want all zero",
			agents,
			machines,
			grants,
			bindings,
		)
	}
}

func TestLaunchAgentProjectPoolGrantCapacityIgnoresRevokedGrantDeletingUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 14, 47, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-project-pool-capacity-replace@example.com",
			DisplayName: "Launch Project Pool Capacity Replace User",
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
					Name:             "Launch Project Pool Capacity Replace Pool",
					Provider:         "test",
					MaxTotalMachines: 5,
					MaxTotalCPU:      intPtrForMachinePoolTest(10),
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             2,
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"project-capacity-replace"}`),
				},
			),
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	maxCPU := 2
	firstGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			MaxTotalCPU:    &maxCPU,
			IdempotencyKey: "idem-launch-project-pool-capacity-replace-grant-1",
		})

	if err != nil {
		t.Fatalf("create first pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-project-pool-capacity-replace",
		"Launch Project Pool Capacity Replace Agent",
		`
name: Launch Project Pool Capacity Replace Agent
instruction: Use project pool capacity.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
tools:
  run_command: {}
`,
		now,
	)

	firstLaunch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-project-pool-capacity-replace-1",
		},
	)
	if err != nil {
		t.Fatalf("launch first agent: %v", err)
	}
	if len(firstLaunch.MachineBindings) != 1 {
		t.Fatalf("first launch bindings = %+v, want one", firstLaunch.MachineBindings)
	}
	if _, err := store.Execution().DeleteProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		firstGrant.ID,
	); err != nil {
		t.Fatalf("revoke first pool grant: %v", err)
	}
	firstMachine, err := store.Execution().GetMachine(ctx, testOrgID, firstLaunch.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get first machine after revoke: %v", err)
	}
	if firstMachine.LifecycleState != "deleting" {
		t.Fatalf("first machine lifecycle after grant revoke = %q, want deleting", firstMachine.LifecycleState)
	}
	secondGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			MaxTotalCPU:    &maxCPU,
			IdempotencyKey: "idem-launch-project-pool-capacity-replace-grant-2",
		})

	if err != nil {
		t.Fatalf("create second pool grant: %v", err)
	}
	secondLaunch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-project-pool-capacity-replace-2",
		},
	)
	if err != nil {
		t.Fatalf("second launch after grant replacement: %v", err)
	}
	if len(secondLaunch.MachineBindings) != 1 {
		t.Fatalf("second launch bindings = %+v, want one", secondLaunch.MachineBindings)
	}
	secondGeneratedGrant := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, secondLaunch.MachineBindings[0].MachineID)
	if secondGeneratedGrant.ProjectMachinePoolGrantID != secondGrant.ID {
		t.Fatalf(
			"second generated grant pool grant = %s, want %s",
			secondGeneratedGrant.ProjectMachinePoolGrantID,
			secondGrant.ID,
		)
	}
}

func TestLaunchAgentPoolPerMachineCPUCapacityRollsBackAllRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 14, 50, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-per-machine-cpu-capacity@example.com",
			DisplayName: "Launch Pool Per Machine CPU Capacity User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	maxCPU := 10
	maxMachineCPU := 1
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             "Launch Per Machine CPU Capacity Pool",
					Provider:         "test",
					MaxTotalMachines: 5,
					MaxTotalCPU:      &maxCPU,
					MaxMachineCPU:    &maxMachineCPU,
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"per-machine-cpu-capacity"}`),
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
			IdempotencyKey: "idem-launch-per-machine-cpu-capacity-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-per-machine-cpu-capacity-pool",
		"Launch Per Machine CPU Capacity Pool Agent",
		`
name: Launch Per Machine CPU Capacity Pool Agent
instruction: Use too much cpu on one machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 1
    initial_num_machines: 1
    machine_cpu: 2
tools:
  run_command: {}
`,
		now,
	)

	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-per-machine-cpu-capacity-agent",
		},
	); !errors.Is(
		err,
		storeerr.ErrStateTransitionConflict,
	) {
		t.Fatalf("launch per-machine cpu capacity error = %v, want ErrStateTransitionConflict", err)
	}
	var agents, machines, grants, bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agents WHERE project_id = $1 AND idempotency_key = 'idem-launch-per-machine-cpu-capacity-agent'`, testProjectID).
		Scan(&agents); err != nil {
		t.Fatalf("count rolled back agents: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM machines WHERE org_id = $1 AND machine_pool_id = $2`, testOrgID, machinePool.ID).
		Scan(&machines); err != nil {
		t.Fatalf("count pool machines: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM project_machine_grants WHERE project_id = $1 AND project_machine_pool_grant_id = $2`, testProjectID, poolGrant.ID).
		Scan(&grants); err != nil {
		t.Fatalf("count generated grants: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agent_machine_bindings WHERE project_id = $1`, testProjectID).
		Scan(&bindings); err != nil {
		t.Fatalf("count machine bindings: %v", err)
	}
	if agents != 0 || machines != 0 || grants != 0 || bindings != 0 {
		t.Fatalf(
			"per-machine cpu capacity rollback counts agents=%d machines=%d grants=%d bindings=%d, want all zero",
			agents,
			machines,
			grants,
			bindings,
		)
	}
}

func TestConcurrentLaunchesRespectPoolCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 15, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-concurrent-capacity@example.com",
			DisplayName: "Launch Pool Concurrent Capacity User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Concurrent Capacity Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"concurrent-capacity"}`)},
		1,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-concurrent-capacity-pool-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-concurrent-capacity-pool",
		"Launch Concurrent Capacity Pool Agent",
		`
name: Launch Concurrent Capacity Pool Agent
instruction: Use one pool machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 1
    initial_num_machines: 1
tools:
  run_command: {}
`,
		now,
	)

	start := make(chan struct{})
	type launchOutcome struct {
		result executionstore.LaunchAgentResult
		err    error
	}
	outcomes := make(chan launchOutcome, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := store.Execution().LaunchAgent(
				ctx,
				executionstore.LaunchAgentInput{
					ProjectID:      testProjectID,
					ProfileID:      profile.ID,
					AgentConfigID:  profile.CurrentConfigID,
					LaunchedBy:     userPrincipal(user.ID),
					IdempotencyKey: fmt.Sprintf("idem-launch-concurrent-capacity-agent-%d", index),
				},
			)
			outcomes <- launchOutcome{result: result, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	var successes, capacityFailures int
	for outcome := range outcomes {
		switch {
		case outcome.err == nil:
			successes++
			if len(outcome.result.MachineBindings) != 1 || len(outcome.result.ProvisionMachineIDs) != 1 {
				t.Fatalf("successful launch did not allocate one pool machine: %+v", outcome.result)
			}
		case errors.Is(outcome.err, storeerr.ErrStateTransitionConflict):
			capacityFailures++
		default:
			t.Fatalf("unexpected concurrent launch error: %v", outcome.err)
		}
	}
	if successes != 1 || capacityFailures != 1 {
		t.Fatalf(
			"concurrent launch outcomes successes=%d capacityFailures=%d, want 1 each",
			successes,
			capacityFailures,
		)
	}
	var machines, agents int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM machines WHERE org_id = $1 AND machine_pool_id = $2 AND deleted_at IS NULL`, testOrgID, machinePool.ID).
		Scan(&machines); err != nil {
		t.Fatalf("count concurrent pool machines: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agents WHERE project_id = $1`, testProjectID).
		Scan(&agents); err != nil {
		t.Fatalf("count concurrent agents: %v", err)
	}
	if machines != 1 || agents != 1 {
		t.Fatalf("concurrent launch persisted machines=%d agents=%d, want one of each", machines, agents)
	}
}

func TestPoolLaunchMachineProvisioningActivatesBindingAfterDaemonRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 15, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"launch-pool-runtime@example.com",
		"Launch Pool Runtime User",
	)
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Runtime Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	_, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-runtime-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-runtime-pool",
		"Launch Runtime Pool Agent",
		"idem-launch-runtime-pool-agent",
		now,
	)
	initialMachine, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get initial pool machine: %v", err)
	}
	if result.MachineBindings[0].State != "attached" || initialMachine.LifecycleState != "provisioning" {
		t.Fatalf("initial binding=%+v machine=%+v", result.MachineBindings[0], initialMachine)
	}
	poolDefaultProvisioning, err := executionstore.MachineProvisioningFromDefaults(
		machinePool.DefaultMachineCPU,
		machinePool.DefaultMachineMemoryMB,
		machinePool.DefaultMachineProviderOptions,
	)
	if err != nil {
		t.Fatalf("parse expected pool machine provisioning: %v", err)
	}
	resolvedProvisioning, err := store.Execution().ResolveMachineProvisioning(
		machinePool.Provider,
		executionstore.MachinePoolProviderPolicy{
			DefaultProvisioning: poolDefaultProvisioning,
			ResourceLimits: executionstore.MachineResourceLimits{
				MaxTotalCPU:        machinePool.MaxTotalCPU,
				MaxTotalMemoryMB:   machinePool.MaxTotalMemoryMB,
				MaxMachineCPU:      machinePool.MaxMachineCPU,
				MaxMachineMemoryMB: machinePool.MaxMachineMemoryMB,
			},
			ProviderConfig: machinePool.ProviderConfig,
		},
		executionstore.MachineProvisioningOverlay{},
		executionstore.MachineProvisioningOverlay{},
	)
	if err != nil {
		t.Fatalf("resolve expected pool machine provisioning: %v", err)
	}
	poolEnvironment, err := executionstore.MachineEnvironmentFromColumns(
		machinePool.DefaultMachineEnv,
		machinePool.DefaultMachineSecretEnv,
	)
	if err != nil {
		t.Fatalf("parse expected pool machine environment: %v", err)
	}
	if initialMachine.SourceKind != "pool" || initialMachine.MachinePoolID != machinePool.ID ||
		initialMachine.Provider != machinePool.Provider {
		t.Fatalf("unexpected launched pool machine: %+v pool=%+v", initialMachine, machinePool)
	}
	requireMachineProvisioningForTest(
		t,
		machineProvisioningFromRecordForTest(t, initialMachine),
		resolvedProvisioning,
	)
	requireMachineEnvironmentForTest(
		t,
		machineEnvironmentFromRecordForTest(t, initialMachine),
		poolEnvironment,
	)
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	if claimed.LifecycleState != "provisioning" || claimed.ProvisionAttempts != 1 {
		t.Fatalf("claimed machine = %+v", claimed)
	}
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			ProvisionAttempt: claimed.ProvisionAttempts,
			TokenName:        "test bootstrap",
		},
	)
	if err != nil {
		t.Fatalf("create system machine daemon token: %v", err)
	}
	if providerProvisioning.DaemonToken.Token == "" {
		t.Fatalf(
			"unexpected system token: %+v token=%q",
			providerProvisioning.DaemonToken.Record,
			providerProvisioning.DaemonToken.Token,
		)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		result.MachineBindings[0].MachineID,
		claimed.ProvisionAttempts,
		"test-resource-runtime",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		"test-resource-runtime",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete pool machine provisioning: %v", err)
	}
	provisioned, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get provisioned machine: %v", err)
	}
	if provisioned.LifecycleState != "active" || provisioned.ConnectionState != "offline" ||
		provisioned.ProviderResourceID != "test-resource-runtime" {
		t.Fatalf("provisioned machine = %+v", provisioned)
	}
	if _, err := store.Execution().BootstrapMachineDaemon(
		ctx,
		executionstore.MachineDaemonBootstrapInput{
			OrgID:         testOrgID,
			MachineID:     result.MachineBindings[0].MachineID,
			DaemonTokenID: providerProvisioning.DaemonToken.Record.ID,
		},
	); err != nil {
		t.Fatalf("bootstrap pool daemon: %v", err)
	}
	if _, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			DaemonTokenID:    providerProvisioning.DaemonToken.Record.ID,
			DaemonInstanceID: testID("daemon-agent-launch"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}
	binding := getAgentMachineBindingForTest(
		t,
		ctx,
		store,
		testProjectID,
		result.Agent.ID,
		result.MachineBindings[0].ID,
	)
	if binding.State != "attached" {
		t.Fatalf("binding after runtime registration = %+v, want attached", binding)
	}
	if _, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, result.Agent.ID, userPrincipal(user.ID)); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list pool machines for cleanup: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != result.MachineBindings[0].MachineID {
		t.Fatalf("cleanup machines = %+v", cleanup)
	}
	if err := store.Execution().CompletePoolMachineDeletion(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		1,
	); err == nil {
		t.Fatal("expected deletion completion before delete intent to fail")
	}
	binding = getAgentMachineBindingForTest(t, ctx, store, testProjectID, result.Agent.ID, result.MachineBindings[0].ID)
	if binding.State != "attached" {
		t.Fatalf("rejected deletion completion changed binding: %+v", binding)
	}
	if _, err := store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     testOrgID,
			MachineID: result.MachineBindings[0].MachineID,
		},
	); !storeerr.IsNotFound(
		err,
	) {
		t.Fatalf("generic delete pool machine error = %v, want not found", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE machines
		SET lifecycle_reason_message = 'stale candidate changed',
		    lifecycle_version = lifecycle_version + 1,
		    updated_at = statement_timestamp()
		WHERE org_id = $1 AND id = $2
	`, testOrgID, result.MachineBindings[0].MachineID); err != nil {
		t.Fatalf("mutate cleanup candidate: %v", err)
	}
	if _, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "test_cleanup",
			LifecycleReasonMessage:   "test cleanup",
			ExpectedLifecycleVersion: cleanup[0].Machine.LifecycleVersion,
		},
	); err != nil ||
		ok {
		t.Fatalf("stale claim pool machine deletion ok=%v err=%v, want false/nil", ok, err)
	}
	current, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get current machine before deleting: %v", err)
	}
	deleting, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "test_cleanup",
			LifecycleReasonMessage:   "test cleanup",
			ExpectedLifecycleVersion: current.LifecycleVersion,
		},
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine deletion ok=%v err=%v", ok, err)
	} else if deleting.Machine.LifecycleReasonCode != "test_cleanup" ||
		deleting.Machine.LifecycleReasonMessage != "test cleanup" {
		t.Fatalf(
			"deleting reason code=%q message=%q, want test cleanup",
			deleting.Machine.LifecycleReasonCode,
			deleting.Machine.LifecycleReasonMessage,
		)
	}
	if _, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "test_cleanup",
			LifecycleReasonMessage:   "test cleanup",
			ExpectedLifecycleVersion: deleting.Machine.LifecycleVersion,
		},
	); err != nil ||
		ok {
		t.Fatalf("second claim pool machine deletion ok=%v err=%v, want false/nil", ok, err)
	}
	if err := store.Execution().CompletePoolMachineDeletion(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		deleting.Machine.DeleteAttempts,
	); err != nil {
		t.Fatalf("complete pool machine deletion: %v", err)
	}
	deletedMachine, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get deleted machine: %v", err)
	}
	if deletedMachine.LifecycleState != "deleted" || deletedMachine.DeletedAt == nil {
		t.Fatalf("deleted machine = %+v", deletedMachine)
	}
	binding = getAgentMachineBindingForTest(t, ctx, store, testProjectID, result.Agent.ID, result.MachineBindings[0].ID)
	if binding.State != "released" {
		t.Fatalf("binding after cleanup = %+v, want released", binding)
	}
	if count := countProjectMachineGrantsForMachineForTest(t, ctx, store, testOrgID, testProjectID, result.MachineBindings[0].MachineID); count != 0 {
		t.Fatalf("generated grants after cleanup = %d, want 0", count)
	}
}

func TestPoolProvisionMaxAttemptsCleanupOnlyClaimsStaleProvisioning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 18, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-max-attempts@example.com",
			DisplayName: "Launch Pool Max Attempts User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Max Attempts Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	_, err = store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-max-attempts-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-max-attempts-pool",
		"Launch Max Attempts Pool Agent",
		"idem-launch-max-attempts-agent",
		now,
	)
	if _, err := pool.Exec(ctx, `
		UPDATE machines
		SET lifecycle_state = 'provision_failed',
		    provision_attempts = $3,
		    next_reconcile_after = $4,
		    updated_at = $4
		WHERE org_id = $1 AND id = $2
	`, testOrgID, result.MachineBindings[0].MachineID, executionstore.DefaultPoolMachineProvisionFailureLimit, now.Add(2*time.Second)); err != nil {
		t.Fatalf("seed exhausted provision failure: %v", err)
	}
	provisioning, err := store.Execution().ListPoolMachinesForProvisioning(ctx, 10)
	if err != nil {
		t.Fatalf("list pool machines for provisioning: %v", err)
	}
	if len(provisioning) != 0 {
		t.Fatalf("exhausted provision machine should not be retryable: %+v", provisioning)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list pool machines for cleanup: %v", err)
	}
	if len(cleanup) != 0 {
		t.Fatalf("exhausted provision_failed machine should not be cleanup candidate: %+v", cleanup)
	}
	if _, err := pool.Exec(ctx, `
			UPDATE machines
			SET lifecycle_state = 'provisioning',
			    provision_attempts = $3,
			    next_reconcile_after = $4,
			    updated_at = $4
			WHERE org_id = $1 AND id = $2
		`, testOrgID, result.MachineBindings[0].MachineID, executionstore.DefaultPoolMachineProvisionFailureLimit, now.Add(-3*time.Minute)); err != nil {
		t.Fatalf("seed exhausted stale provisioning: %v", err)
	}
	provisioning, err = store.Execution().ListPoolMachinesForProvisioning(ctx, 10)
	if err != nil {
		t.Fatalf("list pool machines for provisioning after stale exhaustion: %v", err)
	}
	if len(provisioning) != 0 {
		t.Fatalf("exhausted stale provisioning machine should not be retryable: %+v", provisioning)
	}
	cleanup, err = store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup after stale exhaustion: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != result.MachineBindings[0].MachineID {
		t.Fatalf("cleanup for exhausted stale provisioning machines = %+v", cleanup)
	}
	if deleting, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "provisioning_stale_cleanup",
			LifecycleReasonMessage:   "cleaning up stale provisioning attempt",
			ExpectedLifecycleVersion: cleanup[0].Machine.LifecycleVersion,
		},
	); err != nil ||
		!ok {
		t.Fatalf("claim exhausted stale provisioning machine deletion ok=%v err=%v", ok, err)
	} else if deleting.Machine.LifecycleState != "deleting" {
		t.Fatalf("exhausted stale provisioning delete claim state = %s, want deleting", deleting.Machine.LifecycleState)
	} else if deleting.Machine.LifecycleReasonCode != "provisioning_stale_cleanup" {
		t.Fatalf(
			"exhausted stale provisioning delete reason = %s, want provisioning_stale_cleanup",
			deleting.Machine.LifecycleReasonCode,
		)
	}
}

func TestPoolProvisioningAttemptFenceRejectsStaleCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 19, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-stale-attempt@example.com",
			DisplayName: "Launch Pool Stale Attempt User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Stale Attempt Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	_, err = store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-stale-attempt-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-stale-attempt-pool",
		"Launch Stale Attempt Pool Agent",
		"idem-launch-stale-attempt-agent",
		now,
	)
	firstClaim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	); err != nil ||
		ok {
		t.Fatalf("early second claim ok=%v err=%v, want false/nil", ok, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines
		 SET next_reconcile_after = statement_timestamp() - interval '1 second',
		     failure_report = '{"stage":"startup_script"}'::jsonb
		 WHERE org_id = $1 AND id = $2`,
		testOrgID,
		result.MachineBindings[0].MachineID,
	); err != nil {
		t.Fatalf("make provisioning claim due: %v", err)
	}
	secondClaim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("second stale claim ok=%v err=%v", ok, err)
	}
	if firstClaim.Machine.ProvisionAttempts != 1 || secondClaim.Machine.ProvisionAttempts != 2 {
		t.Fatalf("unexpected attempts first=%+v second=%+v", firstClaim, secondClaim)
	}
	var failureReport *json.RawMessage
	if err := pool.QueryRow(
		ctx,
		`SELECT failure_report FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		result.MachineBindings[0].MachineID,
	).Scan(&failureReport); err != nil {
		t.Fatalf("load failure report after retry claim: %v", err)
	}
	if failureReport != nil {
		t.Fatalf("failure report after retry claim = %s, want null", *failureReport)
	}
	if _, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			ProvisionAttempt: firstClaim.Machine.ProvisionAttempts,
			TokenName:        "stale bootstrap",
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale provider provisioning start error = %v, want state transition conflict", err)
	}
	tokens, err := store.Execution().ListAllMachineDaemonTokens(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil {
		t.Fatalf("list tokens after stale provider provisioning start: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("stale provider provisioning start created tokens: %+v", tokens)
	}
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			ProvisionAttempt: secondClaim.Machine.ProvisionAttempts,
			TokenName:        "current bootstrap",
		},
	)
	if err != nil {
		t.Fatalf("begin current provider provisioning: %v", err)
	}
	if providerProvisioning.ProviderProvisionAttemptedAt.IsZero() ||
		providerProvisioning.DaemonToken.Token == "" {
		t.Fatalf("current provider provisioning start = %+v", providerProvisioning)
	}
	if _, err := store.Execution().RecordPoolMachineProvisioningResource(
		ctx,
		executionstore.RecordPoolMachineProvisioningResourceInput{
			OrgID:              testOrgID,
			MachineID:          result.MachineBindings[0].MachineID,
			ProviderResourceID: "stale-resource",
			ProvisionAttempt:   firstClaim.Machine.ProvisionAttempts,
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale provider resource observation error = %v, want state transition conflict", err)
	}
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		"stale-resource",
		"",
		firstClaim.Machine.ProvisionAttempts,
	); err == nil {
		t.Fatal("expected stale provisioning completion to fail")
	}
	if err := store.Execution().MarkPoolMachineProvisionFailed(
		ctx,
		executionstore.PoolMachineProvisionFailureInput{
			OrgID:                  testOrgID,
			MachineID:              result.MachineBindings[0].MachineID,
			ProvisionAttempt:       firstClaim.Machine.ProvisionAttempts,
			LifecycleReasonCode:    "stale_failure",
			LifecycleReasonMessage: "stale failure",
			RetryDelay:             3 * time.Minute,
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale provisioning failure error = %v, want ErrStateTransitionConflict", err)
	}
	current, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get current machine: %v", err)
	}
	if current.LifecycleState != "provisioning" || current.ProvisionAttempts != secondClaim.Machine.ProvisionAttempts ||
		current.ProviderResourceID != "" || current.ProviderProvisionAttemptedAt == nil {
		t.Fatalf("stale attempt changed machine: %+v", current)
	}
	seedProviderRuntimeMismatchForTest(t, ctx, pool, result.MachineBindings[0].MachineID)
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		result.MachineBindings[0].MachineID,
		secondClaim.Machine.ProvisionAttempts,
		"current-resource",
	)
	assertProviderRuntimeMismatchClearedForTest(
		t,
		ctx,
		pool,
		result.MachineBindings[0].MachineID,
	)
	replayed, err := store.Execution().RecordPoolMachineProvisioningResource(
		ctx,
		executionstore.RecordPoolMachineProvisioningResourceInput{
			OrgID:              testOrgID,
			MachineID:          result.MachineBindings[0].MachineID,
			ProviderResourceID: "current-resource",
			ProvisionAttempt:   secondClaim.Machine.ProvisionAttempts,
		},
	)
	if err != nil || replayed.ProviderResourceID != "current-resource" {
		t.Fatalf("replay current provider resource observation = %+v err=%v", replayed, err)
	}
	if _, err := store.Execution().RecordPoolMachineProvisioningResource(
		ctx,
		executionstore.RecordPoolMachineProvisioningResourceInput{
			OrgID:              testOrgID,
			MachineID:          result.MachineBindings[0].MachineID,
			ProviderResourceID: "different-resource",
			ProvisionAttempt:   secondClaim.Machine.ProvisionAttempts,
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("different provider resource observation error = %v, want state transition conflict", err)
	}
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		"current-resource",
		"",
		secondClaim.Machine.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete current provisioning attempt: %v", err)
	}
	provisioned, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get current provisioned machine: %v", err)
	}
	if provisioned.LifecycleState != "active" || provisioned.ConnectionState != "offline" ||
		provisioned.ProviderResourceID != "current-resource" {
		t.Fatalf("provisioned current attempt = %+v", provisioned)
	}
}

func TestPoolDeleteFailureFenceRejectsStaleFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 19, 15, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"launch-pool-stale-delete@example.com",
		"Launch Pool Stale Delete User",
	)
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Stale Delete Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	_, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-stale-delete-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-stale-delete-pool",
		"Launch Stale Delete Pool Agent",
		"idem-launch-stale-delete-agent",
		now,
	)
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	beginAndRecordPoolMachineProvisioningForTest(
		t,
		ctx,
		store,
		result.MachineBindings[0].MachineID,
		claimed.ProvisionAttempts,
		"stale-delete-resource",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		"stale-delete-resource",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete pool machine provisioning: %v", err)
	}
	if _, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, result.Agent.ID, userPrincipal(user.ID)); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup machines: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != result.MachineBindings[0].MachineID {
		t.Fatalf("cleanup machines = %+v", cleanup)
	}
	firstDeleting, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      cleanup[0].ReasonCode,
			LifecycleReasonMessage:   cleanup[0].ReasonMessage,
			ExpectedLifecycleVersion: cleanup[0].Machine.LifecycleVersion,
		},
	)
	if err != nil || !ok {
		t.Fatalf("first claim pool machine deletion ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET next_reconcile_after = statement_timestamp() - interval '1 second' WHERE org_id = $1 AND id = $2`,
		testOrgID,
		result.MachineBindings[0].MachineID,
	); err != nil {
		t.Fatalf("make deletion claim due: %v", err)
	}
	secondDeleting, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "deleting_retry",
			LifecycleReasonMessage:   "retrying machine deletion",
			ExpectedLifecycleVersion: firstDeleting.Machine.LifecycleVersion,
		},
	)
	if err != nil || !ok {
		t.Fatalf("second stale claim pool machine deletion ok=%v err=%v", ok, err)
	}
	if err := store.Execution().MarkMachineDeleteFailed(
		ctx,
		executionstore.MachineDeleteFailureInput{
			OrgID:                  testOrgID,
			MachineID:              result.MachineBindings[0].MachineID,
			LifecycleReasonCode:    "stale_delete_failure",
			LifecycleReasonMessage: "stale delete failure",
			RetryDelay:             3 * time.Minute,
			DeleteAttempt:          firstDeleting.Machine.DeleteAttempts,
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale delete failure error = %v, want ErrStateTransitionConflict", err)
	}
	current, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get current machine: %v", err)
	}
	if current.LifecycleState != "deleting" || !current.UpdatedAt.Equal(secondDeleting.Machine.UpdatedAt) ||
		current.LifecycleReasonCode != "deleting_retry" {
		t.Fatalf("stale delete failure changed machine: %+v", current)
	}
	retryDelay := 10 * time.Minute
	if err := store.Execution().MarkMachineDeleteFailed(
		ctx,
		executionstore.MachineDeleteFailureInput{
			OrgID:                  testOrgID,
			MachineID:              result.MachineBindings[0].MachineID,
			LifecycleReasonCode:    "provider_delete_error",
			LifecycleReasonMessage: "provider delete failed",
			RetryDelay:             retryDelay,
			DeleteAttempt:          secondDeleting.Machine.DeleteAttempts,
		},
	); err != nil {
		t.Fatalf("mark machine delete failed: %v", err)
	}
	failed, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get delete failed machine: %v", err)
	}
	if failed.LifecycleState != "delete_failed" || failed.NextReconcileAfter == nil ||
		failed.NextReconcileAfter.Sub(failed.UpdatedAt) != retryDelay {
		t.Fatalf("delete failed machine = %+v", failed)
	}
	if _, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "delete_failed_retry",
			LifecycleReasonMessage:   "retrying failed machine deletion",
			ExpectedLifecycleVersion: failed.LifecycleVersion,
		},
	); err != nil ||
		ok {
		t.Fatalf("early delete_failed claim ok=%v err=%v, want false/nil", ok, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET next_reconcile_after = statement_timestamp() - interval '1 second' WHERE id = $1`,
		result.MachineBindings[0].MachineID,
	); err != nil {
		t.Fatalf("age delete retry: %v", err)
	}
	retryDeleting, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "delete_failed_retry",
			LifecycleReasonMessage:   "retrying failed machine deletion",
			ExpectedLifecycleVersion: failed.LifecycleVersion,
		},
	)
	if err != nil || !ok {
		t.Fatalf("due delete_failed claim ok=%v err=%v", ok, err)
	}
	if retryDeleting.Machine.LifecycleState != "deleting" ||
		retryDeleting.Machine.DeleteAttempts != failed.DeleteAttempts+1 ||
		retryDeleting.Machine.LifecycleReasonCode != "delete_failed_retry" {
		t.Fatalf("retry deleting machine = %+v", retryDeleting)
	}
}

func TestOfflinePoolMachineWithoutRuntimeHistoryMovesToCleanupQueueAfterBootstrapTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 18, 30, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-bootstrap-timeout@example.com",
			DisplayName: "Launch Pool Bootstrap Timeout User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Bootstrap Timeout Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	_, err = store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-bootstrap-timeout-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-bootstrap-timeout-pool",
		"Launch Bootstrap Timeout Pool Agent",
		"idem-launch-bootstrap-timeout-agent",
		now,
	)
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	beginAndRecordPoolMachineProvisioningForTest(
		t,
		ctx,
		store,
		result.MachineBindings[0].MachineID,
		claimed.ProvisionAttempts,
		"bootstrap-timeout-resource",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		"bootstrap-timeout-resource",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete pool machine provisioning: %v", err)
	}
	provisioned, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get provisioned machine: %v", err)
	}
	if provisioned.LifecycleState != "active" || provisioned.ConnectionState != "offline" {
		t.Fatalf("provisioned machine = %+v, want active/offline", provisioned)
	}

	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup before bootstrap timeout: %v", err)
	}
	if len(cleanup) != 0 {
		t.Fatalf("machine should not be cleanup candidate before bootstrap timeout: %+v", cleanup)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE machines
		SET lifecycle_changed_at = statement_timestamp() - $2::bigint * interval '1 second'
		WHERE org_id = $1 AND id = $3
	`, testOrgID, int64(executionstore.StaleMachineBootstrapAge/time.Second)+1, result.MachineBindings[0].MachineID); err != nil {
		t.Fatalf("age offline machine past bootstrap timeout: %v", err)
	}

	cleanup, err = store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup after bootstrap timeout: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != result.MachineBindings[0].MachineID {
		t.Fatalf("cleanup after bootstrap timeout = %+v", cleanup)
	}
	if cleanup[0].ReasonCode != "startup_or_daemon_bootstrap_failed" ||
		cleanup[0].ReasonMessage != "cleaning up machine because startup script or daemon bootstrap did not complete" {
		t.Fatalf(
			"cleanup reason = %s/%q, want startup or daemon bootstrap failure",
			cleanup[0].ReasonCode,
			cleanup[0].ReasonMessage,
		)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE machines
		SET lifecycle_reason_message = 'registered after cleanup list',
		    lifecycle_version = lifecycle_version + 1,
		    lifecycle_changed_at = statement_timestamp(),
		    updated_at = statement_timestamp()
		WHERE org_id = $1 AND id = $2
	`, testOrgID, result.MachineBindings[0].MachineID); err != nil {
		t.Fatalf("mutate cleanup candidate: %v", err)
	}
	if _, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                result.MachineBindings[0].MachineID,
			LifecycleReasonCode:      "startup_or_daemon_bootstrap_failed",
			LifecycleReasonMessage:   "cleaning up machine because startup script or daemon bootstrap did not complete",
			ExpectedLifecycleVersion: cleanup[0].Machine.LifecycleVersion,
		},
	); err != nil ||
		ok {
		t.Fatalf("stale offline cleanup claim pool machine deletion ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestOfflinePoolMachineWithRuntimeHistoryDoesNotBootstrapTimeoutCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 18, 45, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-bootstrap-history@example.com",
			DisplayName: "Launch Pool Bootstrap History User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Bootstrap History Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	_, err = store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-bootstrap-history-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-bootstrap-history-pool",
		"Launch Bootstrap History Pool Agent",
		"idem-launch-bootstrap-history-agent",
		now,
	)
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			ProvisionAttempt: claimed.ProvisionAttempts,
			TokenName:        "test bootstrap",
		},
	)
	if err != nil {
		t.Fatalf("create system machine daemon token: %v", err)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		result.MachineBindings[0].MachineID,
		claimed.ProvisionAttempts,
		"bootstrap-history-resource",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		"bootstrap-history-resource",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete pool machine provisioning: %v", err)
	}
	if _, err := store.Execution().BootstrapMachineDaemon(
		ctx,
		executionstore.MachineDaemonBootstrapInput{
			OrgID:         testOrgID,
			MachineID:     result.MachineBindings[0].MachineID,
			DaemonTokenID: providerProvisioning.DaemonToken.Record.ID,
		},
	); err != nil {
		t.Fatalf("bootstrap pool daemon: %v", err)
	}
	if _, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			DaemonTokenID:    providerProvisioning.DaemonToken.Record.ID,
			DaemonInstanceID: testID("daemon-bootstrap-history"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}

	cleanup, err := store.Execution().ListPoolMachinesForCleanup(
		ctx,
		executionstore.DefaultPoolMachineProvisionFailureLimit,
		10,
	)
	if err != nil {
		t.Fatalf("list cleanup after bootstrap timeout: %v", err)
	}
	if len(cleanup) != 0 {
		t.Fatalf("machine with runtime history should not be bootstrap cleanup candidate: %+v", cleanup)
	}
}

func TestSystemBootstrapTokenRetryDoesNotRevokePriorToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 19, 30, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-token-retry@example.com",
			DisplayName: "Launch Pool Token Retry User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Token Retry Pool",
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	_, err = store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-token-retry-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	result := launchPoolAgentForTest(
		t,
		ctx,
		store,
		user.ID,
		machinePool,
		"launch-token-retry-pool",
		"Launch Token Retry Pool Agent",
		"idem-launch-token-retry-agent",
		now,
	)
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	firstProviderProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			ProvisionAttempt: claimed.ProvisionAttempts,
			TokenName:        "test bootstrap 1",
		},
	)
	if err != nil {
		t.Fatalf("create first system token: %v", err)
	}
	secondProviderProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			ProvisionAttempt: claimed.ProvisionAttempts,
			TokenName:        "test bootstrap 2",
		},
	)
	if err != nil {
		t.Fatalf("create second system token: %v", err)
	}
	tokens, err := store.Execution().ListAllMachineDaemonTokens(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("list machine daemon tokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("tokens = %+v, want two bootstrap tokens", tokens)
	}
	for _, token := range tokens {
		if token.ID != firstProviderProvisioning.DaemonToken.Record.ID &&
			token.ID != secondProviderProvisioning.DaemonToken.Record.ID {
			t.Fatalf("unexpected token in list: %+v", token)
		}
		if token.RevokedAt != nil {
			t.Fatalf("system bootstrap token should remain usable before runtime registration: %+v", token)
		}
	}
	byoTokenPage, err := store.Execution().ListBYOMachineDaemonTokens(
		ctx,
		executionstore.ListBYOMachineDaemonTokensInput{OrgID: testOrgID, MachineID: result.MachineBindings[0].MachineID, Limit: 10},
	)
	if err != nil {
		t.Fatalf("list BYO machine daemon tokens: %v", err)
	}
	byoTokens := byoTokenPage.Tokens
	if len(byoTokens) != 0 {
		t.Fatalf("BYO token list should hide system bootstrap tokens: %+v", byoTokens)
	}
	if _, err := store.Execution().RevokeBYOMachineDaemonToken(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		firstProviderProvisioning.DaemonToken.Record.ID,
		"revoked",
	); !storeerr.IsNotFound(
		err,
	) {
		t.Fatalf("revoke system bootstrap token through BYO path error = %v, want not found", err)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		result.MachineBindings[0].MachineID,
		claimed.ProvisionAttempts,
		"resource-token-retry",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		result.MachineBindings[0].MachineID,
		"resource-token-retry",
		"",
		int32(1),
	); err != nil {
		t.Fatalf("complete pool machine provisioning: %v", err)
	}
	provisioned, err := store.Execution().GetMachine(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("get provisioned machine: %v", err)
	}
	if provisioned.LifecycleState != "active" || provisioned.ConnectionState != "offline" {
		t.Fatalf(
			"provisioned machine state = %s/%s, want active/offline",
			provisioned.LifecycleState,
			provisioned.ConnectionState,
		)
	}
	if _, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        result.MachineBindings[0].MachineID,
			DaemonTokenID:    secondProviderProvisioning.DaemonToken.Record.ID,
			DaemonInstanceID: testID("daemon-agent-launch-retry"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("register daemon runtime with second token: %v", err)
	}
	tokens, err = store.Execution().ListAllMachineDaemonTokens(ctx, testOrgID, result.MachineBindings[0].MachineID)
	if err != nil {
		t.Fatalf("list machine daemon tokens after registration: %v", err)
	}
	for _, token := range tokens {
		switch token.ID {
		case secondProviderProvisioning.DaemonToken.Record.ID:
			if token.RevokedAt != nil {
				t.Fatalf("active system bootstrap token was revoked: %+v", token)
			}
		case firstProviderProvisioning.DaemonToken.Record.ID:
			if token.RevokedAt == nil || token.RevokeReason != "replaced_by_registered_runtime" {
				t.Fatalf("stale system bootstrap token was not revoked: %+v", token)
			}
		default:
			t.Fatalf("unexpected token after registration: %+v", token)
		}
	}
}

func TestLaunchAgentWithPoolGrantRejectsCapacityAndRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 9, 20, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "launch-pool-capacity@example.com",
			DisplayName: "Launch Pool Capacity User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Launch Capacity Pool",
		"test.provider",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-launch-capacity-pool-grant",
		})

	if err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"launch-capacity-pool",
		"Launch Capacity Pool Agent",
		`
name: Launch Capacity Pool Agent
instruction: Use a project machine pool.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
tools:
  run_command: {}
`,
		now,
	)
	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-capacity-first",
		},
	); err != nil {
		t.Fatalf("first pool launch: %v", err)
	}
	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-launch-capacity-second",
		},
	); !errors.Is(
		err,
		storeerr.ErrStateTransitionConflict,
	) {
		t.Fatalf("second pool launch error = %v, want ErrStateTransitionConflict", err)
	}
	var agents, machines, grants, bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agents WHERE project_id = $1 AND idempotency_key = 'idem-launch-capacity-second'`, testProjectID).
		Scan(&agents); err != nil {
		t.Fatalf("count rolled back agents: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM machines WHERE org_id = $1 AND machine_pool_id = $2`, testOrgID, machinePool.ID).
		Scan(&machines); err != nil {
		t.Fatalf("count pool machines: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM project_machine_grants WHERE project_id = $1 AND project_machine_pool_grant_id = $2`, testProjectID, poolGrant.ID).
		Scan(&grants); err != nil {
		t.Fatalf("count generated grants: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM agent_machine_bindings WHERE project_id = $1`, testProjectID).
		Scan(&bindings); err != nil {
		t.Fatalf("count machine bindings: %v", err)
	}
	if agents != 0 || machines != 1 || grants != 1 || bindings != 1 {
		t.Fatalf(
			"capacity rollback counts agents=%d machines=%d grants=%d bindings=%d, want 0/1/1/1",
			agents,
			machines,
			grants,
			bindings,
		)
	}
}

func TestClaimNormalModelCallValidatesActiveAgentConfigAtWatermark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"model-context-config@example.com",
		"Model Context Config User",
	)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"model-context-config",
		"Model Context Config Agent",
		`
name: Model Context Config Agent
instruction: Keep config watermarks typed.
model:
  provider_config: openai-prod
  name: model-context-config
`,
		now,
	)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-model-context-config-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch model-context-config agent: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        launch.Agent.ID,
			Actor:          mustOmnaraActorParams(t, user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"build context"}]`),
			IdempotencyKey: "model-context-config-input",
		},
	)
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(claim, input.ID) {
		t.Fatalf(
			"claim input found=%v kind=%v input_ids=%v want %s",
			found,
			claim.Kind,
			claim.Model.InputIDs,
			input.ID,
		)
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	config := launch.AgentConfig
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		testProjectID,
		launch.Agent.ID,
		admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture active config at admitted frontier: %v", err)
	}
	if snapshot.AgentConfig.ID != config.ID {
		t.Fatalf("config at frontier = %s, want %s", snapshot.AgentConfig.ID, config.ID)
	}
	newConfig := mustCreateAgentConfigFromYAML(t, ctx, store, "model-context-config-changed", `
name: Model Context Config Agent
instruction: Keep changed config typed.
model:
  provider_config: openai-prod
  name: model-context-config
`, now.Add(8*time.Second))
	changed, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(newConfig),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                user.ID,
		IdempotencyKey:         "idem-model-context-config-change",
	})
	if err != nil {
		t.Fatalf("change agent config: %v", err)
	}
	if _, err := store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            launch.Agent.ID,
			RuntimeLockID:      lock.ID,
			OpeningInputIDs:    []ID{input.ID},
			AgentConfigID:      snapshot.AgentConfig.ID,
			InputEventSequence: snapshot.InputEventSequence,
		},
	); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("stale model context frontier error = %v, want ErrAgentNotAdvanceable", err)
	}
	var staleContexts int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts
		WHERE project_id = $1 AND agent_id = $2 AND input_event_sequence = $3`,
		testProjectID,
		launch.Agent.ID,
		snapshot.InputEventSequence,
	).Scan(&staleContexts); err != nil {
		t.Fatalf("count stale model contexts: %v", err)
	}
	if staleContexts != 0 {
		t.Fatalf("stale model contexts = %d, want none", staleContexts)
	}
	if _, err := store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            launch.Agent.ID,
			RuntimeLockID:      lock.ID,
			OpeningInputIDs:    []ID{input.ID},
			AgentConfigID:      config.ID,
			InputEventSequence: changed.ConfigChange.Event.Sequence,
		},
	); !errors.Is(
		err,
		storeerr.ErrStateTransitionConflict,
	) {
		t.Fatalf("stale model context config error = %v, want ErrStateTransitionConflict", err)
	}
	if _, err := store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            launch.Agent.ID,
			RuntimeLockID:      lock.ID,
			OpeningInputIDs:    []ID{input.ID},
			AgentConfigID:      changed.AgentConfig.ID,
			InputEventSequence: changed.ConfigChange.Event.Sequence,
		},
	); err != nil {
		t.Fatalf("current model context config error = %v", err)
	}
}

func TestModelCallRetryUsesCurrentConfiguredModelRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"model-context-revision@example.com",
		"Model Context Revision User",
	)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"model-context-revision",
		"Model Context Revision Agent",
		`
name: Model Context Revision Agent
instruction: Keep resolved revisions durable.
model:
  provider_config: openai-prod
  name: model-context-revision
`,
		now,
	)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-model-context-revision-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch model-context-revision agent: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        launch.Agent.ID,
			Actor:          mustOmnaraActorParams(t, user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"build context"}]`),
			IdempotencyKey: "model-context-revision-input",
		},
	)
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launch.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		store,
		testProjectID,
		launch.Agent.ID,
		lock.ID,
	)
	if !found {
		t.Fatal("expected input admission")
	}
	configuredModel, err := store.Models().GetConfiguredModel(ctx, testOrgID, launch.AgentConfig.ConfiguredModelID)
	if err != nil {
		t.Fatalf("load configured model: %v", err)
	}
	initialClaim, err := store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            launch.Agent.ID,
			RuntimeLockID:      lock.ID,
			OpeningInputIDs:    []ID{input.ID},
			AgentConfigID:      launch.AgentConfig.ID,
			InputEventSequence: admitted.Events[0].Sequence,
		},
	)
	if err != nil {
		t.Fatalf("claim initial model context: %v", err)
	}
	if !initialClaim.Created || !initialClaim.Claimed || initialClaim.Context.AttemptNumber != 1 ||
		initialClaim.Context.ConfiguredModelRevisionID != configuredModel.CurrentRevisionID {
		t.Fatalf("initial model context = %+v, want attempt 1 on revision %s", initialClaim, configuredModel.CurrentRevisionID)
	}
	if _, err := store.Execution().RecordRetryableModelCallFailure(ctx, executionstore.RecordRecoverableModelCallFailureInput{
		ProjectID:          testProjectID,
		AgentID:            launch.Agent.ID,
		ModelCallContextID: initialClaim.Context.ID,
		RuntimeLockID:      lock.ID,
		ErrorKind:          "transient",
		ErrorCode:          "test_retry_before_revision_change",
		ErrorMessage:       "retry after configured model revision changes",
	}); err != nil {
		t.Fatalf("record retryable model failure: %v", err)
	}

	updatedProviderSlug := "model-context-revision-updated"
	updated, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: configuredModel.ModelProviderConfigID,
		ID:                    configuredModel.ID,
		ProviderModelSlug:     &updatedProviderSlug,
	})
	if err != nil {
		t.Fatalf("patch configured model: %v", err)
	}
	if updated.CurrentRevisionID == configuredModel.CurrentRevisionID {
		t.Fatal("configured model patch did not create a new revision")
	}
	retryClaim, err := store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     testProjectID,
		AgentID:                       launch.Agent.ID,
		PredecessorModelCallContextID: initialClaim.Context.ID,
		RuntimeLockID:                 lock.ID,
	})
	if err != nil {
		t.Fatalf("claim same-frontier retry: %v", err)
	}
	if !retryClaim.Created || !retryClaim.Claimed || retryClaim.Context.AttemptNumber != 2 ||
		retryClaim.Context.InputEventSequence != initialClaim.Context.InputEventSequence ||
		retryClaim.Context.ConfiguredModelRevisionID != updated.CurrentRevisionID {
		t.Fatalf("same-frontier retry = %+v, want attempt 2 on current revision %s", retryClaim, updated.CurrentRevisionID)
	}
	if err := executionstore.IntegrationValidateResponseEnvelopeForModelCallContext(
		ctx,
		store.q,
		modelenvelope.ResponseEnvelope{RequestedProviderModelSlug: updated.ProviderModelSlug},
		retryClaim.Context,
	); err != nil {
		t.Fatalf("validate current retry provider slug: %v", err)
	}
	if err := executionstore.IntegrationValidateResponseEnvelopeForModelCallContext(
		ctx,
		store.q,
		modelenvelope.ResponseEnvelope{RequestedProviderModelSlug: configuredModel.ProviderModelSlug},
		retryClaim.Context,
	); err == nil {
		t.Fatalf("same-frontier retry accepted stale provider slug %q instead of current slug %q", configuredModel.ProviderModelSlug, updated.ProviderModelSlug)
	}
	if _, err := store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: testProjectID,
		AgentID:   launch.Agent.ID,
		Actor:     mustOmnaraActorParams(t, user.ID),
	}); err != nil {
		t.Fatalf("cancel retrying frontier: %v", err)
	}
	if err := store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, launch.Agent.ID, lock.ID); err != nil {
		t.Fatalf("release canceled runtime: %v", err)
	}

	nextInput, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        launch.Agent.ID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"build a new frontier"}]`),
		IdempotencyKey: "model-context-revision-new-frontier",
	})
	if err != nil {
		t.Fatalf("create new-frontier input: %v", err)
	}
	nextLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launch.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire new-frontier runtime: %v", err)
	}
	nextAdmitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		store,
		testProjectID,
		launch.Agent.ID,
		nextLock.ID,
	)
	if !found {
		t.Fatal("expected new-frontier input admission")
	}
	newFrontierClaim, err := store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            launch.Agent.ID,
		RuntimeLockID:      nextLock.ID,
		OpeningInputIDs:    []ID{nextInput.ID},
		AgentConfigID:      launch.AgentConfig.ID,
		InputEventSequence: nextAdmitted.Events[0].Sequence,
	})
	if err != nil {
		t.Fatalf("claim model context at new frontier: %v", err)
	}
	if !newFrontierClaim.Created || !newFrontierClaim.Claimed ||
		newFrontierClaim.Context.AttemptNumber != 1 ||
		newFrontierClaim.Context.ConfiguredModelRevisionID != updated.CurrentRevisionID {
		t.Fatalf("new-frontier context = %+v, want attempt 1 on revision %s", newFrontierClaim, updated.CurrentRevisionID)
	}
	if err := executionstore.IntegrationValidateResponseEnvelopeForModelCallContext(
		ctx,
		store.q,
		modelenvelope.ResponseEnvelope{RequestedProviderModelSlug: updated.ProviderModelSlug},
		newFrontierClaim.Context,
	); err != nil {
		t.Fatalf("validate new-frontier provider slug: %v", err)
	}
}

func TestClaimNormalModelCallDoesNotPinProjectModelGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"model-grant-context@example.com",
		"Model Grant Context User",
	)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"model-grant-context",
		"Model Grant Context Agent",
		`
name: Model Grant Context Agent
instruction: Keep runtime model grants enforced.
model:
  provider_config: openai-prod
  name: model-grant-context
`,
		now,
	)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-model-grant-context-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch model-grant-context agent: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        launch.Agent.ID,
			Actor:          mustOmnaraActorParams(t, user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"build context"}]`),
			IdempotencyKey: "model-grant-context-input",
		},
	)
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launch.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		store,
		testProjectID,
		launch.Agent.ID,
		lock.ID,
	)
	if !found {
		t.Fatal("expected input admission")
	}
	config := launch.AgentConfig
	grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		testOrgID,
		testProjectID,
		config.ConfiguredModelID,
	)
	if err != nil {
		t.Fatalf("load active model grant: %v", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, grant.ID); err != nil {
		t.Fatalf("revoke model grant: %v", err)
	}
	claim, err := store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            launch.Agent.ID,
			RuntimeLockID:      lock.ID,
			OpeningInputIDs:    []ID{input.ID},
			AgentConfigID:      config.ID,
			InputEventSequence: admitted.Events[0].Sequence,
		},
	)
	if err != nil {
		t.Fatalf("claim context after grant revocation: %v", err)
	}
	if claim.Context.State != executionstore.ModelCallContextStarted {
		t.Fatalf("context should exist before live grant preflight: %+v", claim)
	}
}

func TestRetargetAgentProfileAndLaunchLineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 5, 22, 9, 0, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "retarget@example.com", DisplayName: "Retarget User"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "retarget", "Retarget Agent", `
name: Retarget Agent
instruction: First instruction.
model:
  provider_config: openai-prod
  name: gpt-test
`, now)
	originalConfigID := profile.CurrentConfigID
	if profile.CurrentGeneration != 1 {
		t.Fatalf("expected initial generation 1, got %d", profile.CurrentGeneration)
	}

	retargetInput := func(sourceYAML string, expectedCurrentConfigID ID) executionstore.RetargetAgentProfileInput {
		config := mustCreateAgentConfigFromYAML(
			t,
			ctx,
			store,
			"retarget-"+expectedCurrentConfigID.String()+sourceYAML,
			sourceYAML,
			now.Add(time.Second),
		)
		return executionstore.RetargetAgentProfileInput{
			ProjectID:               testProjectID,
			ProfileID:               profile.ID,
			ExpectedCurrentConfigID: expectedCurrentConfigID,
			ConfigID:                config.ID,
		}
	}

	updated, err := store.Execution().RetargetAgentProfile(ctx, retargetInput(`
name: Retarget Agent
instruction: Second instruction.
model:
  provider_config: openai-prod
  name: gpt-test
`, originalConfigID))
	if err != nil {
		t.Fatalf("retarget agent profile: %v", err)
	}
	if updated.CurrentGeneration != 2 || updated.CurrentConfigID == originalConfigID {
		t.Fatalf(
			"expected generation 2 with new config, got generation %d config change %v",
			updated.CurrentGeneration,
			updated.CurrentConfigID != originalConfigID,
		)
	}
	retargetedConfigID := updated.CurrentConfigID
	generation1, err := store.q.GetAgentProfileVersionByGeneration(
		ctx,
		dbsqlc.GetAgentProfileVersionByGenerationParams{ProjectID: testProjectID, ProfileID: profile.ID, Generation: 1},
	)
	if err != nil {
		t.Fatalf("load profile generation 1: %v", err)
	}
	if generation1.AgentConfigID != originalConfigID || generation1.Reason != "create" {
		t.Fatalf("generation 1 = %+v, want original create version", generation1)
	}
	generation2, err := store.q.GetAgentProfileVersionByGeneration(
		ctx,
		dbsqlc.GetAgentProfileVersionByGenerationParams{ProjectID: testProjectID, ProfileID: profile.ID, Generation: 2},
	)
	if err != nil {
		t.Fatalf("load profile generation 2: %v", err)
	}
	if generation2.AgentConfigID != retargetedConfigID || generation2.Reason != "retarget" {
		t.Fatalf("generation 2 = %+v, want retarget version", generation2)
	}

	noop, err := store.Execution().RetargetAgentProfile(ctx, retargetInput(`
name: Retarget Agent
instruction: Second instruction.
model:
  provider_config: openai-prod
  name: gpt-test
`, retargetedConfigID))
	if err != nil {
		t.Fatalf("no-op retarget agent profile: %v", err)
	}
	if noop.CurrentGeneration != 2 || noop.CurrentConfigID != retargetedConfigID {
		t.Fatalf("expected no-op retarget to keep generation 2, got %d", noop.CurrentGeneration)
	}
	if _, err := store.Execution().RetargetAgentProfile(ctx, retargetInput(`
name: Retarget Agent
instruction: Stale writer instruction.
model:
  provider_config: openai-prod
  name: gpt-test
`, originalConfigID)); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale retarget should fail with ErrStateTransitionConflict, got %v", err)
	}

	retargetedLaunch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  retargetedConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "retargeted",
			IdempotencyKey: "idem-launch-retargeted",
		},
	)
	if err != nil {
		t.Fatalf("launch retargeted config: %v", err)
	}
	if retargetedLaunch.Agent.CurrentConfigID != retargetedConfigID {
		t.Fatalf(
			"retargeted launch should use requested config, got %s want %s",
			retargetedLaunch.Agent.CurrentConfigID,
			retargetedConfigID,
		)
	}

	pinned, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  originalConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "pinned",
			IdempotencyKey: "idem-launch-pinned",
		},
	)
	if err != nil {
		t.Fatalf("launch pinned to original config: %v", err)
	}
	if pinned.Agent.CurrentConfigID != originalConfigID {
		t.Fatalf(
			"pinned launch should use selected config, got %s want %s",
			pinned.Agent.CurrentConfigID,
			originalConfigID,
		)
	}
	configOnly, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			AgentConfigID:  retargetedConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "config only",
			IdempotencyKey: "idem-launch-config-only",
		},
	)
	if err != nil {
		t.Fatalf("launch config-only agent: %v", err)
	}
	if configOnly.Agent.CurrentConfigID != retargetedConfigID || configOnly.Agent.Name != "Retarget Agent" {
		t.Fatalf("config-only launch should use requested config and config name, got agent=%+v", configOnly.Agent)
	}

	other := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "retarget-other", "Other Agent", `
name: Other Agent
instruction: Unrelated profile.
model:
  provider_config: openai-prod
  name: gpt-test
`, now)
	if _, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  other.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			Message:        "foreign",
			IdempotencyKey: "idem-launch-foreign",
		},
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("launch with foreign config should fail lineage check with ErrNotFound, got %v", err)
	}
}
