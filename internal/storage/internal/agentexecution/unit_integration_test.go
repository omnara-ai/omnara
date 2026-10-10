//go:build integration

package agentexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type unitPublisher struct {
	publish func(context.Context, notifications.PostCommitIntent)
}

func (p unitPublisher) PublishPostCommit(ctx context.Context, intent notifications.PostCommitIntent) {
	p.publish(ctx, intent)
}

func unitTestCell(t *testing.T, publisher notifications.PostCommitPublisher) (*AgentCell, *pgxpool.Pool) {
	t.Helper()
	pool := integrationdb.OpenUnmigratedPool(t, t.Context())
	_, err := pool.Exec(t.Context(), `
CREATE TABLE agents(id uuid PRIMARY KEY, project_id uuid NOT NULL, org_id uuid NOT NULL,
                    parent_agent_id uuid, root_agent_id uuid NOT NULL, state text NOT NULL DEFAULT 'active');
CREATE TABLE unit_probe(id bigint GENERATED ALWAYS AS IDENTITY, agent_id uuid, phase text, labels text[]);
CREATE TABLE deferred_probe(id integer UNIQUE DEFERRABLE INITIALLY DEFERRED);`)
	require.NoError(t, err)
	return NewCell("test", pool, publisher, nil), pool
}

func unitTestAgent(t *testing.T, u *Unit, project, agent uuid.UUID) *Handle {
	t.Helper()
	require.NoError(t, u.CreatedAgent(project, agent, uuid.Nil))
	h, err := u.Agent(AgentRoute{CellID: u.CellID(), ProjectID: project, AgentID: agent, RootAgentID: agent})
	require.NoError(t, err)
	return h
}

func TestUnitCommitLifecycle(t *testing.T) {
	ctx := t.Context()
	var published []notifications.PostCommitIntent
	var pool *pgxpool.Pool
	cell, pool := unitTestCell(
		t,
		unitPublisher{publish: func(ctx context.Context, intent notifications.PostCommitIntent) {
			require.NoError(t, ctx.Err())
			require.Nil(t, ctx.Done())
			var phases []string
			require.NoError(
				t,
				pool.QueryRow(ctx, `SELECT array_agg(phase ORDER BY id) FROM unit_probe`).Scan(&phases),
			)
			require.Equal(t, []string{"write"}, phases)
			published = append(published, intent)
		}},
	)
	u, err := cell.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = u.Rollback(ctx) }()
	_, raw := any(u.DB()).(pgx.Tx)
	require.False(t, raw)
	rows, err := u.DB().Query(ctx, `SELECT 1`)
	require.NoError(t, err)
	require.Nil(t, rows.Conn())
	rows.Close()
	require.NoError(t, rows.Err())
	project := uuid.New()
	higher := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	lower := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	unitTestAgent(t, u, project, higher)
	unitTestAgent(t, u, project, lower)
	require.Equal(t, lower, u.orderedHandles()[0].route.AgentID)
	_, err = u.DB().Exec(ctx, `INSERT INTO unit_probe(agent_id,phase) VALUES($1,'write')`, lower)
	require.NoError(t, err)
	u.Notifications().AddDaemonWork(uuid.New())
	require.Empty(t, published)
	require.NoError(t, u.Commit(ctx, "probe"))
	require.Len(t, published, 1)
	var agents []uuid.UUID
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT array_agg(agent_id ORDER BY id) FROM unit_probe`).Scan(&agents),
	)
	require.Equal(t, []uuid.UUID{lower}, agents)
	_, err = u.handles[lower].Route()
	require.ErrorIs(t, err, ErrUnitClosed)
	require.ErrorIs(t, u.Commit(ctx, "again"), ErrUnitClosed)
}

func TestUnitSavepointSnapshots(t *testing.T) {
	ctx := t.Context()
	var published []notifications.PostCommitIntent
	cell, pool := unitTestCell(
		t,
		unitPublisher{publish: func(_ context.Context, intent notifications.PostCommitIntent) {
			published = append(published, intent)
		}},
	)
	u, err := cell.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = u.Rollback(ctx) }()
	project, agent, machine := uuid.New(), uuid.New(), uuid.New()
	h := unitTestAgent(t, u, project, agent)
	mutation := &executionMutation{head: ExecutionHead{StopSequence: 1}}
	h.mutation = mutation
	pending := u.Notifications()
	pending.AddDaemonWork(machine)
	process := uuid.New()
	pending.AddDaemonProcessTermination(machine, process)
	u.webhooks[agent] = webhookTarget{orgID: uuid.New(), events: []string{"model_output"}}
	originalTarget := u.webhooks[agent]
	rejected := errors.New("reject inner")
	var escaped *Handle
	require.ErrorIs(t, u.Savepoint(ctx, func(u *Unit) error {
		mutation.head.StopSequence = 2
		pending.AddDaemonWork(uuid.New())
		pending.AddDaemonProcessTermination(machine, uuid.New())
		u.webhooks[agent] = webhookTarget{orgID: uuid.New()}
		delete(u.handles, agent)
		escaped = unitTestAgent(t, u, project, uuid.New())
		escaped.mutation = &executionMutation{head: ExecutionHead{StopSequence: 3}}
		delete(u.handles, escaped.route.AgentID)
		_, err := u.DB().
			Exec(ctx, `INSERT INTO unit_probe(phase) VALUES ('rollback'); SELECT pg_advisory_xact_lock(8173)`)
		require.NoError(t, err)
		require.NoError(t, u.Savepoint(ctx, func(u *Unit) error {
			_, err := u.DB().Exec(ctx, `INSERT INTO unit_probe(phase) VALUES ('nested release')`)
			return err
		}))
		require.Empty(t, published)
		return rejected
	}), rejected)
	require.Same(t, pending, u.Notifications())
	require.Same(t, h, u.handles[agent])
	restored := h.mutation
	require.EqualValues(t, 1, restored.head.StopSequence)
	require.Equal(t, originalTarget, u.webhooks[agent])
	_, err = escaped.Route()
	require.ErrorIs(t, err, ErrUnitClosed)
	var unlocked bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(8173)`).Scan(&unlocked))
	require.True(t, unlocked)
	require.NoError(t, u.Savepoint(ctx, func(u *Unit) error {
		require.Error(t, u.Commit(ctx, "nested"))
		require.Error(t, u.Rollback(ctx))
		restored.head.StopSequence = 4
		return nil
	}))
	require.NoError(t, u.Commit(ctx, "snapshot"))
	require.Len(t, published, 2)
	for _, intent := range published {
		if terminate, ok := intent.(notifications.DaemonProcessTerminationCommitted); ok {
			require.Equal(t, []uuid.UUID{process}, terminate.ProcessIDs)
		}
	}
	var phases []string
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT array_agg(phase ORDER BY id) FROM unit_probe`).Scan(&phases),
	)
	require.Empty(t, phases)
}

func TestUnitFailuresSuppressPublication(t *testing.T) {
	for _, scenario := range []string{"rollback", "deferred commit", "aborted SQL", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			cell, pool := unitTestCell(
				t,
				unitPublisher{publish: func(context.Context, notifications.PostCommitIntent) {
					t.Error("notification from failed transaction")
				}},
			)
			u, err := cell.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = u.Rollback(ctx) }()
			unitTestAgent(t, u, uuid.New(), uuid.New())
			u.Notifications().AddDaemonWork(uuid.New())
			switch scenario {
			case "rollback":
				require.NoError(t, u.Rollback(ctx))
			case "deferred commit":
				_, err := u.DB().Exec(ctx, `INSERT INTO deferred_probe VALUES (1),(1)`)
				require.NoError(t, err)
			case "aborted SQL":
				_, err := u.DB().Exec(ctx, `SELECT 1 / 0`)
				require.Error(t, err)
				_, err = u.DB().Exec(ctx, `SELECT 1`)
				require.ErrorIs(t, err, ErrUnitAborted)
			case "cancel":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			require.Error(t, u.Commit(ctx, scenario))
			_ = u.Rollback(t.Context())
			var count int
			require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM unit_probe`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestUnitSavepointRecoversDatabaseErrors(t *testing.T) {
	ctx := t.Context()
	cell, _ := unitTestCell(t, nil)
	require.NoError(t, cell.Transact(ctx, func(u *Unit) error {
		err := u.Savepoint(ctx, func(u *Unit) error {
			_, err := u.DB().Exec(ctx, `SELECT 1 / 0`)
			require.Error(t, err)
			return nil
		})
		require.ErrorIs(t, err, ErrUnitAborted)
		_, err = u.DB().Exec(ctx, `INSERT INTO deferred_probe VALUES (1)`)
		require.NoError(t, err)
		return err
	}))
}

