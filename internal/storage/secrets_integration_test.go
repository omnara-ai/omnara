//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func oauthSecretMaterialForTest(
	accessToken, refreshToken string,
	lifetime secrets.OAuthAccessTokenLifetime,
) secrets.OAuthTokenSetMaterial {
	material := secrets.OAuthTokenSetMaterial{
		AccessToken:         accessToken,
		AccessTokenLifetime: lifetime,
	}
	if refreshToken != "" {
		material.Refresh = &secrets.OAuthRefreshMaterial{
			RefreshToken:  refreshToken,
			TokenEndpoint: "https://auth.example.com/token",
			ClientID:      "client-id",
			Resource:      "https://example.com/mcp",
		}
	}
	return material
}

func TestUpdateSecretMetadataUsesPostLockDatabaseTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Secret metadata lock", authz.OrgRoleAdmin)
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "metadata-lock",
		Material:  secrets.GenericMaterial{Value: "secret"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin secret lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	var blockingPID int32
	if err := lockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get secret lock backend: %v", err)
	}
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM secrets WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		secret.ID,
	); err != nil {
		t.Fatalf("lock secret: %v", err)
	}
	type updateResult struct {
		record secretstore.SecretRecord
		err    error
	}
	done := make(chan updateResult, 1)
	go func() {
		record, updateErr := store.Secrets().UpdateSecretMetadata(context.Background(), secretstore.UpdateSecretMetadataInput{
			OrgID:    testOrgID,
			SecretID: secret.ID,
			Name:     "metadata-lock-updated",
			Actor:    userPrincipal(admin.ID),
		})
		done <- updateResult{record: record, err: updateErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "FROM secrets", blockingPID)
	var releaseFloor time.Time
	if err := lockTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
		t.Fatalf("read secret lock release floor: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release secret lock: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("update secret metadata: %v", result.err)
	}
	if result.record.UpdatedAt.Before(releaseFloor) {
		t.Fatalf("updated_at = %s, want at or after lock release floor %s", result.record.UpdatedAt, releaseFloor)
	}
}

