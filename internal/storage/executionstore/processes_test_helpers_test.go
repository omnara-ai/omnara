//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func createQuestionInteractionForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
) executionstore.AgentInteractionRecord {
	t.Helper()
	value := questionInteractionFormForTest(t)
	execution, err := fixture.Store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateQuestionForToolCall(
				executionstore.CreateQuestionInteractionInput{Form: value},
			), nil
		},
	)
	if err != nil {
		t.Fatalf("create question interaction: %v", err)
	}
	interaction, ok := execution.CommandResult.(executionstore.AgentInteractionRecord)
	if !ok {
		t.Fatalf("question command returned %T", execution.CommandResult)
	}
	if execution.Disposition == executionstore.ToolCallDispositionRunning {
		if err := fixture.Store.Execution().ReleaseToolCallRuntimeOwnership(
			ctx,
			executionstore.ReleaseToolCallRuntimeOwnershipInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallID,
				RuntimeLockID: fixture.Lock.ID,
			},
		); err != nil {
			t.Fatalf("release question tool call: %v", err)
		}
	}
	return interaction
}

func questionInteractionFormForTest(t *testing.T) interactionform.Form {
	t.Helper()
	value, err := interactionform.New(
		"Question",
		nil,
		[]interactionform.Question{{
			Prompt:  "Continue?",
			Options: []interactionform.Option{{Label: "Yes"}},
		}},
	)
	if err != nil {
		t.Fatalf("build question interaction: %v", err)
	}
	return value
}

func createPermissionInteractionForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
	request json.RawMessage,
) executionstore.AgentInteractionRecord {
	t.Helper()
	permissionRequest, err := toolpermission.ParseRequest(request)
	if err != nil {
		t.Fatalf("parse permission request: %v", err)
	}
	interaction, err := fixture.Store.Execution().CreatePermissionInteraction(
		ctx,
		executionstore.CreatePermissionInteractionInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
			Request:       permissionRequest,
		},
	)
	if err != nil {
		t.Fatalf("create permission interaction: %v", err)
	}
	return interaction
}

type toolCallFixtureInput struct {
	ProjectID          ID
	AgentID            ID
	SourceEventID      ID
	ModelCallContextID ID
	RuntimeLockID      ID
	ProviderCallID     string
	Name               string
	Input              json.RawMessage
	Type               string
}

