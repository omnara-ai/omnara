//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestManagedChannelRegistrationReusesScopedIdentitiesWithoutAgentsOrGrants(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelAuthorityFixture(t, ctx, "managed-registration")
	input, parent := managedChannelRegistrationInputs(f)
	parent.ProjectID, parent.IntegrationInstallID = uuid.New(), uuid.New()
	originalParent := parent
	var agentsBefore int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&agentsBefore))
	register := func() (integrationstore.IntegrationTargetRecord, error) {
		return f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	}
	first, second := integrationdb.RunAsync(register), integrationdb.RunAsync(register)
	created := integrationdb.AwaitSuccess(t, first, "first managed registration")
	concurrent := integrationdb.AwaitSuccess(t, second, "concurrent managed registration")
	require.Equal(t, created.ID, concurrent.ID)
	require.NotEqual(t, created.Created, concurrent.Created, "one canonical child wins concurrent registration")
	require.Equal(t, originalParent, parent, "registration must not modify its caller's descriptor")
	require.NotEqual(t, uuid.Nil, created.ParentChannelID)
	registeredParent, err := f.Store.Integrations().GetIntegrationTarget(ctx, testProjectID, created.ParentChannelID)
	require.NoError(t, err)
	for _, target := range []integrationstore.IntegrationTargetRecord{created, registeredParent} {
		require.Equal(t, testProjectID, target.ProjectID)
		require.Equal(t, f.InstallID, target.IntegrationInstallID)
	}
	require.Equal(t, uuid.Nil, registeredParent.ParentChannelID)
	input.ParentChannelID = registeredParent.ID
	replayed, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, nil)
	require.NoError(t, err)
	require.Equal(t, created.ID, replayed.ID, "an existing parent ID reuses the same immutable relationship")
	require.False(t, replayed.Created)
	_, err = f.Store.Integrations().RegisterExternalChannel(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "external registration remains restricted to external connections")

	var agentsAfter, targets, bindings int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM agents),
       (SELECT count(*) FROM integration_targets WHERE integration_install_id=$1),
       (SELECT count(*) FROM integration_target_bindings WHERE integration_install_id=$1)`, f.InstallID).
		Scan(&agentsAfter, &targets, &bindings))
	require.Equal(t, agentsBefore, agentsAfter)
	require.Equal(t, 3, targets, "fixture root plus one canonical parent and child")
	require.Zero(t, bindings, "neither registration nor parentage grants receive/read/send access")
}

func TestManagedChannelRegistrationInvalidParentOrChildRollsBack(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"invalid_child", "both_parent_forms", "nested_parent", "same_address"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelAuthorityFixture(t, ctx, "managed-invalid-parent")
			input, parent := managedChannelRegistrationInputs(f)
			switch name {
			case "invalid_child":
				input.ProviderRef = " "
			case "both_parent_forms":
				input.ParentChannelID = f.Target.ID
			case "nested_parent":
				parent.ParentChannelID = f.Target.ID
			case "same_address":
				parent.ProviderRef = " \t" + input.ProviderRef + "\n"
			}
			_, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			var targets int
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM integration_targets WHERE integration_install_id=$1`, f.InstallID).Scan(&targets))
			require.Equal(t, 1, targets, "failed registration leaves only the fixture root")
		})
	}
}

func TestManagedChannelRegistrationFailurePreservesExistingParent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelAuthorityFixture(t, ctx, "managed-parent-preserved")
	input, parent := managedChannelRegistrationInputs(f)
	parent.ProviderRef, parent.DisplayName = f.Target.ProviderRef, "Resolved parent name"
	input.ProviderRef = " "
	_, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	stored, err := f.Store.Integrations().GetIntegrationTarget(ctx, testProjectID, f.Target.ID)
	require.NoError(t, err)
	require.Equal(t, f.Target.DisplayName, stored.DisplayName, "child failure also rolls back a parent metadata refresh")

	input.ProviderRef = "child"
	created, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	require.NoError(t, err)
	require.Equal(t, f.Target.ID, created.ParentChannelID)
	parent.ProviderRef = "different-parent"
	_, err = f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	require.ErrorIs(t, err, storeerr.ErrConflict, "registration cannot replace the child's immutable parent")
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, testProjectID, f.InstallID, parent.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "incompatible existing child rolls back a newly registered parent")
}

func TestManagedChannelRegistrationRejectsExternalConnection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := createIntegrationProjectAdmin(t, ctx, store, "managed-rejects-external@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "managed-rejects-external-profile")
	createIntegrationBoundAgent(t, ctx, store, profile, user.ID, "managed-rejects-external-agent")
	install, err := store.Integrations().CreateExternalIntegrationInstall(ctx, externalConnectionInput(user.ID))
	require.NoError(t, err)
	definition, err := store.Integrations().PublishExternalChannelDefinition(ctx, externalDefinitionInput(install.ID))
	require.NoError(t, err)
	target, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "conversation", ProviderRefKind: "thread", DisplayName: "Customer conversation",
	})
	require.NoError(t, err)
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: target.ChannelDefinitionID,
		ProviderRef: "child", ProviderRefKind: "thread", ParentChannelID: target.ID,
	}
	_, err = store.Integrations().RegisterManagedChannel(ctx, input, nil)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = store.Integrations().GetIntegrationTargetByProviderRef(ctx, testProjectID, install.ID, input.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	created, err := store.Integrations().RegisterExternalChannel(ctx, input)
	require.NoError(t, err, "the same resolved external address remains valid through external setup")
	require.Equal(t, target.ID, created.ParentChannelID)
}

