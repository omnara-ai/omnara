//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInstallRoutesIdentityAndOAuthReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-profile")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install")

	profileInput := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_PROFILE"),
		profile.ID, admin.ID, credentialID, "A_PROFILE", "T_SHARED",
	)
	profileInput.OAuthFlowID = integrationOAuthFlowID(1)
	profileInstall, err := store.Integrations().UpsertIntegrationInstall(ctx, profileInput)
	if err != nil {
		t.Fatalf("create managed install: %v", err)
	}
	if !profileInstall.Created || profileInstall.IntegrationAppID != profileInput.IntegrationAppID ||
		profileInstall.ProviderAccountRef != "A_PROFILE" ||
		profileInstall.State != integrationstore.IntegrationInstallStateActive ||
		profileInstall.CredentialSecretID != credentialID ||
		profileInstall.LastOAuthFlowID != profileInput.OAuthFlowID {
		t.Fatalf("unexpected managed install: %+v", profileInstall)
	}
	consumed, err := store.Integrations().IntegrationOAuthFlowConsumed(ctx, profileInput.OAuthFlowID)
	if err != nil {
		t.Fatalf("check consumed oauth flow: %v", err)
	}
	if !consumed {
		t.Fatal("oauth flow should be consumed")
	}
	assertJSONRawEqual(t, profileInstall.ProviderConfig, `{}`)
	assertJSONRawEqual(
		t,
		profileInstall.ProviderIdentity,
		`{"bot_user_id":"B_A_PROFILE"}`,
	)

	standaloneInput := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_STANDALONE"),
		NilID, admin.ID, credentialID, "A_STANDALONE", "T_SHARED",
	)
	standaloneInput.IntegrationKind = integrationstore.IntegrationKindManaged
	standaloneInstall, err := store.Integrations().UpsertIntegrationInstall(ctx, standaloneInput)
	if err != nil {
		t.Fatalf("create standalone install: %v", err)
	}
	require.Equal(t, standaloneInput.IntegrationAppID, standaloneInstall.IntegrationAppID)
	require.Equal(t, credentialID, standaloneInstall.CredentialSecretID)
	var routes, agents int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
   (SELECT count(*) FROM integration_routes WHERE integration_install_id = $1),
   (SELECT count(*) FROM agents WHERE project_id = $2)`, standaloneInstall.ID, testProjectID).Scan(&routes, &agents))
	require.Zero(t, routes, "a connection does not require a behavior route")
	require.Zero(t, agents, "connection registration does not launch an agent")
	missingTenant := standaloneInput
	missingTenant.ProviderAccountRef = "A_MISSING_TENANT"
	missingTenant.ProviderTenantID = ""
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, missingTenant); err == nil {
		t.Fatal("slack install without a provider tenant succeeded")
	}
	missingCredential := standaloneInput
	missingCredential.ProviderAccountRef = "A_MISSING_CREDENTIAL"
	missingCredential.CredentialSecretID = NilID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, missingCredential); err == nil {
		t.Fatal("slack install without a credential secret succeeded")
	}

	missingApp := standaloneInput
	missingApp.IntegrationAppID = NilID
	_, err = store.Integrations().UpsertIntegrationInstall(ctx, missingApp)
	require.Error(t, err, "managed connections require a real app")
	otherProfile := createIntegrationTestProfile(t, ctx, store, "different-route-profile")
	repointed := profileInput
	changedRoute := *profileInput.InitialRoute
	changedRoute.AgentProfileID = otherProfile.ID
	repointed.InitialRoute, repointed.OAuthFlowID = &changedRoute, NilID
	_, err = store.Integrations().UpsertIntegrationInstall(ctx, repointed)
	require.ErrorIs(t, err, storeerr.ErrConflict, "setup cannot replace an existing route's profile")
	rotatedCredentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"rotated",
	)
	rotated := profileInput
	rotated.CredentialSecretID = rotatedCredentialID
	rotated.ConnectionMode = "socket"
	rotated.State = integrationstore.IntegrationInstallStateDisabled
	rotated.DisplayName = ""
	rotated.Metadata = json.RawMessage(`{"team_name":"Renamed"}`)
	rotated.OAuthFlowID = integrationOAuthFlowID(2)
	updated, err := store.Integrations().UpsertIntegrationInstall(ctx, rotated)
	if err != nil {
		t.Fatalf("rotate integration install: %v", err)
	}
	if updated.Created || updated.ID != profileInstall.ID || updated.CredentialSecretID != rotatedCredentialID ||
		updated.ConnectionMode != "socket" || updated.State != integrationstore.IntegrationInstallStateDisabled ||
		updated.ProviderAccountRef != profileInstall.ProviderAccountRef ||
		updated.DisplayName != profileInstall.DisplayName ||
		updated.LastOAuthFlowID != rotated.OAuthFlowID {
		t.Fatalf("unexpected rotated install: %+v", updated)
	}
	assertJSONRawEqual(t, updated.Metadata, `{"team_name":"Renamed"}`)
	withoutFlow := rotated
	withoutFlow.DisplayName = "Omnara Prime"
	withoutFlow.OAuthFlowID = NilID
	preserved, err := store.Integrations().UpsertIntegrationInstall(ctx, withoutFlow)
	if err != nil {
		t.Fatalf("update install without oauth flow: %v", err)
	}
	if preserved.LastOAuthFlowID != rotated.OAuthFlowID ||
		preserved.DisplayName != "Omnara Prime" {
		t.Fatalf("unexpected install after non-oauth update: %+v", preserved)
	}

	if _, err := store.Integrations().UpsertIntegrationInstall(
		ctx, rotated,
	); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("same-flow reinstall error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}
	olderFlow := rotated
	olderFlow.OAuthFlowID = profileInput.OAuthFlowID
	if _, err := store.Integrations().UpsertIntegrationInstall(
		ctx, olderFlow,
	); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("older-flow reinstall error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}
	reusedFlow := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_REUSED_FLOW"),
		profile.ID, admin.ID, credentialID, "A_REUSED_FLOW", "T_SHARED",
	)
	reusedFlow.OAuthFlowID = rotated.OAuthFlowID
	if _, err := store.Integrations().UpsertIntegrationInstall(
		ctx, reusedFlow,
	); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("cross-install oauth flow reuse error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}

	badJSON := standaloneInput
	badJSON.ProviderAccountRef = "A_BAD_JSON"
	badJSON.ProviderIdentity = json.RawMessage(`[]`)
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, badJSON); err == nil {
		t.Fatal("install with non-object provider_identity succeeded")
	}

	orgSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "integration-org-secret",
		Material:  secrets.GenericMaterial{Value: "value"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org secret: %v", err)
	}
	badCredential := standaloneInput
	badCredential.ProviderAccountRef = "A_BAD_CREDENTIAL"
	badCredential.CredentialSecretID = orgSecret.ID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, badCredential); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("org-owned credential error = %v, want ErrNotFound", err)
	}
	wrongKindSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "integration-wrong-kind",
		Material:       secrets.GenericMaterial{Value: "value"},
		Actor:          userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create wrong-kind credential: %v", err)
	}
	badCredential.ProviderAccountRef = "A_WRONG_KIND"
	badCredential.CredentialSecretID = wrongKindSecret.ID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, badCredential); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("wrong-kind credential error = %v, want ErrNotFound", err)
	}
}

func TestIntegrationInstallAuthorizationAndSlackIdentityScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-scope-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-scope-profile")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install-scope")

	outsider, err := store.Identity().CreateVerifiedUser(ctx, CreateVerifiedUserInput{
		Email:       "install-scope-outsider@example.com",
		DisplayName: "Integration Outsider",
	})
	if err != nil {
		t.Fatalf("create integration outsider: %v", err)
	}
	unauthorized := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_UNAUTHORIZED"),
		profile.ID, outsider.ID, credentialID, "A_UNAUTHORIZED", "T_SCOPE",
	)
	if _, err := store.Integrations().UpsertIntegrationInstall(
		ctx, unauthorized,
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("unauthorized installer error = %v, want ErrUnauthorized", err)
	}

	identity := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_GLOBAL_IDENTITY"),
		profile.ID, admin.ID, credentialID, "A_GLOBAL_IDENTITY", "T_SCOPE",
	)
	mustCreateIntegrationInstall(t, ctx, store, identity)
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("delete referenced profile error = %v, want ErrConflict", err)
	}
	otherProject, err := store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID:          testOrgID,
		Creator:        userPrincipal(admin.ID),
		Name:           "Integration Identity Other Project",
		IdempotencyKey: "integration-identity-other-project",
	})
	if err != nil {
		t.Fatalf("create other integration project: %v", err)
	}
	otherConfigID := mustCreateAgentConfig(
		t,
		ctx,
		store,
		otherProject.ID,
	)
	otherProfile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       otherProject.ID,
		Name:            "Integration Identity Other Profile",
		CurrentConfigID: otherConfigID,
		IdempotencyKey:  "integration-identity-other-profile",
	})
	if err != nil {
		t.Fatalf("create other integration profile: %v", err)
	}
	otherIdentity := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, otherProject.ID, identity.ProviderAccountRef),
		otherProfile.ID,
		admin.ID,
		createIntegrationCredential(t, ctx, store, otherProject.ID, admin.ID, "other-project"),
		identity.ProviderAccountRef,
		identity.ProviderTenantID,
	)
	otherIdentity.ProjectID = otherProject.ID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, otherIdentity); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("cross-project provider identity error = %v, want ErrConflict", err)
	}

	disabledProfile := createIntegrationTestProfile(t, ctx, store, "disabled-install-profile")
	disabledInstall := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_DISABLED_PROFILE"),
		disabledProfile.ID, admin.ID, credentialID, "A_DISABLED_PROFILE", "T_DISABLED_PROFILE",
	)
	disabledInstall.State = integrationstore.IntegrationInstallStateDisabled
	mustCreateIntegrationInstall(t, ctx, store, disabledInstall)
	if err := store.Execution().DeleteAgentProfile(
		ctx, testProjectID, disabledProfile.ID,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("disabled connection still retains a live configured route: %v", err)
	}
}

func TestIntegrationInstallRechecksProfileAfterLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-profile-lock@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-profile-lock")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"install-profile-lock",
	)
	input := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_PROFILE_LOCK"),
		profile.ID, admin.ID, credentialID, "A_PROFILE_LOCK", "T_PROFILE_LOCK",
	)

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin integration profile blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get integration profile blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`SELECT id FROM agent_profiles WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		profile.ID,
	); err != nil {
		t.Fatalf("lock integration profile: %v", err)
	}
	type installResult struct {
		record integrationstore.IntegrationInstallRecord
		err    error
	}
	done := make(chan installResult, 1)
	go func() {
		record, installErr := store.Integrations().UpsertIntegrationInstall(context.Background(), input)
		done <- installResult{record: record, err: installErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "-- name: LockAgentProfile", blockingPID)
	if _, err := blockingTx.Exec(
		ctx,
		`UPDATE agent_profiles SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
			 WHERE project_id = $1 AND id = $2 AND deleted_at IS NULL`,
		testProjectID,
		profile.ID,
	); err != nil {
		t.Fatalf("delete integration profile: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`UPDATE agent_profile_versions SET deleted_at = statement_timestamp()
			 WHERE project_id = $1 AND profile_id = $2 AND deleted_at IS NULL`,
		testProjectID,
		profile.ID,
	); err != nil {
		t.Fatalf("delete integration profile versions: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("commit integration profile deletion: %v", err)
	}
	result := <-done
	if !errors.Is(result.err, storeerr.ErrNotFound) {
		t.Fatalf("install after profile deletion error = %v, want not found", result.err)
	}
	var installCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM integration_installs
		 WHERE provider = $1 AND provider_tenant_id = $2 AND provider_account_ref = $3`,
		input.Provider,
		input.ProviderTenantID,
		input.ProviderAccountRef,
	).Scan(&installCount); err != nil {
		t.Fatalf("count integration installs after profile deletion: %v", err)
	}
	if installCount != 0 {
		t.Fatalf("integration installs after profile deletion = %d, want 0", installCount)
	}
}

func TestIntegrationInstallDeletionWaitsForTargetCreation(t *testing.T) {
	t.Parallel()
	const label = "target-wins"
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, _, credentialID := createIntegrationAgentFixture(
		t,
		ctx,
		store,
		"install-delete-"+label,
	)
	installInput := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_TARGET_INSTALL_DELETE_"+label),
		NilID, admin.ID, credentialID, "A_TARGET_INSTALL_DELETE_"+label, "T_TARGET_INSTALL_DELETE_"+label,
	)
	installInput.IntegrationKind = integrationstore.IntegrationKindManaged
	install := mustCreateIntegrationInstall(t, ctx, store, installInput)
	definitionID := createSlackIntegrationDefinition(t, ctx, store, install)
	targetInput := integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		ChannelDefinitionID:  definitionID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "C_DELETE:" + label,
		ProviderRefKind:      "thread",
	}

	blockingTx := integrationdb.BeginTx(t, ctx, pool)
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("load integration install blocker backend: %v", err)
	}
	if _, err := dbsqlc.New(blockingTx).LockIntegrationInstallForMutation(
		ctx,
		dbsqlc.LockIntegrationInstallForMutationParams{
			ProjectID: testProjectID,
			ID:        install.ID,
		},
	); err != nil {
		t.Fatalf("lock integration install: %v", err)
	}
	targetDone := integrationdb.RunAsync(func() (integrationstore.IntegrationTargetRecord, error) {
		return store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	})
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationTargetCreateAuthority ",
		blockingPID,
	)
	targetPID := integrationLifecycleWaiterPID(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationTargetCreateAuthority ",
		blockingPID,
	)
	deleteDone := integrationdb.RunAsyncError(func() error {
		return store.Integrations().DeleteIntegrationInstallOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationInstallLifecycleExclusive ",
		targetPID,
	)
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release integration install blocker: %v", err)
	}
	targetOutcome := integrationdb.Await(t, targetDone, "integration target creation")
	if err := integrationdb.Await(t, deleteDone, "integration install deletion"); err != nil {
		t.Fatalf("delete integration install: %v", err)
	}
	if targetOutcome.Err != nil || !targetOutcome.Value.Created {
		t.Fatalf("target creation before deletion = %+v err=%v", targetOutcome.Value, targetOutcome.Err)
	}
	var activeTargets int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM integration_targets
		 WHERE project_id = $1 AND integration_install_id = $2 AND deleted_at IS NULL`,
		testProjectID,
		install.ID,
	).Scan(&activeTargets); err != nil {
		t.Fatalf("count active targets after install deletion: %v", err)
	}
	if activeTargets != 0 {
		t.Fatalf("active targets after install deletion = %d, want 0", activeTargets)
	}
}

func TestIntegrationInstallDeletionFreezesTargetAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-growth@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-growth")
	install, definitionID := createExternalIntegrationTestConnection(t, ctx, store, admin.ID)

	first, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, ChannelDefinitionID: definitionID,
		IntegrationInstallID: install.ID, ProviderRef: "C_FIRST", ProviderRefKind: "thread",
	})
	if err != nil {
		t.Fatalf("create first target: %v", err)
	}
	firstAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "install-growth-first")
	firstBinding := bindIntegrationTestTarget(t, ctx, store, firstAgent.ID, first)
	mustCreateExternalChannelInput(t, ctx, store, firstBinding, "U_GROWTH", "Ev-first", "select first target")
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, firstAgent.ID, claim.AgentID)
	selected, err := store.Execution().GetAgentCurrentChannelID(ctx, testProjectID, firstAgent.ID)
	require.NoError(t, err)
	require.Equal(t, first.ID, selected)
	secondAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "install-growth-second")

	controlTx := integrationdb.BeginTx(t, ctx, pool)
	if _, err := dbsqlc.New(controlTx).LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: firstAgent.ID,
	}); err != nil {
		t.Fatalf("block existing target agent: %v", err)
	}
	deleteDone := integrationdb.RunAsyncError(func() error {
		return store.Integrations().DeleteIntegrationInstallOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	targetDone := integrationdb.RunAsync(func() (integrationstore.IntegrationTargetRecord, error) {
		return store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID, IntegrationInstallID: install.ID,
			ProviderRef: "C_SECOND", ProviderRefKind: "thread",
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationInstallLifecycleShared", 1)

	// Deleting one install must not block registration or input admission on
	// another install. Targets have no implicit agent owner.
	otherInstall, otherDefinitionID := createExternalIntegrationTestConnection(t, ctx, store, admin.ID)
	otherCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	otherTarget, err := store.Integrations().CreateIntegrationTarget(
		otherCtx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: otherDefinitionID, IntegrationInstallID: otherInstall.ID,
			ProviderRef: "C_OTHER", ProviderRefKind: "thread",
		},
	)
	if err != nil {
		t.Fatalf("create unrelated install target during deletion: %v", err)
	}
	otherBinding := bindIntegrationTestTarget(t, otherCtx, store, secondAgent.ID, otherTarget)
	mustCreateExternalChannelInput(t, otherCtx, store, otherBinding, "U_GROWTH", "Ev-other", "other install")
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release existing target agent: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "install deletion"); err != nil {
		t.Fatalf("delete install in one transaction attempt: %v", err)
	}
	if outcome := integrationdb.Await(t, targetDone, "late target creation"); !storeerr.IsNotFound(outcome.Err) {
		t.Fatalf("late target creation error = %v, want not found", outcome.Err)
	}
	var activeTargets, selectedTargets int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM integration_targets WHERE integration_install_id = $1 AND deleted_at IS NULL),
		(SELECT count(*) FROM agents WHERE integration_target_id = $2)`,
		install.ID, first.ID,
	).Scan(&activeTargets, &selectedTargets); err != nil {
		t.Fatalf("read deleted install state: %v", err)
	}
	if activeTargets != 0 || selectedTargets != 0 {
		t.Fatalf("deleted install retains targets=%d selections=%d", activeTargets, selectedTargets)
	}
	if _, err := store.Integrations().GetIntegrationTarget(ctx, testProjectID, otherTarget.ID); err != nil {
		t.Fatalf("unrelated install target after deletion: %v", err)
	}
}

