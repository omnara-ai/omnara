//go:build integration

package integrationdb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type OutputBoundedPlanDB struct {
	pgx.Tx
	T               testing.TB
	BuffersPerRow   int
	FixedBuffers    int
	PlanningBuffers int
}

func (db OutputBoundedPlanDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	db.Check(ctx, sql, args...)
	rows, err := db.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return rows, nil
}

func (db OutputBoundedPlanDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	db.Check(ctx, sql, args...)
	return db.Tx.QueryRow(ctx, sql, args...)
}

func ExplainPrepared(t testing.TB, ctx context.Context, tx pgx.Tx, sql string, args ...any) []byte {
	t.Helper()
	statement, err := tx.Conn().Prepare(ctx, "bounded_plan", sql)
	require.NoError(t, err)
	literals := make([]string, len(args))
	for i, arg := range args {
		encoded, err := tx.Conn().TypeMap().Encode(statement.ParamOIDs[i], pgx.TextFormatCode, arg, []byte{})
		require.NoError(t, err)
		if encoded == nil {
			literals[i] = "NULL"
		} else {
			literals[i] = "'" + strings.ReplaceAll(string(encoded), "'", "''") + "'"
		}
	}
	var raw []byte
	explain := "EXPLAIN (ANALYZE, BUFFERS, SUMMARY, FORMAT JSON) EXECUTE bounded_plan(" + strings.Join(literals, ",") + ")"
	require.NoError(t, tx.QueryRow(ctx, explain).Scan(&raw))
	require.NoError(t, tx.Conn().Deallocate(ctx, "bounded_plan"))
	return raw
}

func PlanningBuffers(t testing.TB, raw []byte) int {
	t.Helper()
	var plans []struct{ Planning PlanNode }
	require.NoError(t, json.Unmarshal(raw, &plans))
	require.Len(t, plans, 1)
	return plans[0].Planning.Hit + plans[0].Planning.Read
}

func (db OutputBoundedPlanDB) Check(ctx context.Context, sql string, args ...any) []byte {
	db.T.Helper()
	raw := ExplainPrepared(db.T, ctx, db.Tx, sql, args...)
	rows := int(ReadPlan(db.T, raw).Rows)
	perRow := db.BuffersPerRow
	if perRow == 0 {
		perRow = 40
	}
	require.LessOrEqual(db.T, PlanBuffers(db.T, raw), db.FixedBuffers+perRow*rows, "%s", raw)
	if db.PlanningBuffers > 0 {
		require.LessOrEqual(db.T, PlanningBuffers(db.T, raw), db.PlanningBuffers, "%s", raw)
	}
	db.T.Logf("plan rows=%d execution_buffers=%d planning_buffers=%d",
		rows, PlanBuffers(db.T, raw), PlanningBuffers(db.T, raw))
	return raw
}
