//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func controlReceiveInput(f providerStateFixture, event string) integrationstore.ReceiveIntegrationControlInput {
	return integrationstore.ReceiveIntegrationControlInput{
		IntegrationAppID: f.app.ID, ProviderTenantID: "42", EventID: event,
		Payload:      json.RawMessage(`{"action":"changed","id":9007199254740993}`),
		Capabilities: testChannelCapabilities("github"),
	}
}

func receiveControl(t *testing.T, f providerStateFixture, event string) integrationstore.IntegrationControlReceipt {
	t.Helper()
	receipt, err := f.store.Integrations().ReceiveIntegrationControl(t.Context(), controlReceiveInput(f, event))
	require.NoError(t, err)
	return receipt
}

func claimControl(t *testing.T, f providerStateFixture) integrationstore.IntegrationControlReceipt {
	t.Helper()
	receipt, found, err := f.store.Integrations().ClaimNextIntegrationControl(t.Context(),
		integrationstore.ClaimNextIntegrationControlInput{
			Capability: testChannelCapabilities("github")[0], LeaseDuration: time.Minute,
		})
	require.NoError(t, err)
	require.True(t, found)
	return receipt
}

func controlFinishInput(
	receipt integrationstore.IntegrationControlReceipt, outcome integrationstore.IntegrationControlOutcome,
) integrationstore.FinishIntegrationControlInput {
	input := integrationstore.FinishIntegrationControlInput{
		IntegrationAppID: receipt.IntegrationAppID, ID: receipt.ID, LeaseToken: receipt.LeaseToken,
		LeaseGeneration: receipt.LeaseGeneration, Outcome: outcome, Capabilities: testChannelCapabilities("github"),
	}
	if outcome == integrationstore.IntegrationControlRetry || outcome == integrationstore.IntegrationControlFailed {
		input.LastError = json.RawMessage(`{"code":"provider_unavailable"}`)
	}
	return input
}

func makeControlDue(t *testing.T, f providerStateFixture, id uuid.UUID) {
	t.Helper()
	_, err := f.store.pool.Exec(t.Context(), `UPDATE integration_control_receipts
SET available_at=statement_timestamp()-interval '1 second',
lease_expires_at=CASE WHEN state='processing' THEN statement_timestamp()-interval '1 second' END WHERE id=$1`, id)
	require.NoError(t, err)
}

