//go:build integration

package executionstore_test

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestSubagentCompletedReportPreservesInteractionSelection(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	before := f.selectOrigin(t, f.a.ID)
	require.True(t, before.AutoSelect)
	require.Equal(t, f.a.ID, before.IntegrationTargetID)

	parent, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	child, err := spawnSubagentForTest(
		t, f.ctx, f.store, parent, f.profile.CurrentConfigID, "reporter", "interaction-selection-report", nil,
	)
	require.NoError(t, err)
	childLock, err := f.store.Execution().AcquireAgentRuntimeLock(
		f.ctx, testProjectID, child.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	require.NoError(t, err)
	opening, found := admitNextAgentInputAndOpenTurnForTest(
		t, f.ctx, f.store, testProjectID, child.Agent.ID, childLock.ID,
	)
	require.True(t, found)
	require.Len(t, opening.Inputs, 1)
	prepared1, err := f.store.Execution().
		PrepareNormalModelCall(f.ctx, executionstore.PrepareNormalModelCallInput{
			ProjectID:       testProjectID,
			AgentID:         child.Agent.ID,
			RuntimeLockID:   childLock.ID,
			OpeningInputIDs: []uuid.UUID{opening.Inputs[0].ID},
		})
	claim := prepared1.Claim

	require.NoError(t, err)
	providerModelSlug := modelProviderSlugForContext(
		t, f.ctx, f.store, testProjectID, child.Agent.ID, claim.Context.ID,
	)
	const report = "The build failed because the dependency checksum changed."
	_, err = f.store.Execution().RecordModelOutputAndCompleteContext(
		f.ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            child.Agent.ID,
			RuntimeLockID:      childLock.ID,
			ModelCallContextID: claim.Context.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: providerModelSlug,
				ServedProviderModelSlug:    providerModelSlug,
				APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
				APIVariant:                 modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "resp_interaction_selection_report",
					Content:    []modelenvelope.ResponsePart{{Type: "text", Text: report}},
					StopReason: modelenvelope.StopReasonEndTurn,
				},
			},
		},
	)
	require.NoError(t, err)

	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t, f.ctx, f.store, testProjectID, parent.ID, f.process.Lock.ID,
	)
	require.True(t, found)
	require.Len(t, admitted.Inputs, 1)
	require.Equal(t, uuid.Nil, admitted.Inputs[0].IntegrationTargetID)
	turnEvents, err := f.store.Execution().ListTurnEventsForRead(
		f.ctx, testProjectID, parent.ID, admitted.Turn.ID, 0, 10,
	)
	require.NoError(t, err)
	require.Len(t, turnEvents, 1)
	require.Equal(t, admitted.Inputs[0].ID, turnEvents[0].AgentInputID)
	require.Contains(t, string(turnEvents[0].ContentBlocks), report)
	selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, parent.ID)
	require.NoError(t, err)
	require.Equal(
		t,
		before,
		selection,
		"admitting the completed subagent report must preserve the Slack selection",
	)
}

func TestCronTriggerAgentInputPreservesInteractionSelection(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	before := f.selectOrigin(t, f.a.ID)
	require.True(t, before.AutoSelect)
	require.Equal(t, f.a.ID, before.IntegrationTargetID)

	input := cronTriggerInput("Scheduled review", f.process.AgentID, true)
	trigger, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(
		f.ctx,
		`UPDATE cron_triggers SET next_fire_after = statement_timestamp() - interval '1 minute' WHERE id = $1`,
		trigger.ID,
	)
	require.NoError(t, err)
	stats, err := crontrigger.NewService(f.store.Execution(), nil, slog.Default()).FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{Claimed: 1, Inputs: 1}, stats)

	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t, f.ctx, f.store, testProjectID, f.process.AgentID, f.process.Lock.ID,
	)
	require.True(t, found)
	require.Len(t, admitted.Inputs, 1)
	require.Equal(t, uuid.Nil, admitted.Inputs[0].IntegrationTargetID)
	turnEvents, err := f.store.Execution().ListTurnEventsForRead(
		f.ctx, testProjectID, f.process.AgentID, admitted.Turn.ID, 0, 10,
	)
	require.NoError(t, err)
	require.Len(t, turnEvents, 1)
	require.Equal(t, admitted.Inputs[0].ID, turnEvents[0].AgentInputID)
	require.Contains(t, string(turnEvents[0].ContentBlocks), input.MessageTemplate)
	selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, before, selection, "admitting the scheduler's input must preserve the Slack selection")
}
