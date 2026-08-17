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

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestListMachinePoolSourcesUsesCapturedNamesAfterSwap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC)

	machinePools := make([]executionstore.MachinePoolRecord, 2)
	for index, name := range []string{"First Pool", "Second Pool"} {
		machinePools[index] = createLaunchTestMachinePool(
			t,
			ctx,
			store,
			name,
			"test.provider",
			defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
			2,
			now.Add(time.Duration(index)*time.Second),
		)
		if _, err := store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePools[index].ID,
			IdempotencyKey: fmt.Sprintf("idem-agent-pool-name-swap-grant-%d", index),
		}); err != nil {
			t.Fatalf("create pool grant: %v", err)
		}
	}
	firstPool, secondPool := machinePools[0], machinePools[1]
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-name-swap-config",
		fmt.Sprintf(`
instruction: Use pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: %s
    max_machines: 1
    initial_num_machines: 0
  - machine_pool_name: %s
    max_machines: 1
    initial_num_machines: 0
tools:
  create_machine:
    type: built_in
`, firstPool.Name, secondPool.Name),
		now.Add(4*time.Second),
	)
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID:       testProjectID,
		CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	renamePool := func(machinePoolID ID, name string) {
		t.Helper()
		if _, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
			OrgID: testOrgID,
			ID:    machinePoolID,
			Name:  &name,
		}); err != nil {
			t.Fatalf("rename machine pool: %v", err)
		}
	}
	renamePool(firstPool.ID, "Temporary Pool")
	renamePool(secondPool.ID, firstPool.Name)
	renamePool(firstPool.ID, secondPool.Name)

	sources, err := store.Execution().ListMachinePoolSources(ctx, testProjectID, agent.ID, config.ID)
	if err != nil {
		t.Fatalf("list machine pool sources: %v", err)
	}
	if len(sources) != 2 ||
		sources[0].MachinePoolName != "First Pool" || sources[0].MachinePoolID != firstPool.ID ||
		sources[1].MachinePoolName != "Second Pool" || sources[1].MachinePoolID != secondPool.ID {
		t.Fatalf("machine pool sources after name swap = %+v", sources)
	}
}

func TestCreatePoolMachineUsesCurrentSourceWhilePoolRemainsConfigured(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-config@example.com",
		"Agent Pool Machine Config")

	capturedPool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Captured Pool",
		"test.provider",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"captured"}`)},
		2,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  capturedPool.ID,
			IdempotencyKey: "idem-agent-pool-machine-captured-grant",
		}); err != nil {
		t.Fatalf("create captured pool grant: %v", err)
	}
	capturedConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-captured-config",
		agentPoolMachineConfigYAMLWithDefaultMachineFields(capturedPool.Name, 2, `
    cwd: /captured
    description: Captured source
    machine_provider_options_overlay:
      startup_script: captured
`),
		now.Add(3*time.Second),
	)
	currentConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-current-config",
		agentPoolMachineConfigYAMLWithDefaultMachineFields(capturedPool.Name, 1, `
    cwd: /current
    description: Current source
    machine_provider_options_overlay:
      startup_script: current