func TestIntegrationTargetRetriesGeneratedReferenceCollisionInTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, _, credentialID := createIntegrationAgentFixture(
		t,
		ctx,
		store,
		"target-ref-collision",
	)
	installInput := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_TARGET_REF_COLLISION"),
		NilID, admin.ID, credentialID, "A_TARGET_REF_COLLISION", "T_TARGET_REF_COLLISION",
	)
	installInput.IntegrationKind = integrationstore.IntegrationKindManaged
	install := mustCreateIntegrationInstall(t, ctx, store, installInput)
	definitionID := createSlackIntegrationDefinition(t, ctx, store, install)
	integrationStore := store.Integrations()
	integrationStore.IntegrationSetTargetRefGenerator(func(string) (string, error) {
		return "slack-fixed", nil
	})
	if _, err := integrationStore.CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            testProjectID,
			ChannelDefinitionID:  definitionID,
			IntegrationInstallID: install.ID,
			ProviderRef:          "C_COLLISION:first",
			ProviderRefKind:      "thread",
		},
	); err != nil {
		t.Fatalf("create collision fixture target: %v", err)
	}

	references := []string{"slack-fixed", "slack-free"}
	generated := 0
	integrationStore.IntegrationSetTargetRefGenerator(func(string) (string, error) {
		ref := references[generated]
		generated++
		return ref, nil
	})
	created, err := integrationStore.CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            testProjectID,
			ChannelDefinitionID:  definitionID,
			IntegrationInstallID: install.ID,
			ProviderRef:          "C_COLLISION:second",
			ProviderRefKind:      "thread",
		},
	)
	if err != nil {
		t.Fatalf("create target after generated reference collision: %v", err)
	}
	if !created.Created || created.TargetRef != "slack-free" || generated != 2 {
		t.Fatalf("target after reference collision = %+v, generated=%d", created, generated)
	}
}

