//go:build integration

package integrationstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationControlRecoveryOverThousandScopesAndThirtyTwoYields(t *testing.T) {
	t.Parallel()
	f := newControlRecoveryFixture(t)
	installs := f.installations(t, 1007)
	receipt := f.receive(t, "large-installation-change")
	require.Equal(t, installs[len(installs)-1].ID, receipt.Progress.EndInstallID)
	_, err := f.store.Integrations().ReceiveIntegrationEvent(t.Context(), integrationstore.ReceiveIntegrationEventInput{
		ProjectID: installs[0].ProjectID, IntegrationInstallID: installs[0].ID, EventID: "disabled-message",
		Payload: json.RawMessage(`{}`), Capabilities: testChannelCapabilities("github"),
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound, "control eligibility cannot reopen ordinary message admission")
	later := f.installations(t, 1)[0]
	require.Positive(t, bytes.Compare(later.ID[:], receipt.Progress.EndInstallID[:]))
	confirmed := make(map[uuid.UUID]bool, len(installs))
	last := uuid.Nil
	yields := 0
	for len(confirmed) < len(installs) {
		claim := f.claim(t)
		require.Equal(t, receipt.ID, claim.ID)
		require.Equal(t, last, claim.Progress.LastInstallID)
		require.Equal(t, receipt.Progress.EndInstallID, claim.Progress.EndInstallID)
		require.EqualValues(t, 1, claim.AttemptsSinceProgress)
		page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(),
			integrationstore.ListConnectorInstallationControlScopesInput{
				IntegrationAppID: f.app.ID, ProviderTenantID: "42", Limit: 100,
				AfterID: last, ThroughID: &claim.Progress.EndInstallID, Capabilities: testChannelCapabilities("github"),
			})
		require.NoError(t, err)
		require.NotEmpty(t, page.Installations)
		require.Equal(t, receipt.Progress.EndInstallID, page.ThroughID)
		// Yield partway through a full page, never at the page's last ID. This
		// catches workers that save the fetched page boundary instead of applied work.
		for _, scope := range page.Installations[:min(29, len(page.Installations))] {
			require.False(t, confirmed[scope.ID], "restart must not repeatedly process the confirmed prefix")
			require.Equal(t, integrationstore.IntegrationInstallStateDisabled, scope.State)
			_, err := f.store.Integrations().SetConnectorInstallationProviderState(t.Context(),
				integrationstore.SetConnectorInstallationProviderStateInput{
					IntegrationAppID: f.app.ID, IntegrationInstallID: scope.ID,
					ProviderTenantID: scope.ProviderTenantID, ProviderAccountRef: scope.ProviderAccountRef,
					ExpectedAppConfigurationRevision: page.AppConfigurationRevision,
					ExpectedConfigurationRevision:    scope.ConfigurationRevision,
					State:                            integrationstore.IntegrationInstallStateActive,
					Capabilities:                     testChannelCapabilities("github"),
				})
			require.NoError(t, err)
			confirmed[scope.ID], last = true, scope.ID
		}
		outcome := integrationstore.IntegrationControlYield
		if len(confirmed) == len(installs) {
			outcome = integrationstore.IntegrationControlCompleted
		}
		finish := controlRecoveryFinish(claim, outcome)
		finish.LastInstallID = &last
		progressed, err := f.store.Integrations().FinishIntegrationControl(t.Context(), finish)
		require.NoError(t, err)
		require.Equal(t, last, progressed.Progress.LastInstallID)
		require.Zero(t, progressed.AttemptsSinceProgress)
		if outcome == integrationstore.IntegrationControlYield {
			yields++
			deleted, err := f.store.Integrations().DeleteRetainedIntegrationControls(t.Context(),
				integrationstore.DeleteRetainedIntegrationControlsInput{Retention: time.Microsecond, Limit: 1})
			require.NoError(t, err)
			require.Zero(t, deleted, "age does not expire unfinished reconciliation")
		}
		if yields == 1 {
			name := "Rotated control configuration"
			_, err := f.store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
				OrgID: f.app.OrgID, ID: f.app.ID, DisplayName: &name,
			})
			require.NoError(t, err)
		}
		// New store composition models a process restart: no in-memory cursor,
		// page, revision or fixed-bound state is carried forward by the consumer.
		f.store = newSecretIntegrationStore(f.store.pool)
		replayed := f.receive(t, "large-installation-change")
		require.Equal(t, progressed.Progress, replayed.Progress)
		require.Equal(t, receipt.Payload, replayed.Payload)
	}
	require.Greater(t, yields, 32)
	require.Len(t, confirmed, 1007)
	for _, install := range installs {
		require.True(t, confirmed[install.ID])
	}
	current, err := f.store.Integrations().GetIntegrationInstallByID(t.Context(), later.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInstallStateDisabled, current.State)
	require.Equal(t, later.ConfigurationRevision, current.ConfigurationRevision)
	fresh := f.receive(t, "later-delivery")
	require.Equal(t, later.ID, fresh.Progress.EndInstallID)
	require.Equal(t, uuid.Nil, fresh.Progress.LastInstallID)
	retained, err := f.store.Integrations().DeleteRetainedIntegrationControls(t.Context(),
		integrationstore.DeleteRetainedIntegrationControlsInput{Retention: 7 * 24 * time.Hour, Limit: 1})
	require.NoError(t, err)
	require.Zero(t, retained)
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_control_receipts
		SET completed_at=statement_timestamp()-interval '8 days' WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	deleted, err := f.store.Integrations().DeleteRetainedIntegrationControls(t.Context(),
		integrationstore.DeleteRetainedIntegrationControlsInput{Retention: 7 * 24 * time.Hour, Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	afterRetention := f.receive(t, "large-installation-change")
	require.NotEqual(t, receipt.ID, afterRetention.ID, "receipt dedupe ends only at terminal retention expiry")
	require.Equal(t, later.ID, afterRetention.Progress.EndInstallID)
}

func TestIntegrationControlRecoveryHasNoTransientClaimCeiling(t *testing.T) {
	t.Parallel()
	f := newControlRecoveryFixture(t)
	installs := f.installations(t, 2)
	first := f.receive(t, "unavailable-provider")
	for attempt := range 40 {
		claimed := f.claim(t)
		require.Equal(t, first.ID, claimed.ID)
		require.EqualValues(t, attempt+1, claimed.LeaseGeneration)
		require.EqualValues(t, min(attempt+1, 30), claimed.AttemptsSinceProgress)
		input := controlRecoveryFinish(claimed, integrationstore.IntegrationControlRetry)
		input.LastError = json.RawMessage(`{"code":"provider_unavailable"}`)
		retried, err := f.store.Integrations().FinishIntegrationControl(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, first.Progress, retried.Progress)
		require.Equal(t, integrationstore.IntegrationEventPending, retried.State)
		failed, err := f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 1)
		require.NoError(t, err)
		require.Zero(t, failed, "healthy control work is not terminalized by attempt count")
		_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_control_receipts
			SET available_at=statement_timestamp()-interval '1 second' WHERE id=$1`, first.ID)
		require.NoError(t, err)
	}
	claimed := f.claim(t)
	input := controlRecoveryFinish(claimed, integrationstore.IntegrationControlYield)
	input.LastInstallID = &installs[0].ID
	progressed, err := f.store.Integrations().FinishIntegrationControl(t.Context(), input)
	require.NoError(t, err)
	require.Zero(t, progressed.AttemptsSinceProgress)
	require.JSONEq(t, `{}`, string(progressed.LastError))
	claimed = f.claim(t)
	require.EqualValues(t, 1, claimed.AttemptsSinceProgress)
	require.Equal(t, installs[0].ID, claimed.Progress.LastInstallID)
	input = controlRecoveryFinish(claimed, integrationstore.IntegrationControlFailed)
	input.LastError = json.RawMessage(`{"code":"invalid_immutable_event"}`)
	failed, err := f.store.Integrations().FinishIntegrationControl(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationEventFailed, failed.State)
	_, found, err := f.store.Integrations().ClaimNextIntegrationControl(t.Context(), controlRecoveryClaimInput())
	require.NoError(t, err)
	require.False(t, found)
}

func TestIntegrationControlRecoveryArrivalWhileCompletionWaits(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	f := newControlRecoveryFixture(t)
	f.installations(t, 2)
	first := f.receive(t, "earlier-delivery")
	claim := f.claim(t)
	blocker := integrationdb.BeginTx(t, ctx, f.store.pool)
	var blockerPID int32
	require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
	_, err := blocker.Exec(ctx, `SELECT id FROM integration_control_receipts WHERE id=$1 FOR UPDATE`, first.ID)
	require.NoError(t, err)
	finishing := integrationdb.RunAsync(func() (integrationstore.IntegrationControlReceipt, error) {
		return f.store.Integrations().FinishIntegrationControl(ctx,
			controlRecoveryFinish(claim, integrationstore.IntegrationControlCompleted))
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.store.pool,
		"-- name: FinishIntegrationControlReceipt ", blockerPID)
	second := f.receive(t, "arrives-during-completion")
	require.NotEqual(t, first.ID, second.ID)
	secondClaim := f.claim(t)
	require.Equal(t, second.ID, secondClaim.ID, "locked first receipt cannot hide a new delivery")
	require.NoError(t, blocker.Commit(ctx))
	completed := integrationdb.AwaitSuccess(t, finishing, "completion while another delivery arrived")
	require.Equal(t, integrationstore.IntegrationEventCompleted, completed.State)
	var state string
	var token uuid.UUID
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT state,lease_token FROM integration_control_receipts WHERE id=$1`, second.ID).Scan(&state, &token))
	require.Equal(t, "processing", state)
	require.Equal(t, secondClaim.LeaseToken, token)
	_, err = f.store.Integrations().FinishIntegrationControl(ctx,
		controlRecoveryFinish(secondClaim, integrationstore.IntegrationControlCompleted))
	require.NoError(t, err)
}

