//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestCurrentChannelInteractionCallbackBetweenSetterAndNextPrompt(t *testing.T) {
	for _, kind := range currentChannelInteractionKinds {
		t.Run(string(kind), func(t *testing.T) {
			ctx := t.Context()
			f := newCurrentChannelInteractionFixture(t, ctx, kind, 2)
			f.selectChannel(t, ctx, f.first.ID)
			prompt := f.prompt(t, ctx, 0)
			require.Equal(t, f.first.ID, prompt.IntegrationTargetID)
			f.selectChannel(t, ctx, f.second.ID)

			// Receiving ordinary messages is unrelated to answering a sent prompt.
			_, _, _, err := f.Store.Execution().CreateAgentContentInput(ctx,
				executionstore.CreateAgentContentInputInput{
					ProjectID: testProjectID, AgentID: f.AgentID, ChannelID: f.first.ID,
					Actor:          mustOmnaraActorParams(t, f.UserID),
					ContentBlocks:  json.RawMessage(`[{"type":"text","text":"ordinary input"}]`),
					IdempotencyKey: "send-only-input",
				})
			require.ErrorIs(t, err, storeerr.ErrNotFound)

			response := f.response(prompt)
			before := f.snapshot(t, ctx, prompt)
			require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(
				ctx, testProjectID, f.firstBinding.ID))
			_, err = f.Store.Execution().ResolveAgentInteraction(ctx, response)
			require.ErrorIs(t, err, storeerr.ErrNotFound, "a pinned channel is not a live grant")
			require.Equal(t, before, f.snapshot(t, ctx, prompt))

			receiver := f.bind(t, ctx, f.first.ID, true, false)
			response.IntegrationTargetBindingID = receiver.ID
			_, err = f.Store.Execution().ResolveAgentInteraction(ctx, response)
			require.ErrorIs(t, err, storeerr.ErrNotFound, "receive-only regrant cannot answer a prompt")
			require.Equal(t, before, f.snapshot(t, ctx, prompt))

			sender := f.bind(t, ctx, f.first.ID, false, true)
			require.NotEqual(t, f.firstBinding.ID, sender.ID)
			require.NotEqual(t, receiver.ID, sender.ID)
			response.IntegrationTargetBindingID = sender.ID
			resolved, err := f.Store.Execution().ResolveAgentInteraction(ctx, response)
			require.NoError(t, err, "same-channel live replacement binding may answer the old prompt")
			require.False(t, resolved.Replayed)
			require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
			require.Equal(t, prompt.IntegrationTargetID, resolved.IntegrationTargetID)
			f.requireCurrent(t, ctx, f.first.ID)
			var inputTarget, inputBinding uuid.UUID
			require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT integration_target_id, integration_target_binding_id FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
				testProjectID, f.AgentID, resolved.ResolvedByInputID).Scan(&inputTarget, &inputBinding))
			require.Equal(t, f.first.ID, inputTarget)
			require.Equal(t, sender.ID, inputBinding, "response provenance records the actual replacement grant")

			// The callback arrived after the setter and before this prompt. This
			// prompt snapshots the callback's selection, not the earlier setter.
			next := f.prompt(t, ctx, 1)
			require.Equal(t, f.first.ID, next.IntegrationTargetID)
			f.selectChannel(t, ctx, f.second.ID)
			beforeReplay := f.snapshot(t, ctx, prompt)
			replayed, err := f.Store.Execution().ResolveAgentInteraction(ctx, response)
			require.NoError(t, err)
			require.True(t, replayed.Replayed)
			require.Equal(t, resolved.ResolvedByInputID, replayed.ResolvedByInputID)
			require.Equal(t, beforeReplay, f.snapshot(t, ctx, prompt), "replay neither redirects nor duplicates input/events")
			storedNext, found, err := f.Store.Execution().GetAgentInteraction(ctx, testProjectID, f.AgentID, next.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, f.first.ID, storedNext.IntegrationTargetID, "later selection cannot mutate an existing pin")
		})
	}
}