func TestSecretsStorageEncryptsVersionsAndListsProjectAvailability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Secrets Admin", "admin")
	developer := createSecretTestUser(t, ctx, store, "Secrets Developer", "member")
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			UserID:    developer.ID,
			Role:      "developer",
		},
	); err != nil {
		t.Fatalf("add project membership: %v", err)
	}

	orgSecret, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "openai",
		Metadata:  resourcemeta.Metadata{"label": "OpenAI", "suite": "availability"},
		Material:  secrets.GenericMaterial{Value: "sk-secret-openai"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org secret: %v", err)
	}
	if orgSecret.ID == NilID || orgSecret.CurrentVersionID != version.ID || orgSecret.CurrentVersionNumber != 1 ||
		version.VersionNumber != 1 {
		t.Fatalf("unexpected org secret/version: secret=%+v version=%+v", orgSecret, version)
	}
	assertNoPlaintextInSecretVersions(t, ctx, store, "sk-secret-openai")
	assertDecryptsCurrentSecretVersion(
		t,
		ctx,
		store,
		newSecretIntegrationKeyWrapper(),
		orgSecret,
		secrets.Payload{secrets.KeyValue: "sk-secret-openai"},
	)

	projectSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "project-openai",
		Metadata:       resourcemeta.Metadata{"label": "Project OpenAI", "suite": "availability"},
		Material:       secrets.GenericMaterial{Value: "sk-secret-project"},
		Actor:          userPrincipal(developer.ID),
	})
	if err != nil {
		t.Fatalf("create project secret: %v", err)
	}
	for _, role := range []string{authz.OrgRoleOwner, authz.OrgRoleAdmin, authz.OrgRoleMember} {
		roleUser := createSecretTestUser(t, ctx, store, "Org visibility "+role, role)
		page, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{OrgID: testOrgID, Actor: userPrincipal(roleUser.ID), Limit: 10})
		if err != nil {
			t.Fatalf("list canonical secrets for org role %s: %v", role, err)
		}
		if got, want := containsSecret(page.Secrets, orgSecret.ID), authz.OrgRoleAllows(role, authz.OrgSecretsList); got != want {
			t.Fatalf("org role %s SQL visibility = %v, authz allows = %v", role, got, want)
		}
	}
	for _, role := range []string{
		authz.ProjectRoleAdmin,
		authz.ProjectRoleDeveloper,
		authz.ProjectRoleOperator,
		authz.ProjectRoleViewer,
	} {
		roleUser := createSecretTestUser(t, ctx, store, "Project visibility "+role, authz.OrgRoleMember)
		if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
			OrgID: testOrgID, ProjectID: testProjectID, UserID: roleUser.ID, Role: role,
		}); err != nil {
			t.Fatalf("add project role %s: %v", role, err)
		}
		page, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{OrgID: testOrgID, Actor: userPrincipal(roleUser.ID), Limit: 10})
		if err != nil {
			t.Fatalf("list canonical secrets for project role %s: %v", role, err)
		}
		if got, want := containsSecret(page.Secrets, projectSecret.ID), authz.ProjectRoleAllows(role, authz.ProjectSecretsList); got != want {
			t.Fatalf("project role %s SQL visibility = %v, authz allows = %v", role, got, want)
		}
	}
	assertNoPlaintextInSecretVersions(t, ctx, store, "sk-secret-project")
	adminVisible, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{OrgID: testOrgID, Actor: userPrincipal(admin.ID), Limit: 10})
	if err != nil {
		t.Fatalf("list canonical secrets as admin: %v", err)
	}
	if !containsSecret(adminVisible.Secrets, orgSecret.ID) || !containsSecret(adminVisible.Secrets, projectSecret.ID) {
		t.Fatalf("admin canonical list missing owners: %+v", adminVisible.Secrets)
	}
	developerVisible, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{OrgID: testOrgID, Actor: userPrincipal(developer.ID), Limit: 10})
	if err != nil {
		t.Fatalf("list canonical secrets as developer: %v", err)
	}
	if containsSecret(developerVisible.Secrets, orgSecret.ID) || !containsSecret(developerVisible.Secrets, projectSecret.ID) {
		t.Fatalf("developer canonical visibility mismatch: %+v", developerVisible.Secrets)
	}
	if _, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{
		OrgID: testOrgID, Actor: userPrincipal(developer.ID),
		Filters: secretstore.SecretListFilters{OwnerKind: secretstore.SecretOwnerOrg}, Limit: 10,
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("explicit org-owner list as developer error = %v, want ErrUnauthorized", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        projectSecret.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(admin.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrInvalidSecretRequest,
	) {
		t.Fatalf("self-grant project secret error = %v, want ErrInvalidSecretRequest", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO secret_grants(id, org_id, secret_id, target_project_id, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, uuid.New(), testOrgID, projectSecret.ID, testProjectID, now.Add(1500*time.Millisecond)); err == nil {
		t.Fatal("direct self-grant project secret succeeded")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("direct self-grant error = %v, want check violation", err)
		}
	}

	available, err := store.Secrets().AuthorizeSecretForProjectReference(
		ctx,
		secretstore.AuthorizeSecretForProjectReferenceInput{OrgID: testOrgID, ProjectID: testProjectID, SecretID: orgSecret.ID},
	)
	if err != nil {
		t.Fatalf("authorize org secret before grant: %v", err)
	}
	if available {
		t.Fatal("org-owned secret should not be project-available before explicit grant")
	}
	grant, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        orgSecret.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create secret grant: %v", err)
	}
	if grant.ID == NilID || grant.TargetProjectID != testProjectID {
		t.Fatalf("unexpected grant: %+v", grant)
	}
	available, err = store.Secrets().AuthorizeSecretForProjectReference(
		ctx,
		secretstore.AuthorizeSecretForProjectReferenceInput{OrgID: testOrgID, ProjectID: testProjectID, SecretID: orgSecret.ID},
	)
	if err != nil {
		t.Fatalf("authorize org secret after grant: %v", err)
	}
	if !available {
		t.Fatal("org-owned secret should be project-available after explicit grant")
	}
	newestOrgSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "anthropic",
		Metadata:  resourcemeta.Metadata{"label": "Anthropic", "suite": "availability"},
		Material:  secrets.GenericMaterial{Value: "sk-secret-anthropic"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create newest org secret: %v", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        newestOrgSecret.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(admin.ID),
		},
	); err != nil {
		t.Fatalf("grant newest org secret: %v", err)
	}
	developerPage, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{
		OrgID: testOrgID, Actor: userPrincipal(developer.ID),
		Filters: secretstore.SecretListFilters{Metadata: map[string]string{"suite": "availability"}}, Limit: 1,
	})
	if err != nil {
		t.Fatalf("list developer canonical page: %v", err)
	}
	if len(developerPage.Secrets) != 1 || developerPage.Secrets[0].ID != projectSecret.ID || developerPage.HasMore {
		t.Fatalf("authorization-before-limit mismatch: %+v", developerPage)
	}
	wantCanonicalPages := []ID{newestOrgSecret.ID, projectSecret.ID, orgSecret.ID}
	var gotCanonicalPages []ID
	var canonicalAfter listing.Cursor
	for {
		page, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{
			OrgID: testOrgID, Actor: userPrincipal(admin.ID),
			Filters: secretstore.SecretListFilters{Metadata: map[string]string{"suite": "availability"}},
			Limit:   1, List: listing.Options{After: canonicalAfter},
		})
		if err != nil {
			t.Fatalf("list paged canonical secrets: %v", err)
		}
		if len(page.Secrets) != 1 {
			t.Fatalf("canonical page returned %d rows, want 1", len(page.Secrets))
		}
		secret := page.Secrets[0]
		gotCanonicalPages = append(gotCanonicalPages, secret.ID)
		if !page.HasMore {
			break
		}
		canonicalAfter = page.Next
	}
	if !reflect.DeepEqual(gotCanonicalPages, wantCanonicalPages) {
		t.Fatalf("canonical pages = %v, want %v", gotCanonicalPages, wantCanonicalPages)
	}

	projectSecretPage, err := store.Secrets().ListProjectAvailableSecrets(
		ctx,
		secretstore.ListProjectAvailableSecretsInput{OrgID: testOrgID, ProjectID: testProjectID, Limit: 10},
	)
	if err != nil {
		t.Fatalf("list project available secrets: %v", err)
	}
	outsider, err := store.Identity().CreateUser(ctx, identitystore.CreateUserInput{DisplayName: "Availability Outsider"})
	if err != nil {
		t.Fatalf("create availability outsider: %v", err)
	}
	if _, err := store.Secrets().ListProjectAvailableSecretsForPrincipal(ctx, secretstore.ListProjectAvailableSecretsForPrincipalInput{
		ListProjectAvailableSecretsInput: secretstore.ListProjectAvailableSecretsInput{
			OrgID: testOrgID, ProjectID: testProjectID, Limit: 10,
		},
		Actor: userPrincipal(outsider.ID),
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("list project availability as outsider error = %v, want ErrUnauthorized", err)
	}
	if _, err := store.Secrets().GetProjectAvailableSecretForPrincipal(
		ctx, testOrgID, testProjectID, projectSecret.ID, userPrincipal(outsider.ID),
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("get project availability as outsider error = %v, want ErrUnauthorized", err)
	}
	projectSecrets := secretsFromAccesses(projectSecretPage.Accesses)
	if len(projectSecrets) != 3 {
		t.Fatalf("project available secret count = %d, want 3: %+v", len(projectSecrets), projectSecrets)
	}
	if !containsSecret(projectSecrets, orgSecret.ID) || !containsSecret(projectSecrets, projectSecret.ID) ||
		!containsSecret(projectSecrets, newestOrgSecret.ID) {
		t.Fatalf("project list missing expected secrets: %+v", projectSecrets)
	}
	for _, projectListed := range projectSecrets {
		if projectListed.ID == orgSecret.ID {
			assertJSONRawEqual(t, projectListed.Metadata, `{"label":"OpenAI","suite":"availability"}`)
		}
	}
	for _, access := range projectSecretPage.Accesses {
		if access.Secret.ID == projectSecret.ID && access.Availability.Source != secretstore.SecretAvailabilityDirect {
			t.Fatalf("project-owned secret availability = %+v", access.Availability)
		}
		if access.Secret.ID == orgSecret.ID && access.Availability.Source != secretstore.SecretAvailabilityGrant {
			t.Fatalf("granted org secret availability = %+v", access.Availability)
		}
	}
	wantProjectPages := []ID{newestOrgSecret.ID, projectSecret.ID, orgSecret.ID}
	var gotProjectPages []ID
	var afterSecret listing.Cursor
	for {
		page, err := store.Secrets().ListProjectAvailableSecrets(
			ctx,
			secretstore.ListProjectAvailableSecretsInput{
				OrgID: testOrgID, ProjectID: testProjectID, Limit: 1,
				List: listing.Options{After: afterSecret},
			},
		)
		if err != nil {
			t.Fatalf("list paged project available secrets: %v", err)
		}
		if len(page.Accesses) != 1 {
			t.Fatalf("paged project available secrets returned %d rows, want 1", len(page.Accesses))
		}
		secret := page.Accesses[0].Secret
		gotProjectPages = append(gotProjectPages, secret.ID)
		if !page.HasMore {
			break
		}
		afterSecret = page.Next
	}
	if !reflect.DeepEqual(gotProjectPages, wantProjectPages) {
		t.Fatalf("paged project available secrets = %v, want %v", gotProjectPages, wantProjectPages)
	}
	filteredProjectSecretPage, err := store.Secrets().ListProjectAvailableSecrets(
		ctx,
		secretstore.ListProjectAvailableSecretsInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			Filters:   secretstore.SecretListFilters{Metadata: map[string]string{"label": "OpenAI"}},
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list project secrets by metadata: %v", err)
	}
	filteredProjectSecrets := secretsFromAccesses(filteredProjectSecretPage.Accesses)
	if len(filteredProjectSecrets) != 1 || filteredProjectSecrets[0].ID != orgSecret.ID {
		t.Fatalf("project metadata filter mismatch: %+v", filteredProjectSecrets)
	}
	orgSecretPage, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{OrgID: testOrgID, Actor: userPrincipal(admin.ID), Filters: secretstore.SecretListFilters{OwnerKind: secretstore.SecretOwnerOrg}, Limit: 10})
	if err != nil {
		t.Fatalf("list org secrets: %v", err)
	}
	orgSecrets := orgSecretPage.Secrets
	if !containsSecret(orgSecrets, orgSecret.ID) || containsSecret(orgSecrets, projectSecret.ID) {
		t.Fatalf("org owner list mismatch: %+v", orgSecrets)
	}
	for _, listed := range orgSecrets {
		if listed.ID == orgSecret.ID {
			assertJSONRawEqual(t, listed.Metadata, `{"label":"OpenAI","suite":"availability"}`)
		}
	}
	filteredOrgSecretPage, err := store.Secrets().ListSecrets(
		ctx,
		secretstore.ListSecretsInput{
			OrgID: testOrgID, Actor: userPrincipal(admin.ID),
			Filters: secretstore.SecretListFilters{OwnerKind: secretstore.SecretOwnerOrg, Metadata: map[string]string{"label": "OpenAI"}},
			Limit:   10,
		},
	)
	if err != nil {
		t.Fatalf("list org secrets by metadata: %v", err)
	}
	filteredOrgSecrets := filteredOrgSecretPage.Secrets
	if len(filteredOrgSecrets) != 1 || filteredOrgSecrets[0].ID != orgSecret.ID {
		t.Fatalf("org metadata filter mismatch: %+v", filteredOrgSecrets)
	}

	updated, replacement, err := store.Secrets().CreateSecretVersion(
		ctx,
		secretstore.CreateSecretVersionInput{
			OrgID:    testOrgID,
			SecretID: orgSecret.ID,
			Material: secrets.GenericMaterial{Value: "sk-secret-openai-2"},
			Actor:    userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create replacement version: %v", err)
	}
	if replacement.VersionNumber != 2 || updated.CurrentVersionID != replacement.ID || updated.CurrentVersionNumber != 2 {
		t.Fatalf("replacement version not current: secret=%+v version=%+v", updated, replacement)
	}
	assertNoPlaintextInSecretVersions(t, ctx, store, "sk-secret-openai-2")
	assertDecryptsCurrentSecretVersion(
		t,
		ctx,
		store,
		newSecretIntegrationKeyWrapper(),
		updated,
		secrets.Payload{secrets.KeyValue: "sk-secret-openai-2"},
	)
	payload, err := store.Secrets().ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  orgSecret.ID,
			Kind:      secretstore.SecretKindGeneric,
		},
	)
	if err != nil {
		t.Fatalf("read project available secret payload: %v", err)
	}
	if !reflect.DeepEqual(payload.Payload, secrets.Payload{secrets.KeyValue: "sk-secret-openai-2"}) {
		t.Fatalf("project available payload = %+v, want current secret payload", payload)
	}
	if payload.CurrentVersionID != updated.CurrentVersionID {
		t.Fatalf("project available payload version = %v, want %v", payload.CurrentVersionID, updated.CurrentVersionID)
	}
	if _, err := store.Secrets().ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  orgSecret.ID,
			Kind:      secretstore.SecretKindOAuthTokenSet,
		},
	); !errors.Is(
		err,
		storeerr.ErrInvalidSecretRequest,
	) {
		t.Fatalf("read project available secret with wrong kind error = %v, want invalid secret request", err)
	}
	oauthSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "project-oauth",
		Material: oauthSecretMaterialForTest(
			"access-old",
			"refresh-old",
			secrets.FixedOAuthAccessTokenLifetime(time.Hour),
		),
		Actor: userPrincipal(developer.ID),
	})
	if err != nil {
		t.Fatalf("create project oauth secret: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE secret_versions
		SET created_at = transaction_timestamp() - interval '2 hours',
		    oauth_access_token_expires_at = transaction_timestamp() - interval '1 hour'
			WHERE org_id = $1 AND secret_id = $2
	`, testOrgID, oauthSecret.CurrentVersionID); err != nil {
		t.Fatalf("expire project oauth access token: %v", err)
	}
	refreshLease, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  oauthSecret.ID,
			TTL:       30 * time.Second,
		},
	)
	if err != nil || !acquired {
		t.Fatalf("acquire oauth rotation lease acquired=%v err=%v", acquired, err)
	}
	manualOAuth, _, err := store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID:    testOrgID,
		SecretID: oauthSecret.ID,
		Material: oauthSecretMaterialForTest("access-manual", "refresh-manual", secrets.FixedOAuthAccessTokenLifetime(time.Hour)),
		Actor:    userPrincipal(developer.ID),
	})
	if err != nil {
		t.Fatalf("manual OAuth version during refresh lease: %v", err)
	}
	if manualOAuth.CurrentVersionNumber != oauthSecret.CurrentVersionNumber+1 {
		t.Fatalf("manual OAuth version = %d, want %d", manualOAuth.CurrentVersionNumber, oauthSecret.CurrentVersionNumber+1)
	}
	staleLease := refreshLease
	staleLease.ExpectedCurrentVersionID = uuid.New()
	if _, err := store.Secrets().RotateProjectAvailableOAuthSecret(
		ctx,
		secretstore.RotateProjectAvailableOAuthSecretInput{
			ProjectID: testProjectID,
			Lease:     staleLease,
			Material: oauthSecretMaterialForTest(
				"access-mismatch",
				"refresh-mismatch",
				secrets.FixedOAuthAccessTokenLifetime(time.Hour),
			),
		},
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("rotate oauth secret with stale version expected ErrConflict, got %v", err)
	}
	if _, err := store.Secrets().RotateProjectAvailableOAuthSecret(
		ctx,
		secretstore.RotateProjectAvailableOAuthSecretInput{
			ProjectID: testProjectID,
			Lease:     refreshLease,
			Material: oauthSecretMaterialForTest(
				"access-new",
				"refresh-new",
				secrets.FixedOAuthAccessTokenLifetime(time.Hour),
			),
		},
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("rotate OAuth secret after manual replacement error = %v, want ErrConflict", err)
	}
	if err := store.Secrets().ReleaseProjectOAuthRefreshLease(ctx, refreshLease); err != nil {
		t.Fatalf("release oauth rotation lease: %v", err)
	}
	if _, _, err := store.Secrets().AcquireProjectOAuthRefreshLease(ctx, secretstore.AcquireProjectOAuthRefreshLeaseInput{
		OrgID: testOrgID, ProjectID: testProjectID, SecretID: oauthSecret.ID, TTL: time.Nanosecond,
	}); !errors.Is(err, storeerr.ErrInvalidSecretRequest) {
		t.Fatalf("sub-millisecond oauth refresh lease error = %v, want ErrInvalidSecretRequest", err)
	}
	maximumTTL := time.Duration(1<<63 - 1)
	maximumLease, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID: testOrgID, ProjectID: testProjectID, SecretID: oauthSecret.ID, TTL: maximumTTL,
		},
	)
	if err != nil || !acquired {
		t.Fatalf("acquire maximum oauth refresh lease acquired=%v err=%v", acquired, err)
	}
	if maximumLease.ExpectedCurrentVersionID != manualOAuth.CurrentVersionID {
		t.Fatalf(
			"maximum oauth refresh lease version = %s, want %s",
			maximumLease.ExpectedCurrentVersionID,
			manualOAuth.CurrentVersionID,
		)
	}
	if err := store.Secrets().ReleaseProjectOAuthRefreshLease(ctx, maximumLease); err != nil {
		t.Fatalf("release maximum oauth refresh lease: %v", err)
	}
	oauthPayloadRecord, err := store.Secrets().ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  oauthSecret.ID,
			Kind:      secretstore.SecretKindOAuthTokenSet,
		},
	)
	if err != nil {
		t.Fatalf("read rotated oauth payload: %v", err)
	}
	oauthPayload := oauthPayloadRecord.Payload
	if oauthPayloadRecord.CurrentVersionID != manualOAuth.CurrentVersionID {
		t.Fatalf(
			"manual oauth payload version = %v, want %v",
			oauthPayloadRecord.CurrentVersionID,
			manualOAuth.CurrentVersionID,
		)
	}
	if oauthPayload[secrets.KeyAccessToken] != "access-manual" ||
		oauthPayload[secrets.KeyRefreshToken] != "refresh-manual" {
		t.Fatalf("manual oauth payload = %+v, want replacement token material", oauthPayload)
	}
	firstLease, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  oauthSecret.ID,
			TTL:       30 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("acquire first oauth refresh lease: %v", err)
	}
	if !acquired || firstLease.OwnerToken == NilID {
		t.Fatalf("first oauth refresh lease acquired=%v lease=%+v, want owner", acquired, firstLease)
	}
	if _, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  oauthSecret.ID,
			TTL:       30 * time.Second,
		},
	); err != nil ||
		acquired {
		t.Fatalf("second oauth refresh lease acquired=%v err=%v, want busy", acquired, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE secret_oauth_refresh_leases
		SET updated_at = statement_timestamp() - interval '2 seconds',
		    expires_at = statement_timestamp() - interval '1 second'
		WHERE org_id = $1 AND secret_id = $2
	`, testOrgID, oauthSecret.ID); err != nil {
		t.Fatalf("expire first oauth refresh lease: %v", err)
	}
	takeoverLease, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  oauthSecret.ID,
			TTL:       30 * time.Second,
		},
	)
	if err != nil || !acquired {
		t.Fatalf("take over expired oauth refresh lease acquired=%v err=%v, want acquired", acquired, err)
	}
	rotationInput := secretstore.RotateProjectAvailableOAuthSecretInput{
		ProjectID: testProjectID,
		Material: oauthSecretMaterialForTest(
			"access-takeover",
			"refresh-takeover",
			secrets.FixedOAuthAccessTokenLifetime(time.Hour),
		),
	}
	rotationInput.Lease = firstLease
	if _, err := store.Secrets().RotateProjectAvailableOAuthSecret(ctx, rotationInput); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("rotate with superseded oauth refresh lease error = %v, want ErrConflict", err)
	}
	rotationInput.Lease = takeoverLease
	takeoverOAuth, err := store.Secrets().RotateProjectAvailableOAuthSecret(ctx, rotationInput)
	if err != nil {
		t.Fatalf("rotate with takeover oauth refresh lease: %v", err)
	}
	if err := store.Secrets().ReleaseProjectOAuthRefreshLease(ctx, takeoverLease); err != nil {
		t.Fatalf("release takeover oauth refresh lease: %v", err)
	}
	manuallyUpdatedOAuth, _, err := store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID:    testOrgID,
		SecretID: oauthSecret.ID,
		Material: oauthSecretMaterialForTest("access-manual", "refresh-manual", secrets.FixedOAuthAccessTokenLifetime(time.Hour)),
		Actor:    userPrincipal(developer.ID),
	})
	if err != nil {
		t.Fatalf("manual OAuth version after refresh lease release: %v", err)
	}
	if manuallyUpdatedOAuth.CurrentVersionNumber != takeoverOAuth.CurrentVersionNumber+1 {
		t.Fatalf(
			"manual OAuth version number = %d, want %d",
			manuallyUpdatedOAuth.CurrentVersionNumber,
			takeoverOAuth.CurrentVersionNumber+1,
		)
	}

	revoked, err := store.Secrets().DeleteSecretGrant(
		ctx,
		secretstore.DeleteSecretGrantInput{
			OrgID: testOrgID, SecretID: grant.SecretID,
			GrantID: grant.ID, Actor: userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if revoked.ID != grant.ID {
		t.Fatalf("revoked grant = %+v, want %+v", revoked, grant)
	}
	available, err = store.Secrets().AuthorizeSecretForProjectReference(
		ctx,
		secretstore.AuthorizeSecretForProjectReferenceInput{OrgID: testOrgID, ProjectID: testProjectID, SecretID: orgSecret.ID},
	)
	if err != nil {
		t.Fatalf("authorize org secret after revoke: %v", err)
	}
	if available {
		t.Fatal("org-owned secret should not be project-available after grant revoke")
	}

	deleted, err := store.Secrets().DeleteSecret(
		ctx,
		secretstore.DeleteSecretInput{OrgID: testOrgID, SecretID: orgSecret.ID, Actor: userPrincipal(admin.ID)},
	)
	if err != nil {
		t.Fatalf("delete org secret: %v", err)
	}
	if deleted.ID != orgSecret.ID {
		t.Fatalf("deleted secret = %+v, want %+v", deleted, orgSecret)
	}
	assertSecretRowsDeleted(t, ctx, store, orgSecret.ID)
}

func TestOAuthAccessTokenLifetimeAgesUntilSecretVersionPersistence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "OAuth Lifetime Admin", "admin")
	lifetime := secrets.NewOAuthAccessTokenLifetime(10*time.Minute, time.Now().Add(-2*time.Minute))
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "oauth-lifetime-aging",
		Material:       oauthSecretMaterialForTest("access-token", "", lifetime),
		Actor:          userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create oauth secret: %v", err)
	}
	var remainingSeconds float64
	if err := pool.QueryRow(ctx, `
		SELECT extract(epoch FROM oauth_access_token_expires_at - statement_timestamp())
		FROM secret_versions
		WHERE org_id = $1 AND secret_id = $2 AND id = $3
	`, testOrgID, secret.ID, secret.CurrentVersionID).Scan(&remainingSeconds); err != nil {
		t.Fatalf("load oauth access token lifetime: %v", err)
	}
	remaining := time.Duration(remainingSeconds * float64(time.Second))
	if remaining <= 7*time.Minute || remaining > 8*time.Minute {
		t.Fatalf("persisted oauth lifetime = %s, want aged lifetime between 7m and 8m", remaining)
	}
}