func TestIntegrationControlRecoveryExpiredLeaseCannotMoveProgress(t *testing.T) {
	t.Parallel()
	f := newControlRecoveryFixture(t)
	installs := f.installations(t, 2)
	f.receive(t, "lease-recovery")
	old := f.claim(t)
	_, err := f.store.pool.Exec(t.Context(), `UPDATE integration_control_receipts
		SET lease_expires_at=now()-interval '1 second',available_at=now()-interval '1 second' WHERE id=$1`, old.ID)
	require.NoError(t, err)
	current := f.claim(t)
	require.NotEqual(t, old.LeaseToken, current.LeaseToken)
	require.Greater(t, current.LeaseGeneration, old.LeaseGeneration)
	stale := controlRecoveryFinish(old, integrationstore.IntegrationControlYield)
	stale.LastInstallID = &installs[0].ID
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), stale)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	fresh := controlRecoveryFinish(current, integrationstore.IntegrationControlYield)
	fresh.LastInstallID = &installs[0].ID
	progressed, err := f.store.Integrations().FinishIntegrationControl(t.Context(), fresh)
	require.NoError(t, err)
	require.Equal(t, installs[0].ID, progressed.Progress.LastInstallID)
	require.Equal(t, old.Progress.EndInstallID, progressed.Progress.EndInstallID)
	stale.Outcome = integrationstore.IntegrationControlCompleted
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), stale)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
}