func TestCurrentChannelInteractionWithoutOriginPreservesSelection(t *testing.T) {
	for _, kind := range currentChannelInteractionKinds {
		t.Run(string(kind), func(t *testing.T) {
			ctx := t.Context()
			f := newCurrentChannelInteractionFixture(t, ctx, kind, 2)
			unrouted := f.prompt(t, ctx, 0)
			require.Equal(t, uuid.Nil, unrouted.IntegrationTargetID)
			f.selectChannel(t, ctx, f.first.ID)
			pinned := f.prompt(t, ctx, 1)
			require.Equal(t, f.first.ID, pinned.IntegrationTargetID)
			f.selectChannel(t, ctx, f.second.ID)

			for _, prompt := range []executionstore.AgentInteractionRecord{unrouted, pinned} {
				response := executionstore.ResolveAgentInteractionInput{
					ProjectID: testProjectID, AgentID: f.AgentID, ID: prompt.ID,
					Resolution: currentChannelInteractionResolution(),
				}
				if prompt.ID == pinned.ID {
					response.Actor = mustOmnaraActorParams(t, f.UserID)
				}
				resolved, err := f.Store.Execution().ResolveAgentInteraction(ctx, response)
				require.NoError(t, err)
				require.False(t, resolved.Replayed)
				require.Equal(t, prompt.IntegrationTargetID, resolved.IntegrationTargetID)
				f.requireCurrent(t, ctx, f.second.ID)
				var noOrigin bool
				require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT integration_target_id IS NULL AND integration_target_binding_id IS NULL
FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
					testProjectID, f.AgentID, resolved.ResolvedByInputID).Scan(&noOrigin))
				require.True(t, noOrigin, "the prompt's pin does not invent response origin")
				f.selectChannel(t, ctx, f.first.ID)
				beforeReplay := f.snapshot(t, ctx, prompt)
				replayed, err := f.Store.Execution().ResolveAgentInteraction(ctx, response)
				require.NoError(t, err)
				require.True(t, replayed.Replayed)
				require.Equal(t, beforeReplay, f.snapshot(t, ctx, prompt))
				f.selectChannel(t, ctx, f.second.ID)
			}
		})
	}
}

func TestCurrentChannelInteractionResolutionRollsBackSelectionAndEffects(t *testing.T) {
	for _, kind := range currentChannelInteractionKinds {
		t.Run(string(kind), func(t *testing.T) {
			ctx := t.Context()
			f := newCurrentChannelInteractionFixture(t, ctx, kind, 1)
			f.selectChannel(t, ctx, f.first.ID)
			prompt := f.prompt(t, ctx, 0)
			f.selectChannel(t, ctx, f.second.ID)
			before := f.snapshot(t, ctx, prompt)

			// Fail at the final wakeup, after selection, response admission and
			// permission/question tool effects. Check the staged state inside the
			// trigger so an earlier, unrelated failure cannot satisfy this test.
			_, err := f.Store.pool.Exec(ctx, `
CREATE FUNCTION fail_current_channel_callback_wakeup() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.metadata->>'reason' = 'interaction_resolved' THEN
    IF NOT EXISTS (
      SELECT 1 FROM agent_interactions interaction
      JOIN agents agent ON agent.id = interaction.agent_id
      JOIN agent_inputs input ON input.agent_id = interaction.agent_id
        AND input.id = interaction.resolved_by_input_id
      WHERE interaction.agent_id = NEW.agent_id AND interaction.state = 'resolved'
        AND agent.integration_target_id = interaction.integration_target_id
        AND input.state = 'resolved' AND input.admitted_event_id IS NOT NULL
    ) THEN
      RAISE EXCEPTION 'callback effects were not staged';
    END IF;
    RAISE EXCEPTION 'forced failure after callback selection';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER fail_current_channel_callback_wakeup
BEFORE INSERT OR UPDATE ON agent_wakeups
FOR EACH ROW EXECUTE FUNCTION fail_current_channel_callback_wakeup()`)
			require.NoError(t, err)
			_, err = f.Store.Execution().ResolveAgentInteraction(ctx, f.response(prompt))
			require.ErrorContains(t, err, "forced failure after callback selection")
			require.Equal(t, before, f.snapshot(t, ctx, prompt),
				"current target, prompt, tool state, inputs and events all roll back together")

			_, err = f.Store.pool.Exec(ctx, `DROP TRIGGER fail_current_channel_callback_wakeup ON agent_wakeups`)
			require.NoError(t, err)
			resolved, err := f.Store.Execution().ResolveAgentInteraction(ctx, f.response(prompt))
			require.NoError(t, err, "the rolled-back response leaves no idempotency residue")
			require.False(t, resolved.Replayed)
			require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
			f.requireCurrent(t, ctx, f.first.ID)
		})
	}
}

var currentChannelInteractionKinds = []executionstore.AgentInteractionKind{
	executionstore.AgentInteractionKindPermission,
	executionstore.AgentInteractionKindQuestion,
}

type currentChannelInteractionFixture struct {
	processDaemonFixture
	kind          executionstore.AgentInteractionKind
	calls         []uuid.UUID
	install       integrationstore.IntegrationInstallRecord
	first, second integrationstore.IntegrationTargetRecord
	firstBinding  integrationstore.IntegrationTargetBindingRecord
}