func insertToolCallForTest(
	ctx context.Context,
	store *Store,
	input toolCallFixtureInput,
) (executionstore.ToolCallRecord, error) {
	if input.Type == "" {
		input.Type = toolcatalog.ToolTypeBuiltIn
	}
	if err := modelenvelope.ValidateToolInput(input.Input); err != nil {
		return executionstore.ToolCallRecord{}, errors.New("tool call input must be a JSON object")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := store.q.WithTx(tx).InsertToolCall(ctx, dbsqlc.InsertToolCallParams{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		SourceEventID:      input.SourceEventID,
		ModelCallContextID: input.ModelCallContextID,
		RuntimeLockID:      input.RuntimeLockID,
		ProviderCallID:     input.ProviderCallID,
		Name:               input.Name,
		Input:              input.Input,
		Type:               input.Type,
	})
	if storeutil.IsUniqueViolation(err) {
		return executionstore.ToolCallRecord{}, storeerr.ErrIdempotencyConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if runtimeErr := store.Execution().EnsureRuntimeLockActive(
			ctx,
			input.ProjectID,
			input.AgentID,
			input.RuntimeLockID,
		); runtimeErr != nil {
			return executionstore.ToolCallRecord{}, runtimeErr
		}
		return executionstore.ToolCallRecord{}, storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	var modelOutputID ID
	var ordinal int32
	if err := tx.QueryRow(ctx, `
SELECT call.model_output_id,
       coalesce(max(block.ordinal) + 1, 0)::integer
FROM tool_calls call
LEFT JOIN content_blocks block
  ON block.agent_id = call.agent_id
 AND block.owner_model_output_id = call.model_output_id
JOIN agents agent ON agent.id = call.agent_id
WHERE agent.project_id = $1
  AND call.agent_id = $2
  AND call.id = $3
GROUP BY call.model_output_id
`, input.ProjectID, input.AgentID, row.ID).Scan(&modelOutputID, &ordinal); err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, tx, executionstore.CreateContentBlockInput{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		OwnerKind:          executionstore.ContentBlockOwnerModelOutput,
		OwnerModelOutputID: modelOutputID,
		Ordinal:            ordinal,
		BlockKind:          executionstore.ContentBlockKindToolCall,
		ToolCallID:         row.ID,
	}); err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	return executionstore.IntegrationToolCallRecordFromInsertSQLC(row), nil
}

type recordingPostCommitPublisher struct {
	intents []notifications.PostCommitIntent
}

func startProcessForTest(
	ctx context.Context,
	store *Store,
	transaction executionstore.ExecuteToolCallInput,
	input executionstore.CreateProcessInput,
) (executionstore.ProcessRecord, error) {
	return executeToolCallCommandForTest[executionstore.ProcessRecord](
		ctx,
		store,
		transaction,
		executionstore.StartProcessForToolCall(input),
	)
}

func createProcessActionForTest(
	ctx context.Context,
	store *Store,
	transaction executionstore.ExecuteToolCallInput,
	input executionstore.CreateProcessActionInput,
) (executionstore.ProcessActionRecord, error) {
	return executeToolCallCommandForTest[executionstore.ProcessActionRecord](
		ctx,
		store,
		transaction,
		executionstore.CreateProcessActionForToolCall(input),
	)
}

func (p *recordingPostCommitPublisher) PublishPostCommit(_ context.Context, intent notifications.PostCommitIntent) {
	p.intents = append(p.intents, intent)
}

func (p *recordingPostCommitPublisher) runtimeEndedCount(runtimeID ID) int {
	count := 0
	for _, intent := range p.intents {
		ended, ok := intent.(notifications.DaemonRuntimeEndedCommitted)
		if ok && ended.RuntimeID == runtimeID {
			count++
		}
	}
	return count
}

func (p *recordingPostCommitPublisher) hasProcessTermination(machineID, processID ID) bool {
	for _, intent := range p.intents {
		termination, ok := intent.(notifications.DaemonProcessTerminationCommitted)
		if !ok || termination.MachineID != machineID {
			continue
		}
		for _, terminatedProcessID := range termination.ProcessIDs {
			if terminatedProcessID == processID {
				return true
			}
		}
	}
	return false
}

func testMachineRef(value string) string {
	return fmt.Sprintf("mchr-%06x", crc32.ChecksumIEEE([]byte(value))&0xffffff)
}

func mustEnsureOmnaraActor(
	t *testing.T,
	ctx context.Context,
	store *Store,
	orgID, projectID, userID ID,
) ID {
	t.Helper()
	params, err := executionstore.OmnaraActorParams(orgID, userPrincipal(userID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	actorID, err := executionstore.IntegrationResolveActorTx(ctx, store.q, projectID, NilID, params, NilID)
	if err != nil {
		t.Fatalf("ensure omnara actor: %v", err)
	}
	return actorID
}

func (fixture processDaemonFixture) omnaraActorID(t *testing.T, ctx context.Context) ID {
	return mustEnsureOmnaraActor(t, ctx, fixture.Store, testOrgID, testProjectID, fixture.UserID)
}

func mustOmnaraActorParams(t *testing.T, userID ID) *executionstore.ActorParams {
	t.Helper()
	params, err := executionstore.OmnaraActorParams(testOrgID, userPrincipal(userID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	return params
}

func (fixture processDaemonFixture) omnaraProducer(t *testing.T) *executionstore.ActorParams {
	return mustOmnaraActorParams(t, fixture.UserID)
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
	user, err := store.Identity().CreateVerifiedUser(ctx, CreateVerifiedUserInput{Email: email, DisplayName: displayName})
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "member"}); err != nil {
		t.Fatalf("add org membership for %s: %v", email, err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{OrgID: testOrgID, ProjectID: testProjectID, UserID: user.ID, Role: projectRole}); err != nil {
		t.Fatalf("add project %s membership for %s: %v", projectRole, email, err)
	}
	return user
}

type processDaemonFixture struct {
	Store     *Store
	OrgID     ID
	AgentID   ID
	MachineID ID
	BindingID ID
	TokenID   ID
	RuntimeID ID
	DaemonID  ID
	UserID    ID
	Lock      executionstore.AgentRuntimeLockRecord
	GrantID   ID
	Now       time.Time
}

func (fixture processDaemonFixture) authority() executionstore.DaemonRuntimeAuthority {
	return executionstore.DaemonRuntimeAuthority{
		OrgID:           fixture.OrgID,
		MachineID:       fixture.MachineID,
		DaemonRuntimeID: fixture.RuntimeID,
		DaemonTokenID:   fixture.TokenID,
	}
}

func (fixture processDaemonFixture) authorityForRuntime(runtimeID ID) executionstore.DaemonRuntimeAuthority {
	authority := fixture.authority()
	authority.DaemonRuntimeID = runtimeID
	return authority
}

func newProcessDaemonFixture(t *testing.T, ctx context.Context, testName string) processDaemonFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithModelCallRetryBackoff(func(int, string) time.Duration { return 0 }))
	now := time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	user := mustCreateProjectOperatorUser(t, ctx, store, "process-"+testName+"@example.com", "Process Tester")
	return newProcessDaemonFixtureInStore(t, ctx, store, user.ID, testName, now)
}

func newProcessDaemonFixtureInStore(
	t *testing.T,
	ctx context.Context,
	store *Store,
	userID ID,
	testName string,
	now time.Time,
) processDaemonFixture {
	t.Helper()
	createdMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Test daemon machine " + testName,
			IdempotencyKey: "idem-machine-" + testName,
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	grant, machine, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      createdMachine.ID,
			IdempotencyKey: "idem-grant-" + testName,
		},
	)
	if err != nil {
		t.Fatalf("create project machine grant: %v", err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:           testOrgID,
			MachineID:       createdMachine.ID,
			Name:            "daemon",
			Token:           "token-" + testName,
			CreatedByUserID: userID,
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	daemonID := testID("daemon-" + testName)
	runtime, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            createdMachine.OrgID,
			MachineID:        createdMachine.ID,
			DaemonTokenID:    token.ID,
			DaemonInstanceID: daemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}
	agentID := mustCreateAgent(t, ctx, store, now)
	binding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: grant.ID,
			MachineRef:            testMachineRef(testName),
			BindingKind:           "explicit",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("bind agent machine: %v", err)
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
	return processDaemonFixture{
		Store:     store,
		OrgID:     machine.OrgID,
		AgentID:   agentID,
		MachineID: machine.ID,
		BindingID: binding.ID,
		TokenID:   token.ID,
		RuntimeID: runtime.ID,
		DaemonID:  daemonID,
		UserID:    userID,
		Lock:      lock,
		GrantID:   grant.ID,
		Now:       now,
	}
}

func newProcessMachineFixtureWithoutDaemonRuntime(
	t *testing.T,
	ctx context.Context,
	testName string,
) processDaemonFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	user := mustCreateProjectOperatorUser(t, ctx, store, "process-"+testName+"@example.com", "Process Tester")
	createdMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Test daemon machine " + testName,
			IdempotencyKey: "idem-machine-" + testName,
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	grant, machine, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      createdMachine.ID,
			IdempotencyKey: "idem-grant-" + testName,
		},
	)
	if err != nil {
		t.Fatalf("create project machine grant: %v", err)
	}
	agentID := mustCreateAgent(t, ctx, store, now)
	binding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: grant.ID,
			MachineRef:            testMachineRef(testName),
			BindingKind:           "explicit",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("bind agent machine: %v", err)
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
	return processDaemonFixture{
		Store:     store,
		OrgID:     machine.OrgID,
		AgentID:   agentID,
		MachineID: machine.ID,
		BindingID: binding.ID,
		UserID:    user.ID,
		Lock:      lock,
		GrantID:   grant.ID,
		Now:       now,
	}
}

