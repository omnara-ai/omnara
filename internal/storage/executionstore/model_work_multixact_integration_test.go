//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
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
			for _, table := range []string{"agents", "agent_runtime_locks"} {
				require.Zero(t, integrationdb.MultiXacts(t, f.Store.pool, table), table+" before")
			}
			counter := &integrationdb.QueryTrace{}
			config := f.Store.pool.Config()
			config.ConnConfig.Tracer = counter
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer pool.Close()
			measured := newIntegrationStore(pool)
			transition, err := measured.Execution().AcceptModelOutputAndAdvance(ctx, input)
			t.Logf("fused %s statements=%d", name, counter.Statements.Load())
			bound := 20
			if continued {
				bound = 29
			}
			require.LessOrEqual(t, counter.Statements.Load(), int64(bound))
			require.NoError(t, err)
			require.Nil(t, transition.Fallback)
			require.Equal(t, continued, transition.Continued)
			for _, table := range []string{"agents", "agent_runtime_locks"} {
				n := integrationdb.MultiXacts(t, f.Store.pool, table)
				t.Logf("%s MultiXact tuples after fused acceptance: %d", table, n)
				require.Zero(t, n, table)
			}
		})
	}
}
