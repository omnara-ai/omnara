//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestInstallationIsSeededSingleton(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	var installationID ID
	if err := pool.QueryRow(ctx, `SELECT id FROM installation WHERE singleton_key = 1`).Scan(&installationID); err != nil {
		t.Fatalf("get installation: %v", err)
	}
	if isNilID(installationID) {
		t.Fatal("installation id is empty")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO installation DEFAULT VALUES`); err == nil {
		t.Fatal("second installation was inserted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM installation`).Scan(&count); err != nil {
		t.Fatalf("count installations: %v", err)
	}
	if count != 1 {
		t.Fatalf("installation count = %d, want 1", count)
	}
}

func TestInsertAgentMachineBindingRejectsDuplicateBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 12, 45, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "agent-machine-binding-replay@example.com",
			DisplayName: "Agent Machine Binding Replay Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	agentID := mustCreateAgent(t, ctx, store, now)
	machine := createContextMachine(t, ctx, store, testID("agent_machine_binding_replay"), user.ID, now)
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machine.GrantID,
			MachineRef:            "mchr-reply1",
			BindingKind:           "explicit",
			Description:           "primary",
			Cwd:                   "/work",
		},
	); err != nil {
		t.Fatalf("bind machine: %v", err)
	}
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machine.GrantID,
			MachineRef:            "mchr-reply1",
			BindingKind:           "explicit",
			Description:           "primary",
			Cwd:                   "/work",
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("duplicate binding error = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		machine.GrantID,
	); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machine.GrantID,
			MachineRef:            "mchr-reply1",
			BindingKind:           "explicit",
			Description:           "primary",
			Cwd:                   "/work",
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("duplicate binding after grant revoke error = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machine.GrantID,
			MachineRef:            "mchr-other1",
			BindingKind:           "explicit",
			Description:           "primary",
			Cwd:                   "/work",
		},
	); !errors.Is(
		err,
		storeerr.ErrIdempotencyConflict,
	) {
		t.Fatalf("conflicting replay error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestReleasedAgentMachineBindingCanReattach(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 12, 46, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(ctx, CreateVerifiedUserInput{
		Email:       "agent-machine-binding-history@example.com",
		DisplayName: "Agent Machine Binding History Tester",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	firstAgentID := mustCreateAgent(t, ctx, store, now)
	secondAgentID := mustCreateAgent(t, ctx, store, now.Add(time.Millisecond))
	machine := createContextMachine(t, ctx, store, testID("agent_machine_binding_history"), user.ID, now)
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(ctx, store.q, executionstore.IntegrationInsertAgentMachineBindingInput{
		ProjectID:             testProjectID,
		AgentID:               firstAgentID,
		ProjectMachineGrantID: machine.GrantID,
		MachineRef:            "mchr-hist00",
		BindingKind:           "pool",
	}); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("pool binding to BYO machine error = %v, want ErrIdempotencyConflict", err)
	}
	first, err := executionstore.IntegrationInsertAgentMachineBindingTx(ctx, store.q, executionstore.IntegrationInsertAgentMachineBindingInput{
		ProjectID:             testProjectID,
		AgentID:               firstAgentID,
		ProjectMachineGrantID: machine.GrantID,
		MachineRef:            "mchr-hist01",
		BindingKind:           "explicit",
	})
	if err != nil {
		t.Fatalf("bind first agent: %v", err)
	}
	second, err := executionstore.IntegrationInsertAgentMachineBindingTx(ctx, store.q, executionstore.IntegrationInsertAgentMachineBindingInput{
		ProjectID:             testProjectID,
		AgentID:               secondAgentID,
		ProjectMachineGrantID: machine.GrantID,
		MachineRef:            "mchr-hist02",
		BindingKind:           "explicit",
	})
	if err != nil {
		t.Fatalf("bind second agent to shared machine: %v", err)
	}
	updated, err := store.q.ReleaseExplicitAgentMachineBinding(ctx, dbsqlc.ReleaseExplicitAgentMachineBindingParams{
		ProjectID: testProjectID,
		AgentID:   firstAgentID,
		MachineID: machine.Machine.ID,
	})
	if err != nil || updated != 1 {
		t.Fatalf("release first binding: updated=%d err=%v", updated, err)
	}
	rebound, err := executionstore.IntegrationInsertAgentMachineBindingTx(ctx, store.q, executionstore.IntegrationInsertAgentMachineBindingInput{
		ProjectID:             testProjectID,
		AgentID:               firstAgentID,
		ProjectMachineGrantID: machine.GrantID,
		MachineRef:            "mchr-hist03",
		BindingKind:           "explicit",
	})
	if err != nil {
		t.Fatalf("reattach first agent: %v", err)
	}
	if rebound.ID == first.ID || rebound.MachineRef == first.MachineRef {
		t.Fatalf("reattached binding reused history: first=%+v rebound=%+v", first, rebound)
	}
	released := getAgentMachineBindingForTest(t, ctx, store, testProjectID, firstAgentID, first.ID)
	shared := getAgentMachineBindingForTest(t, ctx, store, testProjectID, secondAgentID, second.ID)
	if released.State != "released" || shared.State != "attached" {
		t.Fatalf("binding states after reattach: released=%s shared=%s", released.State, shared.State)
	}
	var historyCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::integer
		FROM agent_machine_bindings
		WHERE project_id = $1 AND agent_id = $2 AND machine_id = $3
	`, testProjectID, firstAgentID, machine.Machine.ID).Scan(&historyCount); err != nil {
		t.Fatalf("count binding history: %v", err)
	}
	if historyCount != 2 {
		t.Fatalf("binding history count = %d, want 2", historyCount)
	}
}

func TestUpdateMachineRejectsBindingEnvironmentConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 12, 46, 30, 0, time.UTC)
	secretID := createMachinePoolProviderAuthSecretForTest(
		t,
		ctx,
		store,
		"secret-value",
	)
	machine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Machine Binding Conflict",
		IdempotencyKey: "idem-machine-binding-conflict",
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	_, _, err = store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachineID:      machine.ID,
		IdempotencyKey: "idem-machine-binding-conflict-grant",
	})
	if err != nil {
		t.Fatalf("grant machine: %v", err)
	}
	agentID := mustCreateAgent(t, ctx, store, now)
	bindingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin binding transaction: %v", err)
	}
	defer func() { _ = bindingTx.Rollback(ctx) }()
	qtx := dbsqlc.New(bindingTx)
	literal := "binding"
	sources := []executionstore.IntegrationLaunchMachineSource{{
		Index:     0,
		MachineID: machine.ID,
		Contract: agentconfig.RuntimeMachine{
			EnvOverlay: map[string]*string{"TOKEN": &literal},
		},
	}}
	if err := store.Execution().IntegrationResolveLaunchMachineSourcesTx(ctx, qtx, testOrgID, testProjectID, sources); err != nil {
		t.Fatalf("resolve binding environment: %v", err)
	}
	envOverlay, secretEnvOverlay, err := executionstore.MachineEnvironmentOverlayToColumns(
		sources[0].BindingConfig.EnvironmentOverlay,
	)
	if err != nil {
		t.Fatalf("prepare binding environment: %v", err)
	}
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(ctx, qtx, executionstore.IntegrationInsertAgentMachineBindingInput{
		ProjectID:             testProjectID,
		AgentID:               agentID,
		ProjectMachineGrantID: sources[0].GrantID,
		MachineRef:            "mchr-env001",
		BindingKind:           executionstore.MachineBindingKindExplicit,
		EnvOverlay:            envOverlay,
		SecretEnvOverlay:      secretEnvOverlay,
	}); err != nil {
		t.Fatalf("bind machine: %v", err)
	}
	secretEnv := json.RawMessage(`{"token":"` + secretPublicIDForTest(t, secretID) + `"}`)
	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := store.Execution().UpdateMachine(ctx, executionstore.UpdateMachineInput{
			OrgID:     testOrgID,
			MachineID: machine.ID,
			SecretEnv: &secretEnv,
		})
		updateDone <- updateErr
	}()
	integrationdb.WaitForLockWaiters(t, ctx, pool, "machine_environment:", 1)
	select {
	case updateErr := <-updateDone:
		t.Fatalf("machine update completed before binding commit: %v", updateErr)
	default:
	}
	if err := bindingTx.Commit(ctx); err != nil {
		t.Fatalf("commit binding: %v", err)
	}
	select {
	case updateErr := <-updateDone:
		if updateErr == nil || !strings.Contains(updateErr.Error(), "env and secret_env cannot both set key") {
			t.Fatalf("conflicting baseline update error = %v", updateErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for machine update after binding commit")
	}
	unchanged, err := store.Execution().GetMachine(ctx, testOrgID, machine.ID)
	if err != nil {
		t.Fatalf("load machine after rejected update: %v", err)
	}
	if !sameJSON(unchanged.SecretEnv, json.RawMessage(`{}`)) {
		t.Fatalf("rejected baseline update persisted secret_env %s", unchanged.SecretEnv)
	}
}

func TestReleasedAgentMachineBindingRejectsReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 12, 47, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "agent-machine-binding-released-replay@example.com",
			DisplayName: "Agent Machine Binding Released Replay Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	agentID := mustCreateAgent(t, ctx, store, now)
	machine := createContextMachine(t, ctx, store, testID("agent_machine_binding_released_replay"), user.ID, now)
	binding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machine.GrantID,
			MachineRef:            "mchr-rel001",
			BindingKind:           "explicit",
			Description:           "primary",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("bind machine: %v", err)
	}
	if _, err := store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     testOrgID,
			MachineID: machine.Machine.ID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	deleted, err := store.Execution().GetMachine(ctx, testOrgID, machine.Machine.ID)
	if err != nil {
		t.Fatalf("get deleted machine: %v", err)
	}
	if deleted.DeletedAt == nil {
		t.Fatalf("deleted machine has no deleted_at: %+v", deleted)
	}
	deletedAt := *deleted.DeletedAt
	released := getAgentMachineBindingForTest(t, ctx, store, testProjectID, agentID, binding.ID)
	if released.State != "released" || released.UpdatedAt.Before(deletedAt) {
		t.Fatalf(
			"released binding state/updated_at = %s/%s, want released no earlier than %s",
			released.State,
			released.UpdatedAt,
			deletedAt,
		)
	}
	releasedAt := released.UpdatedAt
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machine.GrantID,
			MachineRef:            "mchr-rel001",
			BindingKind:           "explicit",
			Description:           "primary",
			Cwd:                   "/work",
		},
	); !errors.Is(
		err,
		storeerr.ErrIdempotencyConflict,
	) {
		t.Fatalf("released binding replay error = %v, want ErrIdempotencyConflict", err)
	}
	afterReplay := getAgentMachineBindingForTest(t, ctx, store, testProjectID, agentID, binding.ID)
	if afterReplay.State != "released" || !afterReplay.UpdatedAt.Equal(releasedAt) {
		t.Fatalf(
			"binding after replay state/updated_at = %s/%s, want released/%s",
			afterReplay.State,
			afterReplay.UpdatedAt,
			releasedAt,
		)
	}
}

func TestCreateProjectMachineGrantDoesNotMutateExistingBindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 12, 48, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "agent-machine-binding-retarget-scope@example.com",
			DisplayName: "Agent Machine Binding Retarget Scope Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add org membership: %v", err)
	}
	otherProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(user.ID),
			Name:           "Retarget Scope Project",
			IdempotencyKey: "idem-retarget-scope-project",
		},
	)
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	agentID := mustCreateAgent(t, ctx, store, now.Add(2*time.Millisecond))
	otherConfigID := mustCreateAgentConfig(
		t,
		ctx,
		store,
		otherProject.ID,
		"retarget-scope-other",
		now.Add(3*time.Millisecond),
	)
	otherAgent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: otherProject.ID, CurrentConfigID: otherConfigID},
	)
	if err != nil {
		t.Fatalf("create other project agent: %v", err)
	}
	machine := createContextMachine(t, ctx, store, testID("agent_machine_binding_retarget_scope"), user.ID, now)
	otherGrant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      otherProject.ID,
			MachineID:      machine.Machine.ID,
			IdempotencyKey: "idem-retarget-scope-other-grant",
		},
	)
	if err != nil {
		t.Fatalf("create other project grant: %v", err)
	}
	binding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machine.GrantID,
			MachineRef:            "mchr-rtg001",
			BindingKind:           "explicit",
			Description:           "primary",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("bind machine: %v", err)
	}
	otherBinding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             otherProject.ID,
			AgentID:               otherAgent.ID,
			ProjectMachineGrantID: otherGrant.ID,
			MachineRef:            "mchr-rtg002",
			BindingKind:           "explicit",
			Description:           "other",
			Cwd:                   "/other",
		},
	)
	if err != nil {
		t.Fatalf("bind other project machine: %v", err)
	}
	if _, err := store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		machine.GrantID,
	); err != nil {
		t.Fatalf("revoke default project grant: %v", err)
	}
	replacement, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.Machine.ID,
			IdempotencyKey: "idem-retarget-scope-replacement",
		},
	)
	if err != nil {
		t.Fatalf("create replacement grant: %v", err)
	}
	if replacement.ID == machine.GrantID {
		t.Fatalf("replacement grant reused original grant id %s", replacement.ID)
	}
	afterReplacement := getAgentMachineBindingForTest(t, ctx, store, testProjectID, agentID, binding.ID)
	if afterReplacement.State != "attached" ||
		afterReplacement.MachineID != machine.Machine.ID ||
		!afterReplacement.UpdatedAt.Equal(binding.UpdatedAt) {
		t.Fatalf("binding after replacement grant = %+v, want unchanged binding %+v", afterReplacement, binding)
	}
	otherAfterReplacement := getAgentMachineBindingForTest(
		t,
		ctx,
		store,
		otherProject.ID,
		otherAgent.ID,
		otherBinding.ID,
	)
	if otherAfterReplacement.State != "attached" ||
		otherAfterReplacement.MachineID != machine.Machine.ID ||
		!otherAfterReplacement.UpdatedAt.Equal(otherBinding.UpdatedAt) {
		t.Fatalf(
			"other project binding after replacement grant = state %s machine %s updated_at %s, want attached machine %s updated_at %s",
			otherAfterReplacement.State,
			otherAfterReplacement.MachineID,
			otherAfterReplacement.UpdatedAt,
			machine.Machine.ID,
			otherBinding.UpdatedAt,
		)
	}
}

func TestInsertAgentMachineBindingMapsUniqueConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 12, 50, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "agent-machine-binding-conflict@example.com",
			DisplayName: "Agent Machine Binding Conflict Tester",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	agentID := mustCreateAgent(t, ctx, store, now)
	first := createContextMachine(t, ctx, store, testID("agent_machine_binding_conflict_first"), user.ID, now)
	second := createContextMachine(t, ctx, store, testID("agent_machine_binding_conflict_second"), user.ID, now)
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: first.GrantID,
			MachineRef:            "mchr-dupe01",
			BindingKind:           "explicit",
		},
	); err != nil {
		t.Fatalf("bind first machine: %v", err)
	}
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: second.GrantID,
			MachineRef:            "mchr-dupe01",
			BindingKind:           "explicit",
		},
	); !errors.Is(
		err,
		storeerr.ErrIdempotencyConflict,
	) {
		t.Fatalf("duplicate machine ref error = %v, want ErrIdempotencyConflict", err)
	}
}

type contextMachine struct {
	Machine executionstore.MachineRecord
	GrantID ID
}

func createContextMachine(
	t *testing.T,
	ctx context.Context,
	store *Store,
	id ID,
	userID ID,
	now time.Time,
) contextMachine {
	t.Helper()
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    id.String(),
			IdempotencyKey: "idem-" + id.String(),
		},
	)
	if err != nil {
		t.Fatalf("create machine %s: %v", id, err)
	}
	grantID := testID("project_machine_grant_" + id.String())
	grant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "idem-" + grantID.String(),
		},
	)
	if err != nil {
		t.Fatalf("grant machine %s: %v", id, err)
	}
	return contextMachine{Machine: machine, GrantID: grant.ID}
}