func TestUnitLockPlansAndSavepointLocks(t *testing.T) {
	ctx := t.Context()
	cell, pool := unitTestCell(t, nil)
	project, org := uuid.New(), uuid.New()
	lower := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	higher := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	_, err := pool.Exec(
		ctx,
		`INSERT INTO agents(id,project_id,org_id,parent_agent_id,root_agent_id) VALUES ($1,$3,$4,NULL,$1),($2,$3,$4,$1,$1)`,
		lower,
		higher,
		project,
		org,
	)
	require.NoError(t, err)
	u, err := cell.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = u.Rollback(ctx) }()
	refs := []lifecyclelock.AgentRef{{ProjectID: project, AgentID: higher}}
	require.NoError(t, u.LockAgentRefs(ctx, refs, LifecycleAuthority{}))
	require.Equal(t, lower, u.handles[higher].route.RootAgentID)
	require.ErrorIs(t, u.LockAgentRefs(
		ctx,
		[]lifecyclelock.AgentRef{{ProjectID: project, AgentID: lower}}, IngressAuthority{}),
		ErrLockPlan,
	)
	plan, err := u.PlanAgents(ctx, refs, LifecycleAuthority{})
	require.NoError(t, err)
	other, err := NewCell("other", pool, nil, nil).Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = other.Rollback(ctx) }()
	require.ErrorIs(t, other.LockAgents(ctx, plan), ErrLockPlan)
	rollback := errors.New("rollback locks")
	require.ErrorIs(t, other.Savepoint(ctx, func(u *Unit) error {
		return errors.Join(u.LockAgentRefs(
			ctx,
			[]lifecyclelock.AgentRef{{ProjectID: project, AgentID: lower}}, RepairAuthority{}),
			rollback,
		)
	}), rollback)
	require.Empty(t, other.handles)
	check := integrationdb.BeginTx(t, ctx, pool)
	_, err = check.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE NOWAIT`, lower)
	require.NoError(t, err)
}
