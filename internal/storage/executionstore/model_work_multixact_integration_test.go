//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/stretchr/testify/require"
)

func TestFusedAcceptanceDoesNotCreateMultiXacts(t *testing.T) {
	for _, continued := range []bool{true, false} {
		name := "release"
		if continued {
			name = "continue"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f, input := fusedModelFixture(t, ctx)
			if continued {
				input.Output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonMaxTokens
			}
			_, err := f.Store.pool.Exec(ctx, `CREATE EXTENSION pageinspect;
ALTER TABLE agents SET (autovacuum_enabled=false);
ALTER TABLE agent_runtime_locks SET (autovacuum_enabled=false)`)
			require.NoError(t, err)
			multixacts := func(table string) int {
				t.Helper()
				var n int
				require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT count(*) FROM generate_series(0,pg_relation_size($1::text::regclass)/current_setting('block_size')::int-1) block
CROSS JOIN LATERAL heap_page_items(get_raw_page($1,block)) item
CROSS JOIN LATERAL heap_tuple_infomask_flags(item.t_infomask,item.t_infomask2) flags
WHERE flags.raw_flags @> ARRAY['HEAP_XMAX_IS_MULTI']`, table).Scan(&n))
				return n
			}
			for _, table := range []string{"agents", "agent_runtime_locks"} {
				require.Zero(t, multixacts(table), table+" before")
			}
			counter := &fusedQueryCounter{}
			config := f.Store.pool.Config()
			config.ConnConfig.Tracer = counter
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer pool.Close()
			measured := newIntegrationStore(pool)
			transition, err := measured.Execution().AcceptModelOutputAndAdvance(ctx, input)
			t.Logf("fused %s statements=%d", name, counter.statements)
			bound := 20
			if continued {
				bound = 29
			}
			require.LessOrEqual(t, counter.statements, bound)
			require.NoError(t, err)
			require.Nil(t, transition.Fallback)
			require.Equal(t, continued, transition.Continued)
			for _, table := range []string{"agents", "agent_runtime_locks"} {
				n := multixacts(table)
				t.Logf("%s MultiXact tuples after fused acceptance: %d", table, n)
				require.Zero(t, n, table)
			}
		})
	}
}

type fusedQueryCounter struct{ statements int }

func (c *fusedQueryCounter) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData,
) context.Context {
	c.statements++
	return ctx
}
func (*fusedQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (*fusedQueryCounter) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}
func (c *fusedQueryCounter) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
	c.statements++
}
func (*fusedQueryCounter) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
