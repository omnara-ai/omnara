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
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestIntegrationInstallBindingsIdentityAndOAuthReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "install-fixed-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install")

	profileInput := slackIntegrationInstallInput(
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_PROFILE",
		"T_SHARED",
	)
	profileInput.OAuthFlowID = integrationOAuthFlowID(1)
	profileInstall, err := store.Integrations().UpsertIntegrationInstall(ctx, profileInput)
	if err != nil {
		t.Fatalf("create profile-bound install: %v", err)
	}
	if !profileInstall.Created || profileInstall.AgentProfileID != profile.ID ||
		profileInstall.AgentID != NilID || profileInstall.ProviderAccountRef != "A_PROFILE" ||
		profileInstall.State != integrationstore.IntegrationInstallStateActive || profileInstall.CredentialSecretID != credentialID ||
		profileInstall.LastOAuthFlowID != profileInput.OAuthFlowID {
		t.Fatalf("unexpected profile-bound install: %+v", profileInstall)
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

	fixedInput := slackIntegrationInstallInput(
		NilID,
		agent.ID,
		admin.ID,
		credentialID,
		"A_FIXED",
		"T_SHARED",
	)
	fixedInput.IntegrationKind = "workspace_single_agent"
	fixedInstall, err := store.Integrations().UpsertIntegrationInstall(ctx, fixedInput)
	if err != nil {
		t.Fatalf("create fixed-agent install: %v", err)
	}
	if fixedInstall.AgentID != agent.ID || fixedInstall.AgentProfileID != NilID ||
		fixedInstall.CredentialSecretID != credentialID {
		t.Fatalf("unexpected fixed-agent install: %+v", fixedInstall)
	}
	missingTenant := fixedInput
	missingTenant.ProviderAccountRef = "A_MISSING_TENANT"
	missingTenant.ProviderTenantID = ""
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, missingTenant); err == nil {
		t.Fatal("slack install without a provider tenant succeeded")
	}
	missingCredential := fixedInput
	missingCredential.ProviderAccountRef = "A_MISSING_CREDENTIAL"
	missingCredential.CredentialSecretID = NilID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, missingCredential); err == nil {
		t.Fatal("slack install without a credential secret succeeded")
	}

	neither := fixedInput
	neither.ProviderAccountRef = "A_NEITHER"
	neither.AgentID = NilID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, neither); err == nil {
		t.Fatal("install without a binding succeeded")
	}
	both := fixedInput
	both.ProviderAccountRef = "A_BOTH"
	both.AgentProfileID = profile.ID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, both); err == nil {
		t.Fatal("install with both bindings succeeded")
	}

	repointed := profileInput
	repointed.AgentProfileID = NilID
	repointed.AgentID = agent.ID
	repointed.OAuthFlowID = NilID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, repointed); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("repoint install error = %v, want ErrConflict", err)
	}

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
	rotated.ProviderAgentDisplayName = ""
	rotated.ProviderMetadata = json.RawMessage(`{"team_name":"Renamed"}`)
	rotated.OAuthFlowID = integrationOAuthFlowID(2)
	updated, err := store.Integrations().UpsertIntegrationInstall(ctx, rotated)
	if err != nil {
		t.Fatalf("rotate integration install: %v", err)
	}
	if updated.Created || updated.ID != profileInstall.ID || updated.CredentialSecretID != rotatedCredentialID ||
		updated.ConnectionMode != "socket" || updated.State != integrationstore.IntegrationInstallStateDisabled ||
		updated.ProviderAccountRef != profileInstall.ProviderAccountRef ||
		updated.ProviderAgentDisplayName != profileInstall.ProviderAgentDisplayName ||
		updated.LastOAuthFlowID != rotated.OAuthFlowID {
		t.Fatalf("unexpected rotated install: %+v", updated)
	}
	assertJSONRawEqual(t, updated.ProviderMetadata, `{"team_name":"Renamed"}`)
	withoutFlow := rotated
	withoutFlow.ProviderAgentDisplayName = "Omnara Prime"
	withoutFlow.OAuthFlowID = NilID
	preserved, err := store.Integrations().UpsertIntegrationInstall(ctx, withoutFlow)
	if err != nil {
		t.Fatalf("update install without oauth flow: %v", err)
	}
	if preserved.LastOAuthFlowID != rotated.OAuthFlowID ||
		preserved.ProviderAgentDisplayName != "Omnara Prime" {
		t.Fatalf("unexpected install after non-oauth update: %+v", preserved)
	}

	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, rotated); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("same-flow reinstall error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}
	olderFlow := rotated
	olderFlow.OAuthFlowID = profileInput.OAuthFlowID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, olderFlow); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("older-flow reinstall error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}
	reusedFlow := slackIntegrationInstallInput(
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_REUSED_FLOW",
		"T_SHARED",
	)
	reusedFlow.OAuthFlowID = rotated.OAuthFlowID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, reusedFlow); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("cross-install oauth flow reuse error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}

	badJSON := fixedInput
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
	badCredential := fixedInput
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

func TestIntegrationInstallAuthorizationAndGlobalIdentityScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC)
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
		profile.ID,
		NilID,
		outsider.ID,
		credentialID,
		"A_UNAUTHORIZED",
		"T_SCOPE",
	)
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, unauthorized); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("unauthorized installer error = %v, want ErrUnauthorized", err)
	}

	identity := slackIntegrationInstallInput(
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_GLOBAL_IDENTITY",
		"T_SCOPE",
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
		"integration-identity-other-project",
		now.Add(5*time.Second),
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
		otherProfile.ID,
		NilID,
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
		disabledProfile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_DISABLED_PROFILE",
		"T_DISABLED_PROFILE",
	)
	disabledInstall.State = integrationstore.IntegrationInstallStateDisabled
	mustCreateIntegrationInstall(t, ctx, store, disabledInstall)
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, disabledProfile.ID); err != nil {
		t.Fatalf("delete profile referenced only by disabled install: %v", err)
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
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_PROFILE_LOCK",
		"T_PROFILE_LOCK",
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
	waitForDatabaseLockWait(t, ctx, pool, "-- name: LockAgentProfile", blockingPID)
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
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_DISABLE_GENERATION",
		"T_DISABLE_GENERATION",
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
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_UPDATE_LOCK",
		"T_UPDATE_LOCK",
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
	input.ProviderMetadata = json.RawMessage(`{"version":"updated"}`)
	type updateResult struct {
		record integrationstore.IntegrationInstallRecord
		err    error
	}
	done := make(chan updateResult, 1)
	go func() {
		record, updateErr := store.Integrations().UpsertIntegrationInstall(context.Background(), input)
		done <- updateResult{record: record, err: updateErr}
	}()
	waitForDatabaseLockWait(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationInstallByProviderAccount",
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

func TestIntegrationTargetBindingModesAndScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	targetService := integration.New(store.Execution(), store.Integrations())
	admin := createIntegrationProjectAdmin(t, ctx, store, "target-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "target-profile")
	fixedAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "target-fixed-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "targets")
	profileInstall := mustCreateIntegrationInstall(t, ctx, store, slackIntegrationInstallInput(
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_TARGET_PROFILE",
		"T_TARGET",
	))
	fixedInput := slackIntegrationInstallInput(
		NilID,
		fixedAgent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_FIXED",
		"T_TARGET",
	)
	fixedInput.IntegrationKind = "workspace_single_agent"
	fixedInstall := mustCreateIntegrationInstall(t, ctx, store, fixedInput)

	first, firstLaunch, err := targetService.GetOrCreateTarget(ctx, integration.GetOrCreateTargetInput{
		IntegrationInstallID: profileInstall.ID,
		ProviderRef:          "C123:1712345.000001",
		ProviderRefKind:      "thread",
		DisplayName:          "general",
	})
	if err != nil {
		t.Fatalf("create profile-bound target: %v", err)
	}
	if !first.Created || firstLaunch.Agent.ID == NilID || first.AgentID != firstLaunch.Agent.ID ||
		first.DisplayName != "general" {
		t.Fatalf("unexpected profile-bound target: target=%+v launch=%+v", first, firstLaunch)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO integration_targets(
  project_id, agent_id, integration_install_id, target_ref, provider_ref,
  provider_ref_kind, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, 'thread', statement_timestamp(), statement_timestamp())
`, first.ProjectID, first.AgentID, first.IntegrationInstallID, first.TargetRef, "C_TARGET_REF_COLLISION"); !storeutil.IsUniqueViolationOnConstraint(err, "integration_targets_agent_target_ref_idx") {
		t.Fatalf("duplicate target ref error = %v, want stable target-ref index violation", err)
	}
	assertJSONRawEqual(t, first.ProviderMetadata, `{}`)
	replayed, replayLaunch, err := targetService.GetOrCreateTarget(ctx, integration.GetOrCreateTargetInput{
		IntegrationInstallID: profileInstall.ID,
		ProviderRef:          first.ProviderRef,
		ProviderRefKind:      first.ProviderRefKind,
	})
	if err != nil {
		t.Fatalf("replay profile-bound target: %v", err)
	}
	if replayed.ID != first.ID || replayed.Created || replayLaunch.Agent.ID != NilID {
		t.Fatalf("unexpected target replay: target=%+v launch=%+v", replayed, replayLaunch)
	}

	fixedTarget, fixedLaunch, err := targetService.GetOrCreateTarget(ctx, integration.GetOrCreateTargetInput{
		IntegrationInstallID: fixedInstall.ID,
		ProviderRef:          first.ProviderRef,
		ProviderRefKind:      "thread",
	})
	if err != nil {
		t.Fatalf("create fixed-agent target: %v", err)
	}
	if fixedTarget.AgentID != fixedAgent.ID || fixedLaunch.Agent.ID != NilID || fixedTarget.ID == first.ID {
		t.Fatalf("unexpected fixed-agent target: target=%+v launch=%+v", fixedTarget, fixedLaunch)
	}
	if _, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		AgentID:              first.AgentID,
		IntegrationInstallID: fixedInstall.ID,
		ProviderRef:          "D_WRONG_FIXED_AGENT",
		ProviderRefKind:      "dm",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("fixed install target with another agent error = %v, want ErrConflict", err)
	}

	if _, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		AgentID:              fixedAgent.ID,
		IntegrationInstallID: fixedInstall.ID,
		ProviderRef:          fixedTarget.ProviderRef,
		ProviderRefKind:      "channel",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("provider-ref-kind replay error = %v, want ErrConflict", err)
	}

	if _, err := store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
		ProjectID:           profileInstall.ProjectID,
		ID:                  profileInstall.ID,
		ExpectedOAuthFlowID: &profileInstall.LastOAuthFlowID,
	}); err != nil {
		t.Fatalf("disable profile install: %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		AgentID:              first.AgentID,
		IntegrationInstallID: profileInstall.ID,
		ProviderRef:          "D_DIRECT_AFTER_DISABLE",
		ProviderRefKind:      "dm",
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("disabled direct target create error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := targetService.GetOrCreateTarget(ctx, integration.GetOrCreateTargetInput{
		IntegrationInstallID: profileInstall.ID,
		ProviderRef:          "D_AFTER_DISABLE",
		ProviderRefKind:      "dm",
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("disabled target create error = %v, want ErrUnauthorized", err)
	}
}

func TestIntegrationTargetProviderRefReusableAfterTargetDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "target-reuse-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "target-reuse-profile")
	fixedAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "target-reuse-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "target-reuse")
	input := slackIntegrationInstallInput(
		NilID,
		fixedAgent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_REUSE",
		"T_TARGET_REUSE",
	)
	input.IntegrationKind = "workspace_single_agent"
	install := mustCreateIntegrationInstall(t, ctx, store, input)

	first, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		AgentID:              fixedAgent.ID,
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
		AgentID:              fixedAgent.ID,
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
		profile.ID,
		NilID,
		admin.ID,
		credentialID,
		"A_TARGET_SELECTION",
		"T_TARGET_SELECTION",
	))
	first, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		AgentID:              firstAgent.ID,
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
		AgentID:              firstAgent.ID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "C_SELECTION:333.444",
		ProviderRefKind:      "thread",
	})
	if err != nil {
		t.Fatalf("create second selection target: %v", err)
	}
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
	if _, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		AgentID:              secondAgent.ID,
		IntegrationInstallID: install.ID,
		ProviderRef:          first.ProviderRef,
		ProviderRefKind:      first.ProviderRefKind,
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("provider ref repoint error = %v, want ErrConflict", err)
	}
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
	targetService := integration.New(store.Execution(), store.Integrations())
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
			profile.ID,
			NilID,
			admin.ID,
			credentialID,
			testCase.providerAccountRef,
			testCase.providerTenantID,
		))
		target, _, err := targetService.GetOrCreateTarget(ctx, integration.GetOrCreateTargetInput{
			IntegrationInstallID: install.ID,
			ProviderRef:          testCase.targetRef,
			ProviderRefKind:      "dm",
		})
		if err != nil {
			t.Fatalf("create identity target %d: %v", index, err)
		}
		input := mustCreateIntegrationInput(
			t,
			ctx,
			store,
			install,
			target,
			"U_SHARED",
			fmt.Sprintf("Ev-identity-%d", index),
			"hello",
		)
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
		`SELECT count(*) FROM actors WHERE project_id = $1 AND provider = 'slack' AND provider_tenant_id = 'T_CONCURRENT' AND provider_user_id = 'U_CONCURRENT'`,
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
	targetService := integration.New(store.Execution(), store.Integrations())
	admin := createIntegrationProjectAdmin(t, ctx, store, "input-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "input-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "input-fixed-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "input")
	installInput := slackIntegrationInstallInput(
		NilID,
		agent.ID,
		admin.ID,
		credentialID,
		"A_INPUT",
		"T_INPUT",
	)
	installInput.IntegrationKind = "workspace_single_agent"
	install := mustCreateIntegrationInstall(t, ctx, store, installInput)
	firstTarget, _, err := targetService.GetOrCreateTarget(ctx, integration.GetOrCreateTargetInput{
		IntegrationInstallID: install.ID,
		ProviderRef:          "D_FIRST",
		ProviderRefKind:      "dm",
	})
	if err != nil {
		t.Fatalf("create first input target: %v", err)
	}
	secondTarget, _, err := targetService.GetOrCreateTarget(ctx, integration.GetOrCreateTargetInput{
		IntegrationInstallID: install.ID,
		ProviderRef:          "D_SECOND",
		ProviderRefKind:      "dm",
	})
	if err != nil {
		t.Fatalf("create second input target: %v", err)
	}
	if firstTarget.AgentID != agent.ID || secondTarget.AgentID != agent.ID {
		t.Fatalf("fixed-agent targets diverged: first=%+v second=%+v", firstTarget, secondTarget)
	}
	_, _, err = store.Execution().CreateIntegrationTargetContentInput(ctx, executionstore.CreateIntegrationTargetContentInput{
		IntegrationInstallID: install.ID,
		IntegrationTargetID:  firstTarget.ID,
		ProviderTenantID:     "T_WRONG",
		ProviderUserID:       "U_SHARED",
		ContentBlocks:        json.RawMessage(`[{"type":"text","text":"wrong tenant"}]`),
		IdempotencyKey:       "Ev-wrong-tenant",
	})
	if err == nil {
		t.Fatal("integration input with the wrong provider tenant succeeded")
	}
	firstInput := mustCreateIntegrationInput(
		t,
		ctx,
		store,
		install,
		firstTarget,
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
	secondInput := mustCreateIntegrationInput(
		t,
		ctx,
		store,
		install,
		secondTarget,
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
	if secondAgent.IntegrationTargetID != secondTarget.ID {
		t.Fatalf("second agent current target = %s, want %s", secondAgent.IntegrationTargetID, secondTarget.ID)
	}
	replayed := mustCreateIntegrationInput(
		t,
		ctx,
		store,
		install,
		firstTarget,
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
	disabledReplay := mustCreateIntegrationInput(
		t,
		ctx,
		store,
		install,
		firstTarget,
		"U_SHARED",
		"Ev-first",
		"first",
	)
	if disabledReplay.ID != firstInput.ID {
		t.Fatalf("disabled replay id = %s, want %s", disabledReplay.ID, firstInput.ID)
	}
	_, _, err = store.Execution().CreateIntegrationTargetContentInput(ctx, executionstore.CreateIntegrationTargetContentInput{
		IntegrationInstallID: install.ID,
		IntegrationTargetID:  firstTarget.ID,
		ProviderTenantID:     install.ProviderTenantID,
		ProviderUserID:       "U_SHARED",
		ContentBlocks:        json.RawMessage(`[{"type":"text","text":"new"}]`),
		IdempotencyKey:       "Ev-disabled-new",
	})
	if !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("new input on disabled install error = %v, want ErrUnauthorized", err)
	}
}

func TestIntegrationTargetExternalProducerValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "producer-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "producer-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "producer-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "producer")
	installInput := slackIntegrationInstallInput(
		NilID,
		agent.ID,
		admin.ID,
		credentialID,
		"A_PRODUCER",
		"T_PRODUCER",
	)
	installInput.IntegrationKind = "workspace_single_agent"
	install := mustCreateIntegrationInstall(t, ctx, store, installInput)
	target, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            testProjectID,
		AgentID:              agent.ID,
		IntegrationInstallID: install.ID,
		ProviderRef:          "D_PRODUCER",
		ProviderRefKind:      "dm",
	})
	if err != nil {
		t.Fatalf("create producer target: %v", err)
	}
	externalActor, err := executionstore.IntegrationUpsertActorIdentityTx(ctx, store.q, executionstore.UpsertActorIdentityInput{
		ProjectID:        testProjectID,
		Provider:         integrationstore.IntegrationProviderSlack,
		ProviderTenantID: install.ProviderTenantID,
		ProviderUserID:   "U_PRODUCER",
		DisplayName:      "Producer User",
	})
	if err != nil {
		t.Fatalf("create producer actor: %v", err)
	}
	input := executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID,
		AgentID:   agent.ID,
		Actor: &executionstore.ActorParams{
			Provider:         integrationstore.IntegrationProviderSlack,
			ProviderTenantID: install.ProviderTenantID,
			ProviderUserID:   "U_PRODUCER",
		},
		IntegrationTargetID: target.ID,
		ContentBlocks:       json.RawMessage(`[{"type":"text","text":"from integration"}]`),
		Metadata:            json.RawMessage(`{}`),
		IdempotencyScope:    "integration-producer-validation",
		IdempotencyKey:      "Ev-producer",
	}
	created, _, wasCreated, err := store.Execution().CreateAgentContentInput(ctx, input)
	if err != nil {
		t.Fatalf("create external producer input: %v", err)
	}
	if !wasCreated || created.ActorID != externalActor.ID || created.IntegrationTargetID != target.ID {
		t.Fatalf("unexpected external producer input: %+v", created)
	}
	metadataMismatch := input
	metadataMismatch.Metadata = json.RawMessage(`{"changed":true}`)
	if _, _, _, err := store.Execution().CreateAgentContentInput(ctx, metadataMismatch); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("metadata-mismatched replay error = %v, want ErrIdempotencyConflict", err)
	}

	if _, err := executionstore.IntegrationUpsertActorIdentityTx(ctx, store.q, executionstore.UpsertActorIdentityInput{
		ProjectID:        testProjectID,
		Provider:         integrationstore.IntegrationProviderSlack,
		ProviderTenantID: "T_OTHER",
		ProviderUserID:   "U_OTHER",
	}); err != nil {
		t.Fatalf("create other-tenant actor: %v", err)
	}
	wrongTenant := input
	wrongTenant.Actor = &executionstore.ActorParams{
		Provider:         integrationstore.IntegrationProviderSlack,
		ProviderTenantID: "T_OTHER",
		ProviderUserID:   "U_OTHER",
	}
	wrongTenant.IdempotencyKey = "Ev-wrong-producer-tenant"
	if _, _, _, err := store.Execution().CreateAgentContentInput(ctx, wrongTenant); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("wrong-tenant producer actor error = %v, want ErrUnauthorized", err)
	}
	omnaraWithTarget := input
	omnaraWithTarget.Actor = mustOmnaraActorParams(t, admin.ID)
	omnaraWithTarget.IdempotencyKey = "Ev-omnara-with-target"
	if _, _, _, err := store.Execution().CreateAgentContentInput(ctx, omnaraWithTarget); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("omnara producer with integration target error = %v, want ErrUnauthorized", err)
	}
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
name: Integration Test Agent
instruction: Reply to users.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command: {}
`, time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC))
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

func createIntegrationCredential(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, createdByUserID ID,
	label string,
) ID {
	t.Helper()
	payload, err := slack.CredentialPayload(slack.AppCredentials{
		BotToken:      "xoxb-" + label,
		ClientID:      "client-id-" + label,
		ClientSecret:  "client-" + label,
		SigningSecret: "signing-" + label,
	})
	if err != nil {
		t.Fatalf("build integration credential payload: %v", err)
	}
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: projectID,
		Name:           "integration-" + label,
		Material:       secrets.SlackAppCredentialsMaterialFromPayload(payload),
		Actor:          userPrincipal(createdByUserID),
	})
	if err != nil {
		t.Fatalf("create integration credential: %v", err)
	}
	return secret.ID
}

