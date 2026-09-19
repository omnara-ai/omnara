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
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestIntegrationConnectionIdentityRotationAndOAuthReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "install-fixed-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install")

	profileInput := slackIntegrationConnectionInput(
		profile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_PROFILE",
		"T_SHARED",
	)
	setup := integrationstore.SaveProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, DefinitionID: appdefinition.Slack, Enabled: true,
	}
	profileInput.OAuthFlowID = integrationOAuthFlowID(1)
	profileInstall, _, err := store.Integrations().CompleteSlackAppSetup(ctx, profileInput, setup)
	if err != nil {
		t.Fatalf("create profile-bound install: %v", err)
	}
	if !profileInstall.Created || profileInstall.ProviderAccountRef != "A_PROFILE" ||
		profileInstall.State != integrationstore.IntegrationConnectionStateActive ||
		profileInstall.CredentialSecretID != credentialID ||
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

	fixedInput := slackIntegrationConnectionInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_FIXED",
		"T_SHARED",
	)

	fixedInstall, err := store.Integrations().CreateIntegrationConnection(ctx, fixedInput)
	if err != nil {
		t.Fatalf("create fixed-agent install: %v", err)
	}
	if fixedInstall.CredentialSecretID != credentialID {
		t.Fatalf("unexpected fixed-agent install: %+v", fixedInstall)
	}
	missingTenant := fixedInput
	missingTenant.ProviderAccountRef = "A_MISSING_TENANT"
	missingTenant.ProviderTenantID = ""
	if _, err := store.Integrations().CreateIntegrationConnection(ctx, missingTenant); err == nil {
		t.Fatal("slack install without a provider tenant succeeded")
	}
	missingCredential := fixedInput
	missingCredential.ProviderAccountRef = "A_MISSING_CREDENTIAL"
	missingCredential.CredentialSecretID = uuid.Nil
	if _, err := store.Integrations().CreateIntegrationConnection(ctx, missingCredential); err == nil {
		t.Fatal("slack install without a credential secret succeeded")
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

	rotated.ProviderAgentDisplayName = ""
	rotated.ProviderMetadata = json.RawMessage(`{"team_name":"Renamed"}`)
	rotated.OAuthFlowID = integrationOAuthFlowID(2)
	updated, _, err := store.Integrations().CompleteSlackAppSetup(ctx, rotated, setup)
	if err != nil {
		t.Fatalf("rotate integration connection: %v", err)
	}
	if updated.Created || updated.ID != profileInstall.ID || updated.CredentialSecretID != rotatedCredentialID ||
		updated.State != integrationstore.IntegrationConnectionStateActive ||
		updated.ProviderAccountRef != profileInstall.ProviderAccountRef ||
		updated.ProviderAgentDisplayName != profileInstall.ProviderAgentDisplayName ||
		updated.LastOAuthFlowID != rotated.OAuthFlowID {
		t.Fatalf("unexpected rotated install: %+v", updated)
	}
	assertJSONRawEqual(t, updated.ProviderMetadata, `{"team_name":"Renamed"}`)
	withoutFlow := rotated
	withoutFlow.ProviderAgentDisplayName = "Omnara Prime"
	withoutFlow.OAuthFlowID = uuid.Nil
	withoutFlow.State = integrationstore.IntegrationConnectionStateDisabled
	preserved, err := store.Integrations().UpdateIntegrationConnection(ctx, updated.ID, withoutFlow)
	if err != nil {
		t.Fatalf("update install without oauth flow: %v", err)
	}
	if preserved.LastOAuthFlowID != rotated.OAuthFlowID ||
		preserved.ProviderAgentDisplayName != "Omnara Prime" ||
		preserved.State != integrationstore.IntegrationConnectionStateDisabled {
		t.Fatalf("unexpected install after non-oauth update: %+v", preserved)
	}

	if _, _, err := store.Integrations().CompleteSlackAppSetup(
		ctx, rotated, setup,
	); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("same-flow reinstall error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}
	olderFlow := rotated
	olderFlow.OAuthFlowID = profileInput.OAuthFlowID
	if _, _, err := store.Integrations().CompleteSlackAppSetup(
		ctx, olderFlow, setup,
	); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("older-flow reinstall error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}
	reusedFlow := slackIntegrationConnectionInput(
		profile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_REUSED_FLOW",
		"T_SHARED",
	)
	reusedFlow.OAuthFlowID = rotated.OAuthFlowID
	if _, _, err := store.Integrations().CompleteSlackAppSetup(
		ctx, reusedFlow, setup,
	); !errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		t.Fatalf("cross-install oauth flow reuse error = %v, want ErrIntegrationOAuthFlowConsumed", err)
	}

	badJSON := fixedInput
	badJSON.ProviderAccountRef = "A_BAD_JSON"
	badJSON.ProviderIdentity = json.RawMessage(`[]`)
	if _, err := store.Integrations().CreateIntegrationConnection(ctx, badJSON); err == nil {
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
	if _, err := store.Integrations().CreateIntegrationConnection(
		ctx,
		badCredential,
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
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
	if _, err := store.Integrations().CreateIntegrationConnection(
		ctx,
		badCredential,
	); !errors.Is(
		err,
		storeerr.ErrInvalidRequest,
	) {
		t.Fatalf("wrong-kind credential error = %v, want ErrInvalidRequest", err)
	}
}

func TestIntegrationConnectionAuthorizationAndGlobalIdentityScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-scope-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-scope-profile")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install-scope")

	outsider, err := store.Identity().CreateVerifiedUser(ctx, storagetest.CreateVerifiedUserInput{
		Email:       "install-scope-outsider@example.com",
		DisplayName: "Integration Outsider",
	})
	if err != nil {
		t.Fatalf("create integration outsider: %v", err)
	}
	unauthorized := slackIntegrationConnectionInput(
		profile.ID,
		uuid.Nil,
		outsider.ID,
		credentialID,
		"A_UNAUTHORIZED",
		"T_SCOPE",
	)
	if _, err := store.Integrations().CreateIntegrationConnection(
		ctx, unauthorized,
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("unauthorized installer error = %v, want ErrUnauthorized", err)
	}

	identity := slackIntegrationConnectionInput(
		profile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_GLOBAL_IDENTITY",
		"T_SCOPE",
	)
	mustCreateIntegrationConnection(t, ctx, store, identity)
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID); err != nil {
		t.Fatalf("connection should not own a profile: %v", err)
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
	otherIdentity := slackIntegrationConnectionInput(
		otherProfile.ID,
		uuid.Nil,
		admin.ID,
		createIntegrationCredential(t, ctx, store, otherProject.ID, admin.ID, "other-project"),
		identity.ProviderAccountRef,
		identity.ProviderTenantID,
	)
	otherIdentity.ProjectID = otherProject.ID
	if _, err := store.Integrations().CreateIntegrationConnection(
		ctx,
		otherIdentity,
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("cross-project provider identity error = %v, want ErrConflict", err)
	}

	disabledProfile := createIntegrationTestProfile(t, ctx, store, "disabled-install-profile")
	disabledInstall := slackIntegrationConnectionInput(
		disabledProfile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_DISABLED_PROFILE",
		"T_DISABLED_PROFILE",
	)
	disabledInstall.State = integrationstore.IntegrationConnectionStateDisabled
	mustCreateIntegrationConnection(t, ctx, store, disabledInstall)
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, disabledProfile.ID); err != nil {
		t.Fatalf("delete profile referenced only by disabled install: %v", err)
	}
}

