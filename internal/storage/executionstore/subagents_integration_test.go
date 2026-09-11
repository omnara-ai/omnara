//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
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
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-launch", "Subagent Launch", subagentParentYAML,
	)
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

	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "worker-1", "subagent-launch-child-1", intPtrForSubagentTest(1),
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if child.Agent.ParentAgentID != parent.ID || child.Agent.SubagentKey != "fork" {
		t.Fatalf("child linkage = parent %s key %q", child.Agent.ParentAgentID, child.Agent.SubagentKey)
	}
	if child.AgentInput.ID == NilID {
		t.Fatal("child launch did not queue the task input")
	}

	_, err = spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "worker-2", "subagent-launch-child-2", intPtrForSubagentTest(1),
	)
	if !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("second spawn beyond max_concurrent: err = %v, want conflict", err)
	}

	subagents, err := store.Execution().ListSubagents(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(subagents) != 1 || subagents[0].AgentID != child.Agent.ID ||
		subagents[0].State != executionstore.SubagentStateRunning {
		t.Fatalf("subagents = %+v", subagents)
	}
	if subagents[0].AgentRef != executionstore.SubagentRef(child.Agent.ID) || subagents[0].AgentRef == "" {
		t.Fatalf("subagent ref = %q", subagents[0].AgentRef)
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
	if _, _, err := store.Execution().ArchiveAgent(
		ctx, testProjectID, child.Agent.ID, userPrincipal(user.ID),
	); err != nil {
		t.Fatalf("archive subagent: %v", err)
	}
	var metadata json.RawMessage
	var deliveryMode string
	if err := pool.QueryRow(
		ctx,
		`SELECT metadata, delivery_mode FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'subagent_message'`,
		testProjectID,
		parent.ID,
	).Scan(&metadata, &deliveryMode); err != nil {
		t.Fatalf("load parent notification input: %v", err)
	}
	if deliveryMode != string(executionstore.DeliveryModeSteering) {
		t.Fatalf("parent notification delivery mode = %q, want steering", deliveryMode)
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

func TestArchiveIdleAgentsWaitsForBusyDescendants(t *testing.T) {
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
			input.ArchiveAfterIdleMinutes = intPtrForSubagentTest(1)
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

	_, archived, err := store.Execution().ArchiveIdleAgentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents while leaf is running: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents while a grandchild held a runtime lock, want 0", archived)
	}

	if err := store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, leaf.Agent.ID, leafLock.ID); err != nil {
		t.Fatalf("release leaf runtime lock: %v", err)
	}
	if err := store.Execution().MarkAgentWakeup(
		ctx, testProjectID, leaf.Agent.ID, []byte(`{"reason":"test"}`),
	); err != nil {
		t.Fatalf("mark leaf wakeup: %v", err)
	}
	_, archived, err = store.Execution().ArchiveIdleAgentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents while leaf has a wakeup: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents while a grandchild had a pending wakeup, want 0", archived)
	}

	if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, leaf.Agent.ID); err != nil {
		t.Fatalf("clear leaf wakeup: %v", err)
	}
	_, archived, err = store.Execution().ArchiveIdleAgents(ctx, 10)
	if err != nil {
		t.Fatalf("archive idle subagents before the idle window: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents before the idle window elapsed, want 0", archived)
	}
	_, archived, err = store.Execution().ArchiveIdleAgentsAsOf(ctx, asOf, 10)
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
	parentLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, parent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire parent runtime lock: %v", err)
	}
	parentToolCallIDs := createReadyToolCallsForTest(
		t, ctx, store, parent.ID, user.ID, parentLaunch.AgentConfig.ID, parentLock, "subagent-question-steer",
		[]toolCallSpecForTest{{
			Label: "send",
			Name:  "send_agent_message",
			Input: json.RawMessage(`{"agent_ref":"x","message":"Skip the decision and continue."}`),
		}},
	)
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

	if _, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       parent.ID,
			ToolCallID:    parentToolCallIDs["send"],
			RuntimeLockID: parentLock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.SendSubagentMessageForToolCall(
				executionstore.SendSubagentMessageInput{
					TargetAgentID: child.Agent.ID,
					Message:       "Skip the decision and continue.",
				},
				executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"delivered"}]`),
				},
			), nil
		},
	); err != nil {
		t.Fatalf("send message to child: %v", err)
	}
	var interactionState, deliveryMode string
	if err := pool.QueryRow(
		ctx,
		`SELECT interaction.state, input.delivery_mode
		 FROM agent_interaction_read_projection interaction
		 JOIN agent_inputs input ON input.id = interaction.resolved_by_input_id
		 WHERE interaction.project_id = $1 AND interaction.agent_id = $2`,
		testProjectID,
		child.Agent.ID,
	).Scan(&interactionState, &deliveryMode); err != nil {
		t.Fatalf("load child question after parent message: %v", err)
	}
	if interactionState != string(executionstore.AgentInteractionStateCanceled) || deliveryMode != "steering" {
		t.Fatalf("child question state = %q resolved by %q input, want canceled by steering", interactionState, deliveryMode)
	}
	subagents, err := store.Execution().ListSubagents(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(subagents) != 1 || subagents[0].State == executionstore.SubagentStateWaitingOnHuman {
		t.Fatalf("subagents after parent message = %+v", subagents)
	}
}
