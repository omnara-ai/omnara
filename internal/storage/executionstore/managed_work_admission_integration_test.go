//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestManagedWorkAdmissionGatesNewPoolMachineCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"managed-machine-admission@example.com",
		"Managed Machine Admission",
	)
	machinePool := createDefaultMachinePoolForTest(
		t,
		ctx,
		store,
		machinePoolInputWithDefaultMachineForTest(
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Provider:         "test.provider",
				MaxTotalMachines: 3,
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"default"}`),
			},
		),
	)
	if err := provisionDefaultMachinePoolGrantsForProject(
		ctx,
		store,
		testOrgID,
		testProjectID,
	); err != nil {
		t.Fatalf("create default project pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(t, ctx, store, "managed-machine-admission", `
name: Managed Machine Admission
instruction: Create machines.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+machinePool.Name+`
    max_machines: 3
    initial_num_machines: 0
tools:
  create_machine: {}
`, time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC))
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{
			ProjectID:       testProjectID,
			CurrentConfigID: config.ID,
		},
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
		"managed-admission",
		[]poolMachineToolCallSpec{
			{Label: "allowed", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "denied", Name: "create_machine", Input: json.RawMessage(`{}`)},
		},
	)
	transaction := func(toolCallID ID) executionstore.ExecuteToolCallInput {
		return executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       agent.ID,
			ToolCallID:    toolCallID,
			RuntimeLockID: lock.ID,
		}
	}
	machineInput := executionstore.CreatePoolMachineInput{MachinePoolID: machinePool.ID}
	created, err := createPoolMachineForTest(
		ctx,
		store,
		transaction(toolCalls["allowed"]),
		machineInput,
	)
	if err != nil {
		t.Fatalf("create pool machine before admission closes: %v", err)
	}

	setManagedWorkAdmissionForTest(t, ctx, pool, testOrgID, false)

	replayed, err := createPoolMachineForTest(
		ctx,
		store,
		transaction(toolCalls["allowed"]),
		machineInput,
	)
	if err != nil {
		t.Fatalf("replay admitted pool machine creation: %v", err)
	}
	if replayed.Machine.Machine.ID != created.Machine.Machine.ID {
		t.Fatalf(
			"replayed machine = %s, want %s",
			replayed.Machine.Machine.ID,
			created.Machine.Machine.ID,
		)
	}
	if _, err := createPoolMachineForTest(
		ctx,
		store,
		transaction(toolCalls["denied"]),
		machineInput,
	); !errors.Is(err, storeerr.ErrManagedWorkAdmissionDenied) {
		t.Fatalf("new pool machine error = %v, want managed work admission denial", err)
	}
	var machineCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM machines
WHERE org_id = $1 AND machine_pool_id = $2 AND deleted_at IS NULL
`, testOrgID, machinePool.ID).Scan(&machineCount); err != nil {
		t.Fatalf("count pool machines: %v", err)
	}
	if machineCount != 1 {
		t.Fatalf("pool machine count after denial = %d, want 1", machineCount)
	}

	setManagedWorkAdmissionForTest(t, ctx, pool, testOrgID, true)
	if _, err := createPoolMachineForTest(
		ctx,
		store,
		transaction(toolCalls["denied"]),
		machineInput,
	); err != nil {
		t.Fatalf("create pool machine after admission reopens: %v", err)
	}
}

func TestManagedWorkAdmissionGatesInitialPoolAllocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"managed-launch-admission@example.com",
		"Managed Launch Admission",
	)
	managedPool := createDefaultMachinePoolForTest(
		t,
		ctx,
		store,
		machinePoolInputWithDefaultMachineForTest(
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Provider:         "test.provider",
				MaxTotalMachines: 3,
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"default"}`),
			},
		),
	)
	if err := provisionDefaultMachinePoolGrantsForProject(
		ctx,
		store,
		testOrgID,
		testProjectID,
	); err != nil {
		t.Fatalf("create default project pool grant: %v", err)
	}
	tenantPool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Tenant Admission Pool",
		"test.provider",
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"tenant"}`),
		},
		1,
		time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
	)
	if _, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  tenantPool.ID,
			IdempotencyKey: "tenant-admission-pool-grant",
		},
	); err != nil {
		t.Fatalf("create tenant project pool grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "managed-launch-admission", "Managed Launch Admission", `