func TestIntegrationTargetRechecksAgentAfterArchiveWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, agent, credentialID := createFixedIntegrationFixture(
		t,
		ctx,
		store,
		"target-agent-archive",
	)
	installInput := slackIntegrationConnectionInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_AGENT_ARCHIVE",
		"T_TARGET_AGENT_ARCHIVE",
	)

	install := mustCreateIntegrationConnection(t, ctx, store, installInput)
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: "C_ARCHIVE:target"}
	lease := prepareIntegrationOrigin(t, ctx, store, agent.ID, install.ID, address)

	blockingTx := integrationdb.BeginTx(t, ctx, pool)
	if _, err := dbsqlc.New(blockingTx).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agent.ID},
	); err != nil {
		t.Fatalf("lock integration target agent: %v", err)
	}
	archiveActor := mustOmnaraActorParams(t, admin.ID)
	archiveDone := integrationdb.RunAsyncError(func() error {
		_, _, archiveErr := store.Execution().IntegrationArchiveAgentOnce(
			context.Background(),
			testOrgID,
			testProjectID,
			agent.ID,
			archiveActor,
		)
		return archiveErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	targetDone := integrationdb.RunAsyncError(func() error {
		_, targetErr := store.Execution().AdmitInboxInputSlot(context.Background(), lease, "recipient")
		return targetErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 2)
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release integration target agent blocker: %v", err)
	}
	if err := integrationdb.Await(t, archiveDone, "agent archival"); err != nil {
		t.Fatalf("archive integration target agent: %v", err)
	}
	if err := integrationdb.Await(
		t, targetDone, "integration target creation",
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("target after agent archive error = %v, want state transition conflict", err)
	}
	var targetCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM integration_targets
		 WHERE project_id = $1 AND integration_connection_id = $2 AND provider_ref = $3`,
		testProjectID,
		install.ID,
		address.Ref,
	).Scan(&targetCount); err != nil {
		t.Fatalf("count integration targets after agent archive: %v", err)
	}
	if targetCount != 0 {
		t.Fatalf("integration targets after agent archive = %d, want 0", targetCount)
	}
}

func TestIntegrationConnectionDeletionWaitsForTargetCreation(t *testing.T) {
	t.Parallel()
	const label = "target-wins"
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, agent, credentialID := createFixedIntegrationFixture(
		t,
		ctx,
		store,
		"install-delete-"+label,
	)
	installInput := slackIntegrationConnectionInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_INSTALL_DELETE_"+label,
		"T_TARGET_INSTALL_DELETE_"+label,
	)

	install := mustCreateIntegrationConnection(t, ctx, store, installInput)
	lease := prepareIntegrationOrigin(t, ctx, store, agent.ID, install.ID,
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C_DELETE:" + label})

	blockingTx := integrationdb.BeginTx(t, ctx, pool)
	if _, err := dbsqlc.New(blockingTx).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: testProjectID,
			ID:        agent.ID,
		},
	); err != nil {
		t.Fatalf("lock integration connection: %v", err)
	}
	targetDone := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return store.Execution().AdmitInboxInputSlot(ctx, lease, "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	deleteDone := integrationdb.RunAsyncError(func() error {
		return store.Integrations().DeleteIntegrationConnectionOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationConnectionLifecycleExclusive", 1)
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release integration connection blocker: %v", err)
	}
	targetOutcome := integrationdb.Await(t, targetDone, "integration target creation")
	if err := integrationdb.Await(t, deleteDone, "integration connection deletion"); err != nil {
		t.Fatalf("delete integration connection: %v", err)
	}
	if targetOutcome.Err != nil || !targetOutcome.Value.Created || !targetOutcome.Value.IntegrationTarget.Created {
		t.Fatalf("target creation before deletion = %+v err=%v", targetOutcome.Value, targetOutcome.Err)
	}
	if targetOutcome.Value.AgentInput.IntegrationTargetID != targetOutcome.Value.IntegrationTarget.ID {
		t.Fatal("admitted input and target were not committed together")
	}
	var activeTargets int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM integration_targets
		 WHERE project_id = $1 AND integration_connection_id = $2 AND deleted_at IS NULL`,
		testProjectID,
		install.ID,
	).Scan(&activeTargets); err != nil {
		t.Fatalf("count active targets after install deletion: %v", err)
	}
	if activeTargets != 0 {
		t.Fatalf("active targets after install deletion = %d, want 0", activeTargets)
	}
}