func expireDaemonRuntimeForTest(t *testing.T, ctx context.Context, fixture processDaemonFixture) {
	t.Helper()
	if _, err := fixture.Store.pool.Exec(ctx, `
		UPDATE daemon_runtimes
		SET last_seen_at = statement_timestamp() - INTERVAL '2 seconds',
		    lease_expires_at = statement_timestamp() - INTERVAL '1 second',
		    updated_at = statement_timestamp()
		WHERE org_id = $1 AND machine_id = $2 AND id = $3
	`, fixture.OrgID, fixture.MachineID, fixture.RuntimeID); err != nil {
		t.Fatalf("expire daemon runtime: %v", err)
	}
}

func assertMachineState(
	t *testing.T,
	ctx context.Context,
	store *Store,
	machineID ID,
	lifecycleState executionstore.MachineLifecycleState,
	connectionState executionstore.MachineConnectionState,
) {
	t.Helper()
	machine, err := store.Execution().GetMachine(ctx, testOrgID, machineID)
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	if machine.LifecycleState != lifecycleState || machine.ConnectionState != connectionState {
		t.Fatalf(
			"machine state = %s/%s, want %s/%s",
			machine.LifecycleState,
			machine.ConnectionState,
			lifecycleState,
			connectionState,
		)
	}
}

func assertMachineObservedPlatform(t *testing.T, ctx context.Context, store *Store, machineID ID, osName, arch string) {
	t.Helper()
	machine, err := store.Execution().GetMachine(ctx, testOrgID, machineID)
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	var metadata struct {
		ObservedPlatform struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		} `json:"observed_platform"`
	}
	if err := json.Unmarshal(machine.Metadata, &metadata); err != nil {
		t.Fatalf("parse machine metadata: %v", err)
	}
	if metadata.ObservedPlatform.OS != osName || metadata.ObservedPlatform.Arch != arch {
		t.Fatalf("observed platform = %+v, want %s/%s", metadata.ObservedPlatform, osName, arch)
	}
}

func createToolCallForProcessActionTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	testName string,
) ID {
	return createToolCallForProcessTest(t, ctx, fixture, testName, "read_process")
}

func createToolCallForProcessTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	testName string,
	toolName string,
) ID {
	return createToolCallForProcessTestWithPermission(t, ctx, fixture, testName, toolName, true)
}

func claimToolCallForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, toolCallID, runtimeLockID ID,
	retainRuntimeOwnership bool,
) executionstore.ToolCallRecord {
	t.Helper()
	if _, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       agentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: runtimeLockID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			if !retainRuntimeOwnership {
				return nil, errors.New("test tool call claim requires runtime ownership")
			}
			return executionstore.StartToolCallAsync(), nil
		},
	); err != nil {
		t.Fatalf("claim tool call: %v", err)
	}
	record, err := store.Execution().GetToolCall(ctx, testProjectID, agentID, toolCallID)
	if err != nil {
		t.Fatalf("load claimed tool call: %v", err)
	}
	return record
}

func createToolCallForProcessTestWithPermission(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	testName string,
	toolName string,
	allowed bool,
) ID {
	return createTypedToolCallForProcessTest(
		t,
		ctx,
		fixture,
		testName,
		toolName,
		toolcatalog.ToolTypeBuiltIn,
		allowed,
	)
}

type processToolCallBatchItem struct {
	TestName string
	ToolName string
	ToolType string
	Allowed  bool
}

func builtInProcessToolCallBatchItem(testName, toolName string) processToolCallBatchItem {
	return processToolCallBatchItem{
		TestName: testName,
		ToolName: toolName,
		ToolType: toolcatalog.ToolTypeBuiltIn,
		Allowed:  true,
	}
}

func customProcessToolCallBatchItem(testName, toolName string) processToolCallBatchItem {
	return processToolCallBatchItem{
		TestName: testName,
		ToolName: toolName,
		ToolType: toolcatalog.ToolTypeCustom,
		Allowed:  true,
	}
}

func createCustomReadyToolCallForProcessTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	testName string,
	toolName string,
) ID {
	t.Helper()
	toolCallID := createTypedToolCallForProcessTest(
		t,
		ctx,
		fixture,
		testName,
		toolName,
		toolcatalog.ToolTypeCustom,
		true,
	)
	return toolCallID
}

func createTypedToolCallForProcessTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	testName string,
	toolName string,
	toolType string,
	allowed bool,
) ID {
	t.Helper()
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		testName,
		[]processToolCallBatchItem{{
			TestName: testName,
			ToolName: toolName,
			ToolType: toolType,
			Allowed:  allowed,
		}},
	)
	return toolCallIDs[0]
}

func createToolCallBatchForProcessTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	batchName string,
	items []processToolCallBatchItem,
) []ID {
	t.Helper()
	if batchName == "" {
		t.Fatal("process tool-call batch name is required")
	}
	if len(items) == 0 {
		t.Fatal("process tool-call batch must contain at least one proposal")
	}
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"seed tool call"}]`),
			IdempotencyKey: "process-tool-input-" + batchName,
		},
	)
	if err != nil {
		t.Fatalf("create source agent input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found || len(admitted.Events) != 1 {
		t.Fatalf("admit source agent input: found=%v admitted=%+v", found, admitted)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for model context fixture: %v", err)
	}
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    []ID{input.ID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: admitted.Events[0].Sequence,
	})
	if err != nil {
		t.Fatalf("claim model call context: %v", err)
	}
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		claim.Context.ID,
	)
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(items))
	responseParts := make([]modelenvelope.ResponsePart, 0, len(items))
	seenTestNames := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.TestName == "" || item.ToolName == "" || item.ToolType == "" {
			t.Fatalf("invalid process tool-call batch item: %+v", item)
		}
		if _, exists := seenTestNames[item.TestName]; exists {
			t.Fatalf("duplicate process tool-call batch item name %q", item.TestName)
		}
		seenTestNames[item.TestName] = struct{}{}
		providerCallID := "call_" + item.TestName
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ProviderCallID: providerCallID,
			Type:           item.ToolType,
		})
		responseParts = append(responseParts, modelenvelope.ResponsePart{
			Type:           "tool_call",
			ProviderCallID: providerCallID,
			ToolName:       item.ToolName,
			ToolInput:      json.RawMessage(`{}`),
		})
	}
	_, toolCalls, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			ModelCallContextID: claim.Context.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: providerModelSlug,
				ServedProviderModelSlug:    providerModelSlug,
				APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
				APIVariant:                 modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "resp_" + batchName,
					Content:    responseParts,
					StopReason: modelenvelope.StopReasonToolUse,
				},
			},
			ToolCallBindings: bindings,
		},
	)
	if err != nil {
		t.Fatalf("record tool call proposal: %v", err)
	}
	if len(toolCalls) != len(items) {
		t.Fatalf("recorded tool calls = %+v, want %d", toolCalls, len(items))
	}
	toolCallsByProviderID := make(map[string]executionstore.ToolCallRecord, len(toolCalls))
	for _, toolCall := range toolCalls {
		toolCallsByProviderID[toolCall.ProviderCallID] = toolCall
	}
	toolCallIDs := make([]ID, len(items))
	for index, item := range items {
		providerCallID := "call_" + item.TestName
		toolCall, found := toolCallsByProviderID[providerCallID]
		if !found {
			t.Fatalf("recorded tool calls = %+v, missing provider call %q", toolCalls, providerCallID)
		}
		toolCallIDs[index] = toolCall.ID
		if !item.Allowed {
			continue
		}
		if _, err := fixture.Store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            toolCall.ID,
			RuntimeLockID: fixture.Lock.ID,
		}); err != nil {
			t.Fatalf("mark test tool call %q permission allowed: %v", item.TestName, err)
		}
	}
	return toolCallIDs
}

func modelContextIDForProcessToolCallTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
) ID {
	t.Helper()
	var modelContextID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT model_call_context_id
FROM tool_call_read_projection
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&modelContextID); err != nil {
		t.Fatalf("load tool call model context: %v", err)
	}
	return modelContextID
}

func turnIDForProcessToolCallTest(t *testing.T, ctx context.Context, fixture processDaemonFixture, toolCallID ID) ID {
	t.Helper()
	var turnID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT turn_id
FROM tool_call_read_projection tool_call
WHERE tool_call.project_id = $1 AND tool_call.agent_id = $2 AND tool_call.id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&turnID); err != nil {
		t.Fatalf("load tool call turn: %v", err)
	}
	return turnID
}

func providerCallIDForProcessToolCallTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
) string {
	t.Helper()
	var providerCallID string
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT provider_call_id
FROM tool_call_read_projection
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&providerCallID); err != nil {
		t.Fatalf("load tool call provider call id: %v", err)
	}
	return providerCallID
}

func openingInputAndWatermarkForProcessToolCallTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
) (ID, int64) {
	t.Helper()
	var inputID ID
	var watermark int64
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT opening_event.agent_input_id, event.sequence
FROM tool_call_read_projection tool_call
JOIN model_call_contexts context ON context.agent_id = tool_call.agent_id
  AND context.id = tool_call.model_call_context_id
JOIN agent_events opening_event ON opening_event.agent_id = context.agent_id
  AND opening_event.turn_id = tool_call.turn_id
  AND opening_event.is_opening_event
  AND opening_event.event_kind = 'agent_input'
  AND opening_event.agent_input_id IS NOT NULL
JOIN tool_call_results result ON result.agent_id = tool_call.agent_id
  AND result.tool_call_id = tool_call.id
JOIN agent_events event ON event.agent_id = result.agent_id
  AND event.tool_call_result_id = result.id
WHERE tool_call.project_id = $1 AND tool_call.agent_id = $2 AND tool_call.id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&inputID, &watermark); err != nil {
		t.Fatalf("load tool call opening input and watermark: %v", err)
	}
	return inputID, watermark
}

func appendStopEventForProcessTest(t *testing.T, ctx context.Context, fixture processDaemonFixture) {
	t.Helper()
	if _, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		Actor:     fixture.omnaraProducer(t),
	}); err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
}

func acceptDaemonProcessForTest(
	ctx context.Context,
	store *Store,
	orgID, machineID, runtimeID, processID ID,
) (executionstore.DaemonProcessOffer, bool, error) {
	authority, err := daemonRuntimeAuthorityForTest(ctx, store, orgID, machineID, runtimeID)
	if err != nil {
		return executionstore.DaemonProcessOffer{}, false, err
	}
	offers, err := store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{Authority: authority, Limit: 32},
	)
	if err != nil {
		return executionstore.DaemonProcessOffer{}, false, err
	}
	for _, offer := range offers {
		if processID != NilID && offer.Process.ID != processID {
			continue
		}
		return store.Execution().AcceptDaemonProcess(
			ctx,
			executionstore.AcceptDaemonProcessInput{
				Authority: authority,
				ProcessID: offer.Process.ID,
			},
		)
	}
	return executionstore.DaemonProcessOffer{}, false, nil
}

func acceptDaemonProcessActionForTest(
	ctx context.Context,
	store *Store,
	orgID, machineID, runtimeID, processID, actionID ID,
) (executionstore.ProcessActionRecord, bool, error) {
	authority, err := daemonRuntimeAuthorityForTest(ctx, store, orgID, machineID, runtimeID)
	if err != nil {
		return executionstore.ProcessActionRecord{}, false, err
	}
	offers, err := store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{Authority: authority, Limit: 32},
	)
	if err != nil {
		return executionstore.ProcessActionRecord{}, false, err
	}
	for _, offer := range offers {
		if processID != NilID && offer.ProcessID != processID {
			continue
		}
		if actionID != NilID && offer.ID != actionID {
			continue
		}
		grant, found, err := store.Execution().AcceptDaemonProcessAction(
			ctx,
			executionstore.AcceptDaemonProcessActionInput{
				Authority: authority,
				ProcessID: offer.ProcessID,
				ID:        offer.ID,
			},
		)
		return grant.Action, found, err
	}
	return executionstore.ProcessActionRecord{}, false, nil
}

func daemonRuntimeAuthorityForTest(
	ctx context.Context,
	store *Store,
	orgID, machineID, runtimeID ID,
) (executionstore.DaemonRuntimeAuthority, error) {
	var tokenID ID
	err := store.pool.QueryRow(
		ctx,
		`SELECT daemon_token_id FROM daemon_runtimes WHERE org_id = $1 AND machine_id = $2 AND id = $3`,
		orgID,
		machineID,
		runtimeID,
	).Scan(&tokenID)
	if err != nil {
		return executionstore.DaemonRuntimeAuthority{}, fmt.Errorf("load daemon runtime authority: %w", err)
	}
	return executionstore.DaemonRuntimeAuthority{
		OrgID:           orgID,
		MachineID:       machineID,
		DaemonRuntimeID: runtimeID,
		DaemonTokenID:   tokenID,
	}, nil
}

type terminalProcessActionTestInput struct {
	Name     string
	ToolName string
	Kind     executionstore.ProcessActionKind
	Accepted bool
}

type terminalProcessActionTestResult struct {
	Process    executionstore.ProcessRecord
	Action     executionstore.ProcessActionRecord
	ToolCallID ID
}

func createTerminalProcessActionForLifecycleTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	name, toolName string,
	kind executionstore.ProcessActionKind,
	accepted bool,
) (executionstore.ProcessRecord, executionstore.ProcessActionRecord, ID) {
	t.Helper()
	result := createTerminalProcessActionsForLifecycleTest(
		t,
		ctx,
		fixture,
		[]terminalProcessActionTestInput{{
			Name:     name,
			ToolName: toolName,
			Kind:     kind,
			Accepted: accepted,
		}},
	)[0]
	return result.Process, result.Action, result.ToolCallID
}

func createTerminalProcessActionsForLifecycleTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	inputs []terminalProcessActionTestInput,
) []terminalProcessActionTestResult {
	t.Helper()
	if len(inputs) == 0 {
		t.Fatal("terminal process action fixtures require at least one input")
	}
	items := make([]processToolCallBatchItem, 0, len(inputs)*2)
	for _, input := range inputs {
		items = append(
			items,
			builtInProcessToolCallBatchItem(input.Name+"_process", "run_command"),
			builtInProcessToolCallBatchItem(input.Name+"_action", input.ToolName),
		)
	}
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		inputs[0].Name+"_terminal_actions",
		items,
	)
	results := make([]terminalProcessActionTestResult, 0, len(inputs))
	for index, input := range inputs {
		offset := time.Duration(index) * time.Millisecond
		process, err := startProcessForTest(
			ctx,
			fixture.Store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallIDs[index*2],
				RuntimeLockID: fixture.Lock.ID,
			},
			executionstore.CreateProcessInput{
				AgentMachineBindingID: fixture.BindingID,
				Command:               "cat",
				ShellSelector:         "sh",
				Cwd:                   "/work",
			},
		)
		if err != nil {
			t.Fatalf("start process for %s action: %v", input.Kind, err)
		}
		if _, found, err := acceptDaemonProcessForTest(
			ctx,
			fixture.Store,
			fixture.OrgID,
			fixture.MachineID,
			fixture.RuntimeID,
			process.ID,
		); err != nil || !found {
			t.Fatalf("accept process for %s action found=%t err=%v", input.Kind, found, err)
		}
		started, err := fixture.Store.Execution().MarkProcessStarted(
			ctx,
			executionstore.MarkProcessStartedInput{
				Authority:       fixture.authority(),
				ProjectID:       testProjectID,
				AgentID:         fixture.AgentID,
				ID:              process.ID,
				SourceStartedAt: fixture.Now.Add(2*time.Second + offset),
			},
		)
		if err != nil {
			t.Fatalf("mark process started for %s action: %v", input.Kind, err)
		}
		if !started.ToolResultCommitted {
			t.Fatalf("process start for %s action did not commit its tool result", input.Kind)
		}
		process = started.Process
		actionToolCallID := toolCallIDs[index*2+1]
		action, err := createProcessActionForTest(
			ctx,
			fixture.Store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    actionToolCallID,
				RuntimeLockID: fixture.Lock.ID,
			},
			executionstore.CreateProcessActionInput{
				ProcessID:  process.ID,
				ActionKind: input.Kind,
				Payload:    json.RawMessage(`{}`),
			},
		)
		if err != nil {
			t.Fatalf("create %s action: %v", input.Kind, err)
		}
		if input.Accepted {
			if _, found, err := acceptDaemonProcessActionForTest(
				ctx,
				fixture.Store,
				fixture.OrgID,
				fixture.MachineID,
				fixture.RuntimeID,
				process.ID,
				action.ID,
			); err != nil || !found {
				t.Fatalf("accept %s action found=%t err=%v", input.Kind, found, err)
			}
		}
		exitCode := 0
		if _, err := fixture.Store.Execution().CompleteDaemonProcess(
			ctx,
			executionstore.CompleteDaemonProcessInput{
				Authority:     fixture.authority(),
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ID:            process.ID,
				State:         executionstore.ProcessStateExited,
				ExitCode:      &exitCode,
				SourceEndedAt: fixture.Now.Add(5*time.Second + offset),
			},
		); err != nil {
			t.Fatalf("complete process with %s action: %v", input.Kind, err)
		}
		results = append(results, terminalProcessActionTestResult{
			Process:    process,
			Action:     action,
			ToolCallID: actionToolCallID,
		})
	}
	return results
}

func markProcessStartedForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	process executionstore.ProcessRecord,
	at time.Time,
) {
	t.Helper()
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       fixture.authority(),
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		SourceStartedAt: at,
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
}

func machineUnreachableRecheckForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	fallbackAt time.Time,
) bool {
	t.Helper()
	tx, err := fixture.Store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin machine-unreachable recheck tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	got, err := executionstore.IntegrationMachineStillUnreachableForToolExpiryTx(
		ctx,
		dbsqlc.New(tx),
		fixture.OrgID,
		fixture.MachineID,
		fallbackAt,
		0,
	)
	if err != nil {
		t.Fatalf("machine-unreachable recheck: %v", err)
	}
	return got
}

func forceToolCallResultForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentID, toolCallID ID,
	outcome executionstore.ToolResultOutcome,
	result json.RawMessage,
) {
	t.Helper()
	parts, err := executionstore.ToolResultContentParts(result)
	if err != nil {
		t.Fatalf("forced tool result content parts: %v", err)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin forced tool completion: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
UPDATE tool_calls
SET state = 'completed',
    runtime_lock_id = NULL
FROM agents agent
WHERE agent.project_id = $1
  AND agent.id = tool_calls.agent_id
  AND tool_calls.agent_id = $2
  AND tool_calls.id = $3
  AND tool_calls.state <> 'completed'
`, projectID, agentID, toolCallID); err != nil {
		t.Fatalf("force tool call completed: %v", err)
	}
	record, err := executionstore.IntegrationGetToolCallTx(ctx, tx, projectID, agentID, toolCallID)
	if err != nil {
		t.Fatalf("load forced tool call: %v", err)
	}
	record.Outcome = outcome
	record.ResultContentParts = parts
	if _, err := executionstore.IntegrationAppendToolResultEventTx(
		ctx,
		notifications.NewTxNotifications(),
		tx,
		record,
	); err != nil {
		t.Fatalf("append forced tool result event: %v", err)
	}
	metadata, err := marshalJSON(
		map[string]any{
			"reason":                "tool_result",
			"tool_call_id":          record.ID,
			"model_call_context_id": record.ModelCallContextID,
			"outcome":               outcome,
		},
	)
	if err != nil {
		t.Fatalf("marshal forced tool result wakeup metadata: %v", err)
	}
	if err := dbsqlc.New(tx).MarkAgentWakeup(ctx, dbsqlc.MarkAgentWakeupParams{ProjectID: projectID, AgentID: agentID, Metadata: metadata}); err != nil {
		t.Fatalf("mark forced tool result wakeup: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit forced tool completion: %v", err)
	}
}

func cancelToolCallForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, toolCallID ID,
) {
	t.Helper()
	forceToolCallResultForTest(
		t,
		ctx,
		store,
		testProjectID,
		agentID,
		toolCallID,
		executionstore.ToolResultOutcomeCanceled,
		json.RawMessage(`{"reason":"tool call canceled by test"}`),
	)
}

func assertProcessActionToolResultUsesPublicIDs(
	t *testing.T,
	store *Store,
	agentID ID,
	turnID ID,
	toolCallID ID,
	processID ID,
	actionID ID,
) {
	t.Helper()
	processPublicID, err := publicid.Encode(publicid.KindProcess, processID)
	if err != nil {
		t.Fatalf("encode process id: %v", err)
	}
	actionPublicID, err := publicid.Encode(publicid.KindProcessAction, actionID)
	if err != nil {
		t.Fatalf("encode process action id: %v", err)
	}
	toolCall := completedToolCallForTest(t, store, agentID, turnID, toolCallID)
	body := string(toolCall.ResultContentParts)
	if !strings.Contains(body, processPublicID) || !strings.Contains(body, actionPublicID) {
		t.Fatalf(
			"tool result must expose public process/action ids; body=%s process=%s action=%s",
			body,
			processPublicID,
			actionPublicID,
		)
	}
	if strings.Contains(body, processID.String()) || strings.Contains(body, actionID.String()) {
		t.Fatalf("tool result leaked raw UUIDs; body=%s", body)
	}
}