func TestOAuthRefreshLeaseExpiryIsCheckedAfterRowLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "OAuth Lease Lock Admin", "admin")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "oauth-lease-lock",
		Material: oauthSecretMaterialForTest(
			"access-old",
			"refresh-old",
			secrets.FixedOAuthAccessTokenLifetime(time.Hour),
		),
		Actor: userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create oauth secret: %v", err)
	}
	lease, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(ctx, secretstore.AcquireProjectOAuthRefreshLeaseInput{
		OrgID: testOrgID, ProjectID: testProjectID, SecretID: secret.ID, TTL: time.Minute,
	})
	if err != nil || !acquired {
		t.Fatalf("acquire oauth refresh lease acquired=%v err=%v", acquired, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE secret_oauth_refresh_leases
		SET expires_at = statement_timestamp() + interval '250 milliseconds'
		WHERE org_id = $1 AND secret_id = $2 AND owner_token = $3
	`, lease.OrgID, lease.SecretID, lease.OwnerToken); err != nil {
		t.Fatalf("shorten oauth refresh lease: %v", err)
	}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin oauth refresh lease blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	var blockingPID int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("load oauth refresh lease blocker pid: %v", err)
	}
	if err := blocker.QueryRow(ctx, `
		SELECT owner_token
		FROM secret_oauth_refresh_leases
		WHERE org_id = $1 AND secret_id = $2 AND owner_token = $3
		FOR UPDATE
	`, lease.OrgID, lease.SecretID, lease.OwnerToken).Scan(new(ID)); err != nil {
		t.Fatalf("lock oauth refresh lease: %v", err)
	}

	rotationResult := make(chan error, 1)
	go func() {
		_, rotateErr := store.Secrets().RotateProjectAvailableOAuthSecret(ctx, secretstore.RotateProjectAvailableOAuthSecretInput{
			ProjectID: testProjectID,
			Lease:     lease,
			Material: oauthSecretMaterialForTest(
				"access-new",
				"refresh-new",
				secrets.FixedOAuthAccessTokenLifetime(time.Hour),
			),
		})
		rotationResult <- rotateErr
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "secret_oauth_refresh_leases", blockingPID)
	if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.3)`); err != nil {
		t.Fatalf("wait for oauth refresh lease expiry: %v", err)
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release oauth refresh lease blocker: %v", err)
	}
	if err := <-rotationResult; !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("rotation after lease expiry error = %v, want ErrConflict", err)
	}
}