func TestIntegrationConnectionDeletionFreezesTargetAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-growth@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-growth")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install-growth")
	install := mustCreateIntegrationConnection(t, ctx, store, slackIntegrationConnectionInput(
		profile.ID, uuid.Nil, admin.ID, credentialID, "A_GROWTH", "T_GROWTH",
	))
	firstAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "first-target-agent")
	firstOrigin, err := admitIntegrationOrigin(
		t,
		ctx,
		store,
		firstAgent.ID,
		install.ID,
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C_FIRST"},
	)
	first := firstOrigin.IntegrationTarget
	if err != nil {
		t.Fatalf("create first target: %v", err)
	}
	mustCreateIntegrationInput(t, ctx, store, install, first, "U_GROWTH", "Ev-first", "select first target")
	secondAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "install-growth-second")

	lateLease := prepareIntegrationOrigin(t, ctx, store, secondAgent.ID, install.ID,
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C_SECOND"})
	controlTx := integrationdb.BeginTx(t, ctx, pool)
	if _, err := dbsqlc.New(controlTx).LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: first.AgentID,
	}); err != nil {
		t.Fatalf("block existing target agent: %v", err)
	}
	deleteDone := integrationdb.RunAsyncError(func() error {
		return store.Integrations().DeleteIntegrationConnectionOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	targetDone := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return store.Execution().AdmitInboxInputSlot(ctx, lateLease, "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationConnectionLifecycleShared", 1)

	// Deleting one install must not block a different install or lock the late
	// target's agent while waiting. Exercise both through normal admission paths.
	otherInstall := mustCreateIntegrationConnection(t, ctx, store, slackIntegrationConnectionInput(
		profile.ID, uuid.Nil, admin.ID, credentialID, "A_OTHER", "T_GROWTH",
	))
	otherCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	otherTargetOrigin, err := admitIntegrationOrigin(
		t,
		otherCtx,
		store,
		secondAgent.ID,
		otherInstall.ID,
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C_OTHER"},
	)
	otherTarget := otherTargetOrigin.IntegrationTarget
	if err != nil {
		t.Fatalf("create unrelated install target during deletion: %v", err)
	}
	mustCreateIntegrationInput(t, otherCtx, store, otherInstall, otherTarget, "U_GROWTH", "Ev-other", "other install")
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
		(SELECT count(*) FROM integration_targets WHERE integration_connection_id = $1 AND deleted_at IS NULL),
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
	admin, agent, credentialID := createFixedIntegrationFixture(
		t,
		ctx,
		store,
		"target-ref-collision",
	)
	installInput := slackIntegrationConnectionInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_REF_COLLISION",
		"T_TARGET_REF_COLLISION",
	)

	install := mustCreateIntegrationConnection(t, ctx, store, installInput)
	integrationStore := store.Integrations()
	integrationStore.IntegrationSetTargetRefGenerator(func(string) (string, error) {
		return "slack-fixed", nil
	})
	if _, err := admitIntegrationOrigin(t, ctx, store, agent.ID, install.ID,
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C_COLLISION:first"}); err != nil {
		t.Fatalf("create collision fixture target: %v", err)
	}

	references := []string{"slack-fixed", "slack-free"}
	generated := 0
	integrationStore.IntegrationSetTargetRefGenerator(func(string) (string, error) {
		ref := references[generated]
		generated++
		return ref, nil
	})
	createdOrigin, err := admitIntegrationOrigin(
		t,
		ctx,
		store,
		agent.ID,
		install.ID,
		integrationstore.ConversationAddress{Kind: "thread", Ref: "C_COLLISION:second"},
	)
	created := createdOrigin.IntegrationTarget
	if err != nil {
		t.Fatalf("create target after generated reference collision: %v", err)
	}
	if !created.Created || created.TargetRef != "slack-free" || generated != 2 {
		t.Fatalf("target after reference collision = %+v, generated=%d", created, generated)
	}
}