func slackIntegrationInstallInput(
	agentProfileID, agentID, installedByUserID, credentialSecretID ID,
	providerAccountRef, providerTenantID string,
) integrationstore.UpsertIntegrationInstallInput {
	return integrationstore.UpsertIntegrationInstallInput{
		OrgID:                    testOrgID,
		ProjectID:                testProjectID,
		AgentProfileID:           agentProfileID,
		AgentID:                  agentID,
		InstalledByUserID:        installedByUserID,
		Provider:                 integrationstore.IntegrationProviderSlack,
		IntegrationKind:          slack.IntegrationKindAgentProfile,
		ConnectionMode:           slack.ConnectionModeWebhook,
		State:                    integrationstore.IntegrationInstallStateActive,
		ProviderTenantID:         providerTenantID,
		ProviderAccountRef:       providerAccountRef,
		ProviderAgentDisplayName: "Omnara",
		CredentialSecretID:       credentialSecretID,
		ProviderIdentity: json.RawMessage(fmt.Sprintf(
			`{"bot_user_id":%q}`,
			"B_"+providerAccountRef,
		)),
		ProviderMetadata: json.RawMessage(`{"team_name":"Acme"}`),
	}
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

func mustCreateIntegrationInput(
	t *testing.T,
	ctx context.Context,
	store *Store,
	install integrationstore.IntegrationInstallRecord,
	target integrationstore.IntegrationTargetRecord,
	providerUserID, idempotencyKey, text string,
) executionstore.AgentInputRecord {
	t.Helper()
	input, _, err := store.Execution().CreateIntegrationTargetContentInput(ctx, executionstore.CreateIntegrationTargetContentInput{
		IntegrationInstallID: install.ID,
		IntegrationTargetID:  target.ID,
		ProviderTenantID:     install.ProviderTenantID,
		ProviderUserID:       providerUserID,
		ContentBlocks:        json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%q}]`, text)),
		IdempotencyKey:       idempotencyKey,
	})
	if err != nil {
		t.Fatalf("create integration input: %v", err)
	}
	return input
}
