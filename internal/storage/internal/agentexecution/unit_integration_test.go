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
	return NewCell(pool, publisher, nil), pool
}

func unitTestAgent(t *testing.T, u *Unit, project, agent uuid.UUID) *Handle {
	t.Helper()
	require.NoError(t, u.CreatedAgent(project, agent, uuid.Nil))
	h, err := u.Agent(AgentRoute{ProjectID: project, AgentID: agent, RootAgentID: agent})
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
	u, err := BeginTestUnit(ctx, cell)
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
	u, err := BeginTestUnit(ctx, cell)
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
	rejected := errors.New("reject inner")
	var escaped *Handle
	require.ErrorIs(t, u.Savepoint(ctx, func(u *Unit) error {
		mutation.head.StopSequence = 2
		pending.AddDaemonWork(uuid.New())
		pending.AddDaemonProcessTermination(machine, uuid.New())
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
			u, err := BeginTestUnit(ctx, cell)
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
	u, err := BeginTestUnit(ctx, cell)
	require.NoError(t, err)
	defer func() { _ = u.Rollback(ctx) }()
	refs := []lifecyclelock.AgentRef{{ProjectID: project, AgentID: higher}}
	require.NoError(t, u.LockAgentRefs(ctx, refs, nil))
	require.Equal(t, lower, u.handles[higher].route.RootAgentID)
	plan, err := u.PlanAgents(ctx, refs, nil)
	require.NoError(t, err)
	require.ErrorIs(t,
		u.LockAgentRefs(ctx,
			[]lifecyclelock.AgentRef{{ProjectID: project,
				AgentID: lower}},
			nil),
		ErrLockPlan)
	require.ErrorIs(t, u.active(), ErrUnitAborted)
	other, err := BeginTestUnit(ctx, NewCell(pool, nil, nil))
	require.NoError(t, err)
	defer func() { _ = other.Rollback(ctx) }()
	require.ErrorIs(t, other.LockAgents(ctx, plan), ErrLockPlan)
	rollback := errors.New("rollback locks")
	require.ErrorIs(t, other.Savepoint(ctx, func(u *Unit) error {
		return errors.Join(u.LockAgentRefs(
			ctx,
			[]lifecyclelock.AgentRef{{ProjectID: project, AgentID: lower}}, nil),
			rollback,
		)
	}), rollback)
	require.Empty(t, other.handles)
	check := integrationdb.BeginTx(t, ctx, pool)
	_, err = check.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE NOWAIT`, lower)
	require.NoError(t, err)
}

func TestUnitExpandsLockPlanAndRollsBackAttempt(t *testing.T) {
	ctx := t.Context()
	cell, pool := unitTestCell(t, nil)
	project, org := uuid.New(), uuid.New()
	low,
		high := uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff")
	_,
		err := pool.Exec(ctx,
		`INSERT INTO agents(id,project_id,org_id,root_agent_id) VALUES($1,$3,$4,$1),($2,$3,$4,$2)`,
		low,
		high,
		project,
		org)
	require.NoError(t, err)
	for _, first := range []uuid.UUID{low, high} {
		t.Run(first.String(), func(t *testing.T) {
			second := low
			if first == low {
				second = high
			}
			attempts := 0
			_, err := Retry(ctx, "expand", func(ctx context.Context) (struct{}, error) {
				attempts++
				u, err := BeginTestUnit(ctx, cell)
				if err != nil {
					return struct{}{}, err
				}
				defer func() { _ = u.Rollback(ctx) }()
				if err = u.LockAgentRefs(ctx, []lifecyclelock.AgentRef{{ProjectID: project, AgentID: first}}, nil); err != nil {
					return struct{}{}, err
				}
				if _, err = u.DB().Exec(ctx, `INSERT INTO unit_probe(agent_id,phase) VALUES($1,'attempt')`, first); err != nil {
					return struct{}{}, err
				}
				if _, err = u.Handle(project, second); err != nil {
					return struct{}{}, err
				}
				return struct{}{}, u.Commit(ctx, "expanded")
			})
			require.NoError(t, err)
			require.Equal(t, 2, attempts)
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM unit_probe WHERE agent_id=$1`, first).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func TestUnitLockPlanExpansionIsBounded(t *testing.T) {
	attempts := 0
	_, err := Retry(t.Context(), "bounded", func(context.Context) (struct{}, error) {
		attempts++
		return struct{}{}, &LockPlanError{Agents: []lifecyclelock.AgentRef{{ProjectID: uuid.New(), AgentID: uuid.New()}}}
	})
	require.ErrorIs(t, err, ErrLockPlan)
	require.Equal(t, 8, attempts)
	attempts = 0
	ref := lifecyclelock.AgentRef{ProjectID: uuid.New(), AgentID: uuid.New()}
	_, err = Retry(t.Context(), "no progress", func(context.Context) (struct{}, error) {
		attempts++
		return struct{}{}, &LockPlanError{Agents: []lifecyclelock.AgentRef{ref}}
	})
	require.ErrorIs(t, err, ErrLockPlan)
	require.Equal(t, 2, attempts)
}

func TestUnitExpandedClaimKeepsOriginalAgent(t *testing.T) {
	ctx := t.Context()
	cell, pool := unitTestCell(t, nil)
	project, org, first, other, parent := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `
CREATE TABLE agent_wakeups(agent_id uuid PRIMARY KEY, ready_at timestamptz NOT NULL);
CREATE TABLE agent_runtime_locks(agent_id uuid PRIMARY KEY);`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO agents(id,project_id,org_id,root_agent_id)
SELECT id,$1,$2,id FROM unnest($3::uuid[]) id`, project, org, []uuid.UUID{first, other, parent})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO agent_wakeups VALUES($1,now()-interval '1 day'),($2,now())`, first, other)
	require.NoError(t, err)
	attempts := 0
	route, err := Retry(ctx, "expand claim", func(ctx context.Context) (AgentRoute, error) {
		attempts++
		u, err := BeginTestUnit(ctx, cell)
		if err != nil {
			return AgentRoute{}, err
		}
		defer func() { _ = u.Rollback(ctx) }()
		route, err := u.ClaimAgent(ctx)
		if err != nil {
			return route, err
		}
		if _, missing := u.Handle(project, parent); missing != nil {
			require.NoError(t, u.Rollback(ctx))
			_, err = pool.Exec(ctx, `UPDATE agent_wakeups SET ready_at='epoch' WHERE agent_id=$1`, other)
			require.NoError(t, err)
			return AgentRoute{}, missing
		}
		return route, u.Commit(ctx, "expanded claim")
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	require.Equal(t, first, route.AgentID)
}

func TestUnitExpandsCompleteRequestedPlan(t *testing.T) {
	ctx := t.Context()
	cell, pool := unitTestCell(t, nil)
	project, org := uuid.New(), uuid.New()
	first := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	ids := []uuid.UUID{first}
	var refs []lifecyclelock.AgentRef
	for i := byte(1); i <= 12; i++ {
		id := uuid.UUID{15: i}
		ids = append(ids, id)
		refs = append(refs, lifecyclelock.AgentRef{ProjectID: project, AgentID: id})
	}
	_, err := pool.Exec(ctx, `INSERT INTO agents(id,project_id,org_id,root_agent_id)
SELECT id,$1,$2,id FROM unnest($3::uuid[]) id`, project, org, ids)
	require.NoError(t, err)
	attempts := 0
	_, err = Retry(ctx, "complete expansion", func(ctx context.Context) (struct{}, error) {
		attempts++
		u, err := BeginTestUnit(ctx, cell)
		if err != nil {
			return struct{}{}, err
		}
		defer func() { _ = u.Rollback(ctx) }()
		if err = u.LockAgentRefs(ctx, []lifecyclelock.AgentRef{{ProjectID: project, AgentID: first}}, nil); err != nil {
			return struct{}{}, err
		}
		if err = u.LockAgentRefs(ctx, refs, nil); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, u.Commit(ctx, "complete expansion")
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
}

func TestCellBeginRequiresRetryBoundary(t *testing.T) {
	cell, _ := unitTestCell(t, nil)
	unit, err := cell.Begin(t.Context())
	if unit != nil {
		_ = unit.Rollback(t.Context())
	}
	require.ErrorContains(t, err, "retry boundary")
}

func TestUnitSavepointPropagatesLockPlan(t *testing.T) {
	ctx := t.Context()
	cell, pool := unitTestCell(t, nil)
	project, org, first, second := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO agents(id,project_id,org_id,root_agent_id)
SELECT id,$1,$2,id FROM unnest($3::uuid[]) id`, project, org, []uuid.UUID{first, second})
	require.NoError(t, err)
	attempts := 0
	_, err = Retry(ctx, "savepoint expansion", func(ctx context.Context) (struct{}, error) {
		attempts++
		unit, err := BeginTestUnit(ctx, cell)
		if err != nil {
			return struct{}{}, err
		}
		defer func() { _ = unit.Rollback(ctx) }()
		if err = unit.LockAgentRefs(ctx, []lifecyclelock.AgentRef{{ProjectID: project, AgentID: first}}, nil); err != nil {
			return struct{}{}, err
		}
		err = unit.Savepoint(ctx, func(u *Unit) error {
			_, err := u.DB().Exec(ctx, `INSERT INTO unit_probe(agent_id,phase) VALUES($1,'advance')`, first)
			if err != nil {
				return err
			}
			_, err = u.Handle(project, second)
			return err
		})
		if attempts == 1 {
			var missing *LockPlanError
			require.ErrorAs(t, err, &missing)
			require.Contains(t, missing.Agents, lifecyclelock.AgentRef{ProjectID: project, AgentID: second})
		}
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, unit.Commit(ctx, "expanded continuation")
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM unit_probe WHERE agent_id=$1`, first).Scan(&count))
	require.Equal(t, 1, count)
}
