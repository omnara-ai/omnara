//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestManagedChannelRegistrationRollsBackParentWhenUnparentedCreatorWins(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelAuthorityFixture(t, ctx, "managed-unparented-race")
	input, parent := managedChannelRegistrationInputs(f)
	winnerTx := integrationdb.BeginTx(t, ctx, f.Store.pool)
	var winnerPID int32
	require.NoError(t, winnerTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&winnerPID))
	winner, err := f.Store.Integrations().CreateIntegrationTargetTx(ctx, winnerTx, input)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, winner.ParentChannelID)
	// This uncommitted address is invisible to registration's initial lookup,
	// while its unique-index entry will block the later child INSERT.
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, testProjectID, f.InstallID, input.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	registration := integrationdb.RunAsync(func() (integrationstore.IntegrationTargetRecord, error) {
		return f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "-- name: InsertIntegrationTarget ", winnerPID)
	// Reaching the child INSERT proves lookup already saw no winner and the
	// inline parent was prepared in the losing transaction. No timing sleeps or
	// production hooks are needed to hold this interleaving.
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, testProjectID, f.InstallID, parent.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "the provisional parent is not committed independently")
	require.NoError(t, winnerTx.Commit(ctx))
	loser := integrationdb.Await(t, registration, "managed registration after unparented creator commits")
	require.ErrorIs(t, loser.Err, storeerr.ErrConflict)
	require.Equal(t, uuid.Nil, loser.Value.ID)
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, testProjectID, f.InstallID, parent.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "the losing transaction must roll back its unused parent")

	replayed, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	require.NoError(t, err)
	require.Equal(t, winner.ID, replayed.ID)
	require.False(t, replayed.Created)
	require.Equal(t, uuid.Nil, replayed.ParentChannelID)
	var targets, parents, bindings int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM integration_targets WHERE integration_install_id=$1),
  (SELECT count(*) FROM integration_targets WHERE integration_install_id=$1 AND provider_ref=$2),
  (SELECT count(*) FROM integration_target_bindings WHERE integration_install_id=$1)`,
		f.InstallID, parent.ProviderRef).Scan(&targets, &parents, &bindings))
	require.Equal(t, 2, targets, "fixture root and the exact unparented winner")
	require.Zero(t, parents)
	require.Zero(t, bindings)
}