func TestSecretGrantRevocationWaitsForInFlightOAuthRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "OAuth Grant Revocation Admin", "admin")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "oauth-grant-revocation",
		Material: oauthSecretMaterialForTest(
			"access-old",
			"refresh-old",
			secrets.FixedOAuthAccessTokenLifetime(time.Hour),
		),
		Actor: userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create OAuth secret: %v", err)
	}
	grant, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID:           testOrgID,
		SecretID:        secret.ID,
		TargetProjectID: testProjectID,
		Actor:           userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create secret grant: %v", err)
	}
	lease, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID: testOrgID, ProjectID: testProjectID, SecretID: secret.ID, TTL: time.Minute,
		},
	)
	if err != nil || !acquired {
		t.Fatalf("acquire OAuth refresh lease acquired=%v err=%v", acquired, err)
	}

	controlTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin OAuth lease control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	if err := controlTx.QueryRow(ctx, `
		SELECT owner_token
		FROM secret_oauth_refresh_leases
		WHERE org_id = $1 AND secret_id = $2 AND owner_token = $3
		FOR UPDATE
	`, lease.OrgID, lease.SecretID, lease.OwnerToken).Scan(new(ID)); err != nil {
		t.Fatalf("lock OAuth refresh lease: %v", err)
	}

	rotationDone := make(chan error, 1)
	go func() {
		_, rotateErr := store.Secrets().RotateProjectAvailableOAuthSecret(
			context.Background(),
			secretstore.RotateProjectAvailableOAuthSecretInput{
				ProjectID: testProjectID,
				Lease:     lease,
				Material: oauthSecretMaterialForTest(
					"access-new",
					"refresh-new",
					secrets.FixedOAuthAccessTokenLifetime(time.Hour),
				),
			},
		)
		rotationDone <- rotateErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockSecretOAuthRefreshLease", 1)

	revocationDone := make(chan error, 1)
	go func() {
		_, revokeErr := store.Secrets().DeleteSecretGrant(
			context.Background(),
			secretstore.DeleteSecretGrantInput{
				OrgID: testOrgID, SecretID: secret.ID, GrantID: grant.ID,
				Actor: userPrincipal(admin.ID),
			},
		)
		revocationDone <- revokeErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockSecret", 1)
	select {
	case err := <-revocationDone:
		t.Fatalf("secret grant revocation completed before OAuth rotation: %v", err)
	default:
	}

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release OAuth lease control transaction: %v", err)
	}
	for operation, done := range map[string]<-chan error{
		"rotate OAuth secret": rotationDone,
		"revoke secret grant": revocationDone,
	} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", operation, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting to %s", operation)
		}
	}
	if _, err := store.Secrets().GetSecretGrant(
		ctx,
		testOrgID,
		grant.ID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("secret grant after revocation error = %v, want not found", err)
	}
	rotated, err := store.Secrets().GetSecret(ctx, testOrgID, secret.ID)
	if err != nil {
		t.Fatalf("load rotated OAuth secret: %v", err)
	}
	if rotated.CurrentVersionID == secret.CurrentVersionID {
		t.Fatal("OAuth rotation did not persist before grant revocation")
	}
}

func TestResolveMachineProviderAuthTokenReportsMissingSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, err := store.Execution().ResolveMachineProviderAuthToken(
		ctx,
		testOrgID,
		management.Tenant,
		uuid.New(),
		"",
	)
	if err == nil || err.Error() != "machine pool provider auth secret is unavailable" || errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing provider auth secret error = %v", err)
	}
}

func TestCanonicalSecretPaginationIsStableForEqualTimestamps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Pagination Admin", "admin")
	want := make([]ID, 0, 3)
	for _, name := range []string{"equal-a", "equal-b", "equal-c"} {
		record, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: name,
			Metadata: resourcemeta.Metadata{"pagination": "equal"},
			Material: secrets.GenericMaterial{Value: name}, Actor: userPrincipal(admin.ID),
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		want = append(want, record.ID)
	}
	sort.Slice(want, func(i, j int) bool { return want[i].String() > want[j].String() })
	var got []ID
	var after listing.Cursor
	for {
		page, err := store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{
			OrgID: testOrgID, Actor: userPrincipal(admin.ID),
			Filters: secretstore.SecretListFilters{OwnerKind: secretstore.SecretOwnerOrg, Metadata: map[string]string{"pagination": "equal"}},
			Limit:   1, List: listing.Options{After: after},
		})
		if err != nil {
			t.Fatalf("list canonical page: %v", err)
		}
		if len(page.Secrets) != 1 {
			t.Fatalf("page size = %d, want 1", len(page.Secrets))
		}
		record := page.Secrets[0]
		got = append(got, record.ID)
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equal-timestamp pages = %v, want %v", got, want)
	}
}

func TestDeleteSecretReferencedByMachinePoolReturnsConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Machine Pool Secret Admin", "admin")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "machine-pool-provider-auth",
		Material:  secrets.GenericMaterial{Value: "pool-provider-token"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create provider auth secret: %v", err)
	}
	if _, err := createMachinePoolReferencingSecretForTest(
		ctx,
		store,
		"Secret FK Pool",
		secret.ID,
	); err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	if _, err := store.Secrets().DeleteSecret(
		ctx,
		secretstore.DeleteSecretInput{OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(admin.ID)},
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("delete referenced secret error = %v, want ErrConflict", err)
	}
}

