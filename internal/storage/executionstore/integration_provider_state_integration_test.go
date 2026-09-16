//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type providerStateFixture struct {
	store *Store
	app   integrationstore.IntegrationAppRecord
	admin identitystore.UserRecord
}

func newProviderStateFixture(t *testing.T) providerStateFixture {
	t.Helper()
	pool := openIntegrationDB(t, t.Context())
	seedMigratedDB(t, t.Context(), pool)
	f := providerStateFixture{store: newSecretIntegrationStore(pool)}
	f.admin = createIntegrationProjectAdmin(t, t.Context(), f.store, "provider-state@example.com")
	var err error
	f.app, err = f.store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, Provider: "github", ProviderAppRef: "provider-state-app", DisplayName: "Provider state",
		ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	return f
}

func (f providerStateFixture) install(
	t *testing.T, projectID uuid.UUID, tenant, repository string, state integrationstore.IntegrationInstallState,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: projectID, IntegrationAppID: f.app.ID,
		InstalledBy: identitystore.NewUserPrincipal(f.admin.ID), Provider: "github",
		IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
		State: state, ProviderTenantID: tenant, ProviderAccountRef: repository,
	}
	install, err := f.store.Integrations().UpsertIntegrationInstall(t.Context(), input)
	require.NoError(t, err)
	return install
}

func (f providerStateFixture) listInput() integrationstore.ListConnectorInstallationControlScopesInput {
	return integrationstore.ListConnectorInstallationControlScopesInput{
		IntegrationAppID: f.app.ID, ProviderTenantID: "42", Limit: 1, Capabilities: testChannelCapabilities("github"),
	}
}

func (f providerStateFixture) stateInput(
	install integrationstore.IntegrationInstallRecord, state integrationstore.IntegrationInstallState,
) integrationstore.SetConnectorInstallationProviderStateInput {
	return integrationstore.SetConnectorInstallationProviderStateInput{
		IntegrationAppID: f.app.ID, IntegrationInstallID: install.ID,
		ProviderTenantID: install.ProviderTenantID, ProviderAccountRef: install.ProviderAccountRef,
		ExpectedAppConfigurationRevision: f.app.ConfigurationRevision,
		ExpectedConfigurationRevision:    install.ConfigurationRevision, State: state,
		Capabilities: testChannelCapabilities("github"),
	}
}

func TestProviderControlListsDisabledAcrossProjectsWithoutChangingOrdinaryAccess(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	other, err := f.store.Identity().CreateProjectForPrincipal(t.Context(), identitystore.CreateProjectForPrincipalInput{
		OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(f.admin.ID),
		Name: "Other project", IdempotencyKey: "provider-control-other",
	})
	require.NoError(t, err)
	active := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateActive)
	disabled := f.install(t, other.ID, "42", "102", integrationstore.IntegrationInstallStateDisabled)
	f.install(t, testProjectID, "43", "103", integrationstore.IntegrationInstallStateActive)
	deleted := f.install(t, testProjectID, "42", "104", integrationstore.IntegrationInstallStateActive)
	require.NoError(t, f.store.Integrations().DeleteIntegrationInstall(t.Context(), testProjectID, deleted.ID))
	input := f.listInput()
	want := map[uuid.UUID]uuid.UUID{active.ID: testProjectID, disabled.ID: other.ID}
	seen := make(map[uuid.UUID]bool)
	for pageIndex := range 2 {
		page, listErr := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), input)
		require.NoError(t, listErr)
		require.Equal(t, f.app.ConfigurationRevision, page.AppConfigurationRevision)
		require.Len(t, page.Installations, 1)
		require.Equal(t, pageIndex == 0, page.HasMore)
		row := page.Installations[0]
		require.Equal(t, want[row.ID], row.ProjectID)
		require.False(t, seen[row.ID])
		require.Equal(t, "42", row.ProviderTenantID)
		seen[row.ID] = true
		input.AfterID = row.ID
		// A state/revision change cannot move an installation across the cursor.
		_, err = f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), f.stateInput(
			active, integrationstore.IntegrationInstallStateActive))
		if pageIndex == 0 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
		}
	}
	_, err = f.store.Integrations().GetConnectorIntegrationInstallByID(t.Context(), f.app.ID, disabled.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.store.Integrations().ReceiveIntegrationEvent(t.Context(), integrationstore.ReceiveIntegrationEventInput{
		ProjectID: other.ID, IntegrationInstallID: disabled.ID, EventID: "disabled",
		Payload: json.RawMessage(`{}`), Capabilities: testChannelCapabilities("github"),
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound, "control lookup must not open ordinary receipt admission")
	input.ProviderTenantID, input.AfterID = "missing", uuid.Nil
	page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), input)
	require.NoError(t, err)
	require.Empty(t, page.Installations)
	require.False(t, page.HasMore)
}