func TestIntegrationConnectionDeletionSerializesWithScopeDeletion(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"project", "organization"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			admin, agent, credentialID := createFixedIntegrationFixture(
				t,
				ctx,
				store,
				"install-scope-delete-"+scope,
			)
			installInput := slackIntegrationConnectionInput(
				uuid.Nil,
				agent.ID,
				admin.ID,
				credentialID,
				"A_INSTALL_SCOPE_DELETE_"+scope,
				"T_INSTALL_SCOPE_DELETE_"+scope,
			)

			install := mustCreateIntegrationConnection(t, ctx, store, installInput)
			targetOrigin, err := admitIntegrationOrigin(
				t,
				ctx,
				store,
				agent.ID,
				install.ID,
				integrationstore.ConversationAddress{Kind: "thread", Ref: "C_SCOPE_DELETE:" + scope},
			)
			target := targetOrigin.IntegrationTarget
			if err != nil {
				t.Fatalf("create unbound integration target: %v", err)
			}
			var boundTargetID *uuid.UUID
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
			if _, err := dbsqlc.New(controlTx).LockIntegrationConnectionForMutation(
				ctx,
				dbsqlc.LockIntegrationConnectionForMutationParams{
					ProjectID: testProjectID,
					ID:        install.ID,
				},
			); err != nil {
				t.Fatalf("lock integration connection for scope contention: %v", err)
			}

			installDeleteDone := integrationdb.RunAsyncError(func() error {
				return store.Integrations().DeleteIntegrationConnectionOnceForIntegration(
					context.Background(),
					testProjectID,
					install.ID,
				)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationConnectionForMutation", 1)

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
				t.Fatalf("release integration connection control transaction: %v", err)
			}

			if err := integrationdb.Await(t, installDeleteDone, "integration connection deletion"); err != nil {
				t.Fatalf("delete integration connection before %s deletion: %v", scope, err)
			}
			if err := integrationdb.Await(t, scopeDeleteDone, scope+" deletion"); err != nil {
				t.Fatalf("delete %s after integration connection: %v", scope, err)
			}

			var activeInstallCount, activeTargetCount int
			if err := pool.QueryRow(
				ctx,
				`SELECT
				   (SELECT count(*)::integer FROM integration_connections
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

func TestDisableIntegrationConnectionRequiresCurrentOAuthGeneration(t *testing.T) {
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
	input := slackIntegrationConnectionInput(
		profile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_DISABLE_GENERATION",
		"T_DISABLE_GENERATION",
	)
	setup := integrationstore.SaveProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, DefinitionID: appdefinition.Slack, Enabled: true,
	}
	input.OAuthFlowID = integrationOAuthFlowID(18)
	install, _, err := store.Integrations().CompleteSlackAppSetup(ctx, input, setup)
	if err != nil {
		t.Fatalf("create integration connection: %v", err)
	}
	staleOAuthFlowID := install.LastOAuthFlowID

	input.OAuthFlowID = integrationOAuthFlowID(19)
	install, _, err = store.Integrations().CompleteSlackAppSetup(ctx, input, setup)
	if err != nil {
		t.Fatalf("reauthorize integration connection: %v", err)
	}
	applied, err := store.Integrations().DisableIntegrationConnection(
		ctx,
		integrationstore.DisableIntegrationConnectionInput{
			ProjectID:           install.ProjectID,
			ID:                  install.ID,
			ExpectedOAuthFlowID: &staleOAuthFlowID,
		},
	)
	if err != nil {
		t.Fatalf("disable with stale OAuth generation: %v", err)
	}
	if applied {
		t.Fatal("stale OAuth generation disabled reauthorized integration connection")
	}
	current, err := store.Integrations().GetIntegrationConnection(ctx, install.ProjectID, install.ID)
	if err != nil {
		t.Fatalf("load reauthorized integration connection: %v", err)
	}
	if current.State != integrationstore.IntegrationConnectionStateActive {
		t.Fatalf("reauthorized integration connection state = %q, want active", current.State)
	}

	applied, err = store.Integrations().DisableIntegrationConnection(
		ctx,
		integrationstore.DisableIntegrationConnectionInput{
			ProjectID:           install.ProjectID,
			ID:                  install.ID,
			ExpectedOAuthFlowID: &install.LastOAuthFlowID,
		},
	)
	if err != nil {
		t.Fatalf("disable with current OAuth generation: %v", err)
	}
	if !applied {
		t.Fatal("current OAuth generation did not disable integration connection")
	}
}

func TestIntegrationConnectionUpdateUsesPostLockDatabaseTime(t *testing.T) {
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
	input := slackIntegrationConnectionInput(
		profile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_UPDATE_LOCK",
		"T_UPDATE_LOCK",
	)
	setup := integrationstore.SaveProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, DefinitionID: appdefinition.Slack, Enabled: true,
	}
	input.OAuthFlowID = integrationOAuthFlowID(20)
	install, _, err := store.Integrations().CompleteSlackAppSetup(ctx, input, setup)
	if err != nil {
		t.Fatalf("create integration connection: %v", err)
	}

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin integration connection blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get integration connection blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`SELECT id FROM integration_connections WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		install.ProjectID,
		install.ID,
	); err != nil {
		t.Fatalf("lock integration connection: %v", err)
	}

	input.OAuthFlowID = integrationOAuthFlowID(21)
	input.ProviderMetadata = json.RawMessage(`{"version":"updated"}`)
	type updateResult struct {
		record integrationstore.IntegrationConnectionRecord
		err    error
	}
	done := make(chan updateResult, 1)
	go func() {
		record, _, updateErr := store.Integrations().CompleteSlackAppSetup(context.Background(), input, setup)
		done <- updateResult{record: record, err: updateErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockIntegrationConnectionByProviderAccount",
		blockingPID,
	)
	var releaseFloor time.Time
	if err := blockingTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
		t.Fatalf("read integration connection release time: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release integration connection: %v", err)
	}

	result := <-done
	if result.err != nil {
		t.Fatalf("update integration connection: %v", result.err)
	}
	if result.record.UpdatedAt.Before(releaseFloor) {
		t.Fatalf(
			"integration connection updated_at = %s, want at or after lock release %s",
			result.record.UpdatedAt,
			releaseFloor,
		)
	}
	if result.record.LastOAuthFlowID != input.OAuthFlowID {
		t.Fatalf(
			"integration connection OAuth flow = %s, want %s",
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
	profile := createIntegrationTestProfile(t, ctx, store, "target-reuse-profile")
	fixedAgent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "target-reuse-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "target-reuse")
	input := slackIntegrationConnectionInput(
		uuid.Nil,
		fixedAgent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_REUSE",
		"T_TARGET_REUSE",
	)

	install := mustCreateIntegrationConnection(t, ctx, store, input)

	firstOrigin, err := admitIntegrationOrigin(
		t,
		ctx,
		store,
		fixedAgent.ID,
		install.ID,
		integrationstore.ConversationAddress{Kind: "channel", Ref: "C900:reuse"},
	)
	first := firstOrigin.IntegrationTarget
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

	recreatedOrigin, err := admitIntegrationOrigin(
		t,
		ctx,
		store,
		fixedAgent.ID,
		install.ID,
		integrationstore.ConversationAddress{Kind: "channel", Ref: "C900:reuse"},
	)
	recreated := recreatedOrigin.IntegrationTarget
	if err != nil {
		t.Fatalf("recreate target after deletion: %v", err)
	}
	if !recreated.Created || recreated.ID == first.ID {
		t.Fatalf("expected a fresh target for the freed provider ref, got %+v (first %s)", recreated, first.ID)
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
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "identity-agent")
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
	producerIDs := make([]uuid.UUID, 0, len(testCases))
	for index, testCase := range testCases {
		install := mustCreateIntegrationConnection(t, ctx, store, slackIntegrationConnectionInput(
			profile.ID,
			uuid.Nil,
			admin.ID,
			credentialID,
			testCase.providerAccountRef,
			testCase.providerTenantID,
		))
		targetOrigin, err := admitIntegrationOrigin(
			t,
			ctx,
			store,
			agent.ID,
			install.ID,
			integrationstore.ConversationAddress{Kind: "dm", Ref: testCase.targetRef},
		)
		target := targetOrigin.IntegrationTarget
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
	ids := make(chan uuid.UUID, concurrentCalls)
	errs := make(chan error, concurrentCalls)
	var wg sync.WaitGroup
	for range concurrentCalls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			actor, err := executionstore.IntegrationUpsertActorIdentityTx(
				ctx,
				store.q,
				executionstore.UpsertActorIdentityInput{
					ProjectID:        testProjectID,
					Provider:         integrationstore.IntegrationProviderSlack,
					ProviderTenantID: "T_CONCURRENT",
					ProviderUserID:   "U_CONCURRENT",
					DisplayName:      "Concurrent User",
				},
			)
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
	var concurrentID uuid.UUID
	for id := range ids {
		if concurrentID == uuid.Nil {
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

	settled, err := executionstore.IntegrationUpsertActorIdentityTx(
		ctx,
		store.q,
		executionstore.UpsertActorIdentityInput{
			ProjectID:        testProjectID,
			Provider:         integrationstore.IntegrationProviderSlack,
			ProviderTenantID: "T_CONCURRENT",
			ProviderUserID:   "U_CONCURRENT",
			DisplayName:      "Concurrent User",
		},
	)
	if err != nil {
		t.Fatalf("repeat identical actor upsert: %v", err)
	}
	repeated, err := executionstore.IntegrationUpsertActorIdentityTx(
		ctx,
		store.q,
		executionstore.UpsertActorIdentityInput{
			ProjectID:        testProjectID,
			Provider:         integrationstore.IntegrationProviderSlack,
			ProviderTenantID: "T_CONCURRENT",
			ProviderUserID:   "U_CONCURRENT",
			DisplayName:      "Concurrent User",
		},
	)
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
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "input-fixed-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "input")
	installInput := slackIntegrationConnectionInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_INPUT",
		"T_INPUT",
	)

	install := mustCreateIntegrationConnection(t, ctx, store, installInput)
	slot := inboxInputPlan(agent.ID, install, "Ev-first")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "dm", Ref: "D_FIRST"}
	slot.Input.Actor.ProviderUserID = "U_SHARED"
	slot.Input.ContentBlocks = json.RawMessage(`[{"type":"text","text":"first"}]`)
	slot.Input.Metadata = nil
	slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
	fixture := appActivationFixture{ctx: ctx, store: store}
	receipt := freezeInboxInput(t, fixture, slot, "first", time.Minute)
	first, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
	if err != nil {
		t.Fatalf("admit first input and target: %v", err)
	}
	firstTarget, firstInput := first.IntegrationTarget, first.AgentInput
	wrongTenant := inboxInputPlan(agent.ID, install, "Ev-wrong-tenant")
	wrongTenant.Input.Origin.Address = slot.Input.Origin.Address
	wrongTenant.Input.Actor.ProviderTenantID = "T_WRONG"
	wrongReceipt := freezeInboxInput(t, fixture, wrongTenant, "wrong-tenant", time.Minute)
	_, err = store.Execution().AdmitInboxInputSlot(ctx, wrongReceipt.Lease(), "recipient")
	if err == nil {
		t.Fatal("integration input with the wrong provider tenant succeeded")
	}
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
	slot.Input.Origin.Address.Ref = "D_SECOND"
	slot.Input.IdempotencyKey = "Ev-second"
	slot.Input.ContentBlocks = json.RawMessage(`[{"type":"text","text":"second"}]`)
	secondReceipt := freezeInboxInput(t, fixture, slot, "second", time.Minute)
	second, err := store.Execution().AdmitInboxInputSlot(ctx, secondReceipt.Lease(), "recipient")
	if err != nil {
		t.Fatalf("admit second input and target: %v", err)
	}
	secondTarget, secondInput := second.IntegrationTarget, second.AgentInput
	if firstTarget.AgentID != agent.ID || secondTarget.AgentID != agent.ID {
		t.Fatalf("fixed-agent targets diverged: first=%+v second=%+v", firstTarget, secondTarget)
	}
	if secondInput.ID == firstInput.ID {
		t.Fatalf("distinct provider events produced one input %s", firstInput.ID)
	}
	secondAgent, err := store.Execution().GetAgentInProject(ctx, testProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load second target agent: %v", err)
	}
	if secondAgent.IntegrationTargetID != uuid.Nil {
		t.Fatalf("origin without an authorized handler selected target %s", secondAgent.IntegrationTargetID)
	}
	replayed, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
	if err != nil || replayed.AgentInput.ID != firstInput.ID {
		t.Fatalf("replayed input = %+v, err=%v; want %s", replayed, err, firstInput.ID)
	}
	afterReplay, err := store.Execution().GetAgentInProject(ctx, testProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load agent after input replay: %v", err)
	}
	if afterReplay.IntegrationTargetID != secondAgent.IntegrationTargetID {
		t.Fatalf("replay changed interaction destination to %s", afterReplay.IntegrationTargetID)
	}

	lateSlot := inboxInputPlan(agent.ID, install, "Ev-disabled-new")
	lateSlot.Input.Origin.Address = slot.Input.Origin.Address
	lateReceipt := freezeInboxInput(t, fixture, lateSlot, "disabled-new", time.Minute)
	if _, err := store.Integrations().DisableIntegrationConnection(
		ctx,
		integrationstore.DisableIntegrationConnectionInput{
			ProjectID:           install.ProjectID,
			ID:                  install.ID,
			ExpectedOAuthFlowID: &install.LastOAuthFlowID,
		},
	); err != nil {
		t.Fatalf("disable input install: %v", err)
	}
	disabledReplay, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
	if err != nil || disabledReplay.AgentInput.ID != firstInput.ID {
		t.Fatalf("disabled replay = %+v, err=%v; want %s", disabledReplay, err, firstInput.ID)
	}
	_, err = store.Execution().AdmitInboxInputSlot(ctx, lateReceipt.Lease(), "recipient")
	if !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("new input on disabled install error = %v, want ErrUnauthorized", err)
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
		{"disable wins", false, false, storeerr.ErrUnauthorized},
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
			credentialID := createIntegrationCredential(
				t,
				ctx,
				store,
				testProjectID,
				admin.ID,
				"input-disable-race",
			)
			installInput := slackIntegrationConnectionInput(
				uuid.Nil,
				agent.ID,
				admin.ID,
				credentialID,
				"A_INPUT_DISABLE_RACE",
				"T_INPUT_DISABLE_RACE",
			)

			install := mustCreateIntegrationConnection(t, ctx, store, installInput)
			targetOrigin, err := admitIntegrationOrigin(
				t,
				ctx,
				store,
				agent.ID,
				install.ID,
				integrationstore.ConversationAddress{Kind: "dm", Ref: "D_INPUT_DISABLE_RACE"},
			)
			target := targetOrigin.IntegrationTarget
			if err != nil {
				t.Fatalf("create integration target: %v", err)
			}
			mustCreateIntegrationInput(
				t,
				ctx,
				store,
				install,
				target,
				"U_INPUT_DISABLE_RACE",
				"Ev-input-disable-seed",
				"seed",
			)

			controlTx := integrationdb.BeginTx(t, ctx, pool)
			if _, err := dbsqlc.New(controlTx).LockAgentInProject(
				ctx,
				dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agent.ID},
			); err != nil {
				t.Fatalf("lock integration target agent: %v", err)
			}

			idempotencyKey := "Ev-input-install-race"
			slot := inboxInputPlan(agent.ID, install, idempotencyKey)
			slot.Input.Origin.Address = integrationstore.ConversationAddress{
				Kind: target.ProviderRefKind,
				Ref:  target.ProviderRef,
			}
			slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
			receipt := freezeInboxInput(
				t,
				appActivationFixture{ctx: ctx, store: store},
				slot,
				"input-race",
				time.Minute,
			)
			createInput := func() (executionstore.AgentInputRecord, error) {
				result, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
				return result.AgentInput, err
			}
			changeInstall := func() error {
				if tc.deleteInstall {
					return store.Integrations().DeleteIntegrationConnectionOnceForIntegration(
						ctx,
						testProjectID,
						install.ID,
					)
				}
				applied, err := store.Integrations().DisableIntegrationConnection(
					ctx,
					integrationstore.DisableIntegrationConnectionInput{
						ProjectID:           install.ProjectID,
						ID:                  install.ID,
						ExpectedOAuthFlowID: &install.LastOAuthFlowID,
					},
				)
				if err == nil && !applied {
					return errors.New("integration connection disable was not applied")
				}
				return err
			}
			var inputDone <-chan integrationdb.AsyncResult[executionstore.AgentInputRecord]
			var changeDone <-chan error
			if tc.inputWins {
				inputDone = integrationdb.RunAsync(createInput)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				changeDone = integrationdb.RunAsyncError(changeInstall)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationConnectionLifecycleExclusive", 1)
			} else if tc.deleteInstall {
				changeDone = integrationdb.RunAsyncError(changeInstall)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				inputDone = integrationdb.RunAsync(createInput)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockIntegrationConnectionLifecycleShared", 1)
			} else {
				if err := changeInstall(); err != nil {
					t.Fatalf("disable connection: %v", err)
				}
				inputDone = integrationdb.RunAsync(createInput)
			}
			if err := controlTx.Commit(ctx); err != nil {
				t.Fatalf("release integration input control transaction: %v", err)
			}
			outcome := integrationdb.Await(t, inputDone, "integration input admission")
			if !errors.Is(outcome.Err, tc.wantErr) {
				t.Fatalf("input admission error = %v, want %v", outcome.Err, tc.wantErr)
			}
			if tc.inputWins && outcome.Value.ID == uuid.Nil {
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
JOIN integration_targets target ON target.agent_id = agent.id AND target.id = $3
JOIN integration_connections install ON install.id = target.integration_connection_id
WHERE agent.id = $1`, agent.ID, idempotencyKey, target.ID).Scan(
				&inputCount, &targetCleared, &installDeleted, &targetDeleted,
			); err != nil {
				t.Fatalf("read input and install effects: %v", err)
			}
			wantInputs := 0
			if tc.inputWins {
				wantInputs = 1
			}
			if inputCount != wantInputs || !targetCleared ||
				installDeleted != tc.deleteInstall || targetDeleted != tc.deleteInstall {
				t.Fatalf("inputs=%d target_cleared=%t install_deleted=%t target_deleted=%t; want %d, %t, %t, %t",
					inputCount, targetCleared, installDeleted, targetDeleted,
					wantInputs, true, tc.deleteInstall, tc.deleteInstall)
			}
		})
	}
}

func TestIntegrationTargetHostedOriginRequiresInbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "producer-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "producer-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "producer-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "producer")
	installInput := slackIntegrationConnectionInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_PRODUCER",
		"T_PRODUCER",
	)

	install := mustCreateIntegrationConnection(t, ctx, store, installInput)
	targetOrigin, err := admitIntegrationOrigin(
		t,
		ctx,
		store,
		agent.ID,
		install.ID,
		integrationstore.ConversationAddress{Kind: "dm", Ref: "D_PRODUCER"},
	)
	target := targetOrigin.IntegrationTarget
	if err != nil {
		t.Fatalf("create producer target: %v", err)
	}
	// Public input must not manufacture hosted-provider origin even when the
	// supplied actor resembles a valid provider identity. Verified ingress uses
	// the durable inbox after provider authentication.
	for _, actor := range []*executionstore.ActorParams{
		{Provider: "slack", ProviderTenantID: install.ProviderTenantID, ProviderUserID: "U_PRODUCER"},
		{Provider: "slack", ProviderTenantID: "T_OTHER", ProviderUserID: "U_OTHER"},
		{Provider: executionstore.ActorProviderExternal, ProviderUserID: "external-producer"},
		mustOmnaraActorParams(t, admin.ID),
	} {
		_, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID:           testProjectID,
			AgentID:             agent.ID,
			Actor:               actor,
			IntegrationTargetID: target.ID,
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"from integration"}]`,
			),
			IdempotencyKey: "Ev-producer",
		})
		if !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("public hosted origin: %v", err)
		}
	}
	slot := inboxInputPlan(agent.ID, install, "Ev-verified")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{
		Kind: target.ProviderRefKind,
		Ref:  target.ProviderRef,
	}
	fixture := appActivationFixture{ctx: ctx, store: store}
	receipt := freezeInboxInput(t, fixture, slot, "verified", time.Minute)
	created, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
	if err != nil || created.AgentInput.IntegrationTargetID != target.ID {
		t.Fatalf("verified provider input: %+v %v", created, err)
	}
	slot.Input.Actor.ProviderTenantID, slot.Input.IdempotencyKey = "T_OTHER", "Ev-wrong-tenant"
	wrong := freezeInboxInput(t, fixture, slot, "wrong-tenant", time.Minute)
	if _, err := store.Execution().AdmitInboxInputSlot(
		ctx,
		wrong.Lease(),
		"recipient",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("wrong-tenant verified input: %v", err)
	}

}