func TestSecretReferenceAdmissionSerializesWithDeletion(t *testing.T) {
	t.Parallel()
	t.Run("reference wins", func(t *testing.T) {
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		admin := createSecretTestUser(t, ctx, store, "Secret Reference Winner", "admin")
		secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID:     testOrgID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "reference-wins",
			Material:  secrets.GenericMaterial{Value: "provider-token"},
			Actor:     userPrincipal(admin.ID),
		})
		if err != nil {
			t.Fatalf("create secret: %v", err)
		}

		controlTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin reference control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockResourceCreation(ctx, dbsqlc.LockResourceCreationParams{
			ResourceKind: "machine_pools",
			Scope:        testOrgID.String(),
		}); err != nil {
			t.Fatalf("lock machine pool creation: %v", err)
		}

		poolName := "Secret Reference Winner Pool"
		type createOutcome struct {
			record executionstore.MachinePoolRecord
			err    error
		}
		createDone := make(chan createOutcome, 1)
		go func() {
			record, createErr := createMachinePoolReferencingSecretForTest(
				ctx,
				store,
				poolName,
				secret.ID,
			)
			createDone <- createOutcome{record: record, err: createErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockResourceCreation", 1)

		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := store.Secrets().DeleteSecretOnceForIntegration(ctx, secretstore.DeleteSecretInput{
				OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(admin.ID),
			})
			deleteDone <- deleteErr
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockSecret", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release reference control transaction: %v", err)
		}

		select {
		case outcome := <-createDone:
			if outcome.err != nil {
				t.Fatalf("create machine pool before secret deletion: %v", outcome.err)
			}
			if outcome.record.ProviderAuthSecretID != secret.ID {
				t.Fatalf("machine pool secret = %s, want %s", outcome.record.ProviderAuthSecretID, secret.ID)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for machine pool creation")
		}
		select {
		case err := <-deleteDone:
			if !errors.Is(err, storeerr.ErrConflict) {
				t.Fatalf("delete newly referenced secret error = %v, want ErrConflict", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for secret deletion")
		}
	})

	t.Run("deletion wins", func(t *testing.T) {
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		admin := createSecretTestUser(t, ctx, store, "Secret Deletion Winner", "admin")
		secret, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID:     testOrgID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "deletion-wins",
			Material:  secrets.GenericMaterial{Value: "provider-token"},
			Actor:     userPrincipal(admin.ID),
		})
		if err != nil {
			t.Fatalf("create secret: %v", err)
		}

		controlTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin deletion control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if _, err := controlTx.Exec(
			ctx,
			`SELECT id FROM secret_versions WHERE org_id = $1 AND secret_id = $2 AND id = $3 FOR UPDATE`,
			testOrgID,
			secret.ID,
			version.ID,
		); err != nil {
			t.Fatalf("lock secret version: %v", err)
		}

		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := store.Secrets().DeleteSecretOnceForIntegration(ctx, secretstore.DeleteSecretInput{
				OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(admin.ID),
			})
			deleteDone <- deleteErr
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteSecretVersions", 1)

		poolName := "Secret Deletion Winner Pool"
		createDone := make(chan error, 1)
		go func() {
			_, createErr := createMachinePoolReferencingSecretForTest(
				ctx,
				store,
				poolName,
				secret.ID,
			)
			createDone <- createErr
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockSecretForReference", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release deletion control transaction: %v", err)
		}

		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("delete secret before reference creation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for secret deletion")
		}
		select {
		case err := <-createDone:
			if !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("create machine pool after secret deletion error = %v, want ErrNotFound", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for machine pool creation")
		}
		var activePoolCount, versionCount int
		if err := pool.QueryRow(
			ctx,
			`SELECT count(*)::integer FROM machine_pools WHERE org_id = $1 AND name = $2 AND deleted_at IS NULL`,
			testOrgID,
			poolName,
		).Scan(&activePoolCount); err != nil {
			t.Fatalf("count machine pools after secret deletion: %v", err)
		}
		if err := pool.QueryRow(
			ctx,
			`SELECT count(*)::integer FROM secret_versions WHERE org_id = $1 AND secret_id = $2`,
			testOrgID,
			secret.ID,
		).Scan(&versionCount); err != nil {
			t.Fatalf("count secret versions after deletion: %v", err)
		}
		if activePoolCount != 0 || versionCount != 0 {
			t.Fatalf(
				"rows after secret deletion: active pools=%d versions=%d, want both zero",
				activePoolCount,
				versionCount,
			)
		}
	})
}

func TestUserOwnedSecretIsTenantBoundAndGrantable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := createSecretTestUser(t, ctx, store, "OAuth User", "member")
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{OrgID: testOrgID, ProjectID: testProjectID, UserID: user.ID, Role: "developer"},
	); err != nil {
		t.Fatalf("add project membership: %v", err)
	}

	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:       testOrgID,
		OwnerKind:   secretstore.SecretOwnerUser,
		OwnerUserID: user.ID,
		Name:        "github-oauth",
		Metadata:    resourcemeta.Metadata{"provider": "github", "external_user_id": "octo"},
		Material:    oauthSecretMaterialForTest("gh-access", "gh-refresh", secrets.OAuthAccessTokenLifetime{}),
		Actor:       userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create user-owned secret: %v", err)
	}
	if secret.OwnerUserID != user.ID || secret.Kind != secretstore.SecretKindOAuthTokenSet {
		t.Fatalf("unexpected user secret: %+v", secret)
	}
	assertNoPlaintextInSecretVersions(t, ctx, store, "gh-access")
	assertNoPlaintextInSecretVersions(t, ctx, store, "gh-refresh")

	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        secret.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(user.ID),
		},
	); err != nil {
		t.Fatalf("grant user secret: %v", err)
	}
	userSecretPage, err := store.Secrets().ListSecrets(
		ctx,
		secretstore.ListSecretsInput{
			OrgID: testOrgID, Actor: userPrincipal(user.ID),
			Filters: secretstore.SecretListFilters{OwnerKind: secretstore.SecretOwnerUser, Metadata: map[string]string{"external_user_id": "octo"}},
			Limit:   10,
		},
	)
	if err != nil {
		t.Fatalf("list user secrets: %v", err)
	}
	userSecrets := userSecretPage.Secrets
	if len(userSecrets) != 1 || userSecrets[0].ID != secret.ID {
		t.Fatalf("user owner list = %+v, want secret %+v", userSecrets, secret)
	}
	available, err := store.Secrets().AuthorizeSecretForProjectReference(
		ctx,
		secretstore.AuthorizeSecretForProjectReferenceInput{OrgID: testOrgID, ProjectID: testProjectID, SecretID: secret.ID},
	)
	if err != nil {
		t.Fatalf("authorize user secret: %v", err)
	}
	if !available {
		t.Fatal("user-owned granted secret should be project-available")
	}
}

func TestSecretVersionKeyRewrapRetiresOldEncryptionKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	oldWrapper, err := secrets.NewLocalKeyWrapper(
		"old-key",
		map[string][]byte{"old-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new old keyWrapper: %v", err)
	}
	store := newIntegrationStore(pool, WithSecretKeyWrapper(oldWrapper))
	admin := createSecretTestUser(t, ctx, store, "Rotation Admin", "admin")
	secret, firstVersion, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:     testOrgID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "rotating",
			Material:  secrets.GenericMaterial{Value: "old-key-secret-v1"},
			Actor:     userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create secret with old key: %v", err)
	}
	if firstVersion.KeyID != "old-key" {
		t.Fatalf("initial key id = %q, want old-key", firstVersion.KeyID)
	}
	updated, secondVersion, err := store.Secrets().CreateSecretVersion(
		ctx,
		secretstore.CreateSecretVersionInput{
			OrgID:    testOrgID,
			SecretID: secret.ID,
			Material: secrets.GenericMaterial{Value: "old-key-secret-v2"},
			Actor:    userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create second old-key version: %v", err)
	}
	if secondVersion.KeyID != "old-key" || secondVersion.VersionNumber != 2 ||
		updated.CurrentVersionID != secondVersion.ID {
		t.Fatalf("unexpected second version: updated=%+v version=%+v", updated, secondVersion)
	}

	rotatedLocalWrapper, err := secrets.NewLocalKeyWrapper("new-key", map[string][]byte{
		"old-key": []byte("0123456789abcdef0123456789abcdef"),
		"new-key": []byte("abcdef0123456789abcdef0123456789"),
	})
	if err != nil {
		t.Fatalf("new rotated keyWrapper: %v", err)
	}
	rotatedWrapper := &testWrappedByKeyWrapper{base: rotatedLocalWrapper, wrappedBy: "external-test"}
	rotatedStore := newIntegrationStore(pool, WithSecretKeyWrapper(rotatedWrapper))
	result, err := rotatedStore.Secrets().RewrapSecretVersionsByKeyID(ctx, "old-key")
	if err != nil {
		t.Fatalf("rewrap old key: %v", err)
	}
	if result.Scanned != 2 || result.Rewrapped != 2 || result.Remaining != 0 {
		t.Fatalf("rewrap result = %+v, want scanned=2 rewrapped=2 remaining=0", result)
	}
	if _, err := rotatedStore.Secrets().RewrapSecretVersionsByKeyID(ctx, "new-key"); err == nil {
		t.Fatal("expected active-key rewrap to fail")
	}
	for _, version := range []secretstore.SecretVersionRecord{firstVersion, secondVersion} {
		row, err := testQueries(rotatedStore).GetSecretVersion(
			ctx,
			dbsqlc.GetSecretVersionParams{OrgID: secret.OrgID, SecretID: secret.ID, ID: version.ID},
		)
		if err != nil {
			t.Fatalf("get rewrapped version %s: %v", version.ID, err)
		}
		if row.KeyID != "new-key" {
			t.Fatalf("rewrapped key id = %q, want new-key", row.KeyID)
		}
		if row.DekWrappedBy != "external-test" {
			t.Fatalf("rewrapped wrapper = %q, want external-test", row.DekWrappedBy)
		}
	}
	oldRows, err := testQueries(rotatedStore).ListSecretVersionsByKeyID(ctx, dbsqlc.ListSecretVersionsByKeyIDParams{KeyID: "old-key"})
	if err != nil {
		t.Fatalf("list old-key versions: %v", err)
	}
	if len(oldRows) != 0 {
		t.Fatalf("old-key versions remain after rewrap: %+v", oldRows)
	}
	newOnlyLocalWrapper, err := secrets.NewLocalKeyWrapper(
		"new-key",
		map[string][]byte{"new-key": []byte("abcdef0123456789abcdef0123456789")},
	)
	if err != nil {
		t.Fatalf("new-only keyWrapper: %v", err)
	}
	newOnlyWrapper := &testWrappedByKeyWrapper{base: newOnlyLocalWrapper, wrappedBy: "external-test"}
	newOnlyStore := newIntegrationStore(pool, WithSecretKeyWrapper(newOnlyWrapper))
	assertDecryptsCurrentSecretVersion(
		t,
		ctx,
		newOnlyStore,
		newOnlyWrapper,
		updated,
		secrets.Payload{secrets.KeyValue: "old-key-secret-v2"},
	)
	assertDecryptsSecretVersion(
		t,
		ctx,
		newOnlyStore,
		newOnlyWrapper,
		updated,
		firstVersion.ID,
		secrets.Payload{secrets.KeyValue: "old-key-secret-v1"},
	)
}

