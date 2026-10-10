//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/stretchr/testify/require"
)

func TestInteractionSelectionOriginPrecedesBackgroundActor(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	actor, err := executionstore.CronTriggerActor(testOrgID, uuid.New(), "Scheduled thread")
	require.NoError(t, err)
	input, _, _, err := f.store.Execution().
		CreateAgentContentInput(f.ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: actor,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"scheduled"}]`),
		})
	require.NoError(t, err)
	unit, err := f.store.Execution().IntegrationBeginUnit(f.ctx)
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(f.ctx) }()
	_, err = unit.LockAgent(
		f.ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: f.process.AgentID},
		agentexecution.ExternalAuthority{},
	)
	require.NoError(t, err)
	q := dbsqlc.New(unit.DB())
	background := input
	input.IntegrationTargetID = f.b.ID
	require.NoError(t, executionstore.IntegrationApplyAdmissionDestination(
		f.ctx,
		unit,
		testProjectID,
		f.process.AgentID,
		[]executionstore.AgentInputRecord{input, background, background},
	))
	selection, err := q.GetInteractionSelection(f.ctx, dbsqlc.GetInteractionSelectionParams{
		ProjectID: testProjectID, AgentID: f.process.AgentID,
	})
	require.NoError(t, err)
	require.Equal(t, "other", selection.HandlerKey)
	require.Equal(t, f.b.ID, *selection.InteractionTargetID)
	require.NoError(t, executionstore.IntegrationApplyAdmissionDestination(
		f.ctx,
		unit,
		testProjectID,
		f.process.AgentID,
		[]executionstore.AgentInputRecord{background, background},
	))
}