func TestIntegrationControlReceiveDedupeAndFixedScopeBound(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	first := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateActive)
	last := f.install(t, testProjectID, "42", "102", integrationstore.IntegrationInstallStateDisabled)
	_, err := f.store.pool.Exec(t.Context(), `UPDATE integration_installs
SET provider_identity='{"node_id":"R_exact"}' WHERE id=$1`, last.ID)
	require.NoError(t, err)
	receipt := receiveControl(t, f, "fixed")
	require.Equal(t, last.ID, receipt.Progress.EndInstallID)
	f.install(t, testProjectID, "42", "103", integrationstore.IntegrationInstallStateActive)
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_apps SET display_name='Renamed' WHERE id=$1`, f.app.ID)
	require.NoError(t, err)
	input := controlReceiveInput(f, "fixed")
	input.Payload = json.RawMessage(`{ "id":9007199254740993, "action":"changed" }`)
	replay, err := f.store.Integrations().ReceiveIntegrationControl(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, receipt.ID, replay.ID)
	require.Equal(t, receipt.Progress, replay.Progress)
	list := f.listInput()
	list.ThroughID = &receipt.Progress.EndInstallID
	page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), list)
	require.NoError(t, err)
	require.Len(t, page.Installations, 1)
	require.Equal(t, first.ID, page.Installations[0].ID)
	require.True(t, page.HasMore)
	list.AfterID = first.ID
	page, err = f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), list)
	require.NoError(t, err)
	require.Len(t, page.Installations, 1)
	require.Equal(t, last.ID, page.Installations[0].ID)
	require.JSONEq(t, `{"node_id":"R_exact"}`, string(page.Installations[0].ProviderIdentity))
	require.False(t, page.HasMore)
	input.ProviderTenantID = "43"
	_, err = f.store.Integrations().ReceiveIntegrationControl(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	input.ProviderTenantID, input.Payload = "42", json.RawMessage(`{"action":"changed","id":9007199254740992}`)
	_, err = f.store.Integrations().ReceiveIntegrationControl(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	input = controlReceiveInput(f, "fixed")
	input.Capabilities = []channelconnector.Capability{
		{ConnectorKey: testChannelConnector, Provider: "slack"}, {ConnectorKey: "other", Provider: "github"},
	}
	_, err = f.store.Integrations().ReceiveIntegrationControl(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestIntegrationControlEmptyBoundDoesNotGrowOnReplay(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	receipt := receiveControl(t, f, "empty")
	require.Equal(t, uuid.Nil, receipt.Progress.EndInstallID)
	f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateActive)
	replay := receiveControl(t, f, "empty")
	require.Equal(t, receipt.ID, replay.ID)
	list := f.listInput()
	list.ThroughID = &replay.Progress.EndInstallID
	page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), list)
	require.NoError(t, err)
	require.Empty(t, page.Installations)
	require.False(t, page.HasMore)
	claim := claimControl(t, f)
	finished, err := f.store.Integrations().FinishIntegrationControl(t.Context(),
		controlFinishInput(claim, integrationstore.IntegrationControlCompleted))
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationEventCompleted, finished.State)
}

func TestIntegrationControlConfirmedProgressAndUnboundedTransientRetries(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	first := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateActive)
	last := f.install(t, testProjectID, "42", "102", integrationstore.IntegrationInstallStateDisabled)
	received := receiveControl(t, f, "retry")
	claim := claimControl(t, f)
	input := controlFinishInput(claim, integrationstore.IntegrationControlYield)
	input.LastInstallID = &first.ID
	progress, err := f.store.Integrations().FinishIntegrationControl(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, int64(0), progress.AttemptsSinceProgress)
	require.Equal(t, first.ID, progress.Progress.LastInstallID)
	claim = claimControl(t, f)
	input = controlFinishInput(claim, integrationstore.IntegrationControlYield)
	input.LastInstallID = &first.ID
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "unchanged yield cannot reset backoff")
	input = controlFinishInput(claim, integrationstore.IntegrationControlRetry)
	input.LastInstallID = &last.ID
	input.RetryAfter = 48 * time.Hour
	progress, err = f.store.Integrations().FinishIntegrationControl(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, last.ID, progress.Progress.LastInstallID)
	require.Equal(t, int64(0), progress.AttemptsSinceProgress)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), progress.AvailableAt, 5*time.Second)
	// This exceeds the ordinary event claim limit. Control retries remain claimable
	// and the saturated counter cannot overflow or terminalize unfinished work.
	for attempt := range 35 {
		makeControlDue(t, f, received.ID)
		claim = claimControl(t, f)
		require.Equal(t, int64(min(attempt+1, 30)), claim.AttemptsSinceProgress)
		input = controlFinishInput(claim, integrationstore.IntegrationControlRetry)
		input.LastInstallID = &last.ID
		progress, err = f.store.Integrations().FinishIntegrationControl(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, claim.AttemptsSinceProgress, progress.AttemptsSinceProgress)
		require.Equal(t, integrationstore.IntegrationEventPending, progress.State)
		require.WithinDuration(t, time.Now().Add(150*time.Second), progress.AvailableAt, 151*time.Second)
	}
	makeControlDue(t, f, received.ID)
	claim = claimControl(t, f)
	input = controlFinishInput(claim, integrationstore.IntegrationControlRetry)
	input.LastInstallID = &first.ID
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "acknowledged progress cannot move backward")
	outside := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	input.LastInstallID = &outside
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "progress cannot escape its captured bound")
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(),
		controlFinishInput(claim, integrationstore.IntegrationControlCompleted))
	require.NoError(t, err)
}

func TestIntegrationControlLeaseFencesAndIndependentDeliveries(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	received := receiveControl(t, f, "first")
	_, found, err := f.store.Integrations().ClaimNextIntegrationControl(t.Context(),
		integrationstore.ClaimNextIntegrationControlInput{
			Capability: testChannelCapabilities("slack")[0], LeaseDuration: time.Minute,
		})
	require.NoError(t, err)
	require.False(t, found)
	stale := claimControl(t, f)
	makeControlDue(t, f, received.ID)
	current := claimControl(t, f)
	require.Equal(t, stale.LeaseGeneration+1, current.LeaseGeneration)
	require.NotEqual(t, stale.LeaseToken, current.LeaseToken)
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(),
		controlFinishInput(stale, integrationstore.IntegrationControlCompleted))
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	second := receiveControl(t, f, "second")
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(),
		controlFinishInput(current, integrationstore.IntegrationControlCompleted))
	require.NoError(t, err)
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(),
		controlFinishInput(current, integrationstore.IntegrationControlCompleted))
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	next := claimControl(t, f)
	require.Equal(t, second.ID, next.ID)
	otherApp, err := f.store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, Provider: "github", ProviderAppRef: "other-control-app", DisplayName: "Other",
		ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	wrong := controlFinishInput(next, integrationstore.IntegrationControlCompleted)
	wrong.IntegrationAppID = otherApp.ID
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), wrong)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	input := controlReceiveInput(f, "first")
	input.IntegrationAppID = otherApp.ID
	independent, err := f.store.Integrations().ReceiveIntegrationControl(t.Context(), input)
	require.NoError(t, err)
	require.NotEqual(t, received.ID, independent.ID)
}

func TestIntegrationControlReceiveAndFinishRecheckLockedApp(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"receive", "finish"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			f := newProviderStateFixture(t)
			receiveControl(t, f, "existing")
			claim := claimControl(t, f)
			blocker := integrationdb.BeginTx(t, t.Context(), f.store.pool)
			var pid int32
			require.NoError(t, blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid))
			_, err := blocker.Exec(t.Context(), `UPDATE integration_apps SET state='disabled' WHERE id=$1`, f.app.ID)
			require.NoError(t, err)
			pending := integrationdb.RunAsync(func() (integrationstore.IntegrationControlReceipt, error) {
				if operation == "receive" {
					return f.store.Integrations().ReceiveIntegrationControl(t.Context(), controlReceiveInput(f, "new"))
				}
				return f.store.Integrations().FinishIntegrationControl(t.Context(),
					controlFinishInput(claim, integrationstore.IntegrationControlCompleted))
			})
			integrationdb.WaitForLockWaitBlockedBy(t, t.Context(), f.store.pool,
				"-- name: LockIntegrationAppForInstallation ", pid)
			require.NoError(t, blocker.Commit(t.Context()))
			result := integrationdb.Await(t, pending, "control after app disable")
			require.ErrorIs(t, result.Err, storeerr.ErrNotFound)
			var count int
			var state string
			require.NoError(t, f.store.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM integration_control_receipts`).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, f.store.pool.QueryRow(t.Context(), `SELECT state FROM integration_control_receipts WHERE id=$1`,
				claim.ID).Scan(&state))
			require.Equal(t, "processing", state)
		})
	}
}