func TestIntegrationControlRecoveryRetiredChildDoesNotRetireAppWork(t *testing.T) {
	t.Parallel()
	f := newControlRecoveryFixture(t)
	installs := f.installations(t, 3)
	receipt := f.receive(t, "retired-child")
	require.NoError(t, f.store.Integrations().DeleteIntegrationInstall(t.Context(),
		installs[0].ProjectID, installs[0].ID))
	_, err := f.store.Organizations().DeleteProject(t.Context(), testOrgID, installs[2].ProjectID,
		identitystore.NewUserPrincipal(f.admin.ID))
	require.NoError(t, err)
	claim := f.claim(t)
	require.Equal(t, receipt.Progress, claim.Progress, "a retired upper-bound child does not reset receipt identity")
	page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(),
		integrationstore.ListConnectorInstallationControlScopesInput{
			IntegrationAppID: f.app.ID, ProviderTenantID: "42", ThroughID: &claim.Progress.EndInstallID,
			Limit: 100, Capabilities: testChannelCapabilities("github"),
		})
	require.NoError(t, err)
	require.Len(t, page.Installations, 1)
	require.Equal(t, installs[1].ID, page.Installations[0].ID)
	updated, err := f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), f.stateInput(
		installs[1], integrationstore.IntegrationInstallStateActive))
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInstallStateActive, updated.State)
	finish := controlRecoveryFinish(claim, integrationstore.IntegrationControlCompleted)
	finish.LastInstallID = &claim.Progress.EndInstallID
	completed, err := f.store.Integrations().FinishIntegrationControl(t.Context(), finish)
	require.NoError(t, err)
	require.Equal(t, receipt.Progress.EndInstallID, completed.Progress.LastInstallID,
		"authoritatively disappeared children may be acknowledged without recreating them")
	for _, install := range []integrationstore.IntegrationInstallRecord{installs[0], installs[2]} {
		_, err := f.store.Integrations().GetIntegrationInstallByID(t.Context(), install.ID)
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}
}

