//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func systemPrincipalForTest(id ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeSystem, ID: id}
}

func defaultAgentListingForTest() listing.Options {
	return listing.Options{SortField: "created_at", SortDesc: true}
}

const subagentParentYAML = `
instruction: Coordinate helpers.
model:
  provider_config: openai-prod
  name: gpt-test
subagents:
  fork:
    type: self
    max_concurrent: 1
max_subagents: 2
`

func intPtrForSubagentTest(value int) *int {
	return &value
}

func spawnSubagentForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	parent executionstore.AgentRecord,
	configID ID,
	name, idempotencyKey string,
	maxConcurrent *int,
	options ...func(*executionstore.LaunchAgentInput),
) (executionstore.LaunchAgentResult, error) {
	t.Helper()
	actor, err := executionstore.SubagentActorParams(parent.OrgID, parent)
	if err != nil {
		t.Fatalf("subagent actor params: %v", err)
	}
	input := executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  configID,
		LaunchedBy:     systemPrincipalForTest(parent.ID),
		Name:           &name,
		Message:        "Investigate the failing build.",
		MessageActor:   actor,
		IdempotencyKey: idempotencyKey,
		Subagent: &executionstore.SubagentLaunch{
			ParentAgentID: parent.ID,
			Key:           "fork",
			MaxConcurrent: maxConcurrent,
		},
	}
	for _, option := range options {
		option(&input)
	}
	return store.Execution().LaunchAgent(ctx, input)
}

func withoutLaunchMessage(input *executionstore.LaunchAgentInput) {
	input.Message = ""
}

func TestLaunchSubagentLinksParentAndEnforcesLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-launch@example.com", "Subagent Launch")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "subagent-launch", "Subagent Launch", subagentParentYAML)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-launch-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent

	child, err := spawnSubagentForTest(t, ctx, store, parent, profile.CurrentConfigID, "worker-1", "subagent-launch-child-1", intPtrForSubagentTest(1))
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if child.Agent.ParentAgentID != parent.ID || child.Agent.SubagentKey != "fork" {
		t.Fatalf("child linkage = parent %s key %q", child.Agent.ParentAgentID, child.Agent.SubagentKey)
	}
	if child.AgentInput.ID == NilID {
		t.Fatal("child launch did not queue the task input")
	}

	if _, err := spawnSubagentForTest(t, ctx, store, parent, profile.CurrentConfigID, "worker-2", "subagent-launch-child-2", intPtrForSubagentTest(1)); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("second spawn beyond max_concurrent: err = %v, want conflict", err)
	}
	if _, err := spawnSubagentForTest(t, ctx, store, parent, profile.CurrentConfigID, "worker-1", "subagent-launch-child-3", nil); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("duplicate subagent name: err = %v, want invalid request", err)
	}

	subagents, err := store.Execution().ListSubagents(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(subagents) != 1 || subagents[0].AgentID != child.Agent.ID || subagents[0].State != executionstore.SubagentStateRunning {
		t.Fatalf("subagents = %+v", subagents)
	}

	topLevel, err := store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
		ProjectID: testProjectID,
		Limit:     50,
		List:      defaultAgentListingForTest(),
	})
	if err != nil {
		t.Fatalf("list top-level agents: %v", err)
	}
	if containsAgentID(topLevel.Agents, child.Agent.ID) || !containsAgentID(topLevel.Agents, parent.ID) {
		t.Fatalf("top-level listing should hide subagents: %+v", agentIDsForTest(topLevel.Agents))
	}
	withChildren, err := store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
		ProjectID: testProjectID,
		Limit:     50,
		List:      defaultAgentListingForTest(),
		Filters:   executionstore.AgentListFilters{IncludeSubagents: true},
	})
	if err != nil {
		t.Fatalf("list with subagents: %v", err)
	}
	if !containsAgentID(withChildren.Agents, child.Agent.ID) {
		t.Fatalf("include_subagents listing should contain the child")
	}
	parentID := parent.ID
	byParent, err := store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
		ProjectID: testProjectID,
		Limit:     50,
		List:      defaultAgentListingForTest(),
		Filters:   executionstore.AgentListFilters{ParentAgentID: &parentID},
	})
	if err != nil {
		t.Fatalf("list by parent: %v", err)
	}
	if len(byParent.Agents) != 1 || byParent.Agents[0].ID != child.Agent.ID {
		t.Fatalf("parent filter listing = %+v", agentIDsForTest(byParent.Agents))
	}

	descendants, err := store.Execution().ListAgentDescendantIDs(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list descendants: %v", err)
	}
	if len(descendants) != 1 || descendants[0] != child.Agent.ID {
		t.Fatalf("descendants = %v", descendants)
	}

	archived, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, parent.ID, userPrincipal(user.ID))
	if err != nil {
		t.Fatalf("archive parent: %v", err)
	}
	if archived.State != executionstore.AgentStateArchived {
		t.Fatalf("parent state = %s", archived.State)
	}
	childAfter, err := store.Execution().GetAgentInProject(ctx, testProjectID, child.Agent.ID)
	if err != nil {
		t.Fatalf("load child after parent archive: %v", err)
	}
	if childAfter.State != executionstore.AgentStateArchived {
		t.Fatalf("archiving the parent should archive the child, got %s", childAfter.State)
	}
}

func TestSubagentArchiveNotifiesParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-notify@example.com", "Subagent Notify")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-notify", "Subagent Notify", subagentParentYAML,
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-notify-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "notify", "subagent-notify-child", nil,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if _, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, child.Agent.ID, userPrincipal(user.ID)); err != nil {
		t.Fatalf("archive subagent: %v", err)
	}
	var metadata json.RawMessage
	if err := pool.QueryRow(
		ctx,
		`SELECT metadata FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'subagent_message'`,
		testProjectID,
		parent.ID,
	).Scan(&metadata); err != nil {
		t.Fatalf("load parent notification input: %v", err)
	}
	var decodedMetadata struct {
		SubagentMessage struct {
			Kind    string `json:"kind"`
			AgentID string `json:"agent_id"`
		} `json:"subagent_message"`
	}
	if err := json.Unmarshal(metadata, &decodedMetadata); err != nil {
		t.Fatalf("decode parent notification metadata: %v", err)
	}
	if decodedMetadata.SubagentMessage.Kind != "archived" || decodedMetadata.SubagentMessage.AgentID == "" {
		t.Fatalf("parent notification metadata = %s", metadata)
	}
}

func TestSubagentArchiveCompletesParentWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-wait@example.com", "Subagent Wait")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-wait", "Subagent Wait", subagentParentYAML,
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-wait-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "awaited", "subagent-wait-child", nil,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}

	runtimeLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		parent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire parent runtime lock: %v", err)
	}
	toolCallIDs := createReadyToolCallsForTest(
		t,
		ctx,
		store,
		parent.ID,
		user.ID,
		parentLaunch.AgentConfig.ID,
		runtimeLock,
		"subagent-wait",
		[]toolCallSpecForTest{{
			Label: "wait",
			Name:  "wait_agents",
			Input: json.RawMessage(`{"agents":["awaited"]}`),
		}},
	)
	waitToolCallID := toolCallIDs["wait"]
	execution, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       parent.ID,
			ToolCallID:    waitToolCallID,
			RuntimeLockID: runtimeLock.ID,
		},
		func(reader *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			target, err := reader.ResolveSubagentReference(ctx, "awaited")
			if err != nil {
				return nil, err
			}
			if target.AgentID != child.Agent.ID {
				t.Fatalf("resolved %s, want %s", target.AgentID, child.Agent.ID)
			}
			return executionstore.CreateAgentWaitForToolCall(
				executionstore.CreateAgentWaitInput{
					TargetAgentIDs: []ID{target.AgentID},
					Mode:           executionstore.AgentWaitModeAll,
				},
				func(outcome executionstore.AgentWaitOutcome) (executionstore.ToolCallCompletionInput, error) {
					parts, err := executionstore.ToolResultContentParts(mustTestRawJSON(t, outcome))
					if err != nil {
						return executionstore.ToolCallCompletionInput{}, err
					}
					return executionstore.ToolCallCompletionInput{
						Outcome:            executionstore.ToolResultOutcomeSucceeded,
						ResultContentParts: parts,
					}, nil
				},
			), nil
		},
	)
	if err != nil {
		t.Fatalf("create agent wait: %v", err)
	}
	if execution.Disposition != executionstore.ToolCallDispositionWaiting {
		t.Fatalf("wait disposition = %v, want waiting", execution.Disposition)
	}
	if err := store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, parent.ID, runtimeLock.ID); err != nil {
		t.Fatalf("release parent runtime lock: %v", err)
	}

	if _, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, child.Agent.ID, userPrincipal(user.ID)); err != nil {
		t.Fatalf("archive awaited subagent: %v", err)
	}
	waitCall, err := store.Execution().GetToolCall(ctx, testProjectID, parent.ID, waitToolCallID)
	if err != nil {
		t.Fatalf("load wait tool call: %v", err)
	}
	if waitCall.State != executionstore.ToolCallStateCompleted {
		t.Fatalf("wait tool call state = %s, want completed", waitCall.State)
	}
	if !strings.Contains(string(waitCall.ResultContentParts), "archived") {
		t.Fatalf("wait result = %s", waitCall.ResultContentParts)
	}
	var parentNotifications int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'subagent_message'`,
		testProjectID,
		parent.ID,
	).Scan(&parentNotifications); err != nil {
		t.Fatalf("count parent notifications: %v", err)
	}
	if parentNotifications != 0 {
		t.Fatalf("a satisfied wait should not also queue a parent notification, got %d", parentNotifications)
	}
}

func TestExpireToolCallsTimesOutParentWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-timeout@example.com", "Subagent Timeout")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-timeout", "Subagent Timeout", subagentParentYAML,
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-timeout-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "slow", "subagent-timeout-child", nil,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}

	runtimeLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		parent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire parent runtime lock: %v", err)
	}
	toolCallIDs := createReadyToolCallsForTest(
		t,
		ctx,
		store,
		parent.ID,
		user.ID,
		parentLaunch.AgentConfig.ID,
		runtimeLock,
		"subagent-timeout",
		[]toolCallSpecForTest{{
			Label: "wait",
			Name:  "wait_agents",
			Input: json.RawMessage(`{"agents":["slow"],"timeout_seconds":60}`),
		}},
	)
	waitToolCallID := toolCallIDs["wait"]
	timeoutSeconds := 60
	execution, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       parent.ID,
			ToolCallID:    waitToolCallID,
			RuntimeLockID: runtimeLock.ID,
		},
		func(reader *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateAgentWaitForToolCall(
				executionstore.CreateAgentWaitInput{
					TargetAgentIDs: []ID{child.Agent.ID},
					Mode:           executionstore.AgentWaitModeAll,
					TimeoutSeconds: &timeoutSeconds,
				},
				func(outcome executionstore.AgentWaitOutcome) (executionstore.ToolCallCompletionInput, error) {
					parts, err := executionstore.ToolResultContentParts(mustTestRawJSON(t, outcome))
					if err != nil {
						return executionstore.ToolCallCompletionInput{}, err
					}
					return executionstore.ToolCallCompletionInput{
						Outcome:            executionstore.ToolResultOutcomeSucceeded,
						ResultContentParts: parts,
					}, nil
				},
			), nil
		},
	)
	if err != nil {
		t.Fatalf("create agent wait: %v", err)
	}
	if execution.Disposition != executionstore.ToolCallDispositionWaiting {
		t.Fatalf("wait disposition = %v, want waiting", execution.Disposition)
	}
	if err := store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, parent.ID, runtimeLock.ID); err != nil {
		t.Fatalf("release parent runtime lock: %v", err)
	}

	expired, err := store.Execution().ExpireToolCalls(ctx, executionstore.ToolCallExpiryBatchSize)
	if err != nil {
		t.Fatalf("expire tool calls before deadline: %v", err)
	}
	if expired != 0 {
		t.Fatalf("expired %d tool calls before the deadline, want 0", expired)
	}
	var deadlineSet bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deadline_at IS NOT NULL FROM tool_calls WHERE agent_id = $1 AND id = $2`,
		parent.ID,
		waitToolCallID,
	).Scan(&deadlineSet); err != nil {
		t.Fatalf("load wait tool call deadline: %v", err)
	}
	if !deadlineSet {
		t.Fatal("wait tool call has no deadline")
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE tool_calls SET deadline_at = statement_timestamp() - interval '1 second'
		 WHERE agent_id = $1 AND id = $2`,
		parent.ID,
		waitToolCallID,
	); err != nil {
		t.Fatalf("backdate wait tool call deadline: %v", err)
	}

	expired, err = store.Execution().ExpireToolCalls(ctx, executionstore.ToolCallExpiryBatchSize)
	if err != nil {
		t.Fatalf("expire tool calls: %v", err)
	}
	if expired != 1 {
		t.Fatalf("expired %d tool calls, want 1", expired)
	}
	waitCall, err := store.Execution().GetToolCall(ctx, testProjectID, parent.ID, waitToolCallID)
	if err != nil {
		t.Fatalf("load wait tool call: %v", err)
	}
	if waitCall.State != executionstore.ToolCallStateCompleted {
		t.Fatalf("wait tool call state = %s, want completed", waitCall.State)
	}
	var waitResult []struct {
		Value executionstore.AgentWaitOutcome `json:"value"`
	}
	if err := json.Unmarshal(waitCall.ResultContentParts, &waitResult); err != nil {
		t.Fatalf("decode wait result %s: %v", waitCall.ResultContentParts, err)
	}
	if len(waitResult) != 1 || !waitResult[0].Value.TimedOut {
		t.Fatalf("wait result = %s, want timed_out", waitCall.ResultContentParts)
	}
	if len(waitResult[0].Value.Agents) != 1 ||
		waitResult[0].Value.Agents[0].ResultKind != executionstore.SubagentMessageKindTimeout {
		t.Fatalf("wait result = %s, want a timeout target", waitCall.ResultContentParts)
	}
	var pendingTargets int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_wait_targets
		 WHERE agent_id = $1 AND tool_call_id = $2 AND state = 'pending'`,
		parent.ID,
		waitToolCallID,
	).Scan(&pendingTargets); err != nil {
		t.Fatalf("count pending wait targets: %v", err)
	}
	if pendingTargets != 0 {
		t.Fatalf("pending wait targets = %d, want 0", pendingTargets)
	}
	expired, err = store.Execution().ExpireToolCalls(ctx, executionstore.ToolCallExpiryBatchSize)
	if err != nil {
		t.Fatalf("expire tool calls again: %v", err)
	}
	if expired != 0 {
		t.Fatalf("expired %d tool calls on the second pass, want 0", expired)
	}
}