// prepareIntegrationOrigin uses the existing inbox fixture to freeze an ordinary
// provider input. Tests exercise target creation through atomic input admission.
func prepareIntegrationOrigin(
	t *testing.T, ctx context.Context, store *Store, agentID, connectionID uuid.UUID,
	address integrationstore.ConversationAddress,
) integrationstore.IntegrationInboxLease {
	t.Helper()
	connection, err := store.Integrations().GetIntegrationConnection(ctx, testProjectID, connectionID)
	if err != nil {
		t.Fatalf("load origin connection: %v", err)
	}
	slot := inboxInputPlan(agentID, connection, uuid.NewString())
	slot.Input.Origin.Address = address
	slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
	receipt := freezeInboxInput(t, appActivationFixture{ctx: ctx, store: store}, slot, uuid.NewString(), time.Minute)
	return receipt.Lease()
}

func admitIntegrationOrigin(
	t *testing.T, ctx context.Context, store *Store, agentID, connectionID uuid.UUID,
	address integrationstore.ConversationAddress,
) (executionstore.InboxInputResult, error) {
	t.Helper()
	lease := prepareIntegrationOrigin(t, ctx, store, agentID, connectionID, address)
	return store.Execution().AdmitInboxInputSlot(ctx, lease, "recipient")
}

