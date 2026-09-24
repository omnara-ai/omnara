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
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/stretchr/testify/require"
)

func TestProjectIntegrationIdentityRotationAndOAuthReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-admin@example.com")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install")
	input := slackProjectIntegrationSetupInput(admin.ID, credentialID, "A_PROFILE", "T_SHARED")
	input.OAuthFlowID = integrationOAuthFlowID(1)
	input = prepareProjectIntegrationSetup(t, ctx, store, input)
	pending, err := store.Integrations().GetProjectIntegration(ctx, input.ProjectID, input.IntegrationID)
	if err != nil || pending.State != integrationstore.ProjectIntegrationStateDisconnected ||
		pending.CredentialSecretID != uuid.Nil {
		t.Fatalf("metadata-only integration = %+v, err=%v", pending, err)
	}
	integration, err := store.Integrations().ConfigureProjectIntegration(ctx, input)
	if err != nil {
		t.Fatalf("configure integration: %v", err)
	}
	if integration.ID != pending.ID || integration.State != integrationstore.ProjectIntegrationStateActive ||
		integration.CredentialSecretID != credentialID || integration.LastOAuthFlowID != input.OAuthFlowID ||
		integration.SetupRevision != pending.SetupRevision+1 {
		t.Fatalf("unexpected configured integration: %+v", integration)
	}
	consumed, err := store.Integrations().IntegrationOAuthFlowConsumed(ctx, input.OAuthFlowID)
	if err != nil || !consumed {
		t.Fatalf("OAuth flow consumed=%t, err=%v", consumed, err)
	}
	assertJSONRawEqual(t, integration.ProviderConfig, `{}`)
	assertJSONRawEqual(t, integration.ProviderIdentity, `{"bot_user_id":"B_A_PROFILE"}`)
	fixedInput := slackProjectIntegrationSetupInput(admin.ID, credentialID, "A_FIXED", "T_SHARED")
	fixed := mustCreateProjectIntegration(t, ctx, store, fixedInput)
	if fixed.CredentialSecretID != credentialID || fixed.ID == integration.ID {
		t.Fatalf("unexpected independent integration: %+v", fixed)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*integrationstore.ConfigureProjectIntegrationInput)
	}{
		{"missing tenant", func(v *integrationstore.ConfigureProjectIntegrationInput) { v.ProviderTenantID = "" }},
		{
			"missing credential",
			func(v *integrationstore.ConfigureProjectIntegrationInput) { v.CredentialSecretID = uuid.Nil },
		},
		{"non-object identity", func(v *integrationstore.ConfigureProjectIntegrationInput) { v.ProviderIdentity = json.RawMessage(`[]`) }},
		{"missing OAuth flow", func(v *integrationstore.ConfigureProjectIntegrationInput) { v.OAuthFlowID = uuid.Nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			invalid := prepareProjectIntegrationSetup(t, ctx, store, fixedInput)
			tc.mutate(&invalid)
			if _, err := store.Integrations().ConfigureProjectIntegration(
				ctx,
				invalid,
			); !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("invalid setup error = %v, want invalid request", err)
			}
		})
	}
	rotated := input
	rotated.ExpectedSetupRevision = integration.SetupRevision
	rotated.CredentialSecretID = createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "rotated")
	credential, err := store.Secrets().GetSecret(ctx, testOrgID, rotated.CredentialSecretID)
	if err != nil {
		t.Fatal(err)
	}
	rotated.CredentialVersionID = credential.CurrentVersionID
	rotated.ProviderAgentDisplayName = "Omnara Prime"
	rotated.ProviderMetadata = json.RawMessage(`{"team_name":"Renamed"}`)
	rotated.OAuthFlowID = integrationOAuthFlowID(2)
	updated, err := store.Integrations().ConfigureProjectIntegration(ctx, rotated)
	if err != nil {
		t.Fatalf("rotate integration credentials: %v", err)
	}
	if updated.ID != integration.ID || updated.CredentialSecretID != rotated.CredentialSecretID ||
		updated.State != integrationstore.ProjectIntegrationStateActive ||
		updated.ProviderAccountRef != integration.ProviderAccountRef ||
		updated.ProviderAgentDisplayName != rotated.ProviderAgentDisplayName ||
		updated.LastOAuthFlowID != rotated.OAuthFlowID ||
		updated.SetupRevision != integration.SetupRevision+1 {
		t.Fatalf("unexpected rotated integration: %+v", updated)
	}
	assertJSONRawEqual(t, updated.ProviderMetadata, `{"team_name":"Renamed"}`)
	for _, replay := range []integrationstore.ConfigureProjectIntegrationInput{rotated, input} {
		if _, err := store.Integrations().ConfigureProjectIntegration(ctx, replay); !errors.Is(err, storeerr.ErrConflict) {
			t.Fatalf("stale OAuth setup error = %v, want conflict", err)
		}
	}
	changedIdentity := rotated
	changedIdentity.ExpectedSetupRevision = updated.SetupRevision
	changedIdentity.ProviderAccountRef = "A_DIFFERENT"
	changedIdentity.OAuthFlowID = uuid.Must(uuid.NewV7())
	if _, err := store.Integrations().
		ConfigureProjectIntegration(ctx, changedIdentity); !errors.Is(
		err,
		storeerr.ErrInvalidRequest,
	) {
		t.Fatalf("provider identity replacement error = %v, want invalid request", err)
	}
	if applied, err := store.Integrations().DisconnectProjectIntegration(
		ctx,
		integrationstore.DisconnectProjectIntegrationInput{
			ProjectID: updated.ProjectID, IntegrationID: updated.ID, ExpectedSetupRevision: &updated.SetupRevision,
		},
	); err != nil || !applied {
		t.Fatalf("disconnect integration applied=%t, err=%v", applied, err)
	}
	disconnected, err := store.Integrations().GetProjectIntegration(ctx, updated.ProjectID, updated.ID)
	if err != nil || disconnected.State != integrationstore.ProjectIntegrationStateDisconnected ||
		disconnected.LastOAuthFlowID != rotated.OAuthFlowID || disconnected.SetupRevision != updated.SetupRevision+1 {
		t.Fatalf("unexpected disconnected integration: %+v, err=%v", disconnected, err)
	}
	reused := prepareProjectIntegrationSetup(t, ctx, store, fixedInput)
	reused.OAuthFlowID = rotated.OAuthFlowID
	if _, err := store.Integrations().
		ConfigureProjectIntegration(ctx, reused); !errors.Is(
		err,
		storeerr.ErrIntegrationOAuthFlowConsumed,
	) {
		t.Fatalf("cross-integration OAuth reuse error = %v, want consumed", err)
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
	badCredential = prepareProjectIntegrationSetup(t, ctx, store, badCredential)
	if _, err := store.Integrations().ConfigureProjectIntegration(
		ctx,
		badCredential,
	); !errors.Is(err, storeerr.ErrNotFound) {
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
	badCredential = prepareProjectIntegrationSetup(t, ctx, store, badCredential)
	if _, err := store.Integrations().
		ConfigureProjectIntegration(ctx, badCredential); !errors.Is(
		err,
		storeerr.ErrInvalidRequest,
	) {
		t.Fatalf("wrong-kind credential error = %v, want invalid request", err)
	}
}

func TestProjectIntegrationAuthorizationAndIndependentIdentityAcrossProjects(t *testing.T) {
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
	unauthorized := slackProjectIntegrationSetupInput(
		outsider.ID,
		credentialID,
		"A_UNAUTHORIZED",
		"T_SCOPE",
	)
	unauthorized = prepareProjectIntegrationSetup(t, ctx, store, unauthorized)
	if _, err := store.Integrations().ConfigureProjectIntegration(
		ctx, unauthorized,
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("unauthorized installer error = %v, want ErrUnauthorized", err)
	}

	identity := slackProjectIntegrationSetupInput(
		admin.ID,
		credentialID,
		"A_GLOBAL_IDENTITY",
		"T_SCOPE",
	)
	first := mustCreateProjectIntegration(t, ctx, store, identity)
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID); err != nil {
		t.Fatalf("integration setup should not own a profile: %v", err)
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
	otherIdentity := slackProjectIntegrationSetupInput(
		admin.ID,
		createIntegrationCredential(t, ctx, store, otherProject.ID, admin.ID, "other-project"),
		identity.ProviderAccountRef,
		identity.ProviderTenantID,
	)
	otherIdentity.ProjectID = otherProject.ID
	other := mustCreateProjectIntegration(t, ctx, store, otherIdentity)
	if first.ID == other.ID || first.ProjectID == other.ProjectID ||
		first.ProviderTenantID != other.ProviderTenantID || first.ProviderAccountRef != other.ProviderAccountRef {
		t.Fatalf("same-bot integration ownership collapsed: first=%+v other=%+v", first, other)
	}
	if _, err := store.Integrations().
		GetProjectIntegration(ctx, first.ProjectID, other.ID); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("cross-project integration read error = %v, want not found", err)
	}
	if _, err := store.Integrations().DisconnectProjectIntegration(ctx, integrationstore.DisconnectProjectIntegrationInput{
		ProjectID: first.ProjectID, IntegrationID: first.ID,
	}); err != nil {
		t.Fatal(err)
	}
	unaffected, err := store.Integrations().GetProjectIntegration(ctx, other.ProjectID, other.ID)
	if err != nil || unaffected.State != integrationstore.ProjectIntegrationStateActive ||
		unaffected.SetupRevision != other.SetupRevision {
		t.Fatalf("disconnect affected another project's same-bot integration: %+v, err=%v", unaffected, err)
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
	installInput := slackProjectIntegrationSetupInput(
		admin.ID,
		credentialID,
		"A_TARGET_AGENT_ARCHIVE",
		"T_TARGET_AGENT_ARCHIVE",
	)

	install := mustCreateProjectIntegration(t, ctx, store, installInput)
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: "C_ARCHIVE:target"}

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
		// Exercise the target mutation directly: inbox admission has a distinct
		// successful, durable skip outcome when the recipient was archived.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := lifecyclelock.EnterActiveProject(ctx, tx, testOrgID, testProjectID); err != nil {
			return err
		}
		if err := integrationstore.LockIntegrationsTx(ctx, tx, testProjectID, nil, install.ID); err != nil {
			return err
		}
		if err := integrationstore.LockConversationTx(ctx, tx, testProjectID, install.ID, address); err != nil {
			return err
		}
		if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
			ProjectID: testProjectID, AgentID: agent.ID,
		}}); err != nil {
			return err
		}
		_, err = store.Integrations().EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID, IntegrationID: install.ID, Address: address,
		})
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
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
		 WHERE project_id = $1 AND integration_id = $2 AND provider_ref = $3`,
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

func TestProjectIntegrationDeletionWaitsForTargetCreation(t *testing.T) {
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
	installInput := slackProjectIntegrationSetupInput(
		admin.ID,
		credentialID,
		"A_TARGET_INSTALL_DELETE_"+label,
		"T_TARGET_INSTALL_DELETE_"+label,
	)

	install := mustCreateProjectIntegration(t, ctx, store, installInput)
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
		t.Fatalf("lock project integration: %v", err)
	}
	targetDone := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return store.Execution().AdmitInboxInputSlot(ctx, lease, "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	deleteDone := integrationdb.RunAsyncError(func() error {
		return store.Integrations().DeleteProjectIntegrationOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectIntegrationLifecycleExclusive", 1)
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release project integration blocker: %v", err)
	}
	targetOutcome := integrationdb.Await(t, targetDone, "integration target creation")
	if err := integrationdb.Await(t, deleteDone, "project integration deletion"); err != nil {
		t.Fatalf("delete project integration: %v", err)
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
		 WHERE project_id = $1 AND integration_id = $2 AND deleted_at IS NULL`,
		testProjectID,
		install.ID,
	).Scan(&activeTargets); err != nil {
		t.Fatalf("count active targets after install deletion: %v", err)
	}
	if activeTargets != 0 {
		t.Fatalf("active targets after install deletion = %d, want 0", activeTargets)
	}
}

