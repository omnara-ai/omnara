//go:build integration

package executionstore_test

import (
	"context"
	"encoding/base32"
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

func TestProjectAppIdentityRotationAndOAuthReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "install-fixed-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install")
	input := slackProjectAppSetupInput(profile.ID, uuid.Nil, admin.ID, credentialID, "A_PROFILE", "T_SHARED")
	input.OAuthFlowID = integrationOAuthFlowID(1)
	input = prepareProjectAppSetup(t, ctx, store, input)
	pending, err := store.Integrations().GetProjectApp(ctx, input.ProjectID, input.AppID)
	if err != nil || pending.State != integrationstore.ProjectAppStateDisconnected ||
		pending.CredentialSecretID != uuid.Nil {
		t.Fatalf("metadata-only app = %+v, err=%v", pending, err)
	}
	app, err := store.Integrations().ConfigureProjectApp(ctx, input)
	if err != nil {
		t.Fatalf("configure app: %v", err)
	}
	if app.ID != pending.ID || app.State != integrationstore.ProjectAppStateActive ||
		app.CredentialSecretID != credentialID || app.LastOAuthFlowID != input.OAuthFlowID ||
		app.SetupRevision != pending.SetupRevision+1 {
		t.Fatalf("unexpected configured app: %+v", app)
	}
	consumed, err := store.Integrations().IntegrationOAuthFlowConsumed(ctx, input.OAuthFlowID)
	if err != nil || !consumed {
		t.Fatalf("OAuth flow consumed=%t, err=%v", consumed, err)
	}
	assertJSONRawEqual(t, app.ProviderConfig, `{}`)
	assertJSONRawEqual(t, app.ProviderIdentity, `{"bot_user_id":"B_A_PROFILE"}`)
	fixedInput := slackProjectAppSetupInput(uuid.Nil, agent.ID, admin.ID, credentialID, "A_FIXED", "T_SHARED")
	fixed := mustCreateProjectApp(t, ctx, store, fixedInput)
	if fixed.CredentialSecretID != credentialID || fixed.ID == app.ID {
		t.Fatalf("unexpected independent app: %+v", fixed)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*integrationstore.ConfigureProjectAppInput)
	}{
		{"missing tenant", func(v *integrationstore.ConfigureProjectAppInput) { v.ProviderTenantID = "" }},
		{"missing credential", func(v *integrationstore.ConfigureProjectAppInput) { v.CredentialSecretID = uuid.Nil }},
		{"non-object identity", func(v *integrationstore.ConfigureProjectAppInput) { v.ProviderIdentity = json.RawMessage(`[]`) }},
		{"missing OAuth flow", func(v *integrationstore.ConfigureProjectAppInput) { v.OAuthFlowID = uuid.Nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			invalid := prepareProjectAppSetup(t, ctx, store, fixedInput)
			tc.mutate(&invalid)
			if _, err := store.Integrations().ConfigureProjectApp(ctx, invalid); !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("invalid setup error = %v, want invalid request", err)
			}
		})
	}
	rotated := input
	rotated.ExpectedSetupRevision = app.SetupRevision
	rotated.CredentialSecretID = createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "rotated")
	credential, err := store.Secrets().GetSecret(ctx, testOrgID, rotated.CredentialSecretID)
	if err != nil {
		t.Fatal(err)
	}
	rotated.CredentialVersionID = credential.CurrentVersionID
	rotated.ProviderAgentDisplayName = "Omnara Prime"
	rotated.ProviderMetadata = json.RawMessage(`{"team_name":"Renamed"}`)
	rotated.OAuthFlowID = integrationOAuthFlowID(2)
	updated, err := store.Integrations().ConfigureProjectApp(ctx, rotated)
	if err != nil {
		t.Fatalf("rotate app credentials: %v", err)
	}
	if updated.ID != app.ID || updated.CredentialSecretID != rotated.CredentialSecretID ||
		updated.State != integrationstore.ProjectAppStateActive || updated.ProviderAccountRef != app.ProviderAccountRef ||
		updated.ProviderAgentDisplayName != rotated.ProviderAgentDisplayName ||
		updated.LastOAuthFlowID != rotated.OAuthFlowID ||
		updated.SetupRevision != app.SetupRevision+1 {
		t.Fatalf("unexpected rotated app: %+v", updated)
	}
	assertJSONRawEqual(t, updated.ProviderMetadata, `{"team_name":"Renamed"}`)
	// Callback state pins the setup attempt. Same or reversed callbacks cannot
	// discover a new app or acquire the latest revision automatically.
	for _, replay := range []integrationstore.ConfigureProjectAppInput{rotated, input} {
		if _, err := store.Integrations().ConfigureProjectApp(ctx, replay); !errors.Is(err, storeerr.ErrConflict) {
			t.Fatalf("stale OAuth setup error = %v, want conflict", err)
		}
	}
	changedIdentity := rotated
	changedIdentity.ExpectedSetupRevision = updated.SetupRevision
	changedIdentity.ProviderAccountRef = "A_DIFFERENT"
	changedIdentity.OAuthFlowID = uuid.Must(uuid.NewV7())
	if _, err := store.Integrations().
		ConfigureProjectApp(ctx, changedIdentity); !errors.Is(
		err,
		storeerr.ErrInvalidRequest,
	) {
		t.Fatalf("provider identity replacement error = %v, want invalid request", err)
	}
	// Disconnection is a separate operation and preserves OAuth attribution.
	if applied, err := store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: updated.ProjectID, AppID: updated.ID, ExpectedSetupRevision: &updated.SetupRevision,
	}); err != nil || !applied {
		t.Fatalf("disconnect app applied=%t, err=%v", applied, err)
	}
	disconnected, err := store.Integrations().GetProjectApp(ctx, updated.ProjectID, updated.ID)
	if err != nil || disconnected.State != integrationstore.ProjectAppStateDisconnected ||
		disconnected.LastOAuthFlowID != rotated.OAuthFlowID || disconnected.SetupRevision != updated.SetupRevision+1 {
		t.Fatalf("unexpected disconnected app: %+v, err=%v", disconnected, err)
	}
	reused := prepareProjectAppSetup(t, ctx, store, fixedInput)
	reused.OAuthFlowID = rotated.OAuthFlowID
	if _, err := store.Integrations().
		ConfigureProjectApp(ctx, reused); !errors.Is(
		err,
		storeerr.ErrIntegrationOAuthFlowConsumed,
	) {
		t.Fatalf("cross-app OAuth reuse error = %v, want consumed", err)
	}
	orgSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "integration-org-secret",
		Material: secrets.GenericMaterial{Value: "value"}, Actor: userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org secret: %v", err)
	}
	badCredential := fixedInput
	badCredential.CredentialSecretID = orgSecret.ID
	badCredential = prepareProjectAppSetup(t, ctx, store, badCredential)
	if _, err := store.Integrations().ConfigureProjectApp(ctx, badCredential); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("ungranted org credential error = %v, want not found", err)
	}
	wrongKind, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: testProjectID,
		Name: "integration-wrong-kind", Material: secrets.GenericMaterial{Value: "value"}, Actor: userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create wrong-kind secret: %v", err)
	}
	badCredential = fixedInput
	badCredential.CredentialSecretID = wrongKind.ID
	badCredential = prepareProjectAppSetup(t, ctx, store, badCredential)
	if _, err := store.Integrations().
		ConfigureProjectApp(ctx, badCredential); !errors.Is(
		err,
		storeerr.ErrInvalidRequest,
	) {
		t.Fatalf("wrong-kind credential error = %v, want invalid request", err)
	}
}

