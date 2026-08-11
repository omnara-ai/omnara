//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestOrgAPIKeyLifecycleAndAuthorization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)

	creator := mustCreateIdentityUser(t, ctx, store, "key-creator@example.com", "Key Creator")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: creator.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add creator org membership: %v", err)
	}

	otherOrgID := testID("org_api_key_other")
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, 'Other Org', $2, $2)`,
		otherOrgID,
		now,
	); err != nil {
		t.Fatalf("seed other org: %v", err)
	}

	if _, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: creator.ID,
		Name:            "Owner key",
		OrgRole:         authz.OrgRoleOwner,
	}); err == nil {
		t.Fatal("expected owner role to be rejected")
	}

	created, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: creator.ID,
		Name:            "CI key",
		OrgRole:         authz.OrgRoleMember,
	})
	if err != nil {
		t.Fatalf("create org api key: %v", err)
	}
	if created.Record.OrgRole != authz.OrgRoleMember {
		t.Fatalf("unexpected created record: %+v", created.Record)
	}
	var createdMembershipRole string
	if err := pool.QueryRow(
		ctx,
		`SELECT role FROM org_memberships WHERE org_id = $1 AND org_api_key_id = $2`,
		testOrgID,
		created.Record.ID,
	).Scan(&createdMembershipRole); err != nil {
		t.Fatalf("load key org membership: %v", err)
	}
	if createdMembershipRole != authz.OrgRoleMember {
		t.Fatalf("key org membership role = %s, want %s", createdMembershipRole, authz.OrgRoleMember)
	}
	if err := bearertoken.Validate(created.Token, bearertoken.KindOrganization); err != nil {
		t.Fatalf("created token plaintext invalid: %v", err)
	}

	if _, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: creator.ID,
		Name:            "CI key",
		OrgRole:         authz.OrgRoleMember,
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("expected active name conflict, got %v", err)
	}

	principal, err := store.Identity().AuthenticateOrgAPIKey(ctx, created.Token)
	if err != nil {
		t.Fatalf("authenticate org api key: %v", err)
	}
	if principal.Type != identitystore.PrincipalTypeOrgAPIKey ||
		principal.ID != created.Record.ID ||
		principal.OrgID != testOrgID ||
		principal.OrgAPIKeyID != created.Record.ID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	unknownToken, err := bearertoken.Generate(bearertoken.KindOrganization)
	if err != nil {
		t.Fatalf("generate unknown organization token: %v", err)
	}
	if _, err := store.Identity().AuthenticateOrgAPIKey(ctx, unknownToken); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("expected unauthorized for unknown token, got %v", err)
	}

	assertOrgAllowed(t, ctx, store, principal, identitystore.OrgActionRead, true)
	assertOrgAllowed(t, ctx, store, principal, identitystore.OrgActionManage, false)
	assertProjectAllowed(t, ctx, store, principal, identitystore.ProjectActionRead, false)

	if allowed, err := store.Identity().AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
		Principal: principal,
		OrgID:     otherOrgID,
		Action:    identitystore.OrgActionRead,
	}); err != nil || allowed {
		t.Fatalf("expected cross-org denial, got allowed=%v err=%v", allowed, err)
	}

	if _, err := store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID:     testOrgID,
		KeyID:     created.Record.ID,
		ProjectID: testProjectID,
		Role:      "developer",
	}); err != nil {
		t.Fatalf("set project role: %v", err)
	}
	assertProjectAllowed(t, ctx, store, principal, identitystore.ProjectActionRead, true)
	assertProjectAllowed(t, ctx, store, principal, identitystore.ProjectActionManage, true)
	assertProjectAllowed(t, ctx, store, principal, identitystore.ProjectActionAccessManage, false)

	if err := store.Identity().RemoveOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID:     testOrgID,
		KeyID:     created.Record.ID,
		ProjectID: testProjectID,
	}); err != nil {
		t.Fatalf("remove project role: %v", err)
	}
	assertProjectAllowed(t, ctx, store, principal, identitystore.ProjectActionRead, false)
	if err := store.Identity().RemoveOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID:     testOrgID,
		KeyID:     created.Record.ID,
		ProjectID: testProjectID,
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("expected not found removing absent role, got %v", err)
	}

	if _, err := pool.Exec(
		ctx,
		`INSERT INTO org_memberships(org_id, org_api_key_id, role, created_at) VALUES ($1, $2, 'member', $3)`,
		otherOrgID,
		created.Record.ID,
		now,
	); !isForeignKeyViolation(err) {
		t.Fatalf("cross-org membership for key error = %v, want foreign key violation", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE org_memberships SET role = 'owner' WHERE org_id = $1 AND org_api_key_id = $2`,
		testOrgID,
		created.Record.ID,
	); !isCheckViolation(err) {
		t.Fatalf("owner role for key error = %v, want check violation", err)
	}

	updated, err := store.Identity().UpdateOrgAPIKey(ctx, identitystore.UpdateOrgAPIKeyInput{
		OrgID:   testOrgID,
		KeyID:   created.Record.ID,
		Name:    "Deploy key",
		OrgRole: authz.OrgRoleAdmin,
	})
	if err != nil {
		t.Fatalf("update org api key: %v", err)
	}
	if updated.Name != "Deploy key" || updated.OrgRole != authz.OrgRoleAdmin {
		t.Fatalf("unexpected updated record: %+v", updated)
	}
	assertOrgAllowed(t, ctx, store, principal, identitystore.OrgActionManage, true)
	if _, err := store.Identity().UpdateOrgAPIKey(ctx, identitystore.UpdateOrgAPIKeyInput{
		OrgID:   testOrgID,
		KeyID:   created.Record.ID,
		OrgRole: authz.OrgRoleOwner,
	}); err == nil {
		t.Fatal("expected owner role update to be rejected")
	}

	fetched, err := store.Identity().GetOrgAPIKey(ctx, testOrgID, created.Record.ID)
	if err != nil {
		t.Fatalf("get org api key: %v", err)
	}
	if fetched.Name != "Deploy key" || fetched.OrgRole != authz.OrgRoleAdmin {
		t.Fatalf("unexpected fetched record: %+v", fetched)
	}

	if _, err := store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID:     testOrgID,
		KeyID:     created.Record.ID,
		ProjectID: testProjectID,
		Role:      "developer",
	}); err != nil {
		t.Fatalf("set project role before revoke: %v", err)
	}
	revoked, err := store.Identity().RevokeOrgAPIKey(ctx, testOrgID, created.Record.ID, identitystore.PrincipalRecord{})
	if err != nil {
		t.Fatalf("revoke org api key: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatalf("expected revoked_at, got %+v", revoked)
	}
	var orgMembershipRows, projectMembershipRows int
	if err := pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*) FROM org_memberships WHERE org_id = $1 AND org_api_key_id = $2),
		   (SELECT count(*)
		    FROM project_memberships pm
		    JOIN org_memberships om ON om.org_id = pm.org_id AND om.id = pm.org_membership_id
		    WHERE om.org_id = $1 AND om.org_api_key_id = $2)`,
		testOrgID,
		created.Record.ID,
	).Scan(&orgMembershipRows, &projectMembershipRows); err != nil {
		t.Fatalf("count key memberships after revoke: %v", err)
	}
	if orgMembershipRows != 0 || projectMembershipRows != 0 {
		t.Fatalf(
			"key memberships after revoke: org=%d project=%d, want both 0",
			orgMembershipRows, projectMembershipRows,
		)
	}
	if _, err := store.Identity().AuthenticateOrgAPIKey(ctx, created.Token); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("expected revoked key to fail authentication, got %v", err)
	}
	assertOrgAllowed(t, ctx, store, principal, identitystore.OrgActionRead, false)

	again, err := store.Identity().RevokeOrgAPIKey(ctx, testOrgID, created.Record.ID, identitystore.PrincipalRecord{})
	if err != nil {
		t.Fatalf("revoke org api key again: %v", err)
	}
	if again.RevokedAt == nil || !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatalf("expected idempotent revoke to keep original revoked_at, got %+v", again)
	}
	if _, err := store.Identity().UpdateOrgAPIKey(ctx, identitystore.UpdateOrgAPIKeyInput{
		OrgID: testOrgID,
		KeyID: created.Record.ID,
		Name:  "Zombie key",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("expected update of revoked key to conflict, got %v", err)
	}

	reused, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: creator.ID,
		Name:            "Deploy key",
		OrgRole:         authz.OrgRoleMember,
	})
	if err != nil {
		t.Fatalf("reuse revoked key name: %v", err)
	}

	page, err := store.Identity().ListOrgAPIKeysForOrg(ctx, identitystore.ListOrgAPIKeysInput{OrgID: testOrgID, Limit: 1})
	if err != nil {
		t.Fatalf("list org api keys: %v", err)
	}
	if len(page.Keys) != 1 || !page.HasMore || page.Keys[0].ID != reused.Record.ID {
		t.Fatalf("unexpected first page: %+v", page)
	}
	rest, err := store.Identity().ListOrgAPIKeysForOrg(ctx, identitystore.ListOrgAPIKeysInput{
		OrgID: testOrgID,
		Limit: 10,
		After: listing.KeysetCursor{Set: true, CreatedAt: page.Keys[0].CreatedAt, ID: page.Keys[0].ID},
	})
	if err != nil {
		t.Fatalf("list org api keys page two: %v", err)
	}
	if len(rest.Keys) != 1 || rest.HasMore || rest.Keys[0].ID != created.Record.ID ||
		rest.Keys[0].RevokedAt == nil || rest.Keys[0].OrgRole != "" {
		t.Fatalf("unexpected second page: %+v", rest)
	}
}

func TestDeleteOrganizationDeletesOrgAPIKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	seedDefaultProject(t, ctx, store)

	owner := mustCreateIdentityUser(t, ctx, store, "org-key-delete-owner@example.com", "Org Key Delete Owner")
	created, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: owner.ID, Name: "Doomed Key Org", IdempotencyKey: "doomed-key-org"},
	)
	if err != nil {
		t.Fatalf("create org for user: %v", err)
	}
	key, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           created.Org.ID,
		CreatedByUserID: owner.ID,
		Name:            "Doomed key",
		OrgRole:         authz.OrgRoleMember,
	})
	if err != nil {
		t.Fatalf("create org api key: %v", err)
	}
	if _, err := store.Identity().AuthenticateOrgAPIKey(ctx, key.Token); err != nil {
		t.Fatalf("authenticate org api key before org deletion: %v", err)
	}

	if _, err := store.Organizations().DeleteOrganization(ctx, created.Org.ID, userPrincipal(owner.ID)); err != nil {
		t.Fatalf("delete organization: %v", err)
	}

	if _, err := store.Identity().AuthenticateOrgAPIKey(ctx, key.Token); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("authenticate org api key after org deletion error = %v, want ErrUnauthorized", err)
	}
	var keyRows, membershipRows int
	if err := pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*) FROM org_api_keys WHERE org_id = $1),
		   (SELECT count(*) FROM org_memberships WHERE org_id = $1 AND org_api_key_id IS NOT NULL)`,
		created.Org.ID,
	).Scan(&keyRows, &membershipRows); err != nil {
		t.Fatalf("count org api key rows after org deletion: %v", err)
	}
	if keyRows != 0 || membershipRows != 0 {
		t.Fatalf(
			"org api key rows after org deletion: keys=%d memberships=%d, want both 0",
			keyRows, membershipRows,
		)
	}
}

