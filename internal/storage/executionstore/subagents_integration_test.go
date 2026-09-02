//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

func spawnSubagentForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	parent executionstore.AgentRecord,
	configID ID,
	name, idempotencyKey string,
	maxConcurrent *int,
) (executionstore.LaunchAgentResult, error) {
	t.Helper()
	actor, err := executionstore.SubagentActorParams(parent.OrgID, parent)
	if err != nil {
		t.Fatalf("subagent actor params: %v", err)
	}
	return store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  configID,
		LaunchedBy:     systemPrincipalForTest(parent.ID),
		Name:           &name,
		Message:        "Investigate the failing build.",
		MessageActor:   actor,
		IdempotencyKey: idempotencyKey,
		Subagent: &executionstore.SubagentLaunch{
			ParentAgentID: parent.ID,
			Handle:        "fork",
			MaxConcurrent: maxConcurrent,
		},
	})
}

func TestLaunchSubagentLinksParentAndEnforcesLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-launch@example.com", "Subagent Launch")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "subagent-launch", "Subagent Launch", subagentParentYAML, now)
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

	child, err := spawnSubagentForTest(t, ctx, store, parent, profile.CurrentConfigID, "worker-1", "subagent-launch-child-1", intPtr(1))
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if child.Agent.ParentAgentID != parent.ID || child.Agent.SubagentHandle != "fork" {
		t.Fatalf("child linkage = parent %s handle %q", child.Agent.ParentAgentID, child.Agent.SubagentHandle)
	}
	if child.AgentInput.ID == NilID {
		t.Fatal("child launch did not queue the task input")
	}

	if _, err := spawnSubagentForTest(t, ctx, store, parent, profile.CurrentConfigID, "worker-2", "subagent-launch-child-2", intPtr(1)); !errors.Is(err, storeerr.ErrConflict) {
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
	now := time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-notify@example.com", "Subagent Notify")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-notify", "Subagent Notify", subagentParentYAML, now,
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
	now := time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-wait@example.com", "Subagent Wait")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-wait", "Subagent Wait", subagentParentYAML, now,
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