func TestProviderControlFencesUnchangedObservationAndRestoresSameInstallation(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	install := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateActive)
	input := f.stateInput(install, integrationstore.IntegrationInstallStateActive)
	observed, err := f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, install.ConfigurationRevision+1, observed.ConfigurationRevision)
	input.State = integrationstore.IntegrationInstallStateDisabled
	_, err = f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "older disable cannot overwrite observed restoration")
	input.ExpectedConfigurationRevision = observed.ConfigurationRevision
	disabled, err := f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, observed.ConfigurationRevision+1, disabled.ConfigurationRevision)
	page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), f.listInput())
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInstallStateDisabled, page.Installations[0].State)
	input.ExpectedConfigurationRevision = page.Installations[0].ConfigurationRevision
	input.State = integrationstore.IntegrationInstallStateActive
	restored, err := f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
	require.NoError(t, err)
	current, err := f.store.Integrations().GetConnectorIntegrationInstallByID(t.Context(), f.app.ID, install.ID)
	require.NoError(t, err)
	require.Equal(t, install.ID, current.ID)
	require.Equal(t, restored.ConfigurationRevision, current.ConfigurationRevision)
	require.Equal(t, install.CreatedAt, current.CreatedAt)
	require.Equal(t, install.InstalledBy, current.InstalledBy)
	_, err = f.store.Integrations().ReceiveIntegrationEvent(t.Context(), integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, EventID: "restored",
		Payload: json.RawMessage(`{}`), Capabilities: testChannelCapabilities("github"),
	})
	require.NoError(t, err)
}

func TestProviderControlRejectsWrongScopeAndInvalidObservations(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	install := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateActive)
	wrongPairs := []channelconnector.Capability{
		{ConnectorKey: testChannelConnector, Provider: "discord"}, {ConnectorKey: "other", Provider: "github"},
	}
	for _, tc := range []struct {
		name   string
		change func(*integrationstore.SetConnectorInstallationProviderStateInput)
		err    error
	}{
		{"tenant", func(i *integrationstore.SetConnectorInstallationProviderStateInput) { i.ProviderTenantID = "43" },
			storeerr.ErrNotFound},
		{"repository", func(i *integrationstore.SetConnectorInstallationProviderStateInput) { i.ProviderAccountRef = "102" },
			storeerr.ErrNotFound},
		{"app", func(i *integrationstore.SetConnectorInstallationProviderStateInput) { i.IntegrationAppID = uuid.New() },
			storeerr.ErrNotFound},
		{"capability_pair", func(i *integrationstore.SetConnectorInstallationProviderStateInput) {
			i.Capabilities = wrongPairs
		},
			storeerr.ErrNotFound},
		{"revision", func(i *integrationstore.SetConnectorInstallationProviderStateInput) {
			i.ExpectedConfigurationRevision++
		},
			storeerr.ErrStateTransitionConflict},
		{"state", func(i *integrationstore.SetConnectorInstallationProviderStateInput) { i.State = "deleted" },
			storeerr.ErrInvalidRequest},
		{"nul", func(i *integrationstore.SetConnectorInstallationProviderStateInput) { i.ProviderTenantID = "42\x00" },
			storeerr.ErrInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := f.stateInput(install, integrationstore.IntegrationInstallStateDisabled)
			tc.change(&input)
			_, err := f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
			require.ErrorIs(t, err, tc.err)
			current, err := f.store.Integrations().GetIntegrationInstall(t.Context(), testProjectID, install.ID)
			require.NoError(t, err)
			require.Equal(t, install.ConfigurationRevision, current.ConfigurationRevision)
			require.Equal(t, install.State, current.State)
		})
	}
	list := f.listInput()
	list.Capabilities = wrongPairs
	_, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), list)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	list.Capabilities, list.Limit = testChannelCapabilities("github"), 101
	_, err = f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), list)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
}

func TestProviderControlConcurrentObservationsUseOneRevision(t *testing.T) {
	t.Parallel()
	f := newProviderStateFixture(t)
	install := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateDisabled)
	input := f.stateInput(install, integrationstore.IntegrationInstallStateActive)
	blocker := integrationdb.BeginTx(t, t.Context(), f.store.pool)
	var pid int32
	require.NoError(t, blocker.QueryRow(t.Context(), `SELECT pg_backend_pid() FROM integration_installs
WHERE id=$1 FOR UPDATE`, install.ID).Scan(&pid))
	first := integrationdb.RunAsync(func() (integrationstore.SetConnectorInstallationProviderStateResult, error) {
		return f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
	})
	integrationdb.WaitForLockWaitBlockedBy(t, t.Context(), f.store.pool,
		"-- name: LockConnectorInstallationProviderState ", pid)
	second := integrationdb.RunAsync(func() (integrationstore.SetConnectorInstallationProviderStateResult, error) {
		return f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
	})
	require.NoError(t, blocker.Commit(t.Context()))
	a := integrationdb.Await(t, first, "first provider observation")
	b := integrationdb.Await(t, second, "second provider observation")
	if a.Err == nil {
		require.ErrorIs(t, b.Err, storeerr.ErrStateTransitionConflict)
	} else {
		require.ErrorIs(t, a.Err, storeerr.ErrStateTransitionConflict)
		require.NoError(t, b.Err)
	}
	current, err := f.store.Integrations().GetIntegrationInstall(t.Context(), testProjectID, install.ID)
	require.NoError(t, err)
	require.Equal(t, install.ConfigurationRevision+1, current.ConfigurationRevision)
	require.Equal(t, integrationstore.IntegrationInstallStateActive, current.State)
}

