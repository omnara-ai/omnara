//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestChannelWorkflowSemanticReplayRecordsEachReceiptAndPreservesOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "semantic-replay")
	first := f.event(t, ctx, "provider-event-one")
	first.InputKey = "slack:message:T:C:1.000001"
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
	require.NoError(t, err)

	duplicate := f.event(t, ctx, "provider-event-two")
	duplicate.InputKey = first.InputKey
	duplicate.Target.ProviderRef = "different-destination"
	duplicate.Content.Blocks = json.RawMessage(`[{"type":"text","text":"Changed rendering"}]`)
	duplicate.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: first.InputKey, Exists: false}
	replayed, err := f.Store.Execution().DeliverChannelWorkflow(ctx, duplicate)
	require.NoError(t, err, "semantic replay precedes stale rendering conditions")
	require.False(t, replayed.CreatedInput)
	require.Equal(t, accepted.AgentInput.ID, replayed.AgentInput.ID)
	require.Equal(t, accepted.ChannelID, replayed.ChannelID)
	require.Equal(t, accepted.BindingID, replayed.BindingID)
	require.JSONEq(t, string(accepted.ContentBlocks), string(replayed.ContentBlocks))

	// A later worker can choose a different projection. The receipt still names
	// its original accepted input, without another admission or channel binding.
	duplicate.InputKey = "a-different-projection"
	duplicate.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: "missing", Exists: true}
	replayed, err = f.Store.Execution().DeliverChannelWorkflow(ctx, duplicate)
	require.NoError(t, err)
	require.Equal(t, accepted.AgentInput.ID, replayed.AgentInput.ID)
	var outcomes, destinations int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_event_outcomes WHERE project_id=$1 AND agent_id=$2`,
		f.Identity.ProjectID, accepted.AgentInput.AgentID).Scan(&outcomes))
	require.Equal(t, 2, outcomes)
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_targets WHERE project_id=$1 AND integration_install_id=$2`,
		f.Identity.ProjectID, f.Identity.IntegrationInstallID).Scan(&destinations))
	require.Equal(t, 1, destinations)
}

func TestChannelWorkflowInputPresenceGuardsBothArrivalOrders(t *testing.T) {
	t.Parallel()
	for _, filesFirst := range []bool{false, true} {
		name := "plain-first"
		if filesFirst {
			name = "files-first"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newChannelWorkflowFixture(t, ctx, name)
			const plainKey, filesKey = "message:1", "message-files:1"
			lookup, err := f.Store.Execution().LookupChannelWorkflow(ctx, f.Identity, []string{plainKey, filesKey})
			require.NoError(t, err)
			require.False(t, lookup.Exists)
			require.Empty(t, lookup.InputKeys)
			plain := f.event(t, ctx, "plain-callback")
			plain.InputKey = plainKey
			plain.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: filesKey, Exists: false}
			files := f.event(t, ctx, "files-callback")
			files.InputKey = filesKey
			files.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: plainKey, Exists: false}
			first, second := plain, files
			if filesFirst {
				first, second = files, plain
			}
			accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
			require.NoError(t, err)
			second.Prepared, err = f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
			require.NoError(t, err)
			_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, second)
			require.ErrorIs(t, err, executionstore.ErrChannelInputPreconditionChanged)
			lookup, err = f.Store.Execution().LookupChannelWorkflow(ctx, f.Identity, []string{plainKey, filesKey})
			require.NoError(t, err)
			require.True(t, lookup.Exists)
			require.Equal(t, executionstore.AgentStateActive, lookup.AgentState)
			require.Equal(t, []string{first.InputKey}, lookup.InputKeys)
			if filesFirst {
				second.InputKey = filesKey // Suppress plain by replaying the richer input.
			} else {
				second.InputPrecondition.Exists = true
				second.Content.Blocks = json.RawMessage(`[{"type":"text","text":"Files for the previous Slack message."}]`)
			}
			result, err := f.Store.Execution().DeliverChannelWorkflow(ctx, second)
			require.NoError(t, err)
			require.Equal(t, !filesFirst, result.CreatedInput)
			if filesFirst {
				require.Equal(t, accepted.AgentInput.ID, result.AgentInput.ID)
			} else {
				require.NotEqual(t, accepted.AgentInput.ID, result.AgentInput.ID)
			}
		})
	}
}

func TestChannelWorkflowConcurrentDuplicateCallbacksCreateOneInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "duplicate-files")
	seed := f.event(t, ctx, "initial-message")
	_, err := f.Store.Execution().DeliverChannelWorkflow(ctx, seed)
	require.NoError(t, err)
	inputs := []executionstore.DeliverChannelWorkflowInput{
		f.event(t, ctx, "files-event-one"), f.event(t, ctx, "files-event-two"),
	}
	for i := range inputs {
		inputs[i].InputKey = "message-files:2"
		inputs[i].InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: "message:2", Exists: false}
	}
	results := make([]executionstore.ChannelInputResult, len(inputs))
	errs := make([]error, len(inputs))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range inputs {
		workers.Go(func() {
			<-start
			results[i], errs[i] = f.Store.Execution().DeliverChannelWorkflow(ctx, inputs[i])
		})
	}
	close(start)
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, results[0].AgentInput.ID, results[1].AgentInput.ID)
	require.NotEqual(t, results[0].CreatedInput, results[1].CreatedInput)
	var outcomes int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_event_outcomes WHERE project_id=$1 AND agent_input_id=$2`,
		f.Identity.ProjectID, results[0].AgentInput.ID).Scan(&outcomes))
	require.Equal(t, 2, outcomes)
}

func TestChannelWorkflowLookupAllowsAbsentAppendOnlyConversation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "append-only-lookup")
	route, err := f.Store.Integrations().CreateIntegrationRoute(ctx, integrationstore.CreateIntegrationRouteInput{
		ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		DeploymentKey: "append-only", BehaviorKey: "conversation", State: integrationstore.IntegrationRouteStateActive,
	})
	require.NoError(t, err)
	f.Identity.IntegrationRouteID = route.ID
	result, err := f.Store.Execution().LookupChannelWorkflow(ctx, f.Identity, []string{"message:1"})
	require.NoError(t, err)
	require.False(t, result.Exists)
	require.Empty(t, result.InputKeys)
	_, err = f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "lookup must not confer launch authority")
}

func TestChannelWorkflowRequiredInputCannotLaunchEmptyConversation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "missing-required-input")
	input := f.event(t, ctx, "files-callback")
	input.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: "plain-message", Exists: true}
	_, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.ErrorIs(t, err, executionstore.ErrChannelInputPreconditionChanged)
	result, err := f.Store.Execution().LookupChannelWorkflow(ctx, f.Identity, []string{input.InputKey})
	require.NoError(t, err)
	require.False(t, result.Exists)
	var outcomes int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_event_outcomes WHERE receipt_id=$1`, input.Receipt.ReceiptID).Scan(&outcomes))
	require.Zero(t, outcomes)
}