func assertCompletedToolCallWithResult(
	t *testing.T,
	store *Store,
	agentID ID,
	toolCall executionstore.ToolCallRecord,
	reason string,
) {
	t.Helper()
	if toolCall.State != "completed" || toolCall.CompletedAt == nil {
		t.Fatalf("tool call should be completed before checking terminal result: %+v", toolCall)
	}
	result, found, err := store.Execution().GetToolCallResultAuthorityByToolCall(
		context.Background(),
		testProjectID,
		agentID,
		toolCall.ID,
	)
	if err != nil {
		t.Fatalf("get tool call result: %v", err)
	}
	if !found {
		t.Fatalf("completed tool call has no result authority: %+v", toolCall)
	}
	if result.Outcome != toolCall.Outcome {
		t.Fatalf("tool result outcome %q does not match execution outcome %q", result.Outcome, toolCall.Outcome)
	}
	if reason != "" {
		completed := completedToolCallForTest(t, store, agentID, toolCall.TurnID, toolCall.ID)
		if !strings.Contains(string(completed.ResultContentParts), reason) {
			t.Fatalf("typed tool call result body missing %q: %s", reason, completed.ResultContentParts)
		}
	}
}

func assertCompletedProcessActionResult(
	t *testing.T,
	store *Store,
	agentID ID,
	toolCall executionstore.ToolCallRecord,
	wantState executionstore.ProcessActionState,
) {
	t.Helper()
	assertCompletedToolCallWithResult(t, store, agentID, toolCall, "")
	completed := completedToolCallForTest(t, store, agentID, toolCall.TurnID, toolCall.ID)
	var parts []struct {
		Type  string `json:"type"`
		Value struct {
			State executionstore.ProcessActionState `json:"state"`
		} `json:"value"`
	}
	if err := json.Unmarshal(completed.ResultContentParts, &parts); err != nil {
		t.Fatalf("unmarshal process action result parts: %v body=%s", err, completed.ResultContentParts)
	}
	for _, part := range parts {
		if part.Type != "structured_data" {
			continue
		}
		if part.Value.State != wantState {
			t.Fatalf(
				"process action result state=%q, want %q body=%s",
				part.Value.State,
				wantState,
				completed.ResultContentParts,
			)
		}
		return
	}
	t.Fatalf("process action result missing structured_data body=%s", completed.ResultContentParts)
}

func completedToolCallForTest(
	t *testing.T,
	store *Store,
	agentID ID,
	turnID ID,
	toolCallID ID,
) executionstore.ToolCallRecord {
	t.Helper()
	completed, err := store.Execution().ListCompletedToolCallsForTurn(context.Background(), testProjectID, agentID, turnID)
	if err != nil {
		t.Fatalf("list completed tool calls for turn: %v", err)
	}
	for _, record := range completed {
		if record.ID == toolCallID {
			return record
		}
	}
	t.Fatalf("completed tool call %s not found in turn %s; completed=%+v", toolCallID, turnID, completed)
	return executionstore.ToolCallRecord{}
}
