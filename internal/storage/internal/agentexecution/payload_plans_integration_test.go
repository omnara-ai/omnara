//go:build integration

package agentexecution_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecutionPayloadInsertsUseGenericPlans(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	for range 20 {
		receiveCommand(t, h, "steering", "payload")
		_, err := h.AdmitInputs(t.Context())
		require.NoError(t, err)
	}
	for _, name := range []string{"InsertExecutionContent", "AppendExecutionEvent"} {
		var generic, custom int64
		require.NoError(
			t,
			u.DB().
				QueryRow(t.Context(),
					`SELECT generic_plans,custom_plans FROM pg_prepared_statements WHERE statement LIKE $1`,
					"-- name: "+name+" :%").
				Scan(&generic, &custom),
		)
		require.GreaterOrEqual(t, generic, int64(15), name)
		require.LessOrEqual(t, custom, int64(5), name)
	}
}
