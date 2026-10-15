//go:build integration

package integrationdb

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type QueryTrace struct {
	Statements atomic.Int64
	Begins     atomic.Int64
	Commits    atomic.Int64
	OnQuery    func(string)
}

func (t *QueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.Statements.Add(1)
	if strings.EqualFold(data.SQL, "begin") {
		t.Begins.Add(1)
	}
	if t.OnQuery != nil {
		t.OnQuery(data.SQL)
	}
	return ctx
}
func (t *QueryTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && data.CommandTag.String() == "COMMIT" {
		t.Commits.Add(1)
	}
}
func (*QueryTrace) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}
func (t *QueryTrace) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	t.Statements.Add(1)
	if t.OnQuery != nil {
		t.OnQuery(data.SQL)
	}
}
func (*QueryTrace) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
func MultiXacts(t testing.TB, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(t.Context(), `
SELECT count(*) FROM generate_series(0,pg_relation_size($1::text::regclass)/current_setting('block_size')::int-1) block
CROSS JOIN LATERAL heap_page_items(get_raw_page($1,block)) item
CROSS JOIN LATERAL heap_tuple_infomask_flags(item.t_infomask,item.t_infomask2) flags
WHERE flags.raw_flags @> ARRAY['HEAP_XMAX_IS_MULTI']`, table).Scan(&n))
	return n
}
