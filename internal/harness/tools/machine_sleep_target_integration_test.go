//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestRunCommandCommitsBeforeWakingAsleepMachine(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)))
	now := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at) VALUES ($1, 'Tools Test Org', 'tools-sleep-org', $2, $2)`,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at) VALUES ($1, $2, 'Tools Test Project', 'tools-sleep-project', $3, $3)`,
		toolsTestProjectID,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{Email: "tools-sleep@example.com", DisplayName: "Tools Sleep"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	ensureToolsProjectOperator(t, ctx, store, user.ID, now)

	binding := createExecutableBinding(t, ctx, store, user.ID, "asleep", now.Add(time.Second))
	launch := createToolsRuntimeAgentWithMachineSources(
		t,
		ctx,
		store,
		user.ID,
		"tools-sleep",
		[]toolsAgentMachineSource{{MachineName: binding.DisplayName, Cwd: "/asleep"}},
		now.Add(2*time.Second),
	)
	if len(launch.MachineBindings) != 1 {
		t.Fatalf("machine bindings = %+v, want one", launch.MachineBindings)
	}
	agent := launch.Agent
	agentBinding := launch.MachineBindings[0]

	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET sandbox_url = 'https://tools-sleep.test/' WHERE org_id = $1 AND id = $2`,
		toolsTestOrgID,
		binding.MachineID,
	); err != nil {
		t.Fatalf("set machine sandbox url: %v", err)
	}
	var tokenID storage.ID
	if err := pool.QueryRow(
		ctx,
		`SELECT id FROM machine_daemon_tokens WHERE org_id = $1 AND machine_id = $2 AND revoked_at IS NULL LIMIT 1`,
		toolsTestOrgID,
		binding.MachineID,
	).Scan(&tokenID); err != nil {
		t.Fatalf("load daemon token: %v", err)
	}
	if _, err := store.Execution().SleepDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           toolsTestOrgID,
			MachineID:       binding.MachineID,
			DaemonRuntimeID: binding.RuntimeID,
			DaemonTokenID:   tokenID,
		},
	); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}

	executor := Executor{Store: store}
	turn := Turn{ProjectID: toolsTestProjectID, AgentID: agent.ID}
	resolved, err := executor.ResolveMachineExecutionTarget(ctx, turn, agentBinding.MachineRef)
	if err != nil {
		t.Fatalf("resolve asleep machine execution target: %v", err)
	}
	if resolved.ID != agentBinding.ID || resolved.MachineID != binding.MachineID {
		t.Fatalf("resolved binding = %+v, want asleep machine binding %s", resolved, agentBinding.ID)
	}

	input := json.RawMessage(
		`{"command":"pwd","machine_ref":"` + agentBinding.MachineRef + `","wait_ms":750}`,
	)
	toolCallID, lock, admitted, modelContext := createMachineToolCallForDirectStoreTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		launch.AgentConfig.ID,
		"wake-run",
		"run_command",
		input,
		now.Add(5*time.Second),
	)
	wakeCalls := make(chan storage.ID, 1)
	manager := testPoolMachineManager{wake: func(
		ctx context.Context,
		orgID, machineID storage.ID,
	) (bool, error) {
		process, found, err := store.Execution().GetProcessByToolCall(
			ctx,
			toolsTestProjectID,
			launch.Agent.ID,
			toolCallID,
		)
		if err != nil {
			return false, err
		}
		if !found || process.State != executionstore.ProcessStateQueued || process.InitialWaitMS != 750 {
			return false, storeerr.ErrNotFound
		}
		if orgID != toolsTestOrgID || machineID != binding.MachineID {
			return false, storeerr.ErrNotFound
		}
		wakeCalls <- machineID
		return true, nil
	}}
	call := model.ToolCall{ID: "call_wake-run", Name: "run_command", Input: input}
	result, err := (Executor{
		Store:              store,
		MachinePoolManager: manager,
		BackgroundRunner:   immediateIntegrationBackgroundRunner(ctx),
		Now:                func() time.Time { return now.Add(6 * time.Second) },
	}).Dispatch(
		ctx,
		Turn{
			ProjectID:          toolsTestProjectID,
			AgentID:            launch.Agent.ID,
			SourceEventID:      admitted.Events[0].ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: modelContext.ID,
			Tools: map[string]ToolSpec{
				"run_command": {
					Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				},
			},
		},
		call,
	)
	if err != nil {
		t.Fatalf("dispatch run_command: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf(
			"run_command disposition = %d, want deferred; content=%s",
			result.Disposition,
			result.ContentParts,
		)
	}
	select {
	case machineID := <-wakeCalls:
		if machineID != binding.MachineID {
			t.Fatalf("wake machine = %s, want %s", machineID, binding.MachineID)
		}
	default:
		t.Fatal("wake manager was not called")
	}
	var toolState string
	var runtimeReleased bool
	if err := pool.QueryRow(
		ctx,
		`SELECT state, runtime_lock_id IS NULL FROM tool_calls WHERE id = $1`,
		toolCallID,
	).Scan(&toolState, &runtimeReleased); err != nil {
		t.Fatalf("load tool call: %v", err)
	}
	if toolState != "waiting" || !runtimeReleased {
		t.Fatalf("tool state = %s, runtime released = %v", toolState, runtimeReleased)
	}

	wakeCtx, cancelWake := context.WithCancel(ctx)
	manager.wake = func(context.Context, storage.ID, storage.ID) (bool, error) {
		cancelWake()
		return false, errors.New("wake failed")
	}
	cleanupErr := wakeRunCommand(wakeCtx, backgroundToolContext{
		Executor:   Executor{Store: store, MachinePoolManager: manager},
		Turn:       turn,
		ToolCallID: toolCallID,
	})
	if !errors.Is(cleanupErr, context.Canceled) {
		t.Fatalf("cleanup error = %v, want context canceled", cleanupErr)
	}
	manager.wake = func(context.Context, storage.ID, storage.ID) (bool, error) {
		return false, nil
	}
	cleanupErr = wakeRunCommand(ctx, backgroundToolContext{
		Executor:   Executor{Store: store, MachinePoolManager: manager},
		Turn:       turn,
		ToolCallID: toolCallID,
	})
	if !errors.Is(cleanupErr, storeerr.ErrMachineNotReachable) {
		t.Fatalf("cleanup error = %v, want machine not reachable", cleanupErr)
	}
}

func TestSkillToolBroadcastsExecutableBindingsAndReportsOutcomes(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := storage.NewStore(
		pool,
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
	)
	now := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at) VALUES ($1, 'Tools Test Org', 'tools-skill-wake-org', $2, $2)`,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at) VALUES ($1, $2, 'Tools Test Project', 'tools-skill-wake-project', $3, $3)`,
		toolsTestProjectID,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "tools-skill-wake@example.com",
			DisplayName: "Tools Skill Wake",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	ensureToolsProjectOperator(t, ctx, store, user.ID, now)

	first := createExecutableBinding(t, ctx, store, user.ID, "skill-wake-first", now.Add(time.Second))
	second := createExecutableBinding(t, ctx, store, user.ID, "skill-wake-second", now.Add(2*time.Second))
	skill, err := store.Skills().CreateSkillRevision(ctx, skillstore.CreateSkillInput{
		OrgID:          toolsTestOrgID,
		OwnerKind:      skillstore.SkillOwnerProject,
		OwnerProjectID: toolsTestProjectID,
		Name:           "wake-skill",
		Description:    "wake skill",
		SkillMd:        "# Wake skill\nUse the installed files.",
		ArchiveBytes:   []byte("wake skill archive"),
		Actor:          toolsTestUserPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	skillPublicID, err := publicid.Encode(publicid.KindSkill, skill.ID)
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	launch := createToolsRuntimeAgentWithMachineSourcesAndSkills(
		t,
		ctx,
		store,
		user.ID,
		"tools-skill-wake",
		[]toolsAgentMachineSource{
			{MachineName: first.DisplayName, Cwd: "/first"},
			{MachineName: second.DisplayName, Cwd: "/second"},
		},
		[]string{skillPublicID},
		now.Add(4*time.Second),
	)
	_, _, _, modelContext := createMachineToolCallForDirectStoreTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		launch.AgentConfig.ID,
		"skill-wake",
		"skill",
		json.RawMessage(`{"name":"wake-skill"}`),
		now.Add(5*time.Second),
	)

	broadcastCalls := 0
	broadcaster := skillBroadcasterFunc(func(
		_ context.Context,
		_, _, _ string,
		targets []skills.BroadcastTarget,
		_ time.Duration,
	) ([]skills.BroadcastOutcome, error) {
		broadcastCalls++
		if len(targets) != 2 ||
			targets[0].OrgID != toolsTestOrgID ||
			targets[0].MachineID != first.MachineID ||
			targets[1].OrgID != toolsTestOrgID ||
			targets[1].MachineID != second.MachineID {
			t.Fatalf("broadcast targets = %+v", targets)
		}
		return []skills.BroadcastOutcome{
			{Target: targets[0], State: skills.BroadcastStateReady},
			{Target: targets[1], State: skills.BroadcastStateOffline, Error: "wake failed"},
		}, nil
	})
	call := asyncToolContext{
		Executor: Executor{
			Store:            store,
			SkillBroadcaster: broadcaster,
		},
		Turn: Turn{
			ProjectID:          toolsTestProjectID,
			AgentID:            launch.Agent.ID,
			ModelCallContextID: modelContext.ID,
		},
		Call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"wake-skill"}`)},
	}
	result, err := runSkillTool(ctx, call)
	if err != nil {
		t.Fatalf("run skill tool: %v", err)
	}
	completed, ok := result.(completeAsync)
	if !ok {
		t.Fatalf("skill result = %T, want completeAsync", result)
	}
	if broadcastCalls != 1 {
		t.Fatalf("broadcast calls = %d, want 1", broadcastCalls)
	}
	content := asyncCompletionContent(t, completed)
	for _, want := range []string{"Installed on:", "Skill install failed"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("skill result missing %q: %s", want, content)
		}
	}
}

func TestMachineExecutableAcceptsOnlineAndAsleep(t *testing.T) {
	record := executionstore.PoolMachineRecord{}
	record.Binding.State = "attached"
	record.Machine.LifecycleState = "active"
	for state, want := range map[string]bool{"online": true, "asleep": true, "offline": false} {
		record.Machine.ConnectionState = executionstore.MachineConnectionState(state)
		if machineExecutable(record) != want {
			t.Fatalf("machineExecutable(%s) = %v, want %v", state, !want, want)
		}
	}
}

type skillBroadcasterFunc func(
	context.Context,
	string,
	string,
	string,
	[]skills.BroadcastTarget,
	time.Duration,
) ([]skills.BroadcastOutcome, error)

func (f skillBroadcasterFunc) BroadcastAndAwait(
	ctx context.Context,
	skillPublicID string,
	revisionPublicID string,
	archiveDigest string,
	targets []skills.BroadcastTarget,
	timeout time.Duration,
) ([]skills.BroadcastOutcome, error) {
	return f(ctx, skillPublicID, revisionPublicID, archiveDigest, targets, timeout)
}