func TestProjectAppAuthorizationAndIndependentIdentityAcrossProjects(t *testing.T) {
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
	unauthorized := slackProjectAppSetupInput(
		profile.ID,
		uuid.Nil,
		outsider.ID,
		credentialID,
		"A_UNAUTHORIZED",
		"T_SCOPE",
	)
	unauthorized = prepareProjectAppSetup(t, ctx, store, unauthorized)
	if _, err := store.Integrations().ConfigureProjectApp(
		ctx, unauthorized,
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("unauthorized installer error = %v, want ErrUnauthorized", err)
	}

	identity := slackProjectAppSetupInput(
		profile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_GLOBAL_IDENTITY",
		"T_SCOPE",
	)
	first := mustCreateProjectApp(t, ctx, store, identity)
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID); err != nil {
		t.Fatalf("app setup should not own a profile: %v", err)
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
	otherIdentity := slackProjectAppSetupInput(
		otherProfile.ID,
		uuid.Nil,
		admin.ID,
		createIntegrationCredential(t, ctx, store, otherProject.ID, admin.ID, "other-project"),
		identity.ProviderAccountRef,
		identity.ProviderTenantID,
	)
	otherIdentity.ProjectID = otherProject.ID
	other := mustCreateProjectApp(t, ctx, store, otherIdentity)
	if first.ID == other.ID || first.ProjectID == other.ProjectID ||
		first.ProviderTenantID != other.ProviderTenantID || first.ProviderAccountRef != other.ProviderAccountRef {
		t.Fatalf("same-bot app ownership collapsed: first=%+v other=%+v", first, other)
	}
	if _, err := store.Integrations().
		GetProjectApp(ctx, first.ProjectID, other.ID); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("cross-project app read error = %v, want not found", err)
	}
	if _, err := store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: first.ProjectID, AppID: first.ID,
	}); err != nil {
		t.Fatal(err)
	}
	unaffected, err := store.Integrations().GetProjectApp(ctx, other.ProjectID, other.ID)
	if err != nil || unaffected.State != integrationstore.ProjectAppStateActive ||
		unaffected.SetupRevision != other.SetupRevision {
		t.Fatalf("disconnect affected another project's same-bot app: %+v, err=%v", unaffected, err)
	}

	disabledProfile := createIntegrationTestProfile(t, ctx, store, "disabled-install-profile")
	disabledInstall := slackProjectAppSetupInput(
		disabledProfile.ID,
		uuid.Nil,
		admin.ID,
		credentialID,
		"A_DISABLED_PROFILE",
		"T_DISABLED_PROFILE",
	)
	disabled := mustCreateProjectApp(t, ctx, store, disabledInstall)
	if _, err := store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: disabled.ProjectID, AppID: disabled.ID,
	}); err != nil {
		t.Fatal(err)
	}
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
	installInput := slackProjectAppSetupInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_AGENT_ARCHIVE",
		"T_TARGET_AGENT_ARCHIVE",
	)

	install := mustCreateProjectApp(t, ctx, store, installInput)
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
		 WHERE project_id = $1 AND app_id = $2 AND provider_ref = $3`,
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

func TestProjectAppDeletionWaitsForTargetCreation(t *testing.T) {
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
	installInput := slackProjectAppSetupInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_INSTALL_DELETE_"+label,
		"T_TARGET_INSTALL_DELETE_"+label,
	)

	install := mustCreateProjectApp(t, ctx, store, installInput)
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
		t.Fatalf("lock project app: %v", err)
	}
	targetDone := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return store.Execution().AdmitInboxInputSlot(ctx, lease, "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	deleteDone := integrationdb.RunAsyncError(func() error {
		return store.Integrations().DeleteProjectAppOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectAppLifecycleExclusive", 1)
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release project app blocker: %v", err)
	}
	targetOutcome := integrationdb.Await(t, targetDone, "integration target creation")
	if err := integrationdb.Await(t, deleteDone, "project app deletion"); err != nil {
		t.Fatalf("delete project app: %v", err)
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
		 WHERE project_id = $1 AND app_id = $2 AND deleted_at IS NULL`,
		testProjectID,
		install.ID,
	).Scan(&activeTargets); err != nil {
		t.Fatalf("count active targets after install deletion: %v", err)
	}
	if activeTargets != 0 {
		t.Fatalf("active targets after install deletion = %d, want 0", activeTargets)
	}
}

func TestProjectAppDeletionFreezesTargetAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-growth@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-growth")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install-growth")
	install := mustCreateProjectApp(t, ctx, store, slackProjectAppSetupInput(
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
		return store.Integrations().DeleteProjectAppOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	targetDone := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return store.Execution().AdmitInboxInputSlot(ctx, lateLease, "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectAppLifecycleShared", 1)

	// Deleting one install must not block a different install or lock the late
	// target's agent while waiting. Exercise both through normal admission paths.
	otherInstall := mustCreateProjectApp(t, ctx, store, slackProjectAppSetupInput(
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
		(SELECT count(*) FROM integration_targets WHERE app_id = $1 AND deleted_at IS NULL),
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
	installInput := slackProjectAppSetupInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_REF_COLLISION",
		"T_TARGET_REF_COLLISION",
	)

	install := mustCreateProjectApp(t, ctx, store, installInput)
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

func TestProjectAppDeletionSerializesWithScopeDeletion(t *testing.T) {
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
			installInput := slackProjectAppSetupInput(
				uuid.Nil,
				agent.ID,
				admin.ID,
				credentialID,
				"A_INSTALL_SCOPE_DELETE_"+scope,
				"T_INSTALL_SCOPE_DELETE_"+scope,
			)

			install := mustCreateProjectApp(t, ctx, store, installInput)
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
			if err := dbsqlc.New(controlTx).LockProjectAppLifecycleExclusive(
				ctx,
				dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: install.ID},
			); err != nil {
				t.Fatalf("lock project app for scope contention: %v", err)
			}

			installDeleteDone := integrationdb.RunAsyncError(func() error {
				return store.Integrations().DeleteProjectAppOnceForIntegration(
					context.Background(),
					testProjectID,
					install.ID,
				)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectAppLifecycleExclusive", 1)

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
				t.Fatalf("release project app control transaction: %v", err)
			}

			if err := integrationdb.Await(t, installDeleteDone, "project app deletion"); err != nil {
				t.Fatalf("delete project app before %s deletion: %v", scope, err)
			}
			if err := integrationdb.Await(t, scopeDeleteDone, scope+" deletion"); err != nil {
				t.Fatalf("delete %s after project app: %v", scope, err)
			}

			var activeInstallCount, activeTargetCount int
			if err := pool.QueryRow(
				ctx,
				`SELECT
				   (SELECT count(*)::integer FROM project_apps
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

func TestDisconnectProjectAppRequiresCurrentSetupRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "disconnect-revision@example.com")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "disconnect-revision")
	input := prepareProjectAppSetup(t, ctx, store, slackProjectAppSetupInput(
		uuid.Nil, uuid.Nil, admin.ID, credentialID, "A_DISCONNECT_REVISION", "T_DISCONNECT_REVISION",
	))
	app, err := store.Integrations().ConfigureProjectApp(ctx, input)
	if err != nil {
		t.Fatalf("configure app: %v", err)
	}
	staleRevision := app.SetupRevision

	input.ExpectedSetupRevision = app.SetupRevision
	input.OAuthFlowID = uuid.Must(uuid.NewV7())
	app, err = store.Integrations().ConfigureProjectApp(ctx, input)
	if err != nil {
		t.Fatalf("reauthorize app: %v", err)
	}
	applied, err := store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: app.ProjectID, AppID: app.ID, ExpectedSetupRevision: &staleRevision,
	})
	if err != nil || applied {
		t.Fatalf("stale disconnect applied=%t, err=%v", applied, err)
	}
	current, err := store.Integrations().GetProjectApp(ctx, app.ProjectID, app.ID)
	if err != nil || current.State != integrationstore.ProjectAppStateActive ||
		current.SetupRevision != app.SetupRevision {
		t.Fatalf("stale revocation changed reauthorized app: %+v, err=%v", current, err)
	}
	applied, err = store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: app.ProjectID, AppID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
	})
	if err != nil || !applied {
		t.Fatalf("current disconnect applied=%t, err=%v", applied, err)
	}
	current, err = store.Integrations().GetProjectApp(ctx, app.ProjectID, app.ID)
	if err != nil || current.State != integrationstore.ProjectAppStateDisconnected ||
		current.SetupRevision != app.SetupRevision+1 {
		t.Fatalf("disconnect did not advance setup revision: %+v, err=%v", current, err)
	}
}

func TestProjectAppSetupRechecksRevisionAfterLifecycleLock(t *testing.T) {
	t.Parallel()
	for _, changeSetup := range []bool{false, true} {
		name := "settings edit preserves verified setup"
		if changeSetup {
			name = "disconnect rejects stale setup"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			admin := createIntegrationProjectAdmin(t, ctx, store, "app-update-lock@example.com")
			profile := createIntegrationTestProfile(t, ctx, store, "app-update-lock")
			credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "app-update-lock")
			input := prepareProjectAppSetup(t, ctx, store, slackProjectAppSetupInput(
				profile.ID, uuid.Nil, admin.ID, credentialID, "A_UPDATE_LOCK", "T_UPDATE_LOCK",
			))
			app, err := store.Integrations().ConfigureProjectApp(ctx, input)
			if err != nil {
				t.Fatalf("configure app: %v", err)
			}
			input.ExpectedSetupRevision = app.SetupRevision
			input.OAuthFlowID = uuid.Must(uuid.NewV7())
			input.ProviderMetadata = json.RawMessage(`{"version":"updated"}`)

			blockingTx := integrationdb.BeginTx(t, ctx, pool)
			q := dbsqlc.New(blockingTx)
			if err := q.LockProjectAppLifecycleExclusive(
				ctx,
				dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: app.ID},
			); err != nil {
				t.Fatalf("lock app lifecycle: %v", err)
			}
			done := integrationdb.RunAsync(func() (integrationstore.ProjectAppRecord, error) {
				return store.Integrations().ConfigureProjectApp(ctx, input)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectAppLifecycleExclusive", 1)

			// Finish the preceding mutation while verified setup waits at the
			// lifecycle gate. Settings touch updated_at without changing setup.
			settings := json.RawMessage(fmt.Sprintf(
				`{"launcher":{"trigger":"mention","scope_kind":"workspace","scope_ref":"T_UPDATE_LOCK","slots":[{"key":"default","agent_profile_id":%q}]}}`,
				profile.ID.String(),
			))
			edited, err := q.UpdateProjectAppSettings(ctx, dbsqlc.UpdateProjectAppSettingsParams{
				ProjectID: app.ProjectID, ID: app.ID, Settings: settings,
			})
			if err != nil {
				t.Fatalf("edit app settings: %v", err)
			}
			if !edited.UpdatedAt.After(app.UpdatedAt) || edited.SetupRevision != app.SetupRevision {
				t.Fatalf("settings edit changed setup or failed to advance updated_at: %+v", edited)
			}
			if changeSetup {
				rows, err := q.DisconnectProjectApp(ctx, dbsqlc.DisconnectProjectAppParams{
					ProjectID: app.ProjectID, ID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
				})
				if err != nil || rows != 1 {
					t.Fatalf("disconnect preceding setup rows=%d, err=%v", rows, err)
				}
			}
			var releaseFloor time.Time
			if err := blockingTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
				t.Fatal(err)
			}
			if err := blockingTx.Commit(ctx); err != nil {
				t.Fatalf("release app lifecycle: %v", err)
			}

			result := integrationdb.Await(t, done, "app setup")
			if changeSetup {
				if !errors.Is(result.Err, storeerr.ErrConflict) {
					t.Fatalf("stale setup error=%v, want conflict", result.Err)
				}
				current, err := store.Integrations().GetProjectApp(ctx, app.ProjectID, app.ID)
				if err != nil || current.State != integrationstore.ProjectAppStateDisconnected ||
					current.SetupRevision != app.SetupRevision+1 || current.LastOAuthFlowID != app.LastOAuthFlowID {
					t.Fatalf("stale setup changed disconnected app: %+v, err=%v", current, err)
				}
				return
			}
			if result.Err != nil {
				t.Fatalf("configure after settings edit: %v", result.Err)
			}
			if result.Value.SetupRevision != app.SetupRevision+1 || result.Value.LastOAuthFlowID != input.OAuthFlowID ||
				result.Value.State != integrationstore.ProjectAppStateActive || result.Value.UpdatedAt.Before(releaseFloor) {
				t.Fatalf("unexpected setup after lifecycle wait: %+v", result.Value)
			}
			gotSettings, err := json.Marshal(result.Value.Settings)
			if err != nil {
				t.Fatal(err)
			}
			assertJSONRawEqual(t, gotSettings, string(settings))
		})
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
	input := slackProjectAppSetupInput(
		uuid.Nil,
		fixedAgent.ID,
		admin.ID,
		credentialID,
		"A_TARGET_REUSE",
		"T_TARGET_REUSE",
	)

	install := mustCreateProjectApp(t, ctx, store, input)

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

func TestSlackActorIdentityAcrossAppsAndConcurrency(t *testing.T) {
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
		install := mustCreateProjectApp(t, ctx, store, slackProjectAppSetupInput(
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

func TestIntegrationInputDedupeTargetProgressionAndDisconnect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "input-admin@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "input-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "input-fixed-agent")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "input")
	installInput := slackProjectAppSetupInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_INPUT",
		"T_INPUT",
	)

	install := mustCreateProjectApp(t, ctx, store, installInput)
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
	if _, err := store.Integrations().DisconnectProjectApp(
		ctx,
		integrationstore.DisconnectProjectAppInput{
			ProjectID:             install.ProjectID,
			AppID:                 install.ID,
			ExpectedSetupRevision: &install.SetupRevision,
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

func TestIntegrationInputAdmissionSerializesWithAppDisconnectAndDeletion(t *testing.T) {
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
			installInput := slackProjectAppSetupInput(
				uuid.Nil,
				agent.ID,
				admin.ID,
				credentialID,
				"A_INPUT_DISABLE_RACE",
				"T_INPUT_DISABLE_RACE",
			)

			install := mustCreateProjectApp(t, ctx, store, installInput)
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
					return store.Integrations().DeleteProjectAppOnceForIntegration(
						ctx,
						testProjectID,
						install.ID,
					)
				}
				applied, err := store.Integrations().DisconnectProjectApp(
					ctx,
					integrationstore.DisconnectProjectAppInput{
						ProjectID:             install.ProjectID,
						AppID:                 install.ID,
						ExpectedSetupRevision: &install.SetupRevision,
					},
				)
				if err == nil && !applied {
					return errors.New("project app disable was not applied")
				}
				return err
			}
			var inputDone <-chan integrationdb.AsyncResult[executionstore.AgentInputRecord]
			var changeDone <-chan error
			if tc.inputWins {
				inputDone = integrationdb.RunAsync(createInput)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				changeDone = integrationdb.RunAsyncError(changeInstall)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectAppLifecycleExclusive", 1)
			} else if tc.deleteInstall {
				changeDone = integrationdb.RunAsyncError(changeInstall)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				inputDone = integrationdb.RunAsync(createInput)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectAppLifecycleShared", 1)
			} else {
				if err := changeInstall(); err != nil {
					t.Fatalf("disconnect app: %v", err)
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
JOIN project_apps install ON install.id = target.app_id
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
	installInput := slackProjectAppSetupInput(
		uuid.Nil,
		agent.ID,
		admin.ID,
		credentialID,
		"A_PRODUCER",
		"T_PRODUCER",
	)

	install := mustCreateProjectApp(t, ctx, store, installInput)
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
	t *testing.T, ctx context.Context, store *Store, agentID, appID uuid.UUID,
	address integrationstore.ConversationAddress,
) integrationstore.IntegrationInboxLease {
	t.Helper()
	app, err := store.Integrations().GetProjectApp(ctx, testProjectID, appID)
	if err != nil {
		t.Fatalf("load origin app: %v", err)
	}
	slot := inboxInputPlan(agentID, app, uuid.NewString())
	slot.Input.Origin.Address = address
	slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
	receipt := freezeInboxInput(t, appActivationFixture{ctx: ctx, store: store}, slot, uuid.NewString(), time.Minute)
	return receipt.Lease()
}

func admitIntegrationOrigin(
	t *testing.T, ctx context.Context, store *Store, agentID, appID uuid.UUID,
	address integrationstore.ConversationAddress,
) (executionstore.InboxInputResult, error) {
	t.Helper()
	lease := prepareIntegrationOrigin(t, ctx, store, agentID, appID, address)
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

// The first two arguments are retained for fixture callers; app setup does not
// bind a launcher profile or agent. Configure those separately through settings.
func slackProjectAppSetupInput(
	_, _, installedByUserID, credentialSecretID uuid.UUID,
	providerAccountRef, providerTenantID string,
) integrationstore.ConfigureProjectAppInput {
	return integrationstore.ConfigureProjectAppInput{
		OrgID:                    testOrgID,
		ProjectID:                testProjectID,
		InstalledByUserID:        installedByUserID,
		Provider:                 integrationstore.IntegrationProviderSlack,
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

// prepareProjectAppSetup creates the saved app before credentials are attached.
// Explicit flow IDs are retained for replay tests; ordinary fixtures get a fresh flow.
func prepareProjectAppSetup(t *testing.T, ctx context.Context, store *Store,
	input integrationstore.ConfigureProjectAppInput,
) integrationstore.ConfigureProjectAppInput {
	t.Helper()
	nameID := uuid.New()
	app, err := store.Integrations().CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID: input.OrgID, ProjectID: input.ProjectID, DefinitionID: appdefinition.Slack,
		Name: "app-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(nameID[:]),
	})
	if err != nil {
		t.Fatalf("create project app metadata: %v", err)
	}
	input.AppID, input.ExpectedSetupRevision = app.ID, app.SetupRevision
	if input.CredentialSecretID != uuid.Nil {
		secret, err := store.Secrets().GetSecret(ctx, input.OrgID, input.CredentialSecretID)
		if err != nil {
			t.Fatalf("read fixture credential version: %v", err)
		}
		input.CredentialVersionID = secret.CurrentVersionID
	}
	if input.OAuthFlowID == uuid.Nil {
		input.OAuthFlowID = uuid.Must(uuid.NewV7())
	}
	return input
}

func mustCreateProjectApp(
	t *testing.T, ctx context.Context, store *Store, input integrationstore.ConfigureProjectAppInput,
) integrationstore.ProjectAppRecord {
	t.Helper()
	input = prepareProjectAppSetup(t, ctx, store, input)
	app, err := store.Integrations().ConfigureProjectApp(ctx, input)
	if err != nil {
		t.Fatalf("configure project app: %v", err)
	}
	return app
}

func mustCreateIntegrationInput(
	t *testing.T,
	ctx context.Context,
	store *Store,
	install integrationstore.ProjectAppRecord,
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