func TestIntegrationControlRecoveryAmbiguousChildApplicationKeepsConfirmedPrefix(t *testing.T) {
	t.Parallel()
	f := newControlRecoveryFixture(t)
	installs := f.installations(t, 2)
	f.receive(t, "ambiguous-child-write")
	claim := f.claim(t)
	_, err := f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), f.stateInput(
		installs[0], integrationstore.IntegrationInstallStateActive))
	require.NoError(t, err)
	finish := controlRecoveryFinish(claim, integrationstore.IntegrationControlYield)
	finish.LastInstallID = &installs[0].ID
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), finish)
	require.NoError(t, err)
	claim = f.claim(t)
	oldObservation := f.stateInput(installs[1], integrationstore.IntegrationInstallStateActive)
	_, err = f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), oldObservation)
	require.NoError(t, err) // Commit occurred; treat its acknowledgment as lost.
	finish = controlRecoveryFinish(claim, integrationstore.IntegrationControlRetry)
	finish.LastError = json.RawMessage(`{"code":"application_response_lost"}`)
	retried, err := f.store.Integrations().FinishIntegrationControl(t.Context(), finish)
	require.NoError(t, err)
	require.Equal(t, installs[0].ID, retried.Progress.LastInstallID, "ambiguous application is not confirmed progress")
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_control_receipts
		SET available_at=now()-interval '1 second' WHERE id=$1`, claim.ID)
	require.NoError(t, err)
	f.store = newSecretIntegrationStore(f.store.pool)
	claim = f.claim(t)
	_, err = f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), oldObservation)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "a stale observation cannot replay a committed mutation")
	page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(),
		integrationstore.ListConnectorInstallationControlScopesInput{
			IntegrationAppID: f.app.ID, ProviderTenantID: "42", AfterID: claim.Progress.LastInstallID,
			ThroughID: &claim.Progress.EndInstallID, Limit: 100, Capabilities: testChannelCapabilities("github"),
		})
	require.NoError(t, err)
	require.Len(t, page.Installations, 1)
	current := page.Installations[0]
	require.Equal(t, installs[1].ID, current.ID)
	require.Equal(t, integrationstore.IntegrationInstallStateActive, current.State)
	require.Equal(t, installs[1].ConfigurationRevision+1, current.ConfigurationRevision)
	// The provider worker owns re-observation. Core accepts a new observation
	// only with the reloaded revision, independently of the old desired state.
	freshObservation := oldObservation
	freshObservation.ExpectedAppConfigurationRevision = page.AppConfigurationRevision
	freshObservation.ExpectedConfigurationRevision = current.ConfigurationRevision
	freshObservation.State = integrationstore.IntegrationInstallStateDisabled
	_, err = f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), freshObservation)
	require.NoError(t, err)
	finish = controlRecoveryFinish(claim, integrationstore.IntegrationControlCompleted)
	finish.LastInstallID = &current.ID
	_, err = f.store.Integrations().FinishIntegrationControl(t.Context(), finish)
	require.NoError(t, err)
	first, err := f.store.Integrations().GetIntegrationInstallByID(t.Context(), installs[0].ID)
	require.NoError(t, err)
	require.Equal(t, installs[0].ConfigurationRevision+1, first.ConfigurationRevision,
		"recovery must not reapply the already confirmed prefix")
}

func TestIntegrationControlRecoveryUnavailableOwnerFencesAndTerminalizes(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"app_disabled", "app_deleted", "organization_deleted", "owner_project_deleted"} {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			f := newControlRecoveryFixture(t)
			if owner == "owner_project_deleted" {
				var err error
				f.app, err = f.store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
					OrgID: testOrgID, OwnerProjectID: testProjectID, Provider: "github", ProviderAppRef: "project-control-app",
					DisplayName: "Project control", ConnectorKey: testChannelConnector,
					State: integrationstore.IntegrationAppStateActive,
				})
				require.NoError(t, err)
				f.projects = []uuid.UUID{testProjectID}
			}
			f.installations(t, 1)
			processing := f.receive(t, "processing")
			claim := f.claim(t)
			pending := f.receive(t, "pending")
			var err error
			switch owner {
			case "app_disabled":
				state := integrationstore.IntegrationAppStateDisabled
				_, err = f.store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
					OrgID: f.app.OrgID, ID: f.app.ID, State: &state,
				})
			case "app_deleted":
				_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_apps SET deleted_at=now() WHERE id=$1`, f.app.ID)
			case "organization_deleted":
				_, err = f.store.Organizations().DeleteOrganization(t.Context(), testOrgID,
					identitystore.NewUserPrincipal(f.admin.ID))
			case "owner_project_deleted":
				// Isolate the parent gate even before teardown retires its child app.
				_, err = f.store.pool.Exec(t.Context(), `UPDATE projects SET deleted_at=now() WHERE id=$1`, testProjectID)
			}
			require.NoError(t, err)
			_, err = f.store.Integrations().ReceiveIntegrationControl(t.Context(),
				integrationstore.ReceiveIntegrationControlInput{
					IntegrationAppID: f.app.ID, ProviderTenantID: "42", EventID: "new-after-retirement",
					Payload: json.RawMessage(`{}`), Capabilities: testChannelCapabilities("github"),
				})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			_, err = f.store.Integrations().FinishIntegrationControl(t.Context(),
				controlRecoveryFinish(claim, integrationstore.IntegrationControlCompleted))
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			_, found, err := f.store.Integrations().ClaimNextIntegrationControl(t.Context(), controlRecoveryClaimInput())
			require.NoError(t, err)
			require.False(t, found)
			for range 3 {
				_, err := f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 1)
				require.NoError(t, err)
			}
			var processingState, pendingState string
			require.NoError(t, f.store.pool.QueryRow(t.Context(), `SELECT
				(SELECT state FROM integration_control_receipts WHERE id=$1),
				(SELECT state FROM integration_control_receipts WHERE id=$2)`,
				processing.ID, pending.ID).Scan(&processingState, &pendingState))
			require.Equal(t, "processing", processingState, "maintenance preserves the current unexpired lease")
			require.Equal(t, "failed", pendingState)
			_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_control_receipts
				SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, processing.ID)
			require.NoError(t, err)
			for range 3 {
				_, err := f.store.Integrations().FailUnprocessableIntegrationControls(t.Context(), 1)
				require.NoError(t, err)
			}
			require.NoError(t, f.store.pool.QueryRow(t.Context(),
				`SELECT state FROM integration_control_receipts WHERE id=$1`, processing.ID).Scan(&processingState))
			require.Equal(t, "failed", processingState)
		})
	}
}