func TestIntegrationInstallDeletionSerializesWithScopeDeletion(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"project", "organization"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			admin, agent, credentialID := createIntegrationAgentFixture(
				t,
				ctx,
				store,
				"install-scope-delete-"+scope,
			)
			installInput := slackIntegrationInstallInput(
				createSlackIntegrationApp(t, ctx, store, testProjectID, "A_INSTALL_SCOPE_DELETE_"+scope),
				NilID, admin.ID, credentialID, "A_INSTALL_SCOPE_DELETE_"+scope, "T_INSTALL_SCOPE_DELETE_"+scope,
			)
			installInput.IntegrationKind = integrationstore.IntegrationKindManaged
			install := mustCreateIntegrationInstall(t, ctx, store, installInput)
			definitionID := createSlackIntegrationDefinition(t, ctx, store, install)
			target, err := store.Integrations().CreateIntegrationTarget(
				ctx,
				integrationstore.CreateIntegrationTargetInput{
					ProjectID:            testProjectID,
					ChannelDefinitionID:  definitionID,
					IntegrationInstallID: install.ID,
					ProviderRef:          "C_SCOPE_DELETE:" + scope,
					ProviderRefKind:      "thread",
				},
			)
			if err != nil {
				t.Fatalf("create unbound integration target: %v", err)
			}
			var boundTargetID *ID
			if err := pool.QueryRow(
				ctx,
				`SELECT integration_target_id FROM agents WHERE project_id = $1 AND id = $2`,
				testProjectID,
				agent.ID,
			).Scan(&boundTargetID); err != nil {
				t.Fatalf("load integration agent binding: %v", err)
			}
			if boundTargetID != nil {
				t.Fatalf("integration target fixture is bound: %s", *boundTargetID)
			}

			actor, err := executionstore.OmnaraActorParams(testOrgID, userPrincipal(admin.ID))
			if err != nil {
				t.Fatalf("build scope deletion actor: %v", err)
			}
			controlTx := integrationdb.BeginTx(t, ctx, pool)
			if _, err := dbsqlc.New(controlTx).LockIntegrationInstallForMutation(
				ctx,
				dbsqlc.LockIntegrationInstallForMutationParams{
					ProjectID: testProjectID,
					ID:        install.ID,
				},
			); err != nil {
				t.Fatalf("lock integration install for scope contention: %v", err)
			}

			installDeleteDone := integrationdb.RunAsyncError(func() error {
				return store.Integrations().DeleteIntegrationInstallOnceForIntegration(
					context.Background(),
					testProjectID,
					install.ID,
				)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationInstallForMutation", 1)

			scopeDeleteDone := integrationdb.RunAsyncError(func() error {
				if scope == "project" {
					_, deleteErr := store.Organizations().DeleteProjectOnceForIntegration(
						context.Background(),
						testOrgID,
						testProjectID,
						actor,
					)
					return deleteErr
				}
				_, deleteErr := store.Organizations().DeleteOrganizationOnceForIntegration(
					context.Background(),
					testOrgID,
					actor,
				)
				return deleteErr
			})
			gateQuery := "LockProjectLifecycleExclusive"
			if scope == "organization" {
				gateQuery = "LockOrganizationLifecycleExclusive"
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, gateQuery, 1)
			if err := controlTx.Commit(ctx); err != nil {
				t.Fatalf("release integration install control transaction: %v", err)
			}

			if err := integrationdb.Await(t, installDeleteDone, "integration install deletion"); err != nil {
				t.Fatalf("delete integration install before %s deletion: %v", scope, err)
			}
			if err := integrationdb.Await(t, scopeDeleteDone, scope+" deletion"); err != nil {
				t.Fatalf("delete %s after integration install: %v", scope, err)
			}

			var activeInstallCount, activeTargetCount int
			if err := pool.QueryRow(
				ctx,
				`SELECT
				   (SELECT count(*)::integer FROM integration_installs
				    WHERE project_id = $1 AND id = $2 AND deleted_at IS NULL),
				   (SELECT count(*)::integer FROM integration_targets
				    WHERE project_id = $1 AND id = $3 AND deleted_at IS NULL)`,
				testProjectID,
				install.ID,
				target.ID,
			).Scan(&activeInstallCount, &activeTargetCount); err != nil {
				t.Fatalf("count active integration state after %s deletion: %v", scope, err)
			}
			if activeInstallCount != 0 || activeTargetCount != 0 {
				t.Fatalf(
					"active integration state after %s deletion: installs=%d targets=%d",
					scope,
					activeInstallCount,
					activeTargetCount,
				)
			}
		})
	}
}