func TestArchiveIdleSubagentsWaitsForBusyDescendants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-idle@example.com", "Subagent Idle")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-idle", "Subagent Idle", subagentParentYAML,
	)
	topLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-idle-top",
	})
	if err != nil {
		t.Fatalf("launch top-level agent: %v", err)
	}
	top := topLaunch.Agent
	middle, err := spawnSubagentForTest(
		t, ctx, store, top, profile.CurrentConfigID, "middle", "subagent-idle-middle", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.Subagent.ArchiveAfterIdleMinutes = intPtrForSubagentTest(1)
		},
	)
	if err != nil {
		t.Fatalf("spawn middle subagent: %v", err)
	}
	leaf, err := spawnSubagentForTest(
		t, ctx, store, middle.Agent, profile.CurrentConfigID, "leaf", "subagent-idle-leaf", nil,
	)
	if err != nil {
		t.Fatalf("spawn leaf subagent: %v", err)
	}
	for _, agentID := range []ID{middle.Agent.ID, leaf.Agent.ID} {
		if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, agentID); err != nil {
			t.Fatalf("clear wakeup: %v", err)
		}
	}
	leafLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		leaf.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire leaf runtime lock: %v", err)
	}
	asOf := time.Now().Add(2 * time.Hour)

	_, archived, err := store.Execution().ArchiveIdleSubagentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents while leaf is running: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents while a grandchild held a runtime lock, want 0", archived)
	}

	if err := store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, leaf.Agent.ID, leafLock.ID); err != nil {
		t.Fatalf("release leaf runtime lock: %v", err)
	}
	if err := store.Execution().MarkAgentWakeup(ctx, testProjectID, leaf.Agent.ID, []byte(`{"reason":"test"}`)); err != nil {
		t.Fatalf("mark leaf wakeup: %v", err)
	}
	_, archived, err = store.Execution().ArchiveIdleSubagentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents while leaf has a wakeup: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents while a grandchild had a pending wakeup, want 0", archived)
	}

	if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, leaf.Agent.ID); err != nil {
		t.Fatalf("clear leaf wakeup: %v", err)
	}
	_, archived, err = store.Execution().ArchiveIdleSubagents(ctx, 10)
	if err != nil {
		t.Fatalf("archive idle subagents before the idle window: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents before the idle window elapsed, want 0", archived)
	}
	_, archived, err = store.Execution().ArchiveIdleSubagentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents: %v", err)
	}
	if archived != 1 {
		t.Fatalf("archived %d subagents, want 1", archived)
	}
	for _, agentID := range []ID{middle.Agent.ID, leaf.Agent.ID} {
		record, err := store.Execution().GetAgentInProject(ctx, testProjectID, agentID)
		if err != nil {
			t.Fatalf("load agent: %v", err)
		}
		if record.State != executionstore.AgentStateArchived {
			t.Fatalf("agent %s state = %s, want archived", agentID, record.State)
		}
	}
	topRecord, err := store.Execution().GetAgentInProject(ctx, testProjectID, top.ID)
	if err != nil {
		t.Fatalf("load top-level agent: %v", err)
	}
	if topRecord.State != executionstore.AgentStateActive {
		t.Fatalf("top-level agent state = %s, want active", topRecord.State)
	}
}

