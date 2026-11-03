//go:build integration

package executionstore_test

import (
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"strings"
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
	input, _, _, err := f.store.Execution().CreateAgentContentInput(f.ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: actor,
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"scheduled"}]`),
	})
	require.NoError(t, err)
	unit, err := f.store.Execution().IntegrationBeginUnit(f.ctx)
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(f.ctx) }()
	_, err = agentexecution.LockTestAgent(f.ctx, unit, testProjectID, f.process.AgentID, nil)
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
	after, err := q.GetInteractionSelection(f.ctx, dbsqlc.GetInteractionSelectionParams{
		ProjectID: testProjectID, AgentID: f.process.AgentID,
	})
	require.NoError(t, err)
	require.Equal(t, selection, after)
}

func TestInteractionSelectionConditionalQueries(t *testing.T) {
	f := newIntegrationInteractionFixture(t)
	actor, err := executionstore.CronTriggerActor(testOrgID, uuid.New(), "Routine instructions")
	require.NoError(t, err)
	input, _, _, err := f.store.Execution().CreateAgentContentInput(f.ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: actor,
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"routine"}]`),
	})
	require.NoError(t, err)
	f.change(t, nil)
	var queries []string
	config := f.store.pool.Config()
	config.ConnConfig.Tracer = &integrationdb.QueryTrace{OnQuery: func(sql string) {
		fields := strings.Fields(sql)
		if len(fields) > 2 && fields[0] == "--" && fields[1] == "name:" {
			queries = append(queries, fields[2])
		} else {
			queries = append(queries, sql)
		}
	}}
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	require.NoError(t, err)
	defer pool.Close()
	store := newIntegrationStore(pool)
	unit, err := store.Execution().IntegrationBeginUnit(f.ctx)
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(f.ctx) }()
	h, err := agentexecution.LockTestAgent(f.ctx, unit, testProjectID, f.process.AgentID, nil)
	require.NoError(t, err)
	apply := func() {
		require.NoError(t, executionstore.IntegrationApplyAdmissionDestination(
			f.ctx, unit, testProjectID, f.process.AgentID, []executionstore.AgentInputRecord{input}))
	}
	queries = nil
	apply()
	require.Equal(t, []string{"GetInteractionSelection"}, queries)
	tx := integrationdb.BeginTx(t, f.ctx, pool)
	defer func() { _ = tx.Rollback(f.ctx) }()
	queries = nil
	destination, err := executionstore.IntegrationCaptureInteractionDestinationTx(
		f.ctx, tx, testProjectID, f.process.AgentID,
	)
	require.NoError(t, err)
	require.Nil(t, destination)
	require.Equal(t, []string{"GetInteractionSelection"}, queries)
	input.IntegrationTargetID = f.a.ID
	queries = nil
	apply()
	require.Equal(t, []string{"GetInteractionSelection", "GetAgentConfig"}, queries)
	_, err = h.SetInteractionTarget(f.ctx, f.a.ID, "chat", false)
	require.NoError(t, err)
	queries = nil
	apply()
	require.Equal(t, []string{"GetInteractionSelection"}, queries)
	queries = nil
	selection, err := store.Execution().ReconcileInteractionSelectionInUnit(f.ctx, unit, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{AutoSelect: false}, selection)
	require.Equal(t, []string{"GetInteractionSelection", "GetAgentConfig", "WriteExecutionInteractionTarget"}, queries)
}