func newCurrentChannelInteractionFixture(
	t *testing.T, ctx context.Context, kind executionstore.AgentInteractionKind, count int,
) currentChannelInteractionFixture {
	t.Helper()
	f := currentChannelInteractionFixture{
		processDaemonFixture: newProcessDaemonFixture(t, ctx, "current-interaction-"+string(kind)), kind: kind,
	}
	admin := createIntegrationProjectAdmin(t, ctx, f.Store, "current-interaction-admin@example.com")
	install, definitionID := createExternalIntegrationTestConnection(t, ctx, f.Store, admin.ID)
	f.install = install
	var err error
	for i, target := range []*integrationstore.IntegrationTargetRecord{&f.first, &f.second} {
		*target, err = f.Store.Integrations().CreateIntegrationTarget(ctx,
			integrationstore.CreateIntegrationTargetInput{
				ProjectID: testProjectID, ChannelDefinitionID: definitionID, IntegrationInstallID: f.install.ID,
				ProviderRef: fmt.Sprintf("current-interaction-%d", i), ProviderRefKind: "thread",
			})
		require.NoError(t, err)
		binding := f.bind(t, ctx, target.ID, false, true)
		if i == 0 {
			f.firstBinding = binding
		}
	}
	items := make([]processToolCallBatchItem, count)
	for i := range items {
		name := "read_file"
		if kind == executionstore.AgentInteractionKindQuestion {
			name = "ask_question"
		}
		items[i] = builtInProcessToolCallBatchItem(fmt.Sprintf("current-interaction-%d", i), name)
		items[i].Allowed = kind == executionstore.AgentInteractionKindQuestion
	}
	f.calls = createToolCallBatchForProcessTest(t, ctx, f.processDaemonFixture, "current-interaction", items)
	return f
}

func (f currentChannelInteractionFixture) bind(
	t *testing.T, ctx context.Context, target uuid.UUID, receive, send bool,
) integrationstore.IntegrationTargetBindingRecord {
	t.Helper()
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: f.install.ID,
			IntegrationTargetID: target, ReceiveAllowed: receive, SendAllowed: send, Source: "current-interaction",
		})
	require.NoError(t, err)
	return binding
}

func (f currentChannelInteractionFixture) selectChannel(t *testing.T, ctx context.Context, id uuid.UUID) {
	t.Helper()
	_, err := executionstore.IntegrationSetAgentIntegrationTarget(ctx, f.Store.q, testProjectID, f.AgentID, id)
	require.NoError(t, err)
	f.requireCurrent(t, ctx, id)
}

func (f currentChannelInteractionFixture) requireCurrent(t *testing.T, ctx context.Context, want uuid.UUID) {
	t.Helper()
	id, err := newIntegrationStore(f.Store.pool).Execution().GetAgentCurrentChannelID(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	require.Equal(t, want, id)
}

func (f currentChannelInteractionFixture) prompt(
	t *testing.T, ctx context.Context, index int,
) executionstore.AgentInteractionRecord {
	t.Helper()
	if f.kind == executionstore.AgentInteractionKindQuestion {
		return createQuestionInteractionForTest(t, ctx, f.processDaemonFixture, f.calls[index])
	}
	return createPermissionInteractionForTest(t, ctx, f.processDaemonFixture, f.calls[index],
		permissionRequestForStorageTest(t, "read_file"))
}

func currentChannelInteractionResolution() interactionform.Resolution {
	return interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{0}}}}
}

func (f currentChannelInteractionFixture) response(
	prompt executionstore.AgentInteractionRecord,
) executionstore.ResolveAgentInteractionInput {
	return executionstore.ResolveAgentInteractionInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ID: prompt.ID,
		Resolution:           currentChannelInteractionResolution(),
		IntegrationInstallID: f.install.ID, IntegrationTargetID: f.first.ID,
		IntegrationTargetBindingID: f.firstBinding.ID,
		Actor: &executionstore.ActorParams{
			Provider: executionstore.ActorProviderExternal, ProviderUserID: "responder",
		},
	}
}

type currentChannelInteractionSnapshot struct {
	Current     uuid.UUID
	Inputs      int
	Events      int
	Interaction executionstore.AgentInteractionRecord
	Tool        executionstore.ToolCallRecord
}

func (f currentChannelInteractionFixture) snapshot(
	t *testing.T, ctx context.Context, prompt executionstore.AgentInteractionRecord,
) currentChannelInteractionSnapshot {
	t.Helper()
	var result currentChannelInteractionSnapshot
	var err error
	result.Current, err = f.Store.Execution().GetAgentCurrentChannelID(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	var found bool
	result.Interaction, found, err = f.Store.Execution().GetAgentInteraction(ctx, testProjectID, f.AgentID, prompt.ID)
	require.NoError(t, err)
	require.True(t, found)
	result.Tool, err = f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, prompt.ToolCallID)
	require.NoError(t, err)
	require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2),
       (SELECT count(*) FROM agent_events WHERE agent_id = $2)`,
		testProjectID, f.AgentID).Scan(&result.Inputs, &result.Events))
	return result
}