func TestManagedChannelRegistrationRejectsForeignDefinitionAndParent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelAuthorityFixture(t, ctx, "managed-scope")
	_, _, _, other := createChannelLifecycleFixture(t, ctx, f.Store, "other-registration")
	definitionID := createChannelTestDefinition(t, ctx, f.Store, other)
	foreignParent, err := f.Store.Integrations().CreateIntegrationTarget(ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, IntegrationInstallID: other.ID, ChannelDefinitionID: definitionID,
			ProviderRef: "parent", ProviderRefKind: "conversation",
		})
	require.NoError(t, err)
	for _, name := range []string{"project", "child_definition", "parent_definition", "parent_id"} {
		input, parent := managedChannelRegistrationInputs(f)
		parentDescriptor := &parent
		switch name {
		case "project":
			input.ProjectID = uuid.New()
		case "child_definition":
			input.ChannelDefinitionID = definitionID
		case "parent_definition":
			parent.ChannelDefinitionID = definitionID
			parent.ProjectID, parent.IntegrationInstallID = other.ProjectID, other.ID
		case "parent_id":
			input.ParentChannelID, parentDescriptor = foreignParent.ID, nil
		}
		_, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, parentDescriptor)
		require.ErrorIs(t, err, storeerr.ErrNotFound, name)
		var targets int
		require.NoError(t, f.Store.pool.QueryRow(ctx,
			`SELECT count(*) FROM integration_targets WHERE integration_install_id=$1`, f.InstallID).Scan(&targets))
		require.Equal(t, 1, targets, "foreign scope cannot leave a provisional parent or child")
	}
}

func TestManagedChannelRegistrationRechecksLifecycleAfterLockWait(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"parent_deleted", "app_disabled", "app_deleted", "install_disabled", "install_deleted"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelAuthorityFixture(t, ctx, "managed-register-lifecycle")
			input, _ := managedChannelRegistrationInputs(f)
			input.ParentChannelID = f.Target.ID
			lockSQL := `SELECT id FROM integration_apps WHERE id=$1 FOR UPDATE`
			updateSQL := `UPDATE integration_apps SET state='disabled' WHERE id=$1`
			id, queryName := f.AppID, "LockIntegrationTargetCreateAuthority"
			expected := storeerr.ErrUnauthorized
			switch name {
			case "parent_deleted":
				lockSQL = `SELECT id FROM integration_targets WHERE id=$1 FOR UPDATE`
				updateSQL = `UPDATE integration_targets SET deleted_at=statement_timestamp() WHERE id=$1`
				id, queryName, expected = f.Target.ID, "LockIntegrationChannelParent", storeerr.ErrNotFound
			case "app_deleted":
				updateSQL = `UPDATE integration_apps SET state='disabled', deleted_at=statement_timestamp() WHERE id=$1`
			case "install_disabled", "install_deleted":
				id = f.InstallID
				lockSQL = `SELECT id FROM integration_installs WHERE id=$1 FOR UPDATE`
				updateSQL = `UPDATE integration_installs SET state='disabled' WHERE id=$1`
				if name == "install_deleted" {
					updateSQL = `UPDATE integration_installs SET state='disabled', deleted_at=statement_timestamp() WHERE id=$1`
				}
			}
			retirement := integrationdb.BeginTx(t, ctx, f.Store.pool)
			var retirementPID int32
			require.NoError(t, retirement.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&retirementPID))
			_, err := retirement.Exec(ctx, lockSQL, id)
			require.NoError(t, err)
			registration := integrationdb.RunAsyncError(func() error {
				_, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, nil)
				return err
			})
			// The provider resolution is already done. Registration must observe
			// retirement committed while it waits for its final authority lock.
			integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "-- name: "+queryName+" ", retirementPID)
			_, err = retirement.Exec(ctx, updateSQL, id)
			require.NoError(t, err)
			require.NoError(t, retirement.Commit(ctx))
			require.ErrorIs(t, integrationdb.Await(t, registration, "registration after retirement"), expected)
			var children int
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM integration_targets WHERE integration_install_id=$1 AND provider_ref=$2`,
				f.InstallID, input.ProviderRef).Scan(&children))
			require.Zero(t, children)
		})
	}
}

func managedChannelRegistrationInputs(f channelAuthorityFixture) (
	integrationstore.CreateIntegrationTargetInput, integrationstore.CreateIntegrationTargetInput,
) {
	parent := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.InstallID, ChannelDefinitionID: f.Definition.ID,
		ProviderRef: "parent", ProviderRefKind: "conversation",
	}
	child := parent
	child.ProviderRef, child.ProviderRefKind = "child", "thread"
	return child, parent
}

func TestManagedChannelRegistrationPreservesExistingUnparentedAddress(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelAuthorityFixture(t, ctx, "managed-existing-unparented")
	input, parent := managedChannelRegistrationInputs(f)
	existing, err := f.Store.Integrations().CreateIntegrationTarget(ctx, input)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, existing.ParentChannelID)
	resolved, err := f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	require.NoError(t, err)
	require.Equal(t, existing.ID, resolved.ID)
	require.Equal(t, uuid.Nil, resolved.ParentChannelID,
		"resolution cannot reparent an existing inbound or migrated address")
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, testProjectID, f.InstallID, parent.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "no unused parent should be created")
	input.ProviderRefKind = "incompatible-kind"
	_, err = f.Store.Integrations().RegisterManagedChannel(ctx, input, &parent)
	require.ErrorIs(t, err, storeerr.ErrConflict, "existing identity must still validate the resolved kind")
}