func TestProjectIntegrationDeletionFreezesTargetAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-growth@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-growth")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "install-growth")
	install := mustCreateProjectIntegration(t, ctx, store, slackProjectIntegrationSetupInput(
		admin.ID, credentialID, "A_GROWTH", "T_GROWTH",
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
		return store.Integrations().DeleteProjectIntegrationOnceForIntegration(ctx, testProjectID, install.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	targetDone := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return store.Execution().AdmitInboxInputSlot(ctx, lateLease, "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectIntegrationLifecycleShared", 1)

	// Deleting one install must not block a different install or lock the late
	// target's agent while waiting. Exercise both through normal admission paths.
	otherInstall := mustCreateProjectIntegration(t, ctx, store, slackProjectIntegrationSetupInput(
		admin.ID, credentialID, "A_OTHER", "T_GROWTH",
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
		(SELECT count(*) FROM integration_targets WHERE integration_id = $1 AND deleted_at IS NULL),
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

func TestProjectIntegrationDeletionSerializesWithScopeDeletion(t *testing.T) {
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
			installInput := slackProjectIntegrationSetupInput(
				admin.ID,
				credentialID,
				"A_INSTALL_SCOPE_DELETE_"+scope,
				"T_INSTALL_SCOPE_DELETE_"+scope,
			)

			install := mustCreateProjectIntegration(t, ctx, store, installInput)
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
			if err := dbsqlc.New(controlTx).LockProjectIntegrationLifecycleExclusive(
				ctx,
				dbsqlc.LockProjectIntegrationLifecycleExclusiveParams{IntegrationID: install.ID},
			); err != nil {
				t.Fatalf("lock project integration for scope contention: %v", err)
			}

			installDeleteDone := integrationdb.RunAsyncError(func() error {
				return store.Integrations().DeleteProjectIntegrationOnceForIntegration(
					context.Background(),
					testProjectID,
					install.ID,
				)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectIntegrationLifecycleExclusive", 1)

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
				t.Fatalf("release project integration control transaction: %v", err)
			}

			if err := integrationdb.Await(t, installDeleteDone, "project integration deletion"); err != nil {
				t.Fatalf("delete project integration before %s deletion: %v", scope, err)
			}
			if err := integrationdb.Await(t, scopeDeleteDone, scope+" deletion"); err != nil {
				t.Fatalf("delete %s after project integration: %v", scope, err)
			}

			var activeInstallCount, activeTargetCount int
			if err := pool.QueryRow(
				ctx,
				`SELECT
				   (SELECT count(*)::integer FROM project_integrations
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

func TestDisconnectProjectIntegrationRequiresCurrentSetupRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "disconnect-revision@example.com")
	credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "disconnect-revision")
	input := prepareProjectIntegrationSetup(t, ctx, store, slackProjectIntegrationSetupInput(
		admin.ID, credentialID, "A_DISCONNECT_REVISION", "T_DISCONNECT_REVISION",
	))
	integration, err := store.Integrations().ConfigureProjectIntegration(ctx, input)
	if err != nil {
		t.Fatalf("configure integration: %v", err)
	}
	staleRevision := integration.SetupRevision

	input.ExpectedSetupRevision = integration.SetupRevision
	input.OAuthFlowID = uuid.Must(uuid.NewV7())
	integration, err = store.Integrations().ConfigureProjectIntegration(ctx, input)
	if err != nil {
		t.Fatalf("reauthorize integration: %v", err)
	}
	applied, err := store.Integrations().DisconnectProjectIntegration(
		ctx,
		integrationstore.DisconnectProjectIntegrationInput{
			ProjectID: integration.ProjectID, IntegrationID: integration.ID, ExpectedSetupRevision: &staleRevision,
		},
	)
	if err != nil || applied {
		t.Fatalf("stale disconnect applied=%t, err=%v", applied, err)
	}
	current, err := store.Integrations().GetProjectIntegration(ctx, integration.ProjectID, integration.ID)
	if err != nil || current.State != integrationstore.ProjectIntegrationStateActive ||
		current.SetupRevision != integration.SetupRevision {
		t.Fatalf("stale revocation changed reauthorized integration: %+v, err=%v", current, err)
	}
	applied, err = store.Integrations().DisconnectProjectIntegration(
		ctx,
		integrationstore.DisconnectProjectIntegrationInput{
			ProjectID: integration.ProjectID, IntegrationID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
		},
	)
	if err != nil || !applied {
		t.Fatalf("current disconnect applied=%t, err=%v", applied, err)
	}
	current, err = store.Integrations().GetProjectIntegration(ctx, integration.ProjectID, integration.ID)
	if err != nil || current.State != integrationstore.ProjectIntegrationStateDisconnected ||
		current.SetupRevision != integration.SetupRevision+1 {
		t.Fatalf("disconnect did not advance setup revision: %+v, err=%v", current, err)
	}
}

func TestProjectIntegrationSetupRechecksRevisionAfterLifecycleLock(t *testing.T) {
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
			admin := createIntegrationProjectAdmin(t, ctx, store, "integration-update-lock@example.com")
			profile := createIntegrationTestProfile(t, ctx, store, "integration-update-lock")
			credentialID := createIntegrationCredential(t, ctx, store, testProjectID, admin.ID, "integration-update-lock")
			input := prepareProjectIntegrationSetup(t, ctx, store, slackProjectIntegrationSetupInput(
				admin.ID, credentialID, "A_UPDATE_LOCK", "T_UPDATE_LOCK",
			))
			integration, err := store.Integrations().ConfigureProjectIntegration(ctx, input)
			if err != nil {
				t.Fatalf("configure integration: %v", err)
			}
			input.ExpectedSetupRevision = integration.SetupRevision
			input.OAuthFlowID = uuid.Must(uuid.NewV7())
			input.ProviderMetadata = json.RawMessage(`{"version":"updated"}`)

			blockingTx := integrationdb.BeginTx(t, ctx, pool)
			q := dbsqlc.New(blockingTx)
			if err := q.LockProjectIntegrationLifecycleExclusive(
				ctx,
				dbsqlc.LockProjectIntegrationLifecycleExclusiveParams{IntegrationID: integration.ID},
			); err != nil {
				t.Fatalf("lock integration lifecycle: %v", err)
			}
			done := integrationdb.RunAsync(func() (integrationstore.ProjectIntegrationRecord, error) {
				return store.Integrations().ConfigureProjectIntegration(ctx, input)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectIntegrationLifecycleExclusive", 1)

			settings := json.RawMessage(fmt.Sprintf(
				`{"launcher":{"trigger":"mention","scope_kind":"workspace","scope_ref":"T_UPDATE_LOCK","slots":[{"key":"default","agent_profile_id":%q}]}}`,
				profile.ID.String(),
			))
			edited, err := q.UpdateProjectIntegrationSettings(ctx, dbsqlc.UpdateProjectIntegrationSettingsParams{
				ProjectID: integration.ProjectID, ID: integration.ID, Settings: settings,
			})
			if err != nil {
				t.Fatalf("edit integration settings: %v", err)
			}
			if !edited.UpdatedAt.After(integration.UpdatedAt) || edited.SetupRevision != integration.SetupRevision {
				t.Fatalf("settings edit changed setup or failed to advance updated_at: %+v", edited)
			}
			if changeSetup {
				rows, err := q.DisconnectProjectIntegration(ctx, dbsqlc.DisconnectProjectIntegrationParams{
					ProjectID: integration.ProjectID, ID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
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
				t.Fatalf("release integration lifecycle: %v", err)
			}

			result := integrationdb.Await(t, done, "integration setup")
			if changeSetup {
				if !errors.Is(result.Err, storeerr.ErrConflict) {
					t.Fatalf("stale setup error=%v, want conflict", result.Err)
				}
				current, err := store.Integrations().GetProjectIntegration(ctx, integration.ProjectID, integration.ID)
				if err != nil || current.State != integrationstore.ProjectIntegrationStateDisconnected ||
					current.SetupRevision != integration.SetupRevision+1 || current.LastOAuthFlowID != integration.LastOAuthFlowID {
					t.Fatalf("stale setup changed disconnected integration: %+v, err=%v", current, err)
				}
				return
			}
			if result.Err != nil {
				t.Fatalf("configure after settings edit: %v", result.Err)
			}
			if result.Value.SetupRevision != integration.SetupRevision+1 || result.Value.LastOAuthFlowID != input.OAuthFlowID ||
				result.Value.State != integrationstore.ProjectIntegrationStateActive || result.Value.UpdatedAt.Before(
				releaseFloor,
			) {
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
	input := slackProjectIntegrationSetupInput(
		admin.ID,
		credentialID,
		"A_TARGET_REUSE",
		"T_TARGET_REUSE",
	)

	install := mustCreateProjectIntegration(t, ctx, store, input)

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

func TestIntegrationActorIdentityAcrossIntegrationsAndConcurrency(t *testing.T) {
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
	integrationIDs := make([]uuid.UUID, 0, len(testCases))
	for index, testCase := range testCases {
		install := mustCreateProjectIntegration(t, ctx, store, slackProjectIntegrationSetupInput(
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
		integrationIDs = append(integrationIDs, install.ID)
		actor, err := store.Execution().GetActor(ctx, testProjectID, input.ActorID)
		require.NoError(t, err)
		integrationRef, err := publicid.Encode(publicid.KindProjectIntegration, install.ID)
		require.NoError(t, err)
		require.Equal(t, executionstore.ActorProviderIntegration, actor.Provider)
		require.Equal(t, integrationRef, actor.ProviderTenantID)
		require.Equal(t, "U_SHARED", actor.ProviderUserID)
	}
	if producerIDs[0] == producerIDs[1] {
		t.Fatal("same sender must have separate attribution in independent configured integrations")
	}
	if producerIDs[0] == producerIDs[2] {
		t.Fatal("same textual Slack user id collided across workspaces")
	}

	concurrentTenant, err := publicid.Encode(publicid.KindProjectIntegration, integrationIDs[0])
	require.NoError(t, err)
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
					Provider:         executionstore.ActorProviderIntegration,
					ProviderTenantID: concurrentTenant,
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
		`SELECT count(*) FROM actors WHERE project_id = $1 AND provider = 'integration' AND provider_tenant_id = $2 AND provider_user_id = 'U_CONCURRENT'`,
		testProjectID, concurrentTenant,
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
			Provider:         executionstore.ActorProviderIntegration,
			ProviderTenantID: concurrentTenant,
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
			Provider:         executionstore.ActorProviderIntegration,
			ProviderTenantID: concurrentTenant,
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

	require.NoError(t, store.Integrations().DeleteProjectIntegration(ctx, testOrgID, testProjectID, integrationIDs[0]))
	actor, err := store.Execution().GetActor(ctx, testProjectID, producerIDs[0])
	require.NoError(t, err)
	require.Equal(t, "U_SHARED", actor.ProviderUserID)
	require.Equal(t, concurrentTenant, actor.ProviderTenantID)
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
	installInput := slackProjectIntegrationSetupInput(
		admin.ID,
		credentialID,
		"A_INPUT",
		"T_INPUT",
	)

	install := mustCreateProjectIntegration(t, ctx, store, installInput)
	slot := inboxInputPlan(t, agent.ID, install, "Ev-first")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "dm", Ref: "D_FIRST"}
	slot.Input.Actor.ProviderUserID = "U_SHARED"
	slot.Input.ContentBlocks = json.RawMessage(`[{"type":"text","text":"first"}]`)
	slot.Input.Metadata = nil
	slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
	fixture := integrationActivationFixture{ctx: ctx, store: store}
	receipt := freezeInboxInput(t, fixture, slot, "first", time.Minute)
	first, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
	if err != nil {
		t.Fatalf("admit first input and target: %v", err)
	}
	firstTarget, firstInput := first.IntegrationTarget, first.AgentInput
	wrongTenant := inboxInputPlan(t, agent.ID, install, "Ev-wrong-tenant")
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

	lateSlot := inboxInputPlan(t, agent.ID, install, "Ev-disabled-new")
	lateSlot.Input.Origin.Address = slot.Input.Origin.Address
	lateReceipt := freezeInboxInput(t, fixture, lateSlot, "disabled-new", time.Minute)
	if _, err := store.Integrations().DisconnectProjectIntegration(
		ctx,
		integrationstore.DisconnectProjectIntegrationInput{
			ProjectID:             install.ProjectID,
			IntegrationID:         install.ID,
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

func TestIntegrationInputAdmissionSerializesWithIntegrationDisconnectAndDeletion(t *testing.T) {
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
			installInput := slackProjectIntegrationSetupInput(
				admin.ID,
				credentialID,
				"A_INPUT_DISABLE_RACE",
				"T_INPUT_DISABLE_RACE",
			)

			install := mustCreateProjectIntegration(t, ctx, store, installInput)
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
			slot := inboxInputPlan(t, agent.ID, install, idempotencyKey)
			slot.Input.Origin.Address = integrationstore.ConversationAddress{
				Kind: target.ProviderRefKind,
				Ref:  target.ProviderRef,
			}
			slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
			receipt := freezeInboxInput(
				t,
				integrationActivationFixture{ctx: ctx, store: store},
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
					return store.Integrations().DeleteProjectIntegrationOnceForIntegration(
						ctx,
						testProjectID,
						install.ID,
					)
				}
				applied, err := store.Integrations().DisconnectProjectIntegration(
					ctx,
					integrationstore.DisconnectProjectIntegrationInput{
						ProjectID:             install.ProjectID,
						IntegrationID:         install.ID,
						ExpectedSetupRevision: &install.SetupRevision,
					},
				)
				if err == nil && !applied {
					return errors.New("project integration disable was not applied")
				}
				return err
			}
			var inputDone <-chan integrationdb.AsyncResult[executionstore.AgentInputRecord]
			var changeDone <-chan error
			if tc.inputWins {
				inputDone = integrationdb.RunAsync(createInput)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				changeDone = integrationdb.RunAsyncError(changeInstall)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectIntegrationLifecycleExclusive", 1)
			} else if tc.deleteInstall {
				changeDone = integrationdb.RunAsyncError(changeInstall)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
				inputDone = integrationdb.RunAsync(createInput)
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectIntegrationLifecycleShared", 1)
			} else {
				if err := changeInstall(); err != nil {
					t.Fatalf("disconnect integration: %v", err)
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
JOIN project_integrations install ON install.id = target.integration_id
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
	installInput := slackProjectIntegrationSetupInput(
		admin.ID,
		credentialID,
		"A_PRODUCER",
		"T_PRODUCER",
	)

	install := mustCreateProjectIntegration(t, ctx, store, installInput)
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
	slot := inboxInputPlan(t, agent.ID, install, "Ev-verified")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{
		Kind: target.ProviderRefKind,
		Ref:  target.ProviderRef,
	}
	fixture := integrationActivationFixture{ctx: ctx, store: store}
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

func prepareIntegrationOrigin(
	t *testing.T, ctx context.Context, store *Store, agentID, integrationID uuid.UUID,
	address integrationstore.ConversationAddress,
) integrationstore.IntegrationInboxLease {
	t.Helper()
	integration, err := store.Integrations().GetProjectIntegration(ctx, testProjectID, integrationID)
	if err != nil {
		t.Fatalf("load origin integration: %v", err)
	}
	slot := inboxInputPlan(t, agentID, integration, uuid.NewString())
	slot.Input.Origin.Address = address
	slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
	receipt := freezeInboxInput(
		t,
		integrationActivationFixture{ctx: ctx, store: store},
		slot,
		uuid.NewString(),
		time.Minute,
	)
	return receipt.Lease()
}

func admitIntegrationOrigin(
	t *testing.T, ctx context.Context, store *Store, agentID, integrationID uuid.UUID,
	address integrationstore.ConversationAddress,
) (executionstore.InboxInputResult, error) {
	t.Helper()
	lease := prepareIntegrationOrigin(t, ctx, store, agentID, integrationID, address)
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

func slackProjectIntegrationSetupInput(
	installedByUserID, credentialSecretID uuid.UUID,
	providerAccountRef, providerTenantID string,
) integrationstore.ConfigureProjectIntegrationInput {
	return integrationstore.ConfigureProjectIntegrationInput{
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

func prepareProjectIntegrationSetup(t *testing.T, ctx context.Context, store *Store,
	input integrationstore.ConfigureProjectIntegrationInput,
) integrationstore.ConfigureProjectIntegrationInput {
	t.Helper()
	nameID := uuid.New()
	integration, err := store.Integrations().CreateProjectIntegration(ctx, integrationstore.SaveProjectIntegrationInput{
		OrgID: input.OrgID, ProjectID: input.ProjectID, IntegrationType: integrationdefinition.SlackThread,
		Name: "int-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(nameID[:]),
	})
	if err != nil {
		t.Fatalf("create project integration metadata: %v", err)
	}
	input.IntegrationID, input.ExpectedSetupRevision = integration.ID, integration.SetupRevision
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

func mustCreateProjectIntegration(
	t *testing.T, ctx context.Context, store *Store, input integrationstore.ConfigureProjectIntegrationInput,
) integrationstore.ProjectIntegrationRecord {
	t.Helper()
	input = prepareProjectIntegrationSetup(t, ctx, store, input)
	integration, err := store.Integrations().ConfigureProjectIntegration(ctx, input)
	if err != nil {
		t.Fatalf("configure project integration: %v", err)
	}
	return integration
}

func mustCreateIntegrationInput(
	t *testing.T,
	ctx context.Context,
	store *Store,
	install integrationstore.ProjectIntegrationRecord,
	target integrationstore.IntegrationTargetRecord,
	providerUserID, idempotencyKey, text string,
) executionstore.AgentInputRecord {
	t.Helper()
	slot := inboxInputPlan(t, target.AgentID, install, idempotencyKey)
	slot.Input.Origin.Address = integrationstore.ConversationAddress{
		Kind: target.ProviderRefKind,
		Ref:  target.ProviderRef,
	}
	slot.Input.Actor.ProviderUserID = providerUserID
	slot.Input.ContentBlocks = json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%q}]`, text))
	slot.Input.DeliveryMode, slot.Input.CancelOpenInteractions = executionstore.DeliveryModeQueued, false
	receipt := freezeInboxInput(
		t,
		integrationActivationFixture{ctx: ctx, store: store},
		slot,
		uuid.NewString(),
		time.Minute,
	)
	result, err := store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient")
	if err != nil {
		t.Fatalf("admit integration input: %v", err)
	}
	return result.AgentInput

}
