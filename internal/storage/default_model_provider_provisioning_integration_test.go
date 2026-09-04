//go:build integration

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func mustCompleteDefaultModelProviderProvisioning(
	t *testing.T,
	ctx context.Context,
	store *Store,
	orgID ID,
	template modelstore.DefaultModelProviderTemplate,
	credentialValue string,
) {
	t.Helper()
	claim, found, err := store.Organizations().ClaimDefaultModelProviderProvisioningForOrganization(
		ctx,
		orgID,
	)
	if err != nil || !found {
		t.Fatalf("claim default model provider provisioning: found=%t err=%v", found, err)
	}
	if claim.OrgID != orgID {
		t.Fatalf("claimed provisioning = %+v, want organization %s", claim, orgID)
	}
	if err := store.Organizations().CompleteDefaultModelProviderProvisioning(
		ctx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           claim,
			Template:        template,
			CredentialValue: credentialValue,
		},
	); err != nil {
		t.Fatalf("complete default model provider provisioning: %v", err)
	}
}

func TestDefaultModelProviderProvisioningClaimsAreFenced(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newSecretIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "provider-claim@example.com", "Provider Claim Owner")
	template := testProvisioningTemplate()
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: user.ID, Name: "Provider Claim Org", IdempotencyKey: "provider-claim-org",
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	claim, found, err := store.Organizations().ClaimDefaultModelProviderProvisioningForOrganization(
		ctx,
		created.Org.ID,
	)
	if err != nil || !found {
		t.Fatalf("claim provisioning: found=%t err=%v", found, err)
	}
	if claim.OrgID != created.Org.ID || claim.CreatorUserID != user.ID || claim.Attempt != 1 {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	if _, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx); err != nil || found {
		t.Fatalf("concurrent claim: found=%t err=%v", found, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE default_model_provider_provisioning_jobs
		SET claim_expires_at = statement_timestamp() - interval '1 second'
		WHERE organization_id = $1
	`, created.Org.ID); err != nil {
		t.Fatalf("expire provisioning claim: %v", err)
	}
	expiredClaim, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx)
	if err != nil || !found {
		t.Fatalf("reclaim expired provisioning: found=%t err=%v", found, err)
	}
	if expiredClaim.Attempt != 2 || expiredClaim.ClaimToken == claim.ClaimToken {
		t.Fatalf("expired claim = %+v, want attempt 2 with a new token", expiredClaim)
	}
	if err := store.Organizations().CompleteDefaultModelProviderProvisioning(
		ctx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           claim,
			Template:        template,
			CredentialValue: "stale-credential",
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("expired claim completion error = %v, want state transition conflict", err)
	}
	if err := store.Organizations().RetryDefaultModelProviderProvisioning(
		ctx,
		orglifecycle.RetryDefaultModelProviderProvisioningInput{Claim: expiredClaim, Delay: time.Second},
	); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	if _, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx); err != nil || found {
		t.Fatalf("claim before retry is due: found=%t err=%v", found, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE default_model_provider_provisioning_jobs
		SET next_attempt_at = statement_timestamp()
		WHERE organization_id = $1
	`, created.Org.ID); err != nil {
		t.Fatalf("make retry due: %v", err)
	}
	retryClaim, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx)
	if err != nil || !found {
		t.Fatalf("claim retry: found=%t err=%v", found, err)
	}
	if retryClaim.Attempt != 3 || retryClaim.ClaimToken == expiredClaim.ClaimToken {
		t.Fatalf("retry claim = %+v, want attempt 3 with a new token", retryClaim)
	}
	if err := store.Organizations().CompleteDefaultModelProviderProvisioning(
		ctx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           expiredClaim,
			Template:        template,
			CredentialValue: "stale-credential",
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale claim completion error = %v, want state transition conflict", err)
	}
	if err := store.Organizations().CompleteDefaultModelProviderProvisioning(
		ctx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           retryClaim,
			Template:        template,
			CredentialValue: "current-credential",
		},
	); err != nil {
		t.Fatalf("complete current claim: %v", err)
	}
	if _, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx); err != nil || found {
		t.Fatalf("claim completed job: found=%t err=%v", found, err)
	}
}

func TestDefaultModelProviderProvisioningDoesNotClaimConfiguredSecretName(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newSecretIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "provider-secret-name@example.com", "Provider Secret Owner")
	template := testProvisioningTemplate()
	template.CredentialSecretName = strings.Repeat("界", 64)
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: user.ID, Name: "Provider Secret Org", IdempotencyKey: "provider-secret-org",
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if _, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: created.Org.ID, OwnerKind: secretstore.SecretOwnerOrg,
		Name: template.CredentialSecretName, Material: secrets.GenericMaterial{Value: "tenant-value"},
		Actor: identitystore.NewUserPrincipal(user.ID),
	}); err != nil {
		t.Fatalf("create tenant secret: %v", err)
	}
	mustCompleteDefaultModelProviderProvisioning(t, ctx, store, created.Org.ID, template, "cluster-value")
	provider, err := store.Models().GetModelProviderConfigByName(ctx, created.Org.ID, template.Name)
	if err != nil {
		t.Fatalf("get default provider: %v", err)
	}
	credential, err := store.Secrets().GetSecret(ctx, created.Org.ID, provider.CredentialSecretID)
	if err != nil {
		t.Fatalf("get default provider credential: %v", err)
	}
	if !strings.HasPrefix(credential.Name, strings.Repeat("界", 41)+"-") ||
		len([]rune(credential.Name)) != 64 {
		t.Fatalf("credential name = %q, want generated suffix", credential.Name)
	}
}

