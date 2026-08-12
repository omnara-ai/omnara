//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestArchiveAgentCancelsCurrentTurnOpenWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"archive-open-work@example.com",
		"Archive Open Work")

	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"archive-open-work",
		"Archive Open Work",
		`
instruction: Ask before continuing.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  ask_question: {}
`,
		now,
	)
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "archive-open-work",
	})
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	runtimeLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launch.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime lock: %v", err)
	}
	toolCallIDs := createReadyToolCallsForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		launch.AgentConfig.ID,
		runtimeLock,
		"archive-open-work",
		[]toolCallSpecForTest{{
			Label: "question",
			Name:  "ask_question",
			Input: json.RawMessage(`{"questions":[{"id":"q1","prompt":"Continue?"}]}`),
		}},
	)
	questionToolCallID := toolCallIDs["question"]
	form, err := interactionform.New(
		"Archive question",
		nil,
		[]interactionform.Question{{
			Prompt:  "Continue?",
			Options: []interactionform.Option{{Label: "Yes"}},
		}},
	)
	if err != nil {
		t.Fatalf("create question form: %v", err)
	}
	execution, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       launch.Agent.ID,
			ToolCallID:    questionToolCallID,
			RuntimeLockID: runtimeLock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateQuestionForToolCall(
				executionstore.CreateQuestionInteractionInput{Form: form},
			), nil
		},
	)
	if err != nil {
		t.Fatalf("create open interaction: %v", err)
	}
	interaction, ok := execution.CommandResult.(executionstore.AgentInteractionRecord)
	if !ok {
		t.Fatalf("question command returned %T", execution.CommandResult)
	}
	if err := store.Execution().ReleaseToolCallRuntimeOwnership(
		ctx,
		executionstore.ReleaseToolCallRuntimeOwnershipInput{
			ProjectID:     testProjectID,
			AgentID:       launch.Agent.ID,
			ToolCallID:    questionToolCallID,
			RuntimeLockID: runtimeLock.ID,
		},
	); err != nil {
		t.Fatalf("release question tool call: %v", err)
	}

	lockTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin archive agent blocker: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	var blockingPID int32
	if err := lockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get archive blocker backend: %v", err)
	}
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		launch.Agent.ID,
	); err != nil {
		t.Fatalf("lock archived agent: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, archiveErr := store.Execution().ArchiveAgent(
			context.Background(),
			testProjectID,
			launch.Agent.ID,
			userPrincipal(user.ID),
		)
		done <- archiveErr
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, store.pool, "FROM agents", blockingPID)
	var releaseFloor time.Time
	if err := lockTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
		t.Fatalf("read archive lock release floor: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release archived agent lock: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	var archivedAt time.Time
	if err := store.pool.QueryRow(
		ctx,
		`SELECT archived_at FROM agents WHERE project_id = $1 AND id = $2`,
		testProjectID,
		launch.Agent.ID,
	).Scan(&archivedAt); err != nil {
		t.Fatalf("read archived agent timestamp: %v", err)
	}
	if archivedAt.Before(releaseFloor) {
		t.Fatalf("archived_at = %s, want at or after lock release floor %s", archivedAt, releaseFloor)
	}
	archivedInteraction, found, err := store.Execution().GetAgentInteraction(
		ctx,
		testProjectID,
		launch.Agent.ID,
		interaction.ID,
	)
	if err != nil || !found {
		t.Fatalf("get archived interaction: found=%v err=%v", found, err)
	}
	if archivedInteraction.State != executionstore.AgentInteractionStateCanceled || archivedInteraction.ResolvedAt.IsZero() ||
		archivedInteraction.ResolvedAt.Before(archivedAt) {
		t.Fatalf("archived interaction = %+v, want canceled", archivedInteraction)
	}
	toolCall, err := store.Execution().GetToolCall(
		ctx,
		testProjectID,
		launch.Agent.ID,
		questionToolCallID,
	)
	if err != nil {
		t.Fatalf("get archived tool execution: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted ||
		toolCall.Outcome != executionstore.ToolResultOutcomeCanceled ||
		toolCall.CompletedAt == nil ||
		toolCall.CompletedAt.Before(archivedAt) {
		t.Fatalf("archived tool call = %+v, want canceled completion", toolCall)
	}
	result, found, err := store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		launch.Agent.ID,
		questionToolCallID,
	)
	if err != nil {
		t.Fatalf("get archived tool result: %v", err)
	}
	if !found || result.Outcome != executionstore.ToolResultOutcomeCanceled {
		t.Fatalf("archived tool result = %+v found=%v", result, found)
	}
	expectedContent := json.RawMessage(
		`[{"type":"structured_data","value":{"reason":"Agent canceled before this tool call completed."}}]`,
	)
	if !sameJSON(toolCall.ResultContentParts, expectedContent) {
		t.Fatalf("archived tool result content = %s, want %s", toolCall.ResultContentParts, expectedContent)
	}
	if events := listTypedToolResultEventsForToolCall(
		t,
		ctx,
		store,
		launch.Agent.ID,
		questionToolCallID,
	); len(events) != 1 {
		t.Fatalf("archived tool result events = %+v, want one", events)
	}
}