func TestProviderControlRechecksAppAfterWaitingForMutation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sql string
		err       error
	}{
		{"disabled", `UPDATE integration_apps SET state='disabled' WHERE id=$1`, storeerr.ErrNotFound},
		{"deleted", `UPDATE integration_apps SET deleted_at=now() WHERE id=$1`, storeerr.ErrNotFound},
		{"rotated", `UPDATE integration_apps SET provider_metadata='{"rotation":true}' WHERE id=$1`,
			storeerr.ErrStateTransitionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newProviderStateFixture(t)
			install := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateDisabled)
			input := f.stateInput(install, integrationstore.IntegrationInstallStateActive)
			blocker := integrationdb.BeginTx(t, t.Context(), f.store.pool)
			var pid int32
			require.NoError(t, blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid))
			_, err := blocker.Exec(t.Context(), tc.sql, f.app.ID)
			require.NoError(t, err)
			pending := integrationdb.RunAsync(func() (integrationstore.SetConnectorInstallationProviderStateResult, error) {
				return f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
			})
			integrationdb.WaitForLockWaitBlockedBy(t, t.Context(), f.store.pool,
				"-- name: LockIntegrationAppForInstallation ", pid)
			require.NoError(t, blocker.Commit(t.Context()))
			result := integrationdb.Await(t, pending, "provider state after app mutation")
			require.ErrorIs(t, result.Err, tc.err)
			current, err := f.store.Integrations().GetIntegrationInstall(t.Context(), testProjectID, install.ID)
			require.NoError(t, err)
			require.Equal(t, install.State, current.State)
			require.Equal(t, install.ConfigurationRevision, current.ConfigurationRevision)
		})
	}
}

func TestProviderControlNeverRestoresRevokedBindingsOrDisconnectedInstallation(t *testing.T) {
	t.Parallel()
	f := newChannelAuthorityFixture(t, t.Context(), "provider-state-grants")
	grant := f.bindingInput("provider-control")
	grant.SendAllowed = true
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(t.Context(), grant)
	require.NoError(t, err)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(t.Context(), testProjectID, binding.ID))
	install, err := f.Store.Integrations().GetIntegrationInstall(t.Context(), testProjectID, f.InstallID)
	require.NoError(t, err)
	app, err := f.Store.Integrations().GetIntegrationApp(t.Context(), testOrgID, f.AppID)
	require.NoError(t, err)
	input := integrationstore.SetConnectorInstallationProviderStateInput{
		IntegrationAppID: app.ID, IntegrationInstallID: install.ID,
		ProviderTenantID: install.ProviderTenantID, ProviderAccountRef: install.ProviderAccountRef,
		ExpectedAppConfigurationRevision: app.ConfigurationRevision,
		ExpectedConfigurationRevision:    install.ConfigurationRevision,
		Capabilities:                     testChannelCapabilities(testChannelProvider),
	}
	for _, state := range []integrationstore.IntegrationInstallState{
		integrationstore.IntegrationInstallStateDisabled, integrationstore.IntegrationInstallStateActive,
	} {
		input.State = state
		updated, updateErr := f.Store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
		require.NoError(t, updateErr)
		input.ExpectedConfigurationRevision = updated.ConfigurationRevision
	}
	_, err = f.Store.Integrations().GetActiveSendBindingForTarget(t.Context(), testProjectID, f.AgentID, f.Target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.NoError(t, f.Store.Integrations().DeleteIntegrationInstall(t.Context(), testProjectID, install.ID))
	_, err = f.Store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	var deleted, revoked bool
	require.NoError(t, f.Store.pool.QueryRow(t.Context(), `SELECT
  (SELECT deleted_at IS NOT NULL FROM integration_installs WHERE id=$1),
  (SELECT revoked_at IS NOT NULL FROM integration_target_bindings WHERE id=$2)`, install.ID, binding.ID).
		Scan(&deleted, &revoked))
	require.True(t, deleted)
	require.True(t, revoked)
}

func TestProviderControlRejectsRetiredProjectAndOrganization(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"project", "organization"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newProviderStateFixture(t)
			install := f.install(t, testProjectID, "42", "101", integrationstore.IntegrationInstallStateDisabled)
			input := f.stateInput(install, integrationstore.IntegrationInstallStateActive)
			query, id := `UPDATE projects SET deleted_at=now() WHERE id=$1`, testProjectID
			if kind == "organization" {
				query, id = `UPDATE orgs SET deleted_at=now() WHERE id=$1`, testOrgID
			}
			_, err := f.store.pool.Exec(t.Context(), query, id)
			require.NoError(t, err)
			_, err = f.store.Integrations().SetConnectorInstallationProviderState(t.Context(), input)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			page, err := f.store.Integrations().ListConnectorInstallationControlScopes(t.Context(), f.listInput())
			if kind == "organization" {
				require.ErrorIs(t, err, storeerr.ErrNotFound)
			} else {
				require.NoError(t, err)
				require.Empty(t, page.Installations)
			}
		})
	}
}