`),
		now.Add(4*time.Second),
	)
	removedConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-removed-config",
		testAgentConfigYAML(),
		now.Add(4500*time.Millisecond),
	)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: capturedConfig.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		capturedConfig.ID,
		lock,
		"captured-config",
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "rollback", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "stale", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)
	activateAgentConfigForPoolMachineTest(
		t,
		ctx,
		store,
		agent.ID,
		currentConfig.ID,
		"pool-machine-current-config-change",
	)
	currentAgent, err := store.Execution().GetAgentInProject(ctx, testProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load current-config agent: %v", err)
	}
	if currentAgent.CurrentConfigID != currentConfig.ID {
		t.Fatalf("agent current config = %s, want %s", currentAgent.CurrentConfigID, currentConfig.ID)
	}
	t.Run("rolls back command mutation when completion building fails", func(t *testing.T) {
		rollbackErr := errors.New("completion builder failed")
		var rolledBackResult executionstore.CreatePoolMachineResult
		_, err := store.Execution().ExecuteToolCall(
			ctx,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       agent.ID,
				ToolCallID:    toolCalls["rollback"],
				RuntimeLockID: lock.ID,
			},
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.CreatePoolMachineForToolCall(
					executionstore.CreatePoolMachineInput{MachinePoolID: capturedPool.ID},
					func(result executionstore.CreatePoolMachineResult) (executionstore.ToolCallCompletionInput, error) {
						rolledBackResult = result
						return executionstore.ToolCallCompletionInput{}, rollbackErr
					},
				), nil
			},
		)
		if !errors.Is(err, rollbackErr) {
			t.Fatalf("create pool machine completion error = %v, want %v", err, rollbackErr)
		}
		if !rolledBackResult.Created {
			t.Fatalf("pool machine mutation result = %+v, want created before rollback", rolledBackResult)
		}
		if _, err := store.Execution().GetMachine(ctx, testOrgID, rolledBackResult.Machine.Machine.ID); !storeerr.IsNotFound(err) {
			t.Fatalf("load rolled-back pool machine error = %v, want not found", err)
		}
		rolledBackToolCall, err := store.Execution().GetToolCall(
			ctx,
			testProjectID,
			agent.ID,
			toolCalls["rollback"],
		)
		if err != nil {
			t.Fatalf("load rolled-back tool call: %v", err)
		}
		if rolledBackToolCall.State != executionstore.ToolCallStateReady || rolledBackToolCall.RuntimeLockID != NilID {
			t.Fatalf(
				"rolled-back tool call state/runtime = %s/%s, want ready/unowned",
				rolledBackToolCall.State,
				rolledBackToolCall.RuntimeLockID,
			)
		}
		var rolledBackResultCount int
		if err := store.pool.QueryRow(
			ctx,
			`SELECT count(*) FROM tool_call_results WHERE tool_call_id = $1`,
			toolCalls["rollback"],
		).Scan(&rolledBackResultCount); err != nil {
			t.Fatalf("count rolled-back tool results: %v", err)
		}
		if rolledBackResultCount != 0 {
			t.Fatalf("rolled-back tool results = %d, want 0", rolledBackResultCount)
		}
	})
	createTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}
	createInput := executionstore.CreatePoolMachineInput{
		MachinePoolID: capturedPool.ID,
	}
	result, err := createPoolMachineForTest(ctx, store, createTransaction, createInput)
	if err != nil {
		t.Fatalf("create pool machine after config change: %v", err)
	}
	if result.Machine.Binding.Cwd != "/current" || result.Machine.Binding.Description != "Current source" {
		t.Fatalf("created machine binding = %+v, want current source configuration", result.Machine.Binding)
	}
	if !sameJSON(
		result.Machine.Machine.ProviderOptions,
		json.RawMessage(`{"image":"captured","startup_script":"current"}`),
	) {
		t.Fatalf("created machine provider options = %s, want current provisioning", result.Machine.Machine.ProviderOptions)
	}
	staleTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["stale"],
		RuntimeLockID: lock.ID,
	}
	staleInput := executionstore.CreatePoolMachineInput{
		MachinePoolID: capturedPool.ID,
	}
	if _, err := createPoolMachineForTest(ctx, store, staleTransaction, staleInput); !errors.Is(err, storeerr.ErrStateTransitionConflict) ||
		!strings.Contains(err.Error(), "machine pool limit reached") {
		t.Fatalf("stale create after lower max_machines error = %v, want machine pool limit conflict", err)
	}
	activateAgentConfigForPoolMachineTest(
		t,
		ctx,
		store,
		agent.ID,
		removedConfig.ID,
		"pool-machine-removed-config-change",
	)
	if _, err := createPoolMachineForTest(ctx, store, staleTransaction, staleInput); !errors.Is(err, storeerr.ErrStateTransitionConflict) ||
		!strings.Contains(err.Error(), "machine pool is no longer configured") {
		t.Fatalf("stale create after pool source removal error = %v, want source removal conflict", err)
	}
}

func TestCreatePoolMachineUsesResolvedConfigAndCwd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-resolved@example.com",
		"Agent Pool Machine Resolved")

	projectSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "pool-machine-update",
		Material:       secrets.GenericMaterial{Value: "secret-value"},
		Actor:          userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create pool machine update secret: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(ctx, completeMachinePoolCreateInputForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:            testOrgID,
			Name:             "Resolved Pool",
			Provider:         "test.provider",
			DefaultCwd:       "/pool",
			MaxTotalMachines: 3,
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             4,
			DefaultMachineMemoryMB:        8192,
			DefaultMachineEnv:             json.RawMessage(`{"POOL":"base","SHARED":"pool","REMOVE":"pool"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"pool","pool_only":"pool"}`),
		},
	)))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	if _, err := store.Execution().CreateProjectMachinePoolGrant(ctx, projectGrantInputWithDefaultMachineOverlayForTest(
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			DefaultCwd:     "/grant",
			IdempotencyKey: "idem-agent-pool-machine-resolved-grant",
		},
		defaultMachineOverlayFieldsForTest{
			DefaultMachineMemoryMB:               intPtrForMachinePoolTest(4096),
			DefaultMachineEnvOverlay:             json.RawMessage(`{"GRANT":"one","SHARED":"grant","REMOVE":null}`),
			DefaultMachineProviderOptionsOverlay: json.RawMessage(`{"image":"grant","grant_only":"grant"}`),
		},
	)); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-resolved-config",
		agentPoolMachineConfigYAMLWithDefaultMachineFields(machinePool.Name, 2, `
    machine_cpu: 2
    env_overlay:
      AGENT: agent
      SHARED: agent
    machine_provider_options_overlay:
      image: agent
      agent_only: agent