func TestOrgAPIKeyLaunchesAgentsAndChangesConfigs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)

	creator := mustCreateIdentityUser(t, ctx, store, "key-launch-admin@example.com", "Key Launch Admin")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: creator.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add creator org membership: %v", err)
	}
	key, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: creator.ID,
		Name:            "Launcher key",
		OrgRole:         authz.OrgRoleMember,
	})
	if err != nil {
		t.Fatalf("create org api key: %v", err)
	}
	if _, err := store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID:     testOrgID,
		KeyID:     key.Record.ID,
		ProjectID: testProjectID,
		Role:      "admin",
	}); err != nil {
		t.Fatalf("grant key project role: %v", err)
	}

	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"org-key-launch",
		"Org Key Launch Agent",
		`
name: Org Key Launch Agent
instruction: Launched by an org API key.
model:
  provider_config: openai-prod
  name: gpt-test
`,
		now,
	)
	keyPrincipal := identitystore.PrincipalRecord{
		Type:        identitystore.PrincipalTypeOrgAPIKey,
		ID:          key.Record.ID,
		OrgID:       testOrgID,
		OrgAPIKeyID: key.Record.ID,
	}
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     keyPrincipal,
		Message:        "start work",
		IdempotencyKey: "idem-org-key-launch",
	})
	if err != nil {
		t.Fatalf("launch agent as org api key: %v", err)
	}
	publicKeyID, err := publicid.Encode(publicid.KindOrgAPIKey, key.Record.ID)
	if err != nil {
		t.Fatalf("encode key actor id: %v", err)
	}
	var actorDisplayName string
	if err := pool.QueryRow(ctx, `
		SELECT display_name
		FROM actors
		WHERE project_id = $1 AND provider = 'omnara' AND provider_user_id = $2
	`, testProjectID, publicKeyID).Scan(&actorDisplayName); err != nil {
		t.Fatalf("load key launch actor: %v", err)
	}
	if actorDisplayName != "Launcher key" {
		t.Fatalf("key actor display name = %q, want key name", actorDisplayName)
	}

	updated := mustCreateAgentConfigFromYAML(t, ctx, store, "org-key-launch-v2", `
name: Org Key Launch Agent
instruction: Updated by an org API key.
model:
  provider_config: openai-prod
  name: gpt-test
`, now.Add(2*time.Second))
	if _, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(updated),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeOrgAPIKey,
		ActorID:                key.Record.ID,
		Reason:                 "api",
		IdempotencyKey:         "idem-org-key-config-change",
	}); err != nil {
		t.Fatalf("change agent config as org api key: %v", err)
	}
}
