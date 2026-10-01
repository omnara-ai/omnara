//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type interactionQueryTx struct {
	pgx.Tx
	queries []string
}

func (tx *interactionQueryTx) record(sql string) {
	line, _, _ := strings.Cut(sql, "\n")
	tx.queries = append(tx.queries, strings.Fields(line)[2])
}

func (tx *interactionQueryTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.record(sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (tx *interactionQueryTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.record(sql)
	return tx.Tx.Query(ctx, sql, args...) //nolint:rowserrcheck // SQLC consumes the returned rows and checks Err.
}

func (tx *interactionQueryTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.record(sql)
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestInteractionSelectionConditionalQueries(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	actor, err := executionstore.CronTriggerActor(testOrgID, uuid.New(), "Routine instructions")
	require.NoError(t, err)
	input, _, _, err := f.store.Execution().CreateAgentContentInput(f.ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: actor,
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"routine"}]`),
	})
	require.NoError(t, err)
	f.change(t, nil)
	tx := &interactionQueryTx{Tx: integrationdb.BeginTx(t, f.ctx, f.store.pool)}
	q := dbsqlc.New(tx)
	_, err = q.LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	tx.queries = nil
	require.NoError(t, executionstore.IntegrationSelectAdmittedInteractionDestinationTx(
		f.ctx, tx, testProjectID, f.process.AgentID, []executionstore.AgentInputRecord{input},
	))
	require.Equal(t, []string{"GetInteractionSelection"}, tx.queries, "ordinary admission needs only the agent row")
	tx.queries = nil
	destination, err := executionstore.IntegrationCaptureInteractionDestinationTx(
		f.ctx, tx, testProjectID, f.process.AgentID,
	)
	require.NoError(t, err)
	require.Nil(t, destination)
	require.Equal(t, []string{"GetInteractionSelection"}, tx.queries, "dashboard prompt capture skips config resolution")
	tx.queries = nil
	input.IntegrationTargetID = f.a.ID
	require.NoError(t, executionstore.IntegrationSelectAdmittedInteractionDestinationTx(
		f.ctx, tx, testProjectID, f.process.AgentID, []executionstore.AgentInputRecord{input},
	))
	require.Equal(t, []string{"GetInteractionSelection", "GetAgentConfig"}, tx.queries,
		"an origin without configured handlers needs no integration or actor lookup")
	_, err = q.SetInteractionSelection(f.ctx, dbsqlc.SetInteractionSelectionParams{
		ProjectID: testProjectID, AgentID: f.process.AgentID, AutoSelect: false,
		TargetID: &f.a.ID, HandlerKey: new("chat"),
	})
	require.NoError(t, err)
	tx.queries = nil
	require.NoError(t, executionstore.IntegrationSelectAdmittedInteractionDestinationTx(
		f.ctx, tx, testProjectID, f.process.AgentID, []executionstore.AgentInputRecord{input},
	))
	require.Equal(t, []string{"GetInteractionSelection"}, tx.queries, "manual pins bypass origin resolution")
	tx.queries = nil
	selection, err := f.store.Execution().ReconcileInteractionSelectionTx(f.ctx, tx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{AutoSelect: false}, selection,
		"a removed handler clears the destination while preserving manual mode")
	require.Equal(t, []string{"GetInteractionSelection", "GetAgentConfig", "SetInteractionSelection"}, tx.queries)
}

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
	tx := &interactionQueryTx{Tx: integrationdb.BeginTx(t, f.ctx, f.store.pool)}
	q := dbsqlc.New(tx)
	_, err = q.LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	background := input
	input.IntegrationTargetID = f.b.ID
	tx.queries = nil
	require.NoError(t, executionstore.IntegrationSelectAdmittedInteractionDestinationTx(
		f.ctx, tx, testProjectID, f.process.AgentID, []executionstore.AgentInputRecord{input, background, background},
	))
	require.Equal(t, 1, countInteractionQuery(tx.queries, "ListActorIdentitiesByIDs"))
	require.Equal(t, 1, countInteractionQuery(tx.queries, "GetInteractionSelection"))
	selection, err := q.GetInteractionSelection(f.ctx, dbsqlc.GetInteractionSelectionParams{
		ProjectID: testProjectID, AgentID: f.process.AgentID,
	})
	require.NoError(t, err)
	require.Equal(t, "other", selection.HandlerKey)
	require.Equal(t, f.b.ID, *selection.InteractionTargetID)
	tx.queries = nil
	require.NoError(t, executionstore.IntegrationSelectAdmittedInteractionDestinationTx(
		f.ctx, tx, testProjectID, f.process.AgentID, []executionstore.AgentInputRecord{background, background},
	))
	require.Equal(t, []string{"GetInteractionSelection", "ListActorIdentitiesByIDs"}, tx.queries,
		"background content preserves a selected handler without loading config or integrations")
}

func countInteractionQuery(queries []string, name string) int {
	count := 0
	for _, query := range queries {
		if query == name {
			count++
		}
	}
	return count
}