func createIntegrationProjectAdmin(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email string,
) identitystore.UserRecord {
	t.Helper()
	user, err := store.Identity().CreateVerifiedUser(ctx, storagetest.CreateVerifiedUserInput{
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
	userID uuid.UUID,
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

func createFixedIntegrationFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	label string,
) (identitystore.UserRecord, executionstore.AgentRecord, uuid.UUID) {
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
	projectID, createdByUserID uuid.UUID,
	label string,
) uuid.UUID {
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

func slackIntegrationConnectionInput(
	agentProfileID, agentID, installedByUserID, credentialSecretID uuid.UUID,
	providerAccountRef, providerTenantID string,
) integrationstore.SaveIntegrationConnectionInput {
	return integrationstore.SaveIntegrationConnectionInput{
		OrgID:                    testOrgID,
		ProjectID:                testProjectID,
		InstalledByUserID:        installedByUserID,
		Provider:                 integrationstore.IntegrationProviderSlack,
		State:                    integrationstore.IntegrationConnectionStateActive,
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

func integrationOAuthFlowID(sequence int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("018f0000-0000-7000-8000-%012x", sequence))
}

func mustCreateIntegrationConnection(
	t *testing.T,
	ctx context.Context,
	store *Store,
	input integrationstore.SaveIntegrationConnectionInput,
) integrationstore.IntegrationConnectionRecord {
	t.Helper()
	install, err := store.Integrations().CreateIntegrationConnection(ctx, input)
	if err != nil {
		t.Fatalf("create integration connection: %v", err)
	}
	return install
}

func mustCreateIntegrationInput(
	t *testing.T,
	ctx context.Context,
	store *Store,
	install integrationstore.IntegrationConnectionRecord,
	target integrationstore.IntegrationTargetRecord,
	providerUserID, idempotencyKey, text string,
) executionstore.AgentInputRecord {
	t.Helper()
	slot := inboxInputPlan(target.AgentID, install, idempotencyKey)
	slot.Input.Origin.Address = integrationstore.ConversationAddress{
		Kind: target.ProviderRefKind,
		Ref:  target.ProviderRef,
	}
	slot.Input.Actor.ProviderUserID = providerUserID
	slot.Input.ContentBlocks = json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%q}]`, text))
	slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
	receipt := freezeInboxInput(t, appActivationFixture{ctx: ctx, store: store}, slot, uuid.NewString(), time.Minute)
	result, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
	if err != nil {
		t.Fatalf("admit integration input: %v", err)
	}
	return result.AgentInput

}