name: Managed Launch Admission
instruction: Use a managed machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+managedPool.Name+`
    max_machines: 1
    initial_num_machines: 1
tools:
  run_command: {}
`, time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC))
	tenantProfile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "tenant-launch-admission", "Tenant Launch Admission", `
name: Tenant Launch Admission
instruction: Use a tenant-managed machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: `+tenantPool.Name+`
    max_machines: 1
    initial_num_machines: 1
tools:
  run_command: {}
`, time.Date(2026, 8, 10, 9, 0, 1, 0, time.UTC))
	launch := func(idempotencyKey string) (executionstore.LaunchAgentResult, error) {
		return store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: idempotencyKey,
		})
	}
	first, err := launch("managed-launch-admission-first")
	if err != nil {
		t.Fatalf("launch before admission closes: %v", err)
	}
	setManagedWorkAdmissionForTest(t, ctx, pool, testOrgID, false)
	replayed, err := launch("managed-launch-admission-first")
	if err != nil {
		t.Fatalf("replay admitted launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, first.Agent)
	if _, err := launch("managed-launch-admission-second"); !errors.Is(
		err,
		storeerr.ErrManagedWorkAdmissionDenied,
	) {
		t.Fatalf("new managed launch error = %v, want managed work admission denial", err)
	}
	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      tenantProfile.ID,
		AgentConfigID:  tenantProfile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "tenant-launch-admission",
	}); err != nil {
		t.Fatalf("launch with tenant pool while managed admission is closed: %v", err)
	}
	setManagedWorkAdmissionForTest(t, ctx, pool, testOrgID, true)
	if _, err := launch("managed-launch-admission-second"); err != nil {
		t.Fatalf("launch after admission reopens: %v", err)
	}
}

func TestManagedWorkAdmissionGatesNewProcessesOnManagedMachines(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	machinePool := createDefaultMachinePoolForTest(
		t,
		ctx,
		store,
		machinePoolInputWithDefaultMachineForTest(
			executionstore.CreateMachinePoolInput{
				OrgID:            testOrgID,
				Provider:         "test.provider",
				MaxTotalMachines: 3,
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"default"}`),
			},
		),
	)
	providerFixture := providerRuntimeStorageFixture{
		pool:        pool,
		store:       store,
		machinePool: machinePool,
	}
	machine := providerFixture.insertInactiveMachine(t, ctx, "process-admission")
	fixture := providerFixture.createProcessFixture(t, ctx, machine, "process-admission")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"process-admission",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("process-admission-first", "run_command"),
			builtInProcessToolCallBatchItem("process-admission-second", "run_command"),
		},
	)
	transaction := func(toolCallID ID) executionstore.ExecuteToolCallInput {
		return executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		}
	}
	processInput := executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}
	firstProcess, err := startProcessForTest(
		ctx,
		fixture.Store,
		transaction(toolCallIDs[0]),
		processInput,
	)
	if err != nil {
		t.Fatalf("start process before admission closes: %v", err)
	}
	setManagedWorkAdmissionForTest(t, ctx, providerFixture.pool, testOrgID, false)
	replayed, err := startProcessForTest(
		ctx,
		fixture.Store,
		transaction(toolCallIDs[0]),
		processInput,
	)
	if err != nil {
		t.Fatalf("replay admitted process: %v", err)
	}
	if replayed.ID != firstProcess.ID {
		t.Fatalf("replayed process = %s, want %s", replayed.ID, firstProcess.ID)
	}
	if _, err := startProcessForTest(
		ctx,
		fixture.Store,
		transaction(toolCallIDs[1]),
		processInput,
	); !errors.Is(err, storeerr.ErrManagedWorkAdmissionDenied) {
		t.Fatalf("new process error = %v, want managed work admission denial", err)
	}
	var processCount int
	if err := providerFixture.pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM processes
WHERE project_id = $1 AND agent_id = $2
`, testProjectID, fixture.AgentID).Scan(&processCount); err != nil {
		t.Fatalf("count processes: %v", err)
	}
	if processCount != 1 {
		t.Fatalf("process count after denial = %d, want 1", processCount)
	}
	setManagedWorkAdmissionForTest(t, ctx, providerFixture.pool, testOrgID, true)
	if _, err := startProcessForTest(
		ctx,
		fixture.Store,
		transaction(toolCallIDs[1]),
		processInput,
	); err != nil {
		t.Fatalf("start process after admission reopens: %v", err)
	}
}

func TestManagedWorkAdmissionDoesNotGateBYOProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "byo-managed-work-admission")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"byo-managed-work-admission",
		"run_command",
	)
	setManagedWorkAdmissionForTest(t, ctx, fixture.Store.pool, testOrgID, false)
	if _, err := startProcessForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "echo allowed",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	); err != nil {
		t.Fatalf("start BYO process while managed admission is closed: %v", err)
	}
}

func TestManagedWorkAdmissionDoesNotGateTenantPoolProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Tenant Process Admission Pool",
		"test.provider",
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"tenant"}`),
		},
		1,
		time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC),
	)
	providerFixture := providerRuntimeStorageFixture{
		pool:        pool,
		store:       store,
		machinePool: machinePool,
	}
	machine := providerFixture.insertInactiveMachine(t, ctx, "tenant-process-admission")
	fixture := providerFixture.createProcessFixture(t, ctx, machine, "tenant-process-admission")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"tenant-process-admission",
		"run_command",
	)
	setManagedWorkAdmissionForTest(t, ctx, pool, testOrgID, false)
	if _, err := startProcessForTest(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "echo allowed",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	); err != nil {
		t.Fatalf("start tenant-pool process while managed admission is closed: %v", err)
	}
}
