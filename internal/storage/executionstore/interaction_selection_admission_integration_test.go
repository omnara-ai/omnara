//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestInteractionSelectionFollowsLastAdmittedContent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		origins []string
		want    string
	}{
		{"last thread", []string{"chat", "other"}, "other"},
		{"dashboard clears before background", []string{"other", "", "agent", "cron"}, ""},
		{"thread after dashboard", []string{"", "other"}, "other"},
		{"github clears", []string{"other", "github"}, ""},
		{"background preserves", []string{"agent", "cron"}, "chat"},
		{"last eligible thread", []string{"other", "agent", "cron"}, "other"},
		{"external agent ID clears", []string{"other", "external-agent", "cron"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationInteractionFixture(t)
			before := f.selectOrigin(t, f.a.ID)
			var ids []uuid.UUID
			for _, origin := range tc.origins {
				var actor *executionstore.ActorParams
				var err error
				switch origin {
				case "agent", "external-agent":
					actor, err = executionstore.SubagentActorParams(testOrgID, executionstore.AgentRecord{ID: f.process.AgentID})
					require.NoError(t, err)
					if origin == "external-agent" {
						actor.Provider = executionstore.ActorProviderExternal
					}
				case "cron":
					actor, err = executionstore.CronTriggerActor(testOrgID, uuid.New(), "Routine instructions")
					require.NoError(t, err)
				}
				if origin == "" || actor != nil {
					input, _, _, err := f.store.Execution().CreateAgentContentInput(f.ctx, executionstore.CreateAgentContentInputInput{
						ProjectID: testProjectID, AgentID: f.process.AgentID,
						Actor:         actor,
						ContentBlocks: json.RawMessage(`[{"type":"text","text":"dashboard"}]`),
						DeliveryMode:  executionstore.DeliveryModeSteering,
					})
					require.NoError(t, err)
					ids = append(ids, input.ID)
					continue
				}
				integration, target := f.integration, f.a
				if origin == "other" {
					integration, target = f.otherIntegration, f.b
				} else if origin == "github" {
					integration = inboxInputIntegration(t, f.activation(), "github")
				}
				recipient := inboxInputPlan(t, f.process.AgentID, integration, uuid.NewString())
				recipient.Input.CancelOpenInteractions = false
				if origin != "github" {
					recipient.Input.Origin.Address = integrationstore.ConversationAddress{Kind: target.ScopeKind, Ref: target.ScopeRef}
				}
				receipt := freezeInboxInput(t, f.activation(), recipient, uuid.NewString(), time.Minute)
				result, err := f.store.Execution().AdmitInboxInputRecipient(f.ctx, receipt.Lease(), "recipient", nil)
				require.NoError(t, err)
				ids = append(ids, result.AgentInput.ID)
			}
			selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			require.Equal(t, before, selection, "waiting inputs must not retarget the current turn")
			admitted, found := admitNextAgentInputAndOpenTurnForTest(
				t, f.ctx, f.store, testProjectID, f.process.AgentID, f.process.Lock.ID,
			)
			require.True(t, found)
			require.Len(t, admitted.Inputs, len(ids))
			for i := range ids {
				require.Equal(t, ids[i], admitted.Inputs[i].ID)
			}
			selection, err = f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			require.True(t, selection.AutoSelect)
			require.Equal(t, tc.want, selection.HandlerKey)
			if tc.want == "" {
				require.Equal(t, uuid.Nil, selection.IntegrationTargetID)
			}
		})
	}
}

func TestInteractionSelectionToolControlsAutomaticMode(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	set := func(handler string, auto *bool) executionstore.InteractionSelection {
		call := createToolCallForProcessTest(t, f.ctx, f.process, uuid.NewString(), "set_interaction_handler")
		_, err := f.store.Execution().ExecuteToolCall(f.ctx, executionstore.ExecuteToolCallInput{
			ProjectID: testProjectID, AgentID: f.process.AgentID, ToolCallID: call, RuntimeLockID: f.process.Lock.ID,
		}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.SetInteractionHandlerForToolCall(executionstore.SelectInteractionHandlerInput{
				HandlerKey: handler, Args: json.RawMessage(`{}`), AutoSelect: auto,
			}, func(executionstore.InteractionSelection) (executionstore.ToolCallCompletionInput, error) {
				return executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"selected"}]`),
				}, nil
			}), nil
		})
		require.NoError(t, err)
		selected, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
		require.NoError(t, err)
		return selected
	}
	pinned := set("chat", new(false))
	require.False(t, pinned.AutoSelect)
	require.Equal(t, pinned, f.selectOrigin(t, f.b.ID))
	require.Equal(t, pinned, f.selectOrigin(t, uuid.Nil))
	require.Equal(t, pinned, set("chat", nil), "omission preserves the automatic mode")
	require.True(t, set("chat", new(true)).AutoSelect)
	require.Equal(t, "other", f.selectOrigin(t, f.b.ID).HandlerKey)

	dashboard := set("", new(false))
	require.Equal(t, executionstore.InteractionSelection{AutoSelect: false}, dashboard)
	recipient := inboxInputPlan(t, f.process.AgentID, f.otherIntegration, "pinned-dashboard-content")
	recipient.Input.CancelOpenInteractions = false
	recipient.Input.Origin.Address = integrationstore.ConversationAddress{Kind: f.b.ScopeKind, Ref: f.b.ScopeRef}
	receipt := freezeInboxInput(t, f.activation(), recipient, "pinned-dashboard-content", time.Minute)
	received, err := f.store.Execution().AdmitInboxInputRecipient(f.ctx, receipt.Lease(), "recipient", nil)
	require.NoError(t, err)
	require.True(t, received.Created)
	require.Equal(t, f.b.ID, received.AgentInput.IntegrationTargetID)
	selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, dashboard, selection, "receipt must preserve the dashboard-only pin")
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t, f.ctx, f.store, testProjectID, f.process.AgentID, f.process.Lock.ID,
	)
	require.True(t, found)
	require.Len(t, admitted.Inputs, 1)
	require.Equal(t, received.AgentInput.ID, admitted.Inputs[0].ID)
	require.Equal(t, f.b.ID, admitted.Inputs[0].IntegrationTargetID)
	selection, err = f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, dashboard, selection, "admitting an eligible origin must preserve the dashboard-only pin")
}