func TestDisableIntegrationInstallRequiresCurrentOAuthGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "disable-generation@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "disable-generation")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"disable-generation",
	)
	input := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_DISABLE_GENERATION"),
		profile.ID, admin.ID, credentialID, "A_DISABLE_GENERATION", "T_DISABLE_GENERATION",
	)
	input.OAuthFlowID = integrationOAuthFlowID(18)
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
	if err != nil {
		t.Fatalf("create integration install: %v", err)
	}
	staleOAuthFlowID := install.LastOAuthFlowID

	input.OAuthFlowID = integrationOAuthFlowID(19)
	install, err = store.Integrations().UpsertIntegrationInstall(ctx, input)
	if err != nil {
		t.Fatalf("reauthorize integration install: %v", err)
	}
	applied, err := store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
		ProjectID:           install.ProjectID,
		ID:                  install.ID,
		ExpectedOAuthFlowID: &staleOAuthFlowID,
	})
	if err != nil {
		t.Fatalf("disable with stale OAuth generation: %v", err)
	}
	if applied {
		t.Fatal("stale OAuth generation disabled reauthorized integration install")
	}
	current, err := store.Integrations().GetIntegrationInstall(ctx, install.ProjectID, install.ID)
	if err != nil {
		t.Fatalf("load reauthorized integration install: %v", err)
	}
	if current.State != integrationstore.IntegrationInstallStateActive {
		t.Fatalf("reauthorized integration install state = %q, want active", current.State)
	}

	applied, err = store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
		ProjectID:           install.ProjectID,
		ID:                  install.ID,
		ExpectedOAuthFlowID: &install.LastOAuthFlowID,
	})
	if err != nil {
		t.Fatalf("disable with current OAuth generation: %v", err)
	}
	if !applied {
		t.Fatal("current OAuth generation did not disable integration install")
	}
}

func TestIntegrationInstallUpdateUsesPostLockDatabaseTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-update-lock@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-update-lock")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"install-update-lock",
	)
	input := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_UPDATE_LOCK"),
		profile.ID, admin.ID, credentialID, "A_UPDATE_LOCK", "T_UPDATE_LOCK",
	)
	input.OAuthFlowID = integrationOAuthFlowID(20)
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
	if err != nil {
		t.Fatalf("create integration install: %v", err)
	}

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin integration install blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get integration install blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`SELECT id FROM integration_installs WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		install.ProjectID,
		install.ID,
	); err != nil {
		t.Fatalf("lock integration install: %v", err)
	}

	input.OAuthFlowID = integrationOAuthFlowID(21)
	input.Metadata = json.RawMessage(`{"version":"updated"}`)
	type updateResult struct {
		record integrationstore.IntegrationInstallRecord
		err    error
	}
	done := make(chan updateResult, 1)
	go func() {
		record, updateErr := store.Integrations().UpsertIntegrationInstall(context.Background(), input)
		done <- updateResult{record: record, err: updateErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationInstallByAppProviderAccount",
		blockingPID,
	)
	var releaseFloor time.Time
	if err := blockingTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
		t.Fatalf("read integration install release time: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release integration install: %v", err)
	}

	result := <-done
	if result.err != nil {
		t.Fatalf("update integration install: %v", result.err)
	}
	if result.record.UpdatedAt.Before(releaseFloor) {
		t.Fatalf(
			"integration install updated_at = %s, want at or after lock release %s",
			result.record.UpdatedAt,
			releaseFloor,
		)
	}
	if result.record.LastOAuthFlowID != input.OAuthFlowID {
		t.Fatalf(
			"integration install OAuth flow = %s, want %s",
			result.record.LastOAuthFlowID,
			input.OAuthFlowID,
		)
	}
}

func TestIntegrationTargetProviderRefReusableAfterTargetDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "target-reuse-admin@example.com")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "target-reuse")
	input := slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_TARGET_REUSE"),
		NilID, admin.ID, credentialID, "A_TARGET_REUSE", "T_TARGET_REUSE",
	)
	input.IntegrationKind = integrationstore.IntegrationKindManaged
	install := mustCreateIntegrationInstall(t, ctx, store, input)
	definitionID := createSlackIntegrationDefinition(t, ctx, store, install)

	first, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		ChannelDefinitionID:  definitionID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "C900:reuse",
		ProviderRefKind:      "channel",
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_targets
		 SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
		 WHERE project_id = $1 AND id = $2`,
		testProjectID,
		first.ID,
	); err != nil {
		t.Fatalf("soft-delete target: %v", err)
	}

	recreated, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		ChannelDefinitionID:  definitionID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "C900:reuse",
		ProviderRefKind:      "channel",
	})
	if err != nil {
		t.Fatalf("recreate target after deletion: %v", err)
	}
	if !recreated.Created || recreated.ID == first.ID {
		t.Fatalf("expected a fresh target for the freed provider ref, got %+v (first %s)", recreated, first.ID)
	}
}

func TestIntegrationTargetSelectionValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "target-selection-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "target-selection-profile")
	firstAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "target-selection-first")
	secondAgent := createIntegrationBoundAgent(
		t,
		ctx,
		store,
		profile,
		admin.ID,
		"target-selection-second",
	)
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "target-selection")
	install := mustCreateIntegrationInstall(t, ctx, store, slackIntegrationInstallInput(
		createSlackIntegrationApp(t, ctx, store, testProjectID, "A_TARGET_SELECTION"),
		profile.ID, admin.ID, credentialID, "A_TARGET_SELECTION", "T_TARGET_SELECTION",
	))
	definitionID := createSlackIntegrationDefinition(t, ctx, store, install)
	first, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		ChannelDefinitionID:  definitionID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "C_SELECTION:111.222",
		ProviderRefKind:      "thread",
		DisplayName:          "general",
	})
	if err != nil {
		t.Fatalf("create first selection target: %v", err)
	}
	second, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		ChannelDefinitionID:  definitionID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "C_SELECTION:333.444",
		ProviderRefKind:      "thread",
	})
	if err != nil {
		t.Fatalf("create second selection target: %v", err)
	}
	bindIntegrationTestTarget(t, ctx, store, firstAgent.ID, first)
	bindIntegrationTestTarget(t, ctx, store, firstAgent.ID, second)
	loaded, err := store.Integrations().GetIntegrationTarget(ctx, testProjectID, first.ID)
	if err != nil {
		t.Fatalf("get selection target: %v", err)
	}
	if loaded.ID != first.ID || loaded.DisplayName != "general" || loaded.ProviderMetadata == nil {
		t.Fatalf("unexpected loaded selection target: %+v", loaded)
	}
	if _, err := executionstore.IntegrationSetAgentIntegrationTarget(
		ctx,
		store.q,
		testProjectID,
		firstAgent.ID,
		first.ID,
	); err != nil {
		t.Fatalf("set first selection target: %v", err)
	}
	targets, err := store.Integrations().ListIntegrationTargets(ctx, testProjectID, firstAgent.ID)
	if err != nil {
		t.Fatalf("list selection targets: %v", err)
	}
	if len(targets) != 2 || targets[0].ID != first.ID || !targets[0].IsCurrent ||
		targets[0].Provider != integrationstore.IntegrationProviderSlack ||
		targets[0].InstallState != integrationstore.IntegrationInstallStateActive ||
		targets[0].DisplayName != "general" || targets[1].ID != second.ID || targets[1].IsCurrent {
		t.Fatalf("unexpected selection targets: %+v", targets)
	}
	if _, err := executionstore.IntegrationSetAgentIntegrationTarget(
		ctx,
		store.q,
		testProjectID,
		firstAgent.ID,
		NilID,
	); err != nil {
		t.Fatalf("clear selection target: %v", err)
	}
	targets, err = store.Integrations().ListIntegrationTargets(ctx, testProjectID, firstAgent.ID)
	if err != nil {
		t.Fatalf("list cleared selection targets: %v", err)
	}
	if len(targets) != 2 || targets[0].IsCurrent || targets[1].IsCurrent {
		t.Fatalf("cleared selection targets still current: %+v", targets)
	}
	if _, err := executionstore.IntegrationSetAgentIntegrationTarget(
		ctx,
		store.q,
		testProjectID,
		secondAgent.ID,
		first.ID,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("cross-agent selection target error = %v, want ErrConflict", err)
	}
	if _, err := executionstore.IntegrationSetAgentIntegrationTarget(
		ctx,
		store.q,
		testProjectID,
		testID("missing_selection_target_agent"),
		first.ID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing selection agent error = %v, want ErrNotFound", err)
	}
	replayed, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, ChannelDefinitionID: definitionID, IntegrationInstallID: install.ID,
		ProviderRef: first.ProviderRef, ProviderRefKind: first.ProviderRefKind,
	})
	require.NoError(t, err)
	require.Equal(t, first.ID, replayed.ID, "registration preserves the canonical project-owned channel")
	bindIntegrationTestTarget(t, ctx, store, secondAgent.ID, replayed)
	shared, err := store.Integrations().ListIntegrationTargets(ctx, testProjectID, secondAgent.ID)
	require.NoError(t, err)
	require.Len(t, shared, 1, "an explicit second binding grants only that destination")
	require.Equal(t, first.ID, shared[0].ID)
	if _, err := executionstore.IntegrationSetAgentIntegrationTarget(
		ctx,
		store.q,
		testProjectID,
		firstAgent.ID,
		first.ID,
	); err != nil {
		t.Fatalf("restore selection target: %v", err)
	}
	if _, err := store.Integrations().DisableIntegrationInstall(
		ctx,
		integrationstore.DisableIntegrationInstallInput{
			ProjectID:           testProjectID,
			ID:                  install.ID,
			ExpectedOAuthFlowID: &install.LastOAuthFlowID,
		},
	); err != nil {
		t.Fatalf("disable selection install: %v", err)
	}
	preserved, err := store.Execution().GetAgentInProject(ctx, testProjectID, firstAgent.ID)
	if err != nil {
		t.Fatalf("get agent after selection install disable: %v", err)
	}
	if preserved.IntegrationTargetID != first.ID {
		t.Fatalf("target after install disable = %s, want %s", preserved.IntegrationTargetID, first.ID)
	}
	if _, err := executionstore.IntegrationSetAgentIntegrationTarget(
		ctx,
		store.q,
		testProjectID,
		firstAgent.ID,
		first.ID,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("disabled selection target error = %v, want ErrConflict", err)
	}
}