`),
		now.Add(2*time.Second),
	)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: config.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		config.ID,
		lock,
		"resolved",
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)
	sources, err := store.Execution().ListMachinePoolSources(ctx, testProjectID, agent.ID, config.ID)
	if err != nil {
		t.Fatalf("list machine pool sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("machine pool sources = %+v, want one source", sources)
	}

	created, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	})
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}
	if created.Machine.Binding.Cwd != "" {
		t.Fatalf("created binding cwd = %q, want empty", created.Machine.Binding.Cwd)
	}
	if created.Machine.Machine.Cwd != "/grant" {
		t.Fatalf("created machine cwd = %v, want /grant", created.Machine.Machine.Cwd)
	}
	if !sameJSON(created.Machine.Binding.Metadata, json.RawMessage(`{}`)) {
		t.Fatalf("created binding metadata = %s, want empty object", created.Machine.Binding.Metadata)
	}
	if !sameJSON(
		created.Machine.Binding.EnvOverlay,
		json.RawMessage(`{"AGENT":"agent","SHARED":"agent"}`),
	) {
		t.Fatalf("created binding env overlay = %s", created.Machine.Binding.EnvOverlay)
	}
	requireMachineProvisioningForTest(
		t,
		machineProvisioningFromRecordForTest(t, created.Machine.Machine),
		testMachineProvisioning(t, 2, 4096, map[string]any{
			"image": "agent", "pool_only": "pool", "grant_only": "grant", "agent_only": "agent",
		}),
	)
	requireMachineEnvironmentForTest(
		t,
		machineEnvironmentFromRecordForTest(t, created.Machine.Machine),
		executionstore.MachineEnvironment{Env: map[string]string{"POOL": "base", "GRANT": "one", "SHARED": "grant"}},
	)
	cwd := "/mutated"
	env := json.RawMessage(`{"MUTATED":"true"}`)
	secretEnv := json.RawMessage(`{"MUTATED_SECRET":"` + secretPublicIDForTest(t, projectSecret.ID) + `"}`)
	updated, err := store.Execution().UpdateMachine(ctx, executionstore.UpdateMachineInput{
		OrgID:     testOrgID,
		MachineID: created.Machine.Machine.ID,
		Cwd:       &cwd,
		Env:       &env,
		SecretEnv: &secretEnv,
	})
	if err != nil {
		t.Fatalf("mutate pool machine execution defaults: %v", err)
	}
	if updated.Cwd != cwd || !sameJSON(updated.Env, env) || !sameJSON(updated.SecretEnv, secretEnv) {
		t.Fatalf(
			"mutated pool machine execution defaults = %q, %s, %s",
			updated.Cwd,
			updated.Env,
			updated.SecretEnv,
		)
	}
}

func TestCreatePoolMachinePersistsProviderIntentWithoutExternalResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	providers := &externalMachinePoolProviders{}
	store := newIntegrationStore(pool, WithMachinePoolProviders(providers))
	now := time.Date(2026, 6, 15, 9, 40, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-intent@example.com",
		"Agent Pool Machine Intent")

	maxCPU := 2
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:              testOrgID,
					Name:               "Provider Intent Pool",
					Provider:           "external",
					MaxTotalMachines:   2,
					MaxTotalCPU:        &maxCPU,
					MaxTotalMemoryMB:   intPtrForMachinePoolTest(2048),
					MaxMachineCPU:      &maxCPU,
					MaxMachineMemoryMB: intPtrForMachinePoolTest(2048),
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineMemoryMB:        1024,
					DefaultMachineProviderOptions: json.RawMessage(`{"snapshot":"configured"}`),
				},
			),
		),
	)
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-agent-pool-machine-intent-grant",
		},
	); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-intent-config",
		agentPoolMachineConfigYAML(machinePool.Name, 1),
		now.Add(2*time.Second),
	)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: config.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		config.ID,
		lock,
		"intent",
		[]poolMachineToolCallSpec{{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)}},
	)
	created, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	})
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}
	if created.Machine.Machine.CPU != nil || created.Machine.Machine.MemoryMB != nil {
		t.Fatalf(
			"provider-owned resources were persisted before preparation: cpu %v memory %v",
			created.Machine.Machine.CPU,
			created.Machine.Machine.MemoryMB,
		)
	}
	if providers.calls == 0 {
		t.Fatal("provider intent builder was not called")
	}
	for _, update := range []string{
		"id = uuidv7()",
		"org_id = uuidv7()",
		"source_kind = 'byo'",
		"machine_pool_id = NULL",
		"provider = provider || '.changed'",
		`provider_options = '{"changed":true}'::jsonb`,
	} {
		if _, err := pool.Exec(
			ctx,
			fmt.Sprintf("UPDATE machines SET %s WHERE org_id = $1 AND id = $2", update),
			testOrgID,
			created.Machine.Machine.ID,
		); !isPgCode(err, "25006") {
			t.Fatalf("update machine with %q error = %v, want SQLSTATE 25006", update, err)
		}
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET cpu = 2, memory_mb = 2048 WHERE org_id = $1 AND id = $2`,
		testOrgID,
		created.Machine.Machine.ID,
	); err != nil {
		t.Fatalf("persist provider facts once: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET cpu = 4, memory_mb = 4096 WHERE org_id = $1 AND id = $2`,
		testOrgID,
		created.Machine.Machine.ID,
	); err == nil || !strings.Contains(err.Error(), "provisioning columns are immutable") {
		t.Fatalf("rewrite provider facts error = %v, want immutable provisioning columns", err)
	}
}

func TestCreatePoolMachineUsesDefaultPoolSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 9, 40, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-default@example.com",
		"Agent Pool Machine Default")

	defaultPool := createDefaultMachinePoolForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:            testOrgID,
			Provider:         "test.provider",
			DefaultCwd:       "/pool",
			MaxTotalMachines: 3,
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             2,
			DefaultMachineMemoryMB:        2048,
			DefaultMachineEnv:             json.RawMessage(`{"POOL":"base","SHARED":"pool"}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"default","pool_only":"pool"}`),
		},
	))
	if err := provisionDefaultMachinePoolGrantsForProject(
		ctx,
		store,
		testOrgID,
		testProjectID,
	); err != nil {
		t.Fatalf("create default pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(t, ctx, store, "agent-pool-machine-default-config", `
instruction: Use default pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+defaultPool.Name+`
    max_machines: 2
    initial_num_machines: 0
    cwd: /agent
    description: Cluster pool tool machine
    machine_cpu: 1
    machine_memory_mb: 1024
    env_overlay:
      AGENT: "yes"
    machine_provider_options_overlay:
      startup_script: echo ready
tools:
  create_machine: {}
`, now.Add(2*time.Second))
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: config.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		config.ID,
		lock,
		"default",
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)
	sources, err := store.Execution().ListMachinePoolSources(ctx, testProjectID, agent.ID, config.ID)
	if err != nil {
		t.Fatalf("list machine pool sources: %v", err)
	}
	if len(sources) != 1 || sources[0].MachinePoolID != defaultPool.ID ||
		sources[0].MachinePoolName != defaultPool.Name ||
		sources[0].Description != "Cluster pool tool machine" {
		t.Fatalf("default pool sources = %+v, want default pool name %s", sources, defaultPool.Name)
	}
	created, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: defaultPool.ID,
	})
	if err != nil {
		t.Fatalf("create default pool machine: %v", err)
	}
	if created.Machine.Machine.MachinePoolID != defaultPool.ID {
		t.Fatalf(
			"created machine pool = %s, want default pool %s",
			created.Machine.Machine.MachinePoolID,
			defaultPool.ID,
		)
	}
	if created.Machine.Binding.Cwd != "/agent" {
		t.Fatalf("created binding cwd = %q, want /agent", created.Machine.Binding.Cwd)
	}
	if created.Machine.Machine.Cwd != "/pool" {
		t.Fatalf("created machine cwd = %v, want /pool", created.Machine.Machine.Cwd)
	}
	if !sameJSON(
		created.Machine.Binding.EnvOverlay,
		json.RawMessage(`{"AGENT":"yes"}`),
	) {
		t.Fatalf("created binding env overlay = %s", created.Machine.Binding.EnvOverlay)
	}
	requireMachineProvisioningForTest(
		t,
		machineProvisioningFromRecordForTest(t, created.Machine.Machine),
		testMachineProvisioning(t, 1, 1024, map[string]any{
			"image": "default", "pool_only": "pool", "startup_script": "echo ready",
		}),
	)
	requireMachineEnvironmentForTest(
		t,
		machineEnvironmentFromRecordForTest(t, created.Machine.Machine),
		executionstore.MachineEnvironment{Env: map[string]string{"POOL": "base", "SHARED": "pool"}},
	)
}

func TestCreatePoolMachineRejectsResourceCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 9, 45, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-capacity@example.com",
		"Agent Pool Machine Capacity")

	maxCPU := 2
	machinePool, err := store.Execution().CreateMachinePool(ctx, completeMachinePoolCreateInputForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:            testOrgID,
			Name:             "Capacity Pool",
			Provider:         "test.provider",
			MaxTotalMachines: 5,
			MaxTotalCPU:      intPtrForMachinePoolTest(maxCPU),
			MaxMachineCPU:    intPtrForMachinePoolTest(maxCPU),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             2,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"capacity"}`),
		},
	)))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-agent-pool-machine-capacity-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-capacity-config",
		agentPoolMachineConfigYAML(machinePool.Name, 2),
		now.Add(2*time.Second),
	)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: config.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		config.ID,
		lock,
		"capacity",
		[]poolMachineToolCallSpec{
			{Label: "first", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "second", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)
	if _, err := createPoolMachineForTest(
		ctx, store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       agent.ID,
			ToolCallID:    toolCalls["first"],
			RuntimeLockID: lock.ID,
		}, executionstore.CreatePoolMachineInput{
			MachinePoolID: machinePool.ID,
		}); err != nil {
		t.Fatalf("create first pool machine: %v", err)
	}
	_, err = createPoolMachineForTest(
		ctx, store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       agent.ID,
			ToolCallID:    toolCalls["second"],
			RuntimeLockID: lock.ID,
		}, executionstore.CreatePoolMachineInput{
			MachinePoolID: machinePool.ID,
		})
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("second pool machine error = %v, want state transition conflict", err)
	}
}

func TestZeroCapMachinePoolLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"zero-cap-pool-lifecycle@example.com",
		"Zero Cap Pool Lifecycle")

	machinePool, err := store.Execution().CreateMachinePool(ctx, completeMachinePoolCreateInputForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:            testOrgID,
			Name:             "Zero Cap Lifecycle Pool",
			Provider:         "test.provider",
			MaxTotalMachines: 0,
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"zero-cap"}`),
		},
	)))
	if err != nil {
		t.Fatalf("create zero-cap machine pool: %v", err)
	}
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-zero-cap-lifecycle-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	countPoolMachines := func(label string, want int) {
		t.Helper()
		var machines int
		if err := pool.QueryRow(
			ctx,
			`SELECT count(*)::int FROM machines WHERE org_id = $1 AND machine_pool_id = $2`,
			testOrgID,
			machinePool.ID,
		).Scan(&machines); err != nil {
			t.Fatalf("%s: count pool machines: %v", label, err)
		}
		if machines != want {
			t.Fatalf("%s: pool machines = %d, want %d", label, machines, want)
		}
	}

	initialMachinesYAML := `
instruction: Launch a pool machine immediately.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: ` + machinePool.Name + `
    max_machines: 2
    initial_num_machines: 1
tools:
  run_command: {}
`
	compiled := mustCompileAgentYAMLWithMachineSourceResolvers(t, ctx, store, initialMachinesYAML)
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		json.RawMessage(compiled.CanonicalJSON),
		agentconfig.CompilerVersion,
		compiled.Hash,
	); err != nil {
		t.Fatalf("validate config against the zero-cap pool: %v", err)
	}
	initialMachinesProfile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"zero-cap-initial-machines",
		"Zero Cap Initial Machines",
		initialMachinesYAML,
		now.Add(2*time.Second),
	)

	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      initialMachinesProfile.ID,
		AgentConfigID:  initialMachinesProfile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-zero-cap-initial-machines-launch",
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("zero-cap launch error = %v, want ErrStateTransitionConflict", err)
	}
	var launchedAgents int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)::int FROM agents WHERE project_id = $1 AND idempotency_key = 'idem-zero-cap-initial-machines-launch'`,
		testProjectID,
	).Scan(&launchedAgents); err != nil {
		t.Fatalf("count rolled back agents: %v", err)
	}
	if launchedAgents != 0 {
		t.Fatalf("rolled back zero-cap launch left %d agents, want 0", launchedAgents)
	}
	countPoolMachines("after rolled back launch", 0)

	toolMachinesProfile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"zero-cap-tool-machines",
		"Zero Cap Tool Machines",
		agentPoolMachineConfigYAML(machinePool.Name, 2),
		now.Add(4*time.Second),
	)
	launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      toolMachinesProfile.ID,
		AgentConfigID:  toolMachinesProfile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-zero-cap-tool-machines-launch",
	})
	if err != nil {
		t.Fatalf("launch agent without initial machines on the zero-cap pool: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launched.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		launched.Agent.ID,
		user.ID,
		toolMachinesProfile.CurrentConfigID,
		lock,
		"zero-cap",
		[]poolMachineToolCallSpec{
			{Label: "blocked", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "allowed", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)

	if _, err := createPoolMachineForTest(
		ctx, store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       launched.Agent.ID,
			ToolCallID:    toolCalls["blocked"],
			RuntimeLockID: lock.ID,
		}, executionstore.CreatePoolMachineInput{
			MachinePoolID: machinePool.ID,
		}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("zero-cap create_machine error = %v, want ErrStateTransitionConflict", err)
	}
	countPoolMachines("after refused create_machine", 0)

	if _, err := pool.Exec(
		ctx,
		`UPDATE machine_pools SET max_total_machines = 1, updated_at = now() WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machinePool.ID,
	); err != nil {
		t.Fatalf("raise machine pool budget: %v", err)
	}
	created, err := createPoolMachineForTest(
		ctx, store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       launched.Agent.ID,
			ToolCallID:    toolCalls["allowed"],
			RuntimeLockID: lock.ID,
		}, executionstore.CreatePoolMachineInput{
			MachinePoolID: machinePool.ID,
		})
	if err != nil {
		t.Fatalf("create pool machine after raising the budget: %v", err)
	}
	if created.Machine.Machine.MachinePoolID != machinePool.ID {
		t.Fatalf(
			"created machine pool = %s, want %s",
			created.Machine.Machine.MachinePoolID,
			machinePool.ID,
		)
	}
	countPoolMachines("after raised budget", 1)
}