func containsAgentID(agents []executionstore.AgentRecord, id ID) bool {
	for _, agent := range agents {
		if agent.ID == id {
			return true
		}
	}
	return false
}

func agentIDsForTest(agents []executionstore.AgentRecord) []string {
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent.ID.String())
	}
	return out
}

func TestSubagentQuestionSurfacesOnParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-question@example.com", "Subagent Question")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-question", "Subagent Question", subagentParentYAML+"tools:\n  ask_question: {}\n",
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-question-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "asker", "subagent-question-child", nil, withoutLaunchMessage,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	runtimeLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, child.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire child runtime lock: %v", err)
	}
	toolCallIDs := createReadyToolCallsForTest(
		t, ctx, store, child.Agent.ID, user.ID, child.AgentConfig.ID, runtimeLock, "subagent-question",
		[]toolCallSpecForTest{{
			Label: "question",
			Name:  "ask_question",
			Input: json.RawMessage(`{"questions":[{"prompt":"Continue?","options":[{"label":"Yes"}]}]}`),
		}},
	)
	form, err := interactionform.New(
		"Need a decision",
		nil,
		[]interactionform.Question{{Prompt: "Continue?", Options: []interactionform.Option{{Label: "Yes"}}}},
	)
	if err != nil {
		t.Fatalf("create question form: %v", err)
	}
	if _, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       child.Agent.ID,
			ToolCallID:    toolCallIDs["question"],
			RuntimeLockID: runtimeLock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateQuestionForToolCall(
				executionstore.CreateQuestionInteractionInput{Form: form},
			), nil
		},
	); err != nil {
		t.Fatalf("create child question: %v", err)
	}

	descendants, err := store.Execution().ListAgentDescendantIDs(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list descendants: %v", err)
	}
	tree, err := store.Execution().ListAgentInteractions(ctx, executionstore.ListAgentInteractionsInput{
		ProjectID: testProjectID,
		AgentIDs:  append([]ID{parent.ID}, descendants...),
		State:     executionstore.AgentInteractionStateOpen,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list tree interactions: %v", err)
	}
	if len(tree.Interactions) != 1 || tree.Interactions[0].AgentID != child.Agent.ID ||
		tree.Interactions[0].AgentName != "asker" || tree.Interactions[0].SubagentKey != "fork" {
		t.Fatalf("tree interactions = %+v", tree.Interactions)
	}
	var kind string
	if err := pool.QueryRow(
		ctx,
		`SELECT metadata->'subagent_message'->>'kind' FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'subagent_message'`,
		testProjectID,
		parent.ID,
	).Scan(&kind); err != nil {
		t.Fatalf("load parent question notification: %v", err)
	}
	if kind != "question" {
		t.Fatalf("parent notification kind = %q", kind)
	}
}