func TestSlackActorIdentityAcrossInstallsAndConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)

	admin := createIntegrationProjectAdmin(t, ctx, store, "identity-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "identity-profile")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "identity")
	if _, err := executionstore.IntegrationUpsertActorIdentityTx(ctx, store.q, executionstore.UpsertActorIdentityInput{
		ProjectID:      testProjectID,
		Provider:       integrationstore.IntegrationProviderSlack,
		ProviderUserID: "U_MISSING_TENANT",
	}); err == nil {
		t.Fatal("slack actor without a provider tenant succeeded")
	}

	testCases := []struct {
		providerAccountRef string
		providerTenantID   string
		targetRef          string
	}{
		{providerAccountRef: "A_IDENTITY_ONE", providerTenantID: "T_SHARED", targetRef: "D_IDENTITY_ONE"},
		{providerAccountRef: "A_IDENTITY_TWO", providerTenantID: "T_SHARED", targetRef: "D_IDENTITY_TWO"},
		{providerAccountRef: "A_IDENTITY_OTHER", providerTenantID: "T_OTHER", targetRef: "D_IDENTITY_OTHER"},
	}
	producerIDs := make([]ID, 0, len(testCases))
	for index, testCase := range testCases {
		install := mustCreateIntegrationInstall(t, ctx, store, slackIntegrationInstallInput(
			createSlackIntegrationApp(t, ctx, store, testProjectID, testCase.providerAccountRef),
			profile.ID, admin.ID, credentialID, testCase.providerAccountRef, testCase.providerTenantID,
		))
		definitionID := createSlackIntegrationDefinition(t, ctx, store, install)
		routes, err := store.Integrations().ListActiveIntegrationRoutes(ctx, testProjectID, install.ID)
		require.NoError(t, err)
		require.Len(t, routes, 1)
		capabilities := testChannelCapabilities(integrationstore.IntegrationProviderSlack)
		eventID := fmt.Sprintf("Ev-identity-%d", index)
		receipt, err := store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID, EventID: eventID,
			Payload: json.RawMessage(`{"text":"hello"}`), Capabilities: capabilities,
		})
		require.NoError(t, err)
		lease, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx,
			integrationstore.ClaimNextIntegrationEventInput{Capability: capabilities[0], LeaseDuration: time.Minute})
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, receipt.ID, lease.ID)
		prepared, err := store.Execution().PrepareChannelWorkflow(ctx, executionstore.ChannelWorkflowIdentity{
			ProjectID: testProjectID, IntegrationInstallID: install.ID, IntegrationRouteID: routes[0].ID,
			InstanceKey: testCase.targetRef, Capabilities: capabilities,
		})
		require.NoError(t, err)
		accepted, err := store.Execution().DeliverChannelWorkflow(ctx, executionstore.DeliverChannelWorkflowInput{
			Prepared: prepared, InputKey: eventID,
			Receipt: executionstore.ChannelEventLease{
				ReceiptID: lease.ID, LeaseToken: lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration,
			},
			Target: integrationstore.CreateIntegrationTargetInput{
				ChannelDefinitionID: definitionID, ProviderRef: testCase.targetRef, ProviderRefKind: "thread",
			},
			SendAllowed: true, ProviderUserID: "U_SHARED",
			Content: executionstore.PreparedInputContent{Blocks: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
		})
		require.NoError(t, err)
		input := accepted.AgentInput
		var provider, tenant, user string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT provider, provider_tenant_id, provider_user_id FROM actors WHERE project_id = $1 AND id = $2`,
			testProjectID, input.ActorID).Scan(&provider, &tenant, &user))
		require.Equal(t, integrationstore.IntegrationProviderSlack, provider)
		require.Equal(t, install.ProviderTenantID, tenant, "workflow derives tenant from the authorized installation")
		require.Equal(t, "U_SHARED", user)
		producerIDs = append(producerIDs, input.ActorID)
	}
	if producerIDs[0] != producerIDs[1] {
		t.Fatalf("same Slack identity diverged across installs: %s and %s", producerIDs[0], producerIDs[1])
	}
	if producerIDs[0] == producerIDs[2] {
		t.Fatal("same textual Slack user id collided across workspaces")
	}

	const concurrentCalls = 8
	ids := make(chan ID, concurrentCalls)
	errs := make(chan error, concurrentCalls)
	var wg sync.WaitGroup
	for range concurrentCalls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			actor, err := executionstore.IntegrationUpsertActorIdentityTx(ctx, store.q, executionstore.UpsertActorIdentityInput{
				ProjectID:        testProjectID,
				Provider:         integrationstore.IntegrationProviderSlack,
				ProviderTenantID: "T_CONCURRENT",
				ProviderUserID:   "U_CONCURRENT",
				DisplayName:      "Concurrent User",
			})
			if err != nil {
				errs <- err
				return
			}
			ids <- actor.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent actor upsert: %v", err)
	}
	var concurrentID ID
	for id := range ids {
		if concurrentID == NilID {
			concurrentID = id
		}
		if id != concurrentID {
			t.Fatalf("concurrent actor ids diverged: %s and %s", concurrentID, id)
		}
	}
	var concurrentRows int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM actors WHERE project_id = $1 AND provider = 'slack'
         AND provider_tenant_id = 'T_CONCURRENT' AND provider_user_id = 'U_CONCURRENT'`,
		testProjectID,
	).Scan(&concurrentRows); err != nil {
		t.Fatalf("count concurrent actors: %v", err)
	}
	if concurrentRows != 1 {
		t.Fatalf("concurrent actor rows = %d, want 1", concurrentRows)
	}

	settled, err := executionstore.IntegrationUpsertActorIdentityTx(ctx, store.q, executionstore.UpsertActorIdentityInput{
		ProjectID:        testProjectID,
		Provider:         integrationstore.IntegrationProviderSlack,
		ProviderTenantID: "T_CONCURRENT",
		ProviderUserID:   "U_CONCURRENT",
		DisplayName:      "Concurrent User",
	})
	if err != nil {
		t.Fatalf("repeat identical actor upsert: %v", err)
	}
	repeated, err := executionstore.IntegrationUpsertActorIdentityTx(ctx, store.q, executionstore.UpsertActorIdentityInput{
		ProjectID:        testProjectID,
		Provider:         integrationstore.IntegrationProviderSlack,
		ProviderTenantID: "T_CONCURRENT",
		ProviderUserID:   "U_CONCURRENT",
		DisplayName:      "Concurrent User",
	})
	if err != nil {
		t.Fatalf("repeat identical actor upsert: %v", err)
	}
	if repeated.ID != settled.ID || !repeated.UpdatedAt.Equal(settled.UpdatedAt) {
		t.Fatalf(
			"unchanged actor upsert should not rewrite the row, got updated_at %v want %v",
			repeated.UpdatedAt,
			settled.UpdatedAt,
		)
	}
}