func TestCreatePoolMachineReplayMaxAndDeleteLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-lifecycle@example.com",
		"Agent Pool Machine Lifecycle")

	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Lifecycle Pool",
		"test.provider",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"lifecycle"}`)},
		3,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-agent-pool-machine-lifecycle-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-lifecycle-config",
		agentPoolMachineConfigYAML(machinePool.Name, 1),
		now.Add(2*time.Second),
	)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: config.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		config.ID,
		lock,
		"lifecycle",
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "create_second", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "delete", Name: "delete_machine", Input: json.RawMessage(`{}`)},
			{Label: "replacement", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)
	createTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}
	createInput := executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	}
	created, err := createPoolMachineForTest(ctx, store, createTransaction, createInput)
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}
	if !created.Created || created.Machine.Binding.State != "attached" ||
		created.Machine.Machine.LifecycleState != "provisioning" {
		t.Fatalf("created machine result = %+v", created)
	}
	if created.Machine.Binding.BindingKind != executionstore.MachineBindingKindPool {
		t.Fatalf(
			"created machine binding kind = %q, want %q",
			created.Machine.Binding.BindingKind,
			executionstore.MachineBindingKindPool,
		)
	}
	if created.Machine.Binding.CreateToolCallID != createTransaction.ToolCallID {
		t.Fatalf(
			"created machine binding create tool call = %s, want %s",
			created.Machine.Binding.CreateToolCallID,
			createTransaction.ToolCallID,
		)
	}
	if created.Machine.Binding.DeleteToolCallID != NilID {
		t.Fatalf("created machine binding delete tool call = %s, want nil", created.Machine.Binding.DeleteToolCallID)
	}
	if created.Machine.Machine.IdempotencyKey != "" {
		t.Fatalf("created machine idempotency key = %q, want empty", created.Machine.Machine.IdempotencyKey)
	}
	var generatedGrantIdempotencyKey string
	var generatedGrantMetadata json.RawMessage
	generatedGrant := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, created.Machine.Machine.ID)
	if err := pool.QueryRow(ctx, `SELECT coalesce(idempotency_key, ''), metadata FROM project_machine_grants WHERE project_id = $1 AND id = $2`, testProjectID, generatedGrant.ID).
		Scan(&generatedGrantIdempotencyKey, &generatedGrantMetadata); err != nil {
		t.Fatalf("load generated grant fields: %v", err)
	}
	if generatedGrantIdempotencyKey != "" {
		t.Fatalf("generated grant idempotency key = %q, want empty", generatedGrantIdempotencyKey)
	}
	if !sameJSON(generatedGrantMetadata, json.RawMessage(`{}`)) {
		t.Fatalf("generated grant metadata = %s, want empty object", generatedGrantMetadata)
	}
	replayed, err := createPoolMachineForTest(ctx, store, createTransaction, createInput)
	if err != nil {
		t.Fatalf("replay create pool machine: %v", err)
	}
	if replayed.Created || replayed.Machine.Machine.ID != created.Machine.Machine.ID ||
		replayed.Machine.Binding.ID != created.Machine.Binding.ID {
		t.Fatalf("replay result = %+v, want same machine/binding as %+v", replayed, created)
	}
	if replayed.Machine.Binding.CreateToolCallID != createTransaction.ToolCallID {
		t.Fatalf(
			"replayed machine binding create tool call = %s, want %s",
			replayed.Machine.Binding.CreateToolCallID,
			createTransaction.ToolCallID,
		)
	}
	if _, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["create_second"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("second pool machine error = %v, want state transition conflict", err)
	}
	sources, err := store.Execution().ListMachinePoolSources(ctx, testProjectID, agent.ID, config.ID)
	if err != nil {
		t.Fatalf("list machine pool sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("pool source status = %+v", sources)
	}
	deleted, err := deletePoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["delete"],
		RuntimeLockID: lock.ID,
	}, executionstore.DeletePoolMachineInput{
		MachineRef: created.Machine.Binding.MachineRef,
	})
	if err != nil {
		t.Fatalf("delete pool machine: %v", err)
	}
	if deleted.Machine.LifecycleState != "deleting" || deleted.Machine.LifecycleReasonCode != "machine_tool_delete" {
		t.Fatalf("deleted machine = %+v", deleted.Machine)
	}
	if deleted.Binding.CreateToolCallID != createTransaction.ToolCallID {
		t.Fatalf(
			"delete binding create tool call id = %s, want %s",
			deleted.Binding.CreateToolCallID,
			createTransaction.ToolCallID,
		)
	}
	if deleted.Binding.DeleteToolCallID != toolCalls["delete"] {
		t.Fatalf(
			"delete binding delete tool call id = %s, want %s",
			deleted.Binding.DeleteToolCallID,
			toolCalls["delete"],
		)
	}
	var bindingMetadata map[string]json.RawMessage
	if err := json.Unmarshal(deleted.Binding.Metadata, &bindingMetadata); err != nil {
		t.Fatalf("decode delete metadata: %v", err)
	}
	if _, ok := bindingMetadata["delete_tool_call_id"]; ok {
		t.Fatalf("delete metadata still has delete_tool_call_id: %s", deleted.Binding.Metadata)
	}
	replayedDelete, err := deletePoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["delete"],
		RuntimeLockID: lock.ID,
	}, executionstore.DeletePoolMachineInput{
		MachineRef: created.Machine.Binding.MachineRef,
	})
	if err != nil {
		t.Fatalf("replay delete pool machine: %v", err)
	}
	if replayedDelete.Binding.ID != deleted.Binding.ID ||
		replayedDelete.Binding.DeleteToolCallID != deleted.Binding.DeleteToolCallID {
		t.Fatalf("replayed delete = %+v, want binding %+v", replayedDelete, deleted.Binding)
	}
	replayedCreateAfterDelete, err := createPoolMachineForTest(ctx, store, createTransaction, createInput)
	if err != nil {
		t.Fatalf("replay create pool machine after delete: %v", err)
	}
	if replayedCreateAfterDelete.Created ||
		replayedCreateAfterDelete.Machine.Machine.ID != created.Machine.Machine.ID ||
		replayedCreateAfterDelete.Machine.Binding.ID != created.Machine.Binding.ID {
		t.Fatalf(
			"replay create after delete = %+v, want same machine/binding as %+v",
			replayedCreateAfterDelete,
			created,
		)
	}
	if replayedCreateAfterDelete.Machine.Binding.CreateToolCallID != createTransaction.ToolCallID ||
		replayedCreateAfterDelete.Machine.Binding.DeleteToolCallID != toolCalls["delete"] {
		t.Fatalf(
			"replay create after delete binding tool calls = create %s delete %s, want create %s delete %s",
			replayedCreateAfterDelete.Machine.Binding.CreateToolCallID,
			replayedCreateAfterDelete.Machine.Binding.DeleteToolCallID,
			createTransaction.ToolCallID,
			toolCalls["delete"],
		)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(ctx, executionstore.DefaultPoolMachineProvisionFailureLimit, 10)
	if err != nil {
		t.Fatalf("list cleanup: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != created.Machine.Machine.ID ||
		cleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("cleanup = %+v", cleanup)
	}
	replacementTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["replacement"],
		RuntimeLockID: lock.ID,
	}
	replacementInput := executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	}
	if _, err := createPoolMachineForTest(ctx, store, replacementTransaction, replacementInput); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("replacement pool machine while deleting error = %v, want state transition conflict", err)
	}

	claimedDelete, ok, err := store.Execution().ClaimPoolMachineDeletion(ctx, executionstore.MachineDeletingInput{
		OrgID:                    testOrgID,
		MachineID:                created.Machine.Machine.ID,
		LifecycleReasonCode:      cleanup[0].ReasonCode,
		LifecycleReasonMessage:   cleanup[0].ReasonMessage,
		ExpectedLifecycleVersion: cleanup[0].Machine.LifecycleVersion,
	})
	if err != nil || !ok {
		t.Fatalf("claim pool machine deletion ok=%v err=%v", ok, err)
	}
	if err := store.Execution().MarkMachineDeleteFailed(ctx, executionstore.MachineDeleteFailureInput{
		OrgID:                  testOrgID,
		MachineID:              created.Machine.Machine.ID,
		LifecycleReasonCode:    "provider_delete_error",
		LifecycleReasonMessage: "provider delete failed",
		RetryDelay:             10 * time.Minute,
		DeleteAttempt:          claimedDelete.Machine.DeleteAttempts,
	}); err != nil {
		t.Fatalf("mark machine delete failed: %v", err)
	}
	deleteFailed, err := store.Execution().GetMachine(ctx, testOrgID, created.Machine.Machine.ID)
	if err != nil {
		t.Fatalf("get delete failed machine: %v", err)
	}
	if deleteFailed.LifecycleState != "delete_failed" {
		t.Fatalf("delete failed machine state = %s, want delete_failed", deleteFailed.LifecycleState)
	}
	if _, err := createPoolMachineForTest(ctx, store, replacementTransaction, replacementInput); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("replacement pool machine while delete_failed error = %v, want state transition conflict", err)
	}
}

func TestDeletePoolMachineAllowsFreshProvisioningMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-provisioning-delete@example.com",
		"Agent Pool Machine Provisioning Delete")

	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Provisioning Delete Pool",
		"test.provider",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"provisioning-delete"}`)},
		3,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-agent-pool-machine-provisioning-delete-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-provisioning-delete-config",
		agentPoolMachineConfigYAML(machinePool.Name, 1),
		now.Add(2*time.Second),
	)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: config.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		config.ID,
		lock,
		"provisioning-delete",
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "delete", Name: "delete_machine", Input: json.RawMessage(`{}`)},
		},
	)
	created, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	})
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}
	if created.Machine.Machine.ProvisionAttempts != 0 {
		t.Fatalf("created provisioning attempts = %d, want 0", created.Machine.Machine.ProvisionAttempts)
	}
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		created.Machine.Machine.ID,
	)
	if err != nil || !ok {
		t.Fatalf("claim pool machine for provisioning ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	if claimed.LifecycleState != "provisioning" {
		t.Fatalf("claimed machine state = %s, want provisioning", claimed.LifecycleState)
	}
	deleted, err := deletePoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       agent.ID,
		ToolCallID:    toolCalls["delete"],
		RuntimeLockID: lock.ID,
	}, executionstore.DeletePoolMachineInput{
		MachineRef: created.Machine.Binding.MachineRef,
	})
	if err != nil {
		t.Fatalf("delete provisioning pool machine: %v", err)
	}
	if deleted.Machine.LifecycleState != "deleting" || deleted.Machine.LifecycleReasonCode != "machine_tool_delete" {
		t.Fatalf("deleted provisioning machine = %+v", deleted.Machine)
	}
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		created.Machine.Machine.ID,
		"late-resource",
		"",
		claimed.ProvisionAttempts,
	); err == nil {
		t.Fatal("expected provisioning completion after delete to fail")
	}
	current, err := store.Execution().GetMachine(ctx, testOrgID, created.Machine.Machine.ID)
	if err != nil {
		t.Fatalf("get machine after rejected provisioning completion: %v", err)
	}
	if current.LifecycleState != "deleting" || current.ProviderResourceID != "" {
		t.Fatalf("machine after rejected provisioning completion = %+v", current)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(ctx, executionstore.DefaultPoolMachineProvisionFailureLimit, 10)
	if err != nil {
		t.Fatalf("list cleanup after provisioning delete request: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != created.Machine.Machine.ID ||
		cleanup[0].ReasonCode != "deleting_retry" {
		t.Fatalf("cleanup after provisioning delete request = %+v", cleanup)
	}
}

func TestListMachinePoolSourcesIncludesZeroMaxPool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC)

	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Zero Max Pool",
		"test.provider",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"zero"}`)},
		3,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-agent-pool-machine-zero-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-zero-config",
		agentPoolMachineConfigYAML(machinePool.Name, 0),
		now.Add(2*time.Second),
	)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: config.ID},
	)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	sources, err := store.Execution().ListMachinePoolSources(ctx, testProjectID, agent.ID, config.ID)
	if err != nil {
		t.Fatalf("list machine pool sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("zero max pool sources = %+v", sources)
	}
}

func TestPoolMachineToolsExcludeExplicitPoolBackedMachineSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-explicit@example.com",
		"Agent Pool Machine Explicit")

	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Explicit Source Pool",
		"test.provider",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"explicit"}`)},
		3,
		now,
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-agent-pool-machine-explicit-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	poolConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-explicit-pool-config",
		agentPoolMachineConfigYAML(machinePool.Name, 1),
		now.Add(2*time.Second),
	)
	poolAgent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: poolConfig.ID},
	)
	if err != nil {
		t.Fatalf("create pool agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		poolAgent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		poolAgent.ID,
		user.ID,
		poolConfig.ID,
		lock,
		"explicit",
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)
	created, err := createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       poolAgent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	})
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}
	claim, ok, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		created.Machine.Machine.ID,
	)
	if err != nil || !ok {
		t.Fatalf("claim generated machine: ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	beginAndRecordPoolMachineProvisioningForTest(
		t,
		ctx,
		store,
		created.Machine.Machine.ID,
		claimed.ProvisionAttempts,
		"explicit-resource",
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		created.Machine.Machine.ID,
		"explicit-resource",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete generated machine provisioning: %v", err)
	}
	if err := store.q.ReleaseAgentMachineBindingsForMachine(
		ctx,
		dbsqlc.ReleaseAgentMachineBindingsForMachineParams{
			OrgID:     testOrgID,
			MachineID: created.Machine.Machine.ID,
		},
	); err != nil {
		t.Fatalf("release generated pool binding: %v", err)
	}
	generatedGrant := getProjectMachineGrantByMachineForTest(t, ctx, store, testOrgID, testProjectID, created.Machine.Machine.ID)
	explicitAgent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: poolConfig.ID},
	)
	if err != nil {
		t.Fatalf("create explicit-kind binding agent: %v", err)
	}
	explicitBinding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               explicitAgent.ID,
			ProjectMachineGrantID: generatedGrant.ID,
			MachineRef:            "mchr-exp001",
			BindingKind:           executionstore.MachineBindingKindExplicit,
		},
	)
	if err != nil {
		t.Fatalf("create explicit-kind pool-backed binding: %v", err)
	}
	listed, err := executionstore.IntegrationListPoolMachinesTx(ctx, store.q, testProjectID, explicitAgent.ID)
	if err != nil {
		t.Fatalf("list pool machines for explicit agent: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("explicit pool-backed machine source should not list as pool tool machine: %+v", listed)
	}
	if _, err := executionstore.IntegrationGetPoolMachineByRef(ctx, store.q, testProjectID, explicitAgent.ID, explicitBinding.MachineRef); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("get explicit pool-backed machine as pool machine error = %v, want not found", err)
	}
	explicitLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		explicitAgent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire explicit agent runtime lock: %v", err)
	}
	explicitToolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		explicitAgent.ID,
		user.ID,
		poolConfig.ID,
		explicitLock,
		"explicit-delete",
		[]poolMachineToolCallSpec{
			{Label: "delete", Name: "delete_machine", Input: json.RawMessage(`{}`)},
		},
	)
	if _, err := deletePoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       explicitAgent.ID,
		ToolCallID:    explicitToolCalls["delete"],
		RuntimeLockID: explicitLock.ID,
	}, executionstore.DeletePoolMachineInput{
		MachineRef: explicitBinding.MachineRef,
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("delete explicit pool-backed machine as pool machine error = %v, want not found", err)
	}
	_, archivedMachines, err := store.Execution().ArchiveAgent(
		ctx,
		testProjectID,
		explicitAgent.ID,
		userPrincipal(user.ID),
	)
	if err != nil {
		t.Fatalf("archive explicit agent: %v", err)
	}
	if len(archivedMachines) != 0 {
		t.Fatalf("explicit pool-backed binding acquired deletion ownership: %+v", archivedMachines)
	}
	afterArchive, err := store.Execution().GetMachine(ctx, testOrgID, created.Machine.Machine.ID)
	if err != nil {
		t.Fatalf("load explicit pool-backed machine after archive: %v", err)
	}
	if afterArchive.LifecycleState != "active" {
		t.Fatalf("explicit pool-backed machine after archive = %+v", afterArchive)
	}
	if _, err := store.pool.Exec(
		ctx,
		`UPDATE machines SET lifecycle_changed_at = statement_timestamp() - interval '6 minutes' WHERE org_id = $1 AND id = $2`,
		testOrgID,
		created.Machine.Machine.ID,
	); err != nil {
		t.Fatalf("age pool-backed machine lifecycle: %v", err)
	}
	cleanup, err := store.Execution().ListPoolMachinesForCleanup(ctx, executionstore.DefaultPoolMachineProvisionFailureLimit, 10)
	if err != nil {
		t.Fatalf("list cleanup after explicit agent archived: %v", err)
	}
	if len(cleanup) != 1 || cleanup[0].Machine.ID != created.Machine.Machine.ID ||
		cleanup[0].ReasonCode != "startup_or_daemon_bootstrap_failed" {
		t.Fatalf("pool-backed machine cleanup = %+v", cleanup)
	}
}

