//go:build integration && servicee2e && webe2e

package e2e

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
)

const (
	webE2EPassword       = "Correct horse battery staple 1!"
	webE2EProviderConfig = "openai-prod"
	webE2EModelName      = "service-e2e-local"
	webE2EUngrantedModel = "service-e2e-ungranted"
	webE2EOrgName        = "web Org"
	webE2ESwitchOrgName  = "zz web switch target"
)

func TestWebE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	env := newServiceE2EEnvironment(t, ctx, "web")
	env.publicURL = env.apiURL
	env.publicURLHost = strings.TrimPrefix(env.apiURL, "http://")
	env.startEmbeddedWebAPI(t, ctx)

	project := env.bootstrapProjectViaAPI(
		t,
		ctx,
		"web",
		webE2EProviderConfig,
		webE2EModelName,
	)
	orgID, err := publicid.Decode(publicid.KindOrganization, project.orgID)
	if err != nil {
		t.Fatalf("decode organization id: %v", err)
	}
	projectID, err := publicid.Decode(publicid.KindProject, project.projectID)
	if err != nil {
		t.Fatalf("decode project id: %v", err)
	}
	store := storage.NewStore(env.db)
	provider, err := store.Models().GetModelProviderConfigByName(ctx, orgID, webE2EProviderConfig)
	if err != nil {
		t.Fatalf("get web e2e model provider: %v", err)
	}
	defaultMaxOutputTokens := 4096
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  orgID,
		ModelProviderConfigID:  provider.ID,
		Name:                   webE2EUngrantedModel,
		ProviderModelSlug:      webE2EUngrantedModel,
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: &defaultMaxOutputTokens,
	}); err != nil {
		t.Fatalf("create ungranted web e2e model: %v", err)
	}
	adminEmail := "web-admin-" + env.seed + "@example.com"
	viewerEmail := "web-viewer-" + env.seed + "@example.com"
	inviteeEmail := "web-invitee-" + env.seed + "@example.com"
	adminUserID := createWebE2EUser(
		t,
		ctx,
		store,
		orgID,
		projectID,
		adminEmail,
		authz.OrgRoleAdmin,
		authz.ProjectRoleAdmin,
	)
	createWebE2EUser(
		t,
		ctx,
		store,
		orgID,
		projectID,
		viewerEmail,
		authz.OrgRoleMember,
		authz.ProjectRoleViewer,
	)
	createWebE2EUser(
		t,
		ctx,
		store,
		orgID,
		projectID,
		inviteeEmail,
		authz.OrgRoleMember,
		authz.ProjectRoleViewer,
	)
	switchOrg, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:         adminUserID,
		Name:           webE2ESwitchOrgName,
		IdempotencyKey: "web-switch-target-org",
	})
	if err != nil {
		t.Fatalf("create web e2e switch target organization: %v", err)
	}
	secondInvitationOrg, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:         adminUserID,
			Name:           "zz web invitation target two",
			IdempotencyKey: "web-invitation-target-two",
		},
	)
	if err != nil {
		t.Fatalf("create second web e2e invitation organization: %v", err)
	}
	thirdInvitationOrg, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:         adminUserID,
			Name:           "zz web invitation target three",
			IdempotencyKey: "web-invitation-target-three",
		},
	)
	if err != nil {
		t.Fatalf("create third web e2e invitation organization: %v", err)
	}
	for _, invitationOrgID := range []storage.ID{
		switchOrg.Org.ID,
		secondInvitationOrg.Org.ID,
		thirdInvitationOrg.Org.ID,
	} {
		if _, err := store.Identity().CreateOrgInvitation(ctx, identitystore.CreateOrgInvitationInput{
			OrgID: invitationOrgID,
			Email: inviteeEmail,
			Role:  authz.OrgRoleMember,
		}); err != nil {
			t.Fatalf("create web e2e pending organization invitation: %v", err)
		}
	}

	cmd := exec.CommandContext(ctx, "pnpm", "--filter", "@omnara/web", "run", "test:e2e")
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = filepath.Join(env.repoRoot, "frontend")
	cmd.Env = serviceProcessEnv(
		"OMNARA_WEB_E2E_BASE_URL="+env.apiURL,
		"OMNARA_WEB_E2E_PROJECT_ID="+project.projectID,
		"OMNARA_WEB_E2E_ORG_NAME="+webE2EOrgName,
		"OMNARA_WEB_E2E_SWITCH_ORG_NAME="+webE2ESwitchOrgName,
		"OMNARA_WEB_E2E_ADMIN_EMAIL="+adminEmail,
		"OMNARA_WEB_E2E_VIEWER_EMAIL="+viewerEmail,
		"OMNARA_WEB_E2E_INVITEE_EMAIL="+inviteeEmail,
		"OMNARA_WEB_E2E_PASSWORD="+webE2EPassword,
		"OMNARA_WEB_E2E_PROVIDER_CONFIG="+webE2EProviderConfig,
		"OMNARA_WEB_E2E_MODEL_NAME="+webE2EModelName,
		"OMNARA_WEB_E2E_UNGRANTED_MODEL="+webE2EUngrantedModel,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run Playwright: %v\n%s", err, output)
	}
	t.Logf("Playwright output:\n%s", output)
}

func createWebE2EUser(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID storage.ID,
	email, orgRole, projectRole string,
) storage.ID {
	t.Helper()
	start, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: email},
	)
	if err != nil {
		t.Fatalf("start password signup for %s: %v", email, err)
	}
	passwordHash, err := authn.HashPassword(webE2EPassword)
	if err != nil {
		t.Fatalf("hash password for %s: %v", email, err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{
			Token:        start.Token,
			PasswordHash: passwordHash,
			DisplayName:  email,
		},
	)
	if err != nil {
		t.Fatalf("complete password signup for %s: %v", email, err)
	}
	if !completed.Verified {
		t.Fatalf("password signup for %s was not verified", email)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{
			OrgID:  orgID,
			UserID: completed.User.ID,
			Role:   orgRole,
		},
	); err != nil {
		t.Fatalf("add organization membership for %s: %v", email, err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     orgID,
			ProjectID: projectID,
			UserID:    completed.User.ID,
			Role:      projectRole,
		},
	); err != nil {
		t.Fatalf("add project membership for %s: %v", email, err)
	}
	return completed.User.ID
}