func controlRecoveryClaimInput() integrationstore.ClaimNextIntegrationControlInput {
	return integrationstore.ClaimNextIntegrationControlInput{
		Capability: testChannelCapability("github"), LeaseDuration: time.Minute,
	}
}

func controlRecoveryFinish(
	claim integrationstore.IntegrationControlReceipt, outcome integrationstore.IntegrationControlOutcome,
) integrationstore.FinishIntegrationControlInput {
	return integrationstore.FinishIntegrationControlInput{
		IntegrationAppID: claim.IntegrationAppID, ID: claim.ID,
		LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
		Outcome: outcome, Capabilities: testChannelCapabilities("github"),
	}
}

type controlRecoveryFixture struct {
	providerStateFixture
	projects    []uuid.UUID
	nextAccount int
}

func newControlRecoveryFixture(t *testing.T) *controlRecoveryFixture {
	t.Helper()
	f := &controlRecoveryFixture{providerStateFixture: newProviderStateFixture(t), projects: []uuid.UUID{testProjectID}}
	other, err := f.store.Identity().CreateProjectForPrincipal(t.Context(), identitystore.CreateProjectForPrincipalInput{
		OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(f.admin.ID),
		Name: "Other control receipt project", IdempotencyKey: "control-recovery-project",
	})
	require.NoError(t, err)
	f.projects = append(f.projects, other.ID)
	return f
}

func (f *controlRecoveryFixture) installations(t *testing.T, count int) []integrationstore.IntegrationInstallRecord {
	t.Helper()
	installs := make([]integrationstore.IntegrationInstallRecord, 0, count)
	for range count {
		f.nextAccount++
		projectID := f.projects[f.nextAccount%len(f.projects)]
		installs = append(installs, f.install(t, projectID, "42", fmt.Sprint(f.nextAccount),
			integrationstore.IntegrationInstallStateDisabled))
	}
	slices.SortFunc(installs, func(a, b integrationstore.IntegrationInstallRecord) int {
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	return installs
}

func (f *controlRecoveryFixture) receive(t *testing.T, eventID string) integrationstore.IntegrationControlReceipt {
	t.Helper()
	receipt, err := f.store.Integrations().ReceiveIntegrationControl(t.Context(),
		integrationstore.ReceiveIntegrationControlInput{
			IntegrationAppID: f.app.ID, ProviderTenantID: "42", EventID: eventID,
			Payload: json.RawMessage(`{"action":"reconcile"}`), Capabilities: testChannelCapabilities("github"),
		})
	require.NoError(t, err)
	return receipt
}

func (f *controlRecoveryFixture) claim(t *testing.T) integrationstore.IntegrationControlReceipt {
	t.Helper()
	claim, found, err := f.store.Integrations().ClaimNextIntegrationControl(t.Context(), controlRecoveryClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	return claim
}
