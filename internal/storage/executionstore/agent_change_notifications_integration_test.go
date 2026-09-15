//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func requireAgentChangeCount(
	t *testing.T, publisher *recordingPostCommitPublisher,
	agentID uuid.UUID, kind notifications.AgentChangeKind, want int,
) {
	t.Helper()
	count := 0
	for _, intent := range publisher.intents {
		change, ok := intent.(notifications.AgentChangeCommitted)
		if ok && change.AgentID == agentID && slices.Contains(change.Changes, kind) {
			require.Equal(t, testProjectID, change.ProjectID)
			count++
		}
	}
	require.Equal(t, want, count, "committed %s notifications", kind)
}

func TestInteractionChangesPublishOnlyForCommittedMutations(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"question", "permission"} {
		for _, action := range []string{"resolve", "steer", "cancel", "archive", "complete_tool"} {
			if kind == "question" && action == "complete_tool" {
				continue // A question owns completion until it is answered or canceled.
			}
			t.Run(kind+"/"+action, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				fixture := newProcessDaemonFixture(t, ctx, "interaction_change_"+kind+"_"+action)
				publisher := &recordingPostCommitPublisher{}
				fixture.Store = newIntegrationStore(fixture.Store.pool, storage.WithPostCommitPublisher(publisher))
				var interaction executionstore.AgentInteractionRecord
				var toolCallID uuid.UUID
				if kind == "question" {
					toolCallID = createToolCallForProcessTest(t, ctx, fixture, "interaction_change", "ask_question")
					publisher.intents = nil
					interaction = createQuestionInteractionForTest(t, ctx, fixture, toolCallID)
				} else {
					toolCallID = createToolCallForProcessTestWithPermission(
						t, ctx, fixture, "interaction_change", "run_command", false,
					)
					publisher.intents = nil
					interaction = createPermissionInteractionForTest(
						t, ctx, fixture, toolCallID, permissionRequestForStorageTest(t, "run_command"),
					)
				}
				requireAgentChangeCount(t, publisher, fixture.AgentID, notifications.AgentChangeInteractions, 1)
				for _, intent := range publisher.intents {
					if update, ok := intent.(notifications.ToolCallUpdatedCommitted); ok {
						require.Equal(t, testProjectID, update.ProjectID)
						require.Equal(t, toolcatalog.ToolTypeBuiltIn, update.ToolType)
					}
				}

				publisher.intents = nil
				wantState := executionstore.AgentInteractionStateCanceled
				switch action {
				case "resolve":
					option := 0
					if kind == "permission" {
						option = toolpermission.AllowOptionIndex
					}
					input := executionstore.ResolveAgentInteractionInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, ID: interaction.ID,
						Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{option}}}},
						Actor:      mustOmnaraActorParams(t, fixture.UserID),
					}
					_, err := fixture.Store.Execution().ResolveAgentInteraction(ctx, input)
					require.NoError(t, err)
					requireAgentChangeCount(t, publisher, fixture.AgentID, notifications.AgentChangeInteractions, 1)
					publisher.intents = nil
					_, err = fixture.Store.Execution().ResolveAgentInteraction(ctx, input)
					require.NoError(t, err)
					requireAgentChangeCount(t, publisher, fixture.AgentID, notifications.AgentChangeInteractions, 0)
					input.Resolution.Answers[0].OptionIndices = []int{99}
					_, err = fixture.Store.Execution().ResolveAgentInteraction(ctx, input)
					require.Error(t, err)
					require.Empty(t, publisher.intents, "rejected resolution must not publish")
					wantState = executionstore.AgentInteractionStateResolved
				case "steer":
					_, _, created, err := fixture.Store.Execution().CreateAgentContentInput(ctx,
						executionstore.CreateAgentContentInputInput{
							ProjectID: testProjectID, AgentID: fixture.AgentID,
							Actor:         mustOmnaraActorParams(t, fixture.UserID),
							ContentBlocks: json.RawMessage(`[{"type":"text","text":"Continue with the other approach."}]`),
							DeliveryMode:  executionstore.DeliveryModeSteering, CancelOpenInteractions: true,
						})
					require.NoError(t, err)
					require.True(t, created)
				case "cancel":
					_, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
					})
					require.NoError(t, err)
				case "archive":
					_, _, err := fixture.Store.Execution().ArchiveAgent(
						ctx, testProjectID, fixture.AgentID, userPrincipal(fixture.UserID),
					)
					require.NoError(t, err)
					requireAgentChangeCount(t, publisher, fixture.AgentID, notifications.AgentChangeAgent, 1)
				case "complete_tool":
					_, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, ID: toolCallID, RuntimeLockID: fixture.Lock.ID,
						Outcome:            executionstore.ToolResultOutcomeFailed,
						ResultContentParts: json.RawMessage(`[{"type":"text","text":"The command is no longer available."}]`),
					})
					require.NoError(t, err)
				}
				if action != "resolve" {
					requireAgentChangeCount(t, publisher, fixture.AgentID, notifications.AgentChangeInteractions, 1)
				}
				stored, found, err := fixture.Store.Execution().GetAgentInteraction(
					ctx, testProjectID, fixture.AgentID, interaction.ID,
				)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, wantState, stored.State)
			})
		}
	}
}

