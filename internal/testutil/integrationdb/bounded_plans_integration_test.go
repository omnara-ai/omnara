//go:build integration

package integrationdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExplainPreparedPreservesEmptyAndNullParameters(t *testing.T) {
	ctx := t.Context()
	pool := OpenUnmigratedPool(t, ctx)
	tx := BeginTx(t, ctx, pool)
	for _, test := range []struct {
		arg  any
		rows float64
	}{{"", 1}, {nil, 0}, {"'", 0}} {
		raw := ExplainPrepared(t, ctx, tx, "SELECT 1 WHERE $1::text = ''", test.arg)
		require.Equal(t, test.rows, ReadPlan(t, raw).Rows)
	}
}