func TestSecretVersionSchemaRejectsInvalidEncryptionEnvelope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 6, 3, 14, 30, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Envelope Admin", "admin")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "schema-envelope",
		Material:  secrets.GenericMaterial{Value: "schema-secret"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}

	cases := []struct {
		name             string
		payloadKeysSQL   string
		encryptionScheme string
		encryptedDEKSQL  string
		ciphertextSQL    string
	}{
		{
			name:             "empty payload keys",
			payloadKeysSQL:   "'{}'::text[]",
			encryptionScheme: secrets.EncryptionSchemeAES256GCMEnvelopeV1,
			encryptedDEKSQL:  "decode(repeat('01', 48), 'hex')",
			ciphertextSQL:    "decode(repeat('04', 17), 'hex')",
		},
		{
			name:             "null payload key",
			payloadKeysSQL:   "ARRAY[NULL]::text[]",
			encryptionScheme: secrets.EncryptionSchemeAES256GCMEnvelopeV1,
			encryptedDEKSQL:  "decode(repeat('01', 48), 'hex')",
			ciphertextSQL:    "decode(repeat('04', 17), 'hex')",
		},
		{
			name:             "empty payload key",
			payloadKeysSQL:   "ARRAY['']::text[]",
			encryptionScheme: secrets.EncryptionSchemeAES256GCMEnvelopeV1,
			encryptedDEKSQL:  "decode(repeat('01', 48), 'hex')",
			ciphertextSQL:    "decode(repeat('04', 17), 'hex')",
		},
		{
			name:             "unsupported encryption scheme",
			payloadKeysSQL:   "ARRAY['value']::text[]",
			encryptionScheme: "future-scheme",
			encryptedDEKSQL:  "decode(repeat('01', 48), 'hex')",
			ciphertextSQL:    "decode(repeat('04', 17), 'hex')",
		},
		{
			name:             "short local encrypted dek",
			payloadKeysSQL:   "ARRAY['value']::text[]",
			encryptionScheme: secrets.EncryptionSchemeAES256GCMEnvelopeV1,
			encryptedDEKSQL:  "decode(repeat('01', 47), 'hex')",
			ciphertextSQL:    "decode(repeat('04', 17), 'hex')",
		},
		{
			name:             "short payload ciphertext",
			payloadKeysSQL:   "ARRAY['value']::text[]",
			encryptionScheme: secrets.EncryptionSchemeAES256GCMEnvelopeV1,
			encryptedDEKSQL:  "decode(repeat('01', 48), 'hex')",
			ciphertextSQL:    "decode(repeat('04', 16), 'hex')",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.pool.Exec(ctx, `
				INSERT INTO secret_versions(
				    id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by,
				    encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at
					)
					VALUES (
						uuidv7(), $1, $2, 2, `+tt.payloadKeysSQL+`, $3, 'test-key', 'local',
					    `+tt.encryptedDEKSQL+`, decode(repeat('02', 12), 'hex'), decode(repeat('03', 12), 'hex'), `+tt.ciphertextSQL+`,
					    $4::timestamptz
					)
				`, secret.OrgID, secret.ID, tt.encryptionScheme, now.Add(time.Second))
			if !isSecretCheckViolation(err) {
				t.Fatalf("insert invalid envelope error = %v, want check violation", err)
			}
		})
	}
}

func TestSecretSchemaRequiresCurrentVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 6, 3, 14, 40, 0, 0, time.UTC)
	_ = createSecretTestUser(t, ctx, store, "Current Version Admin", "admin")

	_, err := store.pool.Exec(ctx, `
		INSERT INTO secrets(
			id, org_id, management_kind, owner_kind, name, kind, metadata, current_version_id,
			created_at, updated_at
		)
		VALUES (
			uuidv7(), $1, 'tenant', 'org', 'missing-current-version', 'generic', '{}'::jsonb, NULL,
			$2::timestamptz, $2::timestamptz
		)
	`, testOrgID, now)
	// Live secrets must point at a current version; only soft-deleted secrets
	// may have it cleared.
	if !isSQLCheckViolation(err) {
		t.Fatalf("insert secret without current version error = %v, want check violation", err)
	}

	_, err = store.pool.Exec(ctx, `
		INSERT INTO secrets(
			id, org_id, management_kind, owner_kind, name, kind, metadata, current_version_id,
			created_at, updated_at
		)
		VALUES (
			uuidv7(), $1, 'tenant', 'org', 'dangling-current-version', 'generic', '{}'::jsonb, uuidv7(),
			$2::timestamptz, $2::timestamptz
		)
	`, testOrgID, now)
	if !isSecretForeignKeyViolation(err) {
		t.Fatalf("insert secret with dangling current version error = %v, want foreign-key violation", err)
	}
}

func TestSecretAuthorityIsImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Kind Admin", "admin")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "kind-immutable",
		Material:  secrets.GenericMaterial{Value: "secret"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	for _, test := range []struct {
		name  string
		query string
		value any
	}{
		{name: "id", query: `UPDATE secrets SET id = $1 WHERE org_id = $2 AND id = $3`, value: testID("changed-secret-id")},
		{name: "organization", query: `UPDATE secrets SET org_id = $1 WHERE org_id = $2 AND id = $3`, value: testID("changed-secret-org")},
		{name: "management kind", query: `UPDATE secrets SET management_kind = $1 WHERE org_id = $2 AND id = $3`, value: "cluster"},
		{name: "kind", query: `UPDATE secrets SET kind = $1 WHERE org_id = $2 AND id = $3`, value: "oauth_token_set"},
		{name: "owner", query: `UPDATE secrets SET owner_kind = $1 WHERE org_id = $2 AND id = $3`, value: "project"},
	} {
		if _, err := store.pool.Exec(ctx, test.query, test.value, secret.OrgID, secret.ID); !isPgCode(err, "25006") {
			t.Fatalf("update secret %s error = %v, want SQLSTATE 25006", test.name, err)
		}
	}
}

