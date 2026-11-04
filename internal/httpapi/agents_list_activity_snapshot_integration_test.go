//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

// agentListPageTracer runs onPageRead once the agent list page query has
// returned its rows, before the request can issue any other statement.
type agentListPageTracer struct {
	onPageRead func()
}

type agentListPageQueryKey struct{}

func (tracer *agentListPageTracer) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	fields := strings.Fields(data.SQL)
	isPage := len(fields) > 2 && fields[0] == "--" && fields[1] == "name:" &&
		strings.HasPrefix(fields[2], "ListAgentsForProjects")
	return context.WithValue(ctx, agentListPageQueryKey{}, isPage)
}

func (tracer *agentListPageTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if isPage, _ := ctx.Value(agentListPageQueryKey{}).(bool); isPage && data.Err == nil && tracer.onPageRead != nil {
		tracer.onPageRead()
	}
}

// An agent that goes idle right after the list page is read must still come
// back with the running activity it was filtered on.
func TestListAgentsActivityMatchesFilterWhenStateChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	tracer := &agentListPageTracer{}
	config := pool.Config()
	config.ConnConfig.Tracer = tracer
	tracedPool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(tracedPool.Close)
	handler := newIntegrationServer(tracedPool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "list-agents-activity-snapshot")

	running := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "list-activity-snapshot",
	)
	runningID := testPublicID(t, publicid.KindAgent, running.Agent.ID)
	markRunning := func() {
		t.Helper()
		_, err := pool.Exec(
			ctx,
			`INSERT INTO agent_wakeups(agent_id, ready_at, updated_at)
			 VALUES ($1, statement_timestamp(), statement_timestamp())
			 ON CONFLICT (agent_id) DO UPDATE SET
			   ready_at = EXCLUDED.ready_at,
			   updated_at = EXCLUDED.updated_at`,
			running.Agent.ID,
		)
		require.NoError(t, err)
	}
	markIdle := func(agentID uuid.UUID) error {
		if _, err := pool.Exec(ctx, `DELETE FROM agent_wakeups WHERE agent_id = $1`, agentID); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `DELETE FROM agent_runtime_locks WHERE agent_id = $1`, agentID); err != nil {
			return err
		}
		_, err := pool.Exec(
			ctx,
			`UPDATE agent_execution_state SET logical_ready_at = NULL, incomplete_tools = false WHERE agent_id = $1`,
			agentID,
		)
		return err
	}

	// The two sorts run different list queries, so cover both.
	for _, sort := range []string{"-created_at", "-updated_at"} {
		require.NoError(t, markIdle(running.Agent.ID))
		markRunning()
		var transitions atomic.Int32
		var transitionErr error
		tracer.onPageRead = func() {
			if transitions.Add(1) == 1 {
				transitionErr = markIdle(running.Agent.ID)
			}
		}
		listed := requestJSONWithHeaders(
			t, handler, http.MethodGet,
			project.ProjectPath+"/agents?activity_state=running&sort="+sort, "", "", http.StatusOK,
			authHeaders(project.AdminToken),
		)
		tracer.onPageRead = nil
		require.NoError(t, transitionErr, sort)
		require.Equal(t, int32(1), transitions.Load(), sort)
		require.Equal(t, []string{runningID}, listedAgentIDs(t, listed), sort)
		require.Equal(t, "running", listedAgentActivity(t, listed, runningID), sort)
	}
}