func TestDeleteOrganizationDeletesDefaultModelProviderProvisioning(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newSecretIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "provider-delete@example.com", "Provider Delete Owner")
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: user.ID, Name: "Provider Delete Org", IdempotencyKey: "provider-delete-org",
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if _, err := store.Organizations().DeleteOrganization(
		ctx,
		created.Org.ID,
		identitystore.NewUserPrincipal(user.ID),
	); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
	var jobCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM default_model_provider_provisioning_jobs
		WHERE organization_id = $1
	`, created.Org.ID).Scan(&jobCount); err != nil {
		t.Fatalf("count provisioning jobs: %v", err)
	}
	if jobCount != 0 {
		t.Fatalf("provisioning jobs after organization deletion = %d, want 0", jobCount)
	}
}

func TestDefaultModelProviderProvisioningWaitingBehindProjectDeletionCreatesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newSecretIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "provider-project-delete@example.com", "Provider Project Owner")
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: user.ID, Name: "Provider Project Delete", IdempotencyKey: "provider-project-delete",
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	claim, found, err := store.Organizations().ClaimDefaultModelProviderProvisioningForOrganization(
		ctx,
		created.Org.ID,
	)
	if err != nil || !found {
		t.Fatalf("claim provisioning: found=%t err=%v", found, err)
	}
	actor, err := executionstore.OmnaraActorParams(created.Org.ID, userPrincipal(user.ID))
	if err != nil {
		t.Fatalf("build project deletion actor: %v", err)
	}
	controlTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin project deletion control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	var membershipProjectID ID
	if err := controlTx.QueryRow(ctx, `
		SELECT project_id
		FROM project_memberships
		WHERE org_id = $1 AND project_id = $2
		LIMIT 1
		FOR UPDATE
	`, created.Org.ID, created.Project.ID).Scan(&membershipProjectID); err != nil {
		t.Fatalf("lock default project membership: %v", err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := store.Organizations().DeleteProjectOnceForIntegration(
			ctx,
			created.Org.ID,
			created.Project.ID,
			actor,
		)
		deleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteProjectMemberships", 1)
	completeDone := make(chan error, 1)
	go func() {
		completeDone <- store.Organizations().CompleteDefaultModelProviderProvisioning(
			ctx,
			orglifecycle.CompleteDefaultModelProviderProvisioningInput{
				Claim:           claim,
				Template:        testProvisioningTemplate(),
				CredentialValue: "must-not-be-stored",
			},
		)
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleShared", 1)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release project deletion control transaction: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if err := <-completeDone; !errors.Is(err, orglifecycle.ErrDefaultModelProviderProvisioningSuperseded) {
		t.Fatalf("complete provisioning after project deletion error = %v, want superseded", err)
	}
	var providerCount, secretCount, jobCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*)::integer FROM model_provider_configs WHERE org_id = $1),
			(SELECT count(*)::integer FROM secrets WHERE org_id = $1),
			(SELECT count(*)::integer FROM default_model_provider_provisioning_jobs WHERE organization_id = $1)
	`, created.Org.ID).Scan(&providerCount, &secretCount, &jobCount); err != nil {
		t.Fatalf("load provisioning state after project deletion: %v", err)
	}
	if providerCount != 0 || secretCount != 0 || jobCount != 0 {
		t.Fatalf(
			"provisioning state after project deletion: providers=%d secrets=%d jobs=%d",
			providerCount,
			secretCount,
			jobCount,
		)
	}
}

func TestCreatorAccountDeletionPreservesDefaultModelProviderProvisioning(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newSecretIntegrationStore(pool)
	creator := mustCreateIdentityUser(t, ctx, store, "provider-creator@example.com", "Provider Creator")
	coOwner := mustCreateIdentityUser(t, ctx, store, "provider-co-owner@example.com", "Provider Co-owner")
	template := testProvisioningTemplate()
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: creator.ID, Name: "Provider Creator Org", IdempotencyKey: "provider-creator-org",
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: created.Org.ID, UserID: coOwner.ID, Role: "owner",
	}); err != nil {
		t.Fatalf("add co-owner: %v", err)
	}
	if err := store.Identity().DeleteUserAccount(ctx, creator.ID); err != nil {
		t.Fatalf("delete creator account: %v", err)
	}
	var jobCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM default_model_provider_provisioning_jobs
		WHERE organization_id = $1 AND creator_user_id = $2
	`, created.Org.ID, creator.ID).Scan(&jobCount); err != nil {
		t.Fatalf("count provisioning jobs: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("provisioning jobs after creator deletion = %d, want 1", jobCount)
	}
	mustCompleteDefaultModelProviderProvisioning(
		t,
		ctx,
		store,
		created.Org.ID,
		template,
		"cluster-value",
	)
}

func testProvisioningTemplate() modelstore.DefaultModelProviderTemplate {
	return modelstore.DefaultModelProviderTemplate{
		Provisioner:          "test",
		Name:                 "default-provider",
		CredentialSecretName: "default-provider-key",
		APIFormat:            "openai-chat-completions",
		BaseURL:              "https://models.example.test/v1",
		Models: []modelstore.DefaultConfiguredModelTemplate{{
			Name: "default-model", ProviderModelSlug: "example/default",
			ContextWindowTokens: 8192, MaxOutputTokens: 1024,
		}},
	}
}
