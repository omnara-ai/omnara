//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestInboxStateSourceAndReservationConstraints(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	choice := f.menu(t)
	for _, columns := range []string{
		`source='provider',integration_state_id=$2`,
		`source='state',integration_state_id=$2`,
		`reserved_scope_kind='thread'`,
		`reserved_scope_ref='C123:1.2'`,
	} {
		_, err := f.pool.Exec(f.ctx,
			`UPDATE integration_inbox SET `+columns+` WHERE id=$1 AND $2::uuid IS NOT NULL`, f.source.ID, choice.ID)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "invalid combination: %s", columns)
		require.Equal(t, "23514", pgErr.Code)
	}
}
