//go:build integration

package integrationstore_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationControlMaintenanceBoundedSweepSkipsLockedAndHealthy(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	healthy := receiveControl(t, f, "healthy")
	other, err := f.store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, Provider: "github", ProviderAppRef: "retired-control", DisplayName: "Retired",
		ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	otherFixture := f
	otherFixture.app = other
	locked := receiveControl(t, otherFixture, "locked")
	retired := receiveControl(t, otherFixture, "retired")
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_apps SET state='disabled' WHERE id=$1`, other.ID)
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, t.Context(), f.store.pool)
	_, err = blocker.Exec(t.Context(), `SELECT id FROM integration_control_receipts WHERE id=$1 FOR UPDATE`, locked.ID)
	require.NoError(t, err)
	for _, expected := range []struct {
		changed int64
		last    uuid.UUID
	}{
		{0, healthy.ID}, {0, locked.ID}, {1, uuid.Nil},
	} {
		count, sweepErr := f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 1)
		require.NoError(t, sweepErr)
		require.Equal(t, expected.changed, count)
		var last uuid.UUID
		require.NoError(t, f.store.pool.QueryRow(t.Context(), `SELECT last_item_id FROM integration_sweep_cursors
WHERE sweep_kind='control_unprocessable'`).Scan(&last))
		require.Equal(t, expected.last, last, "healthy and locked candidates must consume the page budget")
	}
	require.NoError(t, blocker.Rollback(t.Context()))
	count, err := f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 3)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "next cycle revisits the previously locked receipt")
	for _, id := range []uuid.UUID{locked.ID, retired.ID} {
		var state, code string
		require.NoError(t, f.store.pool.QueryRow(t.Context(), `SELECT state,last_error->>'code'
FROM integration_control_receipts WHERE id=$1`, id).Scan(&state, &code))
		require.Equal(t, "failed", state)
		require.Equal(t, "owner_unavailable", code)
	}
	claim := claimControl(t, f)
	require.Equal(t, healthy.ID, claim.ID)
}

func TestIntegrationControlMaintenanceRespectsLeasesRetentionAndPendingAge(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	oldest, err := f.store.Integrations().OldestPendingIntegrationControl(t.Context())
	require.NoError(t, err)
	require.Nil(t, oldest)
	first := receiveControl(t, f, "first")
	claim := claimControl(t, f)
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_apps SET state='disabled' WHERE id=$1`, f.app.ID)
	require.NoError(t, err)
	count, err := f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 10)
	require.NoError(t, err)
	require.Zero(t, count, "maintenance cannot steal an unexpired claim")
	oldest, err = f.store.Integrations().OldestPendingIntegrationControl(t.Context())
	require.NoError(t, err)
	require.NotNil(t, oldest)
	require.True(t, first.CreatedAt.Equal(*oldest))
	makeControlDue(t, f, claim.ID)
	count, err = f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	oldest, err = f.store.Integrations().OldestPendingIntegrationControl(t.Context())
	require.NoError(t, err)
	require.Nil(t, oldest)
	// Recovery re-enables the app, not its already terminal control delivery.
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_apps SET state='active' WHERE id=$1`, f.app.ID)
	require.NoError(t, err)
	second := receiveControl(t, f, "second")
	_, err = f.store.pool.Exec(t.Context(),
		`UPDATE integration_control_receipts SET attempts_since_progress=30 WHERE id=$1`, second.ID)
	require.NoError(t, err)
	count, err = f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 10)
	require.NoError(t, err)
	require.Zero(t, count, "a live owner has no exhausted retry budget")
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_control_receipts
SET completed_at=statement_timestamp()-interval '8 days' WHERE id=$1`, first.ID)
	require.NoError(t, err)
	count, err = f.store.Integrations().DeleteRetainedIntegrationControls(t.Context(),
		integrationstore.DeleteRetainedIntegrationControlsInput{Retention: 7 * 24 * time.Hour, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	oldest, err = f.store.Integrations().OldestPendingIntegrationControl(t.Context())
	require.NoError(t, err)
	require.NotNil(t, oldest)
	require.True(t, second.CreatedAt.Equal(*oldest))
	reclaim := claimControl(t, f)
	require.Equal(t, second.ID, reclaim.ID)
	require.Equal(t, int64(30), reclaim.AttemptsSinceProgress)
}