func TestSecretNameAndGrantUniqueness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Secrets Admin", "admin")

	input := secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "shared",
		Material:  secrets.GenericMaterial{Value: "secret"},
		Actor:     userPrincipal(admin.ID),
	}
	tooManyEntries := resourcemeta.Metadata{}
	for i := range resourcemeta.MaxEntries + 1 {
		tooManyEntries[fmt.Sprintf("key-%d", i)] = "value"
	}
	tooManyEntriesInput := input
	tooManyEntriesInput.Name = "too-many-entries-metadata"
	tooManyEntriesInput.Metadata = tooManyEntries
	if _, _, err := store.Secrets().CreateSecret(ctx, tooManyEntriesInput); !errors.Is(err, storeerr.ErrInvalidSecretRequest) {
		t.Fatalf("create secret with too many metadata entries error = %v, want ErrInvalidSecretRequest", err)
	}
	oversizedMetadataInput := input
	oversizedMetadataInput.Name = "oversized-metadata"
	oversizedMetadataInput.Metadata = resourcemeta.Metadata{"value": strings.Repeat("x", resourcemeta.MaxValueLength+1)}
	if _, _, err := store.Secrets().CreateSecret(ctx, oversizedMetadataInput); !errors.Is(err, storeerr.ErrInvalidSecretRequest) {
		t.Fatalf("create secret with oversized metadata error = %v, want ErrInvalidSecretRequest", err)
	}
	first, _, err := store.Secrets().CreateSecret(ctx, input)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if _, _, err := store.Secrets().CreateSecret(ctx, input); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("duplicate owner/name error = %v, want ErrConflict", err)
	}
	renamed, err := store.Secrets().UpdateSecretMetadata(
		ctx,
		secretstore.UpdateSecretMetadataInput{
			OrgID:    testOrgID,
			SecretID: first.ID,
			Name:     "shared-renamed",
			Metadata: resourcemeta.Metadata{"label": "renamed"},
			Actor:    userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("update secret metadata: %v", err)
	}
	if renamed.Name != "shared-renamed" {
		t.Fatalf("updated secret metadata = %+v", renamed)
	}
	assertJSONRawEqual(t, renamed.Metadata, `{"label":"renamed"}`)
	if _, err := store.Secrets().UpdateSecretMetadata(
		ctx,
		secretstore.UpdateSecretMetadataInput{
			OrgID:    testOrgID,
			SecretID: first.ID,
			Name:     "bad-metadata",
			Metadata: resourcemeta.Metadata{"": "value"},
			Actor:    userPrincipal(admin.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrInvalidSecretRequest,
	) {
		t.Fatalf("update secret with empty metadata key error = %v, want ErrInvalidSecretRequest", err)
	}
	secondOrgSecret, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:     testOrgID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "other-shared",
			Material:  secrets.GenericMaterial{Value: "second-secret"},
			Actor:     userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create second org secret: %v", err)
	}
	if _, err := store.Secrets().UpdateSecretMetadata(
		ctx,
		secretstore.UpdateSecretMetadataInput{
			OrgID:    testOrgID,
			SecretID: secondOrgSecret.ID,
			Name:     "shared-renamed",
			Actor:    userPrincipal(admin.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("duplicate rename error = %v, want ErrConflict", err)
	}
	projectOwnedSameName, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:          testOrgID,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: testProjectID,
			Name:           "shared",
			Material:       secrets.GenericMaterial{Value: "project-secret"},
			Actor:          userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("project-owned same name should be allowed in distinct namespace: %v", err)
	}
	if projectOwnedSameName.ID == first.ID {
		t.Fatalf("project-owned same name reused org secret id: %+v", projectOwnedSameName)
	}

	grant, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        first.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	loadedGrant, err := store.Secrets().GetSecretGrant(ctx, testOrgID, grant.ID)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}
	if loadedGrant != grant {
		t.Fatalf("loaded grant = %+v, want %+v", loadedGrant, grant)
	}
	loadedForSecret, err := store.Secrets().GetSecretGrantForSourceSecret(ctx, testOrgID, first.ID, grant.ID)
	if err != nil {
		t.Fatalf("get grant for source secret: %v", err)
	}
	if loadedForSecret != grant {
		t.Fatalf("loaded grant for source secret = %+v, want %+v", loadedForSecret, grant)
	}
	loadedForProject, err := store.Secrets().GetSecretGrantForTargetProject(ctx, testOrgID, testProjectID, grant.ID)
	if err != nil {
		t.Fatalf("get grant for target project: %v", err)
	}
	if loadedForProject != grant {
		t.Fatalf("loaded grant for target project = %+v, want %+v", loadedForProject, grant)
	}
	if _, err := store.Secrets().GetSecretGrantForSourceSecret(
		ctx,
		testOrgID,
		projectOwnedSameName.ID,
		grant.ID,
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("get grant for wrong secret error = %v, want ErrNotFound", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        first.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(admin.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("duplicate grant error = %v, want ErrConflict", err)
	}
	otherProject := testID("secret_other_project")
	if _, err := store.pool.Exec(
		ctx,
		`
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Other Project', 'idem-secret-other-project', $3, $3)
`,
		otherProject,
		testOrgID,
		now,
	); err != nil {
		t.Fatalf("seed other project: %v", err)
	}
	if _, err := store.Secrets().GetSecretGrantForTargetProject(
		ctx,
		testOrgID,
		otherProject,
		grant.ID,
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("get grant for wrong target project error = %v, want ErrNotFound", err)
	}
	otherGrant, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        first.ID,
			TargetProjectID: otherProject,
			Actor:           userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("same secret should be grantable to another project: %v", err)
	}
	if otherGrant.ID == grant.ID {
		t.Fatalf("grant to another project reused original grant id: %+v", otherGrant)
	}
}

func TestSecretTenantBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 6, 3, 15, 0, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Boundary Admin", "admin")
	member := createSecretTestUser(t, ctx, store, "Boundary Member", "member")

	secret, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:     testOrgID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "boundary",
			Material:  secrets.GenericMaterial{Value: "secret"},
			Actor:     userPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if _, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:     testOrgID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "member-org",
			Material:  secrets.GenericMaterial{Value: "secret"},
			Actor:     userPrincipal(member.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("create org secret as member error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:          testOrgID,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: testProjectID,
			Name:           "member-project",
			Material:       secrets.GenericMaterial{Value: "secret"},
			Actor:          userPrincipal(member.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("create project secret without project manage error = %v, want ErrUnauthorized", err)
	}
	if _, err := store.Secrets().UpdateSecretMetadata(
		ctx,
		secretstore.UpdateSecretMetadataInput{
			OrgID:    testOrgID,
			SecretID: secret.ID,
			Name:     "member-rename",
			Actor:    userPrincipal(member.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("update org secret as member error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := store.Secrets().CreateSecretVersion(
		ctx,
		secretstore.CreateSecretVersionInput{
			OrgID:    testOrgID,
			SecretID: secret.ID,
			Material: secrets.GenericMaterial{Value: "new-secret"},
			Actor:    userPrincipal(member.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("rotate org secret as member error = %v, want ErrUnauthorized", err)
	}
	if _, err := store.Secrets().DeleteSecret(
		ctx,
		secretstore.DeleteSecretInput{OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(member.ID)},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("delete org secret as member error = %v, want ErrUnauthorized", err)
	}
	otherOrgID := testID("secret_boundary_other_org")
	otherUser, err := store.Identity().CreateUser(ctx, identitystore.CreateUserInput{DisplayName: "Other Project Creator"})
	if err != nil {
		t.Fatalf("create other project creator: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, 'Other Org', $2, $2)`,
		otherOrgID,
		now,
	); err != nil {
		t.Fatalf("seed other org: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: otherOrgID, UserID: otherUser.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add other org membership: %v", err)
	}
	otherProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{OrgID: otherOrgID, Creator: userPrincipal(otherUser.ID), Name: "Other Project"},
	)
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}

	if _, err := store.Secrets().GetSecret(ctx, otherOrgID, secret.ID); err == nil {
		t.Fatal("expected wrong-org get secret to fail")
	}
	available, err := store.Secrets().AuthorizeSecretForProjectReference(
		ctx,
		secretstore.AuthorizeSecretForProjectReferenceInput{OrgID: otherOrgID, ProjectID: otherProject.ID, SecretID: secret.ID},
	)
	if err != nil {
		t.Fatalf("authorize wrong org: %v", err)
	}
	if available {
		t.Fatal("secret should not be available through a different org")
	}
	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        secret.ID,
			TargetProjectID: otherProject.ID,
			Actor:           userPrincipal(admin.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("grant to cross-org project error = %v, want ErrNotFound", err)
	}

	projectA := testProjectID
	projectB := testID("secret_boundary_project_b")
	if _, err := store.pool.Exec(
		ctx,
		`
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Boundary Project B', 'idem-boundary-project-b', $3, $3)
`,
		projectB,
		testOrgID,
		now,
	); err != nil {
		t.Fatalf("seed project b: %v", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        secret.ID,
			TargetProjectID: projectA,
			Actor:           userPrincipal(admin.ID),
		},
	); err != nil {
		t.Fatalf("grant to project a: %v", err)
	}
	available, err = store.Secrets().AuthorizeSecretForProjectReference(
		ctx,
		secretstore.AuthorizeSecretForProjectReferenceInput{OrgID: testOrgID, ProjectID: projectA, SecretID: secret.ID},
	)
	if err != nil {
		t.Fatalf("authorize project a: %v", err)
	}
	if !available {
		t.Fatal("secret should be available to granted project")
	}
	available, err = store.Secrets().AuthorizeSecretForProjectReference(
		ctx,
		secretstore.AuthorizeSecretForProjectReferenceInput{OrgID: testOrgID, ProjectID: projectB, SecretID: secret.ID},
	)
	if err != nil {
		t.Fatalf("authorize project b: %v", err)
	}
	if available {
		t.Fatal("secret should not be available to an ungranted project")
	}
}

func TestUserOwnedSecretRequiresOwnerActorAndMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	owner := createSecretTestUser(t, ctx, store, "Secret Owner", "member")
	other := createSecretTestUser(t, ctx, store, "Other User", "member")
	viewer := createSecretTestUser(t, ctx, store, "Viewer User", "member")
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{OrgID: testOrgID, ProjectID: testProjectID, UserID: owner.ID, Role: "developer"},
	); err != nil {
		t.Fatalf("add owner project membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{OrgID: testOrgID, ProjectID: testProjectID, UserID: other.ID, Role: "developer"},
	); err != nil {
		t.Fatalf("add other project membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{OrgID: testOrgID, ProjectID: testProjectID, UserID: viewer.ID, Role: "viewer"},
	); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	outsider, err := store.Identity().CreateUser(ctx, identitystore.CreateUserInput{DisplayName: "Outside User"})
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}

	if _, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:       testOrgID,
			OwnerKind:   secretstore.SecretOwnerUser,
			OwnerUserID: owner.ID,
			Name:        "not-yours",
			Material:    secrets.GenericMaterial{Value: "secret"},
			Actor:       userPrincipal(other.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("create user secret as different user error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:       testOrgID,
			OwnerKind:   secretstore.SecretOwnerUser,
			OwnerUserID: outsider.ID,
			Name:        "outsider",
			Material:    secrets.GenericMaterial{Value: "secret"},
			Actor:       userPrincipal(outsider.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("create user secret for non-member error = %v, want ErrUnauthorized", err)
	}

	secret, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:       testOrgID,
			OwnerKind:   secretstore.SecretOwnerUser,
			OwnerUserID: owner.ID,
			Name:        "owned-oauth",
			Material:    oauthSecretMaterialForTest("access", "", secrets.OAuthAccessTokenLifetime{}),
			Actor:       userPrincipal(owner.ID),
		},
	)
	if err != nil {
		t.Fatalf("create owner secret: %v", err)
	}
	if _, err := store.Secrets().UpdateSecretMetadata(
		ctx,
		secretstore.UpdateSecretMetadataInput{OrgID: testOrgID, SecretID: secret.ID, Name: "stolen", Actor: userPrincipal(other.ID)},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("update user secret as other user error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := store.Secrets().CreateSecretVersion(
		ctx,
		secretstore.CreateSecretVersionInput{
			OrgID:    testOrgID,
			SecretID: secret.ID,
			Material: oauthSecretMaterialForTest("new-access", "", secrets.OAuthAccessTokenLifetime{}),
			Actor:    userPrincipal(other.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("rotate user secret as other user error = %v, want ErrUnauthorized", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        secret.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(other.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("grant user secret as other user error = %v, want ErrUnauthorized", err)
	}
	grant, err := store.Secrets().CreateSecretGrant(
		ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           testOrgID,
			SecretID:        secret.ID,
			TargetProjectID: testProjectID,
			Actor:           userPrincipal(owner.ID),
		},
	)
	if err != nil {
		t.Fatalf("grant user secret as owner: %v", err)
	}
	if _, err := store.Secrets().DeleteSecretGrant(
		ctx,
		secretstore.DeleteSecretGrantInput{
			OrgID: testOrgID, SecretID: grant.SecretID,
			GrantID: grant.ID, Actor: userPrincipal(viewer.ID),
		},
	); !errors.Is(
		err,
		storeerr.ErrNotFound,
	) {
		t.Fatalf("revoke user secret grant as viewer error = %v, want ErrNotFound", err)
	}
	if _, err := store.Secrets().DeleteSecretGrant(
		ctx,
		secretstore.DeleteSecretGrantInput{
			OrgID: testOrgID, SecretID: grant.SecretID,
			GrantID: grant.ID, Actor: userPrincipal(other.ID),
		},
	); err != nil {
		t.Fatalf("project manager should be able to revoke project grant: %v", err)
	}
	if _, err := store.Secrets().DeleteSecret(
		ctx,
		secretstore.DeleteSecretInput{OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(other.ID)},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("delete user secret as other user error = %v, want ErrUnauthorized", err)
	}
}

func newSecretIntegrationKeyWrapper() secrets.KeyWrapper {
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		panic(err)
	}
	return keyWrapper
}

func newSecretIntegrationStore(pool *pgxpool.Pool, opts ...Option) *Store {
	allOpts := []Option{
		WithSecretKeyWrapper(newSecretIntegrationKeyWrapper()),
		WithMachinePoolProviders(mergingMachinePoolProviders{}),
	}
	allOpts = append(allOpts, opts...)
	return newIntegrationStore(pool, allOpts...)
}

func createMachinePoolReferencingSecretForTest(
	ctx context.Context,
	store *Store,
	name string,
	secretID ID,
) (executionstore.MachinePoolRecord, error) {
	return store.Execution().CreateMachinePool(ctx, machinePoolInputWithDefaultMachineForTest(
		executionstore.CreateMachinePoolInput{
			OrgID:                testOrgID,
			Name:                 name,
			Provider:             "test.provider",
			ProviderConfig:       json.RawMessage(`{}`),
			ProviderAuthSecretID: secretID,
			MaxTotalMachines:     1,
			MaxTotalCPU:          intPtrForMachinePoolTest(1),
			MaxTotalMemoryMB:     intPtrForMachinePoolTest(1024),
			MaxMachineCPU:        intPtrForMachinePoolTest(1),
			MaxMachineMemoryMB:   intPtrForMachinePoolTest(1024),
		},
		defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`),
		},
	))
}

type testWrappedByKeyWrapper struct {
	base      secrets.KeyWrapper
	wrappedBy string
}

func (w *testWrappedByKeyWrapper) ActiveKeyID(ctx context.Context) (string, error) {
	return w.base.ActiveKeyID(ctx)
}

func (w *testWrappedByKeyWrapper) WrapDataKey(
	ctx context.Context,
	dataKey []byte,
	associatedData []byte,
) (secrets.WrappedDataKey, error) {
	wrapped, err := w.base.WrapDataKey(ctx, dataKey, associatedData)
	if err != nil {
		return secrets.WrappedDataKey{}, err
	}
	wrapped.WrappedBy = w.wrappedBy
	return wrapped, nil
}

func (w *testWrappedByKeyWrapper) UnwrapDataKey(
	ctx context.Context,
	wrapped secrets.WrappedDataKey,
	associatedData []byte,
) ([]byte, error) {
	wrapped.WrappedBy = secrets.DEKWrappedByLocal
	return w.base.UnwrapDataKey(ctx, wrapped, associatedData)
}

func createSecretTestUser(t *testing.T, ctx context.Context, store *Store, name, orgRole string) identitystore.UserRecord {
	t.Helper()
	user, err := store.Identity().CreateUser(
		ctx,
		identitystore.CreateUserInput{DisplayName: name},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{
			OrgID:  testOrgID,
			UserID: user.ID,
			Role:   orgRole,
		},
	); err != nil {
		t.Fatalf("add org membership: %v", err)
	}
	return user
}

func assertNoPlaintextInSecretVersions(t *testing.T, ctx context.Context, store *Store, plaintext string) {
	t.Helper()
	var found bool
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM secret_versions
			WHERE convert_to($1::text, 'UTF8') = ciphertext
			   OR position(convert_to($1::text, 'UTF8') in ciphertext) > 0
			   OR position(convert_to($1::text, 'UTF8') in encrypted_dek) > 0
		 )
	`, plaintext).Scan(&found); err != nil {
		t.Fatalf("scan secret version plaintext check: %v", err)
	}
	if found {
		t.Fatalf("secret_versions contains plaintext %q", plaintext)
	}
}

func assertDecryptsCurrentSecretVersion(
	t *testing.T,
	ctx context.Context,
	store *Store,
	keyWrapper secrets.KeyWrapper,
	secret secretstore.SecretRecord,
	want secrets.Payload,
) {
	t.Helper()
	assertDecryptsSecretVersion(t, ctx, store, keyWrapper, secret, secret.CurrentVersionID, want)
}

func assertDecryptsSecretVersion(
	t *testing.T,
	ctx context.Context,
	store *Store,
	keyWrapper secrets.KeyWrapper,
	secret secretstore.SecretRecord,
	versionID ID,
	want secrets.Payload,
) {
	t.Helper()
	row, err := testQueries(store).GetSecretVersion(
		ctx,
		dbsqlc.GetSecretVersionParams{OrgID: secret.OrgID, SecretID: secret.ID, ID: versionID},
	)
	if err != nil {
		t.Fatalf("get secret version: %v", err)
	}
	got, err := secrets.DecryptPayload(
		ctx,
		keyWrapper,
		secrets.EncryptedPayload{
			EncryptionScheme:  row.EncryptionScheme,
			KeyID:             row.KeyID,
			DEKWrappedBy:      row.DekWrappedBy,
			EncryptedDEK:      row.EncryptedDek,
			EncryptedDEKNonce: row.EncryptedDekNonce,
			Nonce:             row.Nonce,
			Ciphertext:        row.Ciphertext,
			PayloadKeys:       row.PayloadKeys,
		},
		secrets.AssociatedData{
			OrgID:         row.OrgID.String(),
			SecretID:      row.SecretID.String(),
			VersionID:     row.ID.String(),
			VersionNumber: row.VersionNumber,
			Kind:          secret.Kind,
		},
	)
	if err != nil {
		t.Fatalf("decrypt persisted secret version %s: %v", versionID, err)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("decrypted key %q = %q, want %q", key, got[key], value)
		}
	}
}

func assertSecretRowsDeleted(t *testing.T, ctx context.Context, store *Store, secretID ID) {
	t.Helper()
	var softDeleted bool
	var versionsCount, grantsCount, leasesCount int
	if err := store.pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL AND current_version_id IS NULL FROM secrets WHERE id = $1`,
		secretID,
	).Scan(&softDeleted); err != nil {
		t.Fatalf("load secret deleted_at: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*)::int FROM secret_versions WHERE secret_id = $1`, secretID).
		Scan(&versionsCount); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*)::int FROM secret_grants WHERE secret_id = $1`, secretID).
		Scan(&grantsCount); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*)::int FROM secret_oauth_refresh_leases WHERE secret_id = $1`, secretID).
		Scan(&leasesCount); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if !softDeleted || versionsCount != 0 || grantsCount != 0 || leasesCount != 0 {
		t.Fatalf(
			"secret should be softDeleted with ciphertext destroyed: softDeleted=%v versions=%d grants=%d leases=%d",
			softDeleted, versionsCount, grantsCount, leasesCount,
		)
	}
}

func assertJSONRawEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal got json %q: %v", string(got), err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("unmarshal want json %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("json = %s, want %s", string(got), want)
	}
}

func containsSecret(records []secretstore.SecretRecord, id ID) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func secretsFromAccesses(accesses []secretstore.ProjectSecretAccessRecord) []secretstore.SecretRecord {
	records := make([]secretstore.SecretRecord, 0, len(accesses))
	for _, access := range accesses {
		records = append(records, access.Secret)
	}
	return records
}

func isSecretCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func isSecretForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