func TestOrdinaryToolCompletionDoesNotPublishInteractionChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "ordinary_tool_notification")
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, storage.WithPostCommitPublisher(publisher))
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "ordinary_tool_notification", "read_process")
	publisher.intents = nil
	_, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ID: toolCallID, RuntimeLockID: fixture.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"No processes are running."}]`),
	})
	require.NoError(t, err)
	requireAgentChangeCount(t, publisher, fixture.AgentID, notifications.AgentChangeInteractions, 0)
	require.Equal(t, []string{"completed"}, publisher.toolCallStates(toolCallID))
}

func TestCancelFinalQueuedChildInputPublishesIdleActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "queued_child_activity")
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, storage.WithPostCommitPublisher(publisher))
	child, err := fixture.Store.Execution().LaunchAgent(ctx, subagentLockTestInput(t, ctx, fixture))
	require.NoError(t, err)
	require.True(t, child.Created)
	require.NotEqual(t, uuid.Nil, child.AgentInput.ID)
	requireActivity := func(state string) {
		t.Helper()
		children, err := fixture.Store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
			ProjectID: testProjectID, Limit: 10,
			Filters: executionstore.AgentListFilters{ParentAgentID: &fixture.AgentID},
		})
		require.NoError(t, err)
		require.Len(t, children.Agents, 1)
		require.Equal(t, child.Agent.ID, children.Agents[0].ID)
		require.NotNil(t, children.Agents[0].Activity)
		require.Equal(t, state, children.Agents[0].Activity.State)
	}
	requireActivity("running")
	publisher.intents = nil
	input := executionstore.CancelQueuedBacklogInputInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, InputID: child.AgentInput.ID,
	}
	require.ErrorIs(t, fixture.Store.Execution().CancelQueuedBacklogInput(ctx, input), storeerr.ErrStateTransitionConflict)
	require.Empty(t, publisher.intents, "rejected cancellation must not publish")
	requireActivity("running")
	input.AgentID = child.Agent.ID
	require.NoError(t, fixture.Store.Execution().CancelQueuedBacklogInput(ctx, input))
	requireActivity("idle")
	require.Zero(t, countAgentWakeups(t, ctx, fixture.Store, child.Agent.ID))
	requireAgentChangeCount(t, publisher, child.Agent.ID, notifications.AgentChangeAgent, 1)
	requireAgentChangeCount(t, publisher, child.Agent.ID, notifications.AgentChangeInteractions, 0)
	publisher.intents = nil
	require.ErrorIs(t, fixture.Store.Execution().CancelQueuedBacklogInput(ctx, input), storeerr.ErrStateTransitionConflict)
	require.Empty(t, publisher.intents, "repeated cancellation must not publish")
}