func TestCreatePoolMachineValidatesProviderPoolConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	providers := &rejectingMachinePoolProviders{}
	store := newIntegrationStore(pool, WithMachinePoolProviders(providers))
	now := time.Date(2026, 6, 16, 10, 30, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-pool-machine-provider-validation@example.com",
		"Agent Pool Machine Provider Validation")

	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Create Tool Provider Validation Pool",
		"test.provider",
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"create-tool-provider-validation"}`),
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
			IdempotencyKey: "idem-create-tool-provider-validation-grant",
		}); err != nil {
		t.Fatalf("create pool grant: %v", err)
	}
	poolConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"agent-pool-machine-provider-validation-config",
		agentPoolMachineConfigYAML(machinePool.Name, 1),
		now.Add(2*time.Second),
	)
	poolAgent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: poolConfig.ID},
	)
	if err != nil {
		t.Fatalf("create pool agent: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		poolAgent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		poolAgent.ID,
		user.ID,
		poolConfig.ID,
		lock,
		"provider-validation",
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)

	providers.reject = true
	_, err = createPoolMachineForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       poolAgent.ID,
		ToolCallID:    toolCalls["create"],
		RuntimeLockID: lock.ID,
	}, executionstore.CreatePoolMachineInput{
		MachinePoolID: machinePool.ID,
	})
	if err == nil || !strings.Contains(err.Error(), "provider validation rejected") {
		t.Fatalf("create pool machine provider validation error = %v", err)
	}
}

type poolMachineToolCallSpec struct {
	Label string
	Name  string
	Input json.RawMessage
}

type externalMachinePoolProviders struct {
	mergingMachinePoolProviders
	calls int
}

func (p *externalMachinePoolProviders) BuildMachineProvisioningIntent(
	_ string,
	_ executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	p.calls++
	machineProvisioning.CPU = nil
	machineProvisioning.MemoryMB = nil
	if machineProvisioning.ProviderOptions == nil {
		return executionstore.MachineProvisioningConfig{}, errors.New("provider options are required")
	}
	return machineProvisioning, nil
}

func activateAgentConfigForPoolMachineTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, configID ID,
	idempotencyKey string,
) {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin activate pool machine config: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := executionstore.IntegrationActivateAgentConfigTx(ctx, notifications.NewTxNotifications(), tx, store.q.WithTx(tx), executionstore.ActivateAgentConfigInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		AgentConfigID:  configID,
		ActorType:      identitystore.PrincipalTypeSystem,
		Reason:         "test",
		IdempotencyKey: idempotencyKey,
	}); err != nil {
		t.Fatalf("activate pool machine config: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit activate pool machine config: %v", err)
	}
}

func createPoolMachineToolCalls(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, userID, configID ID,
	lock executionstore.AgentRuntimeLockRecord,
	label string,
	specs []poolMachineToolCallSpec,
) map[string]ID {
	t.Helper()
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        agentID,
			Actor:          mustOmnaraActorParams(t, userID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"seed machine tool calls"}]`),
			IdempotencyKey: "pool-machine-tool-input-" + label,
		},
	)
	if err != nil {
		t.Fatalf("create tool-call seed input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		store,
		testProjectID,
		agentID,
		lock.ID,
	)
	if !found {
		t.Fatal("expected tool-call seed input admission")
	}
	claim, err := store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            agentID,
			RuntimeLockID:      lock.ID,
			OpeningInputIDs:    []ID{input.ID},
			AgentConfigID:      configID,
			InputEventSequence: admitted.Events[0].Sequence,
		},
	)
	if err != nil {
		t.Fatalf("claim tool-call seed model context: %v", err)
	}
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(specs))
	parts := make([]modelenvelope.ResponsePart, 0, len(specs))
	for _, spec := range specs {
		providerCallID := "call_" + label + "_" + spec.Label
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ProviderCallID: providerCallID,
			Type:           toolcatalog.ToolTypeBuiltIn,
		})
		parts = append(parts, modelenvelope.ResponsePart{
			Type:           modelenvelope.ResponsePartTypeToolCall,
			ProviderCallID: providerCallID,
			ToolName:       spec.Name,
			ToolInput:      spec.Input,
		})
	}
	providerResponse := modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: "gpt-test",
		ServedProviderModelSlug:    "gpt-test",
		APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
		APIVariant:                 modelprotocol.APIVariantDefault,
		Normalized: modelenvelope.ResponseNormalized{
			ID:         "resp_" + label,
			Content:    parts,
			StopReason: modelenvelope.StopReasonToolUse,
		},
	}
	_, records, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            agentID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: claim.Context.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings:   bindings,
		},
	)
	if err != nil {
		t.Fatalf("record tool-call seed output: %v", err)
	}
	if len(records) != len(specs) {
		t.Fatalf("recorded tool calls = %d, want %d", len(records), len(specs))
	}
	out := make(map[string]ID, len(records))
	for index, record := range records {
		if _, err := store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     testProjectID,
				AgentID:       agentID,
				ID:            record.ID,
				RuntimeLockID: lock.ID,
			},
		); err != nil {
			t.Fatalf("allow pool machine tool call: %v", err)
		}
		out[specs[index].Label] = record.ID
	}
	return out
}

func agentPoolMachineConfigYAML(machinePoolName string, maxMachines int) string {
	return `
instruction: Use pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: ` + machinePoolName + `
    max_machines: ` + fmt.Sprint(maxMachines) + `
    initial_num_machines: 0
tools:
  create_machine:
    type: built_in
`
}

func agentPoolMachineConfigYAMLWithDefaultMachineFields(
	machinePoolName string,
	maxMachines int,
	defaultMachineFields string,
) string {
	return `
instruction: Use pool machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: ` + machinePoolName + `
    max_machines: ` + fmt.Sprint(maxMachines) + `
    initial_num_machines: 0
` + defaultMachineFields + `tools:
  create_machine:
    type: built_in
`
}