func TestIntegrationInputDedupeTargetProgressionAndDisable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)

	admin := createIntegrationProjectAdmin(t, ctx, store, "input-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "input-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "input-agent")
	install, definitionID := createExternalIntegrationTestConnection(t, ctx, store, admin.ID)
	firstTarget, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, ChannelDefinitionID: definitionID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "D_FIRST",
		ProviderRefKind:      "dm",
	})
	if err != nil {
		t.Fatalf("create first input target: %v", err)
	}
	secondTarget, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, ChannelDefinitionID: definitionID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "D_SECOND",
		ProviderRefKind:      "dm",
	})
	if err != nil {
		t.Fatalf("create second input target: %v", err)
	}
	firstBinding := bindIntegrationTestTarget(t, ctx, store, agent.ID, firstTarget)
	secondBinding := bindIntegrationTestTarget(t, ctx, store, agent.ID, secondTarget)

	firstInput := mustCreateExternalChannelInput(
		t,
		ctx,
		store,
		firstBinding,
		"U_SHARED",
		"Ev-first",
		"first",
	)
	claim, found, err := store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim integration input: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || len(claim.Model.AdmittedInputTurn.Inputs) != 1 {
		t.Fatalf(
			"claim integration input found=%v executable=%v inputs=%+v",
			found,
			claim.Kind == executionstore.AgentWorkModel,
			claim.Model.AdmittedInputTurn.Inputs,
		)
	}
	admittedInput := claim.Model.AdmittedInputTurn.Inputs[0]
	if admittedInput.ID != firstInput.ID || admittedInput.IntegrationTargetID != firstTarget.ID {
		t.Fatalf(
			"admitted input = %+v, want id %s with integration target %s",
			admittedInput,
			firstInput.ID,
			firstTarget.ID,
		)
	}
	secondInput := mustCreateExternalChannelInput(
		t,
		ctx,
		store,
		secondBinding,
		"U_SHARED",
		"Ev-second",
		"second",
	)
	if secondInput.ID == firstInput.ID {
		t.Fatalf("distinct provider events produced one input %s", firstInput.ID)
	}
	secondAgent, err := store.Execution().GetAgentInProject(ctx, testProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load second target agent: %v", err)
	}
	if secondAgent.IntegrationTargetID != firstTarget.ID {
		t.Fatalf("queued input changed current target = %s, want %s", secondAgent.IntegrationTargetID, firstTarget.ID)
	}
	secondAdmission, found := admitNextAgentInputAndOpenTurnForTest(
		t, ctx, store, testProjectID, agent.ID, claim.RuntimeLock.ID,
	)
	if !found || len(secondAdmission.Inputs) != 1 || secondAdmission.Inputs[0].ID != secondInput.ID {
		t.Fatalf("second admission found=%t inputs=%+v", found, secondAdmission.Inputs)
	}
	secondAgent, err = store.Execution().GetAgentInProject(ctx, testProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load agent after second admission: %v", err)
	}
	if secondAgent.IntegrationTargetID != secondTarget.ID {
		t.Fatalf("admitted input current target = %s, want %s", secondAgent.IntegrationTargetID, secondTarget.ID)
	}
	replayed := mustCreateExternalChannelInput(
		t,
		ctx,
		store,
		firstBinding,
		"U_SHARED",
		"Ev-first",
		"first",
	)
	if replayed.ID != firstInput.ID {
		t.Fatalf("replayed input id = %s, want %s", replayed.ID, firstInput.ID)
	}
	afterReplay, err := store.Execution().GetAgentInProject(ctx, testProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load agent after input replay: %v", err)
	}
	if afterReplay.IntegrationTargetID != secondTarget.ID {
		t.Fatalf("target after replay = %s, want preserved %s", afterReplay.IntegrationTargetID, secondTarget.ID)
	}

	if _, err := store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
		ProjectID:           install.ProjectID,
		ID:                  install.ID,
		ExpectedOAuthFlowID: &install.LastOAuthFlowID,
	}); err != nil {
		t.Fatalf("disable input install: %v", err)
	}
	disabledReplay := mustCreateExternalChannelInput(
		t,
		ctx,
		store,
		firstBinding,
		"U_SHARED",
		"Ev-first",
		"first",
	)
	if disabledReplay.ID != firstInput.ID {
		t.Fatalf("disabled replay id = %s, want %s", disabledReplay.ID, firstInput.ID)
	}
	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: agent.ID, ChannelID: firstTarget.ID,
		Actor: &executionstore.ActorParams{
			Provider: executionstore.ActorProviderExternal, ProviderUserID: "U_SHARED",
		},
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"new"}]`), IdempotencyKey: "Ev-disabled-new",
	})
	if !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("new input on disabled install error = %v, want ErrNotFound", err)
	}
}

func TestIntegrationInputAdmissionSerializesWithInstallDisableAndDeletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		deleteInstall bool
		inputWins     bool
		wantErr       error
	}{
		{"disable wins", false, false, storeerr.ErrNotFound},
		{"deletion wins", true, false, storeerr.ErrNotFound},
		{"input wins", true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)

			admin := createIntegrationProjectAdmin(t, ctx, store, "input-disable-race@example.com")
			profile := createIntegrationTestProfile(t, ctx, store, "input-disable-race-profile")
			agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "input-disable-race-agent")
			install, definitionID := createExternalIntegrationTestConnection(t, ctx, store, admin.ID)
			target, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
				ProjectID: testProjectID, ChannelDefinitionID: definitionID,
				IntegrationInstallID: install.ID,
				ProviderRef:          "D_INPUT_DISABLE_RACE",
				ProviderRefKind:      "dm",
			})
			if err != nil {
				t.Fatalf("create integration target: %v", err)
			}
			binding := bindIntegrationTestTarget(t, ctx, store, agent.ID, target)
			mustCreateExternalChannelInput(
				t,
				ctx,
				store,
				binding,
				"U_INPUT_DISABLE_RACE",
				"Ev-input-disable-seed",
				"seed",
			)
			// Queueing does not select an origin. Set a real current channel so
			// deletion must clear it while disable preserves it.
			if _, err := executionstore.IntegrationSetAgentIntegrationTarget(
				ctx, store.q, testProjectID, agent.ID, target.ID,
			); err != nil {
				t.Fatalf("select current channel before lifecycle race: %v", err)
			}

			controlTx := integrationdb.BeginTx(t, ctx, pool)
			if _, err := dbsqlc.New(controlTx).LockAgentInProject(
				ctx,
				dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agent.ID},
			); err != nil {
				t.Fatalf("lock integration target agent: %v", err)
			}

			idempotencyKey := "Ev-input-install-race"
			createInput := func() (executionstore.AgentInputRecord, error) {
				record, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
					ProjectID: testProjectID, AgentID: agent.ID, ChannelID: target.ID,
					Actor: &executionstore.ActorParams{
						Provider: executionstore.ActorProviderExternal, ProviderUserID: "U_INPUT_DISABLE_RACE",
					},
					ContentBlocks: json.RawMessage(`[{"type":"text","text":"late"}]`), IdempotencyKey: idempotencyKey,
				})
				return record, err
			}
			changeInstall := func() error {
				if tc.deleteInstall {
					return store.Integrations().DeleteIntegrationInstallOnceForIntegration(ctx, testProjectID, install.ID)
				}
				applied, err := store.Integrations().DisableIntegrationInstall(
					ctx,
					integrationstore.DisableIntegrationInstallInput{
						ProjectID:           install.ProjectID,
						ID:                  install.ID,
						ExpectedOAuthFlowID: &install.LastOAuthFlowID,
					},
				)
				if err == nil && !applied {
					return errors.New("integration install disable was not applied")
				}
				return err
			}
			var inputDone <-chan integrationdb.AsyncResult[executionstore.AgentInputRecord]
			var changeDone <-chan error
			if !tc.deleteInstall {
				inputDone = integrationdb.RunAsync(createInput)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				if err := changeInstall(); err != nil {
					t.Fatalf("disable install: %v", err)
				}
			} else {
				if tc.inputWins {
					inputDone = integrationdb.RunAsync(createInput)
				} else {
					changeDone = integrationdb.RunAsyncError(changeInstall)
				}
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				if tc.inputWins {
					changeDone = integrationdb.RunAsyncError(changeInstall)
					integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationInstallLifecycleExclusive", 1)
				} else {
					inputDone = integrationdb.RunAsync(createInput)
					integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationInstallLifecycleShared", 1)
				}
			}
			if err := controlTx.Commit(ctx); err != nil {
				t.Fatalf("release integration input control transaction: %v", err)
			}
			outcome := integrationdb.Await(t, inputDone, "integration input admission")
			if !errors.Is(outcome.Err, tc.wantErr) {
				t.Fatalf("input admission error = %v, want %v", outcome.Err, tc.wantErr)
			}
			if tc.inputWins && outcome.Value.ID == NilID {
				t.Fatal("successful input admission returned no input")
			}
			if changeDone != nil {
				if err := integrationdb.Await(t, changeDone, "install deletion"); err != nil {
					t.Fatalf("delete install: %v", err)
				}
			}

			var inputCount int
			var targetCleared, installDeleted, targetDeleted bool
			if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_idempotency_key = $2),
       agent.integration_target_id IS NULL, install.deleted_at IS NOT NULL, target.deleted_at IS NOT NULL
FROM agents agent
JOIN integration_targets target ON target.project_id = agent.project_id AND target.id = $3
JOIN integration_installs install ON install.id = target.integration_install_id
WHERE agent.id = $1`, agent.ID, idempotencyKey, target.ID).Scan(
				&inputCount, &targetCleared, &installDeleted, &targetDeleted,
			); err != nil {
				t.Fatalf("read input and install effects: %v", err)
			}
			wantInputs := 0
			if tc.inputWins {
				wantInputs = 1
			}
			if inputCount != wantInputs || targetCleared != tc.deleteInstall ||
				installDeleted != tc.deleteInstall || targetDeleted != tc.deleteInstall {
				t.Fatalf("inputs=%d target_cleared=%t install_deleted=%t target_deleted=%t; want %d, %t, %t, %t",
					inputCount, targetCleared, installDeleted, targetDeleted,
					wantInputs, tc.deleteInstall, tc.deleteInstall, tc.deleteInstall)
			}
		})
	}
}

func TestIntegrationTargetExternalProducerValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "producer")
	store, pool := f.store, f.store.pool
	input := f.input()
	input.IdempotencyKey = "Ev-producer"
	_, _, _, err := store.Execution().CreateAgentContentInput(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a claimed author cannot grant channel access")
	binding, err := store.Integrations().CreateIntegrationTargetBinding(ctx, f.grants(true))
	require.NoError(t, err)
	created, _, wasCreated, err := store.Execution().CreateAgentContentInput(ctx, input)
	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, f.target.ID, created.IntegrationTargetID)
	require.Equal(t, binding.ID, created.IntegrationTargetBindingID)
	var provider, providerUser string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT provider, provider_user_id FROM actors WHERE project_id = $1 AND id = $2`,
		testProjectID, created.ActorID).Scan(&provider, &providerUser))
	require.Equal(t, executionstore.ActorProviderExternal, provider)
	require.Equal(t, input.Actor.ProviderUserID, providerUser)

	profile := createIntegrationTestProfile(t, ctx, store, "ungranted-producer-profile")
	otherAgent := createIntegrationBoundAgent(t, ctx, store, profile, f.user.ID, "ungranted-producer-agent")
	wrongAgent := input
	wrongAgent.AgentID, wrongAgent.IdempotencyKey = otherAgent.ID, "Ev-wrong-agent"
	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, wrongAgent)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "an actor cannot confer another agent's authority")
	metadataMismatch := input
	metadataMismatch.Metadata = json.RawMessage(`{"changed":true}`)
	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, metadataMismatch)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)

	forgedProvider := input
	forgedProvider.Actor = &executionstore.ActorParams{
		Provider: integrationstore.IntegrationProviderSlack, ProviderTenantID: "T_OTHER", ProviderUserID: "U_OTHER",
	}
	forgedProvider.IdempotencyKey = "Ev-forged-managed-provider"
	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, forgedProvider)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "external input cannot claim a managed provider identity")
	self := input
	self.Actor = mustOmnaraActorParams(t, f.user.ID)
	self.IdempotencyKey = "Ev-omnara-with-channel"
	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, self)
	require.NoError(t, err, "the authenticated Omnara principal is a valid external-channel author")

	require.NoError(t, store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))
	missingBinding := input
	missingBinding.IdempotencyKey = "Ev-missing-binding"
	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, missingBinding)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "new public channel inputs still require a live receive grant")
	var unauthorizedInputs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs
  WHERE input_idempotency_key IN ('Ev-wrong-agent', 'Ev-missing-binding', 'Ev-forged-managed-provider')`).
		Scan(&unauthorizedInputs))
	require.Zero(t, unauthorizedInputs, "failed authorization cannot append content")
	replay, _, wasCreated, err := store.Execution().CreateAgentContentInput(ctx, input)
	require.NoError(t, err)
	require.False(t, wasCreated)
	require.Equal(t, created.ID, replay.ID)
	require.Equal(t, binding.ID, replay.IntegrationTargetBindingID)
}

func createIntegrationProjectAdmin(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email string,
) identitystore.UserRecord {
	t.Helper()
	user, err := store.Identity().CreateVerifiedUser(ctx, CreateVerifiedUserInput{
		Email:       email,
		DisplayName: "Integration Admin",
	})
	if err != nil {
		t.Fatalf("create integration admin user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add integration admin org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			UserID:    user.ID,
			Role:      "admin",
		},
	); err != nil {
		t.Fatalf("add integration admin project membership: %v", err)
	}
	return user
}

func createIntegrationTestProfile(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key string,
) executionstore.AgentProfileRecord {
	t.Helper()
	return mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, key, "Integration Test Agent "+key, `
instruction: Reply to users.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command: {}
`)
}

func createIntegrationBoundAgent(
	t *testing.T,
	ctx context.Context,
	store *Store,
	profile executionstore.AgentProfileRecord,
	userID ID,
	key string,
) executionstore.AgentRecord {
	t.Helper()
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(userID),
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("launch integration-bound agent: %v", err)
	}
	return launch.Agent
}

func createIntegrationAgentFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	label string,
) (identitystore.UserRecord, executionstore.AgentRecord, ID) {
	t.Helper()
	admin := createIntegrationProjectAdmin(t, ctx, store, label+"@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, label+"-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, label+"-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, label)
	return admin, agent, credentialID
}

func createIntegrationCredential(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, createdByUserID ID,
	label string,
) ID {
	t.Helper()
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: projectID,
		Name:           "integration-" + label,
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-" + label, ClientID: "client-id-" + label,
			ClientSecret: "client-" + label, SigningSecret: "signing-" + label,
		},
		Actor: userPrincipal(createdByUserID),
	})
	if err != nil {
		t.Fatalf("create integration credential: %v", err)
	}
	return secret.ID
}

func slackIntegrationInstallInput(
	appID, routeProfileID, installedByUserID, credentialSecretID ID,
	providerAccountRef, providerTenantID string,
) integrationstore.UpsertIntegrationInstallInput {
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: appID,
		InstalledBy: identitystore.NewUserPrincipal(installedByUserID),
		Provider:    integrationstore.IntegrationProviderSlack, IntegrationKind: integrationstore.IntegrationKindManaged,
		ConnectionMode: "webhook", State: integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: providerTenantID, ProviderAccountRef: providerAccountRef,
		DisplayName: "Omnara", CredentialSecretID: credentialSecretID,
		ProviderIdentity: json.RawMessage(fmt.Sprintf(`{"bot_user_id":%q}`, "B_"+providerAccountRef)),
		Metadata:         json.RawMessage(`{"team_name":"Acme"}`),
	}
	if routeProfileID != NilID {
		input.InitialRoute = &integrationstore.CreateIntegrationRouteInput{
			AgentProfileID: routeProfileID, DeploymentKey: "slack", BehaviorKey: "slack_conversation",
			State: integrationstore.IntegrationRouteStateActive, Configuration: json.RawMessage(`{}`),
		}
	}
	return input
}

func createSlackIntegrationApp(t *testing.T, ctx context.Context, store *Store, projectID ID, appRef string) ID {
	t.Helper()
	app, err := store.Integrations().GetOrCreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, OwnerProjectID: projectID, Provider: integrationstore.IntegrationProviderSlack,
		ProviderAppRef: appRef, ConnectorKey: channelconnector.BuiltInConnectorKey,
		InstallationCredentialKind: "slack_app_credentials", State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	return app.ID
}

func createSlackIntegrationDefinition(
	t *testing.T, ctx context.Context, store *Store, install integrationstore.IntegrationInstallRecord,
) ID {
	t.Helper()
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: install.ProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "slack_thread", Kind: integrationstore.ChannelKindSlackThread,
			SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities:     integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: []channelconnector.Capability{{
				Provider: integrationstore.IntegrationProviderSlack, ConnectorKey: channelconnector.BuiltInConnectorKey,
			}},
		})
	require.NoError(t, err)
	return definition.ID
}

func bindIntegrationTestTarget(
	t *testing.T, ctx context.Context, store *Store, agentID ID, target integrationstore.IntegrationTargetRecord,
) integrationstore.IntegrationTargetBindingRecord {
	t.Helper()
	binding, err := store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: target.ProjectID, AgentID: agentID, IntegrationInstallID: target.IntegrationInstallID,
			IntegrationTargetID: target.ID, Source: "test-setup", ReceiveAllowed: true, SendAllowed: true,
		})
	require.NoError(t, err)
	return binding
}

func integrationOAuthFlowID(sequence int) ID {
	return uuid.MustParse(fmt.Sprintf("018f0000-0000-7000-8000-%012x", sequence))
}

func mustCreateIntegrationInstall(
	t *testing.T,
	ctx context.Context,
	store *Store,
	input integrationstore.UpsertIntegrationInstallInput,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
	if err != nil {
		t.Fatalf("create integration install: %v", err)
	}
	return install
}

func createExternalIntegrationTestConnection(
	t *testing.T, ctx context.Context, store *Store, userID ID,
) (integrationstore.IntegrationInstallRecord, ID) {
	t.Helper()
	install, err := store.Integrations().CreateExternalIntegrationInstall(ctx, externalConnectionInput(userID))
	require.NoError(t, err)
	definition, err := store.Integrations().PublishExternalChannelDefinition(ctx, externalDefinitionInput(install.ID))
	require.NoError(t, err)
	return install, definition.ID
}

func mustCreateExternalChannelInput(
	t *testing.T,
	ctx context.Context,
	store *Store,
	binding integrationstore.IntegrationTargetBindingRecord,
	providerUserID, idempotencyKey, text string,
) executionstore.AgentInputRecord {
	t.Helper()
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: binding.ProjectID, AgentID: binding.AgentID, ChannelID: binding.IntegrationTargetID,
		Actor: &executionstore.ActorParams{
			Provider: executionstore.ActorProviderExternal, ProviderUserID: providerUserID,
		},
		ContentBlocks:  json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%q}]`, text)),
		IdempotencyKey: idempotencyKey,
	})
	require.NoError(t, err)
	require.Equal(t, binding.ID, input.IntegrationTargetBindingID)
	return input
}
