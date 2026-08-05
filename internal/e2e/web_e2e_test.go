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
)

const (
	webE2EPassword       = "Correct horse battery staple 1!"
	webE2EProviderConfig = "openai-prod"
	webE2EModelName      = "service-e2e-local"
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
	adminEmail := "web-admin-" + env.seed + "@example.com"
	viewerEmail := "web-viewer-" + env.seed + "@example.com"
	createWebE2EUser(
		t,
		ctx,
		store,
		orgID,
		projectID,
		adminEmail,
		authz.ProjectRoleAdmin,
	)
	createWebE2EUser(
		t,
		ctx,
		store,
		orgID,
		projectID,
		viewerEmail,
		authz.ProjectRoleViewer,
	)

	cmd := exec.CommandContext(ctx, "pnpm", "--filter", "@omnara/web", "run", "test:e2e")
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = filepath.Join(env.repoRoot, "frontend")
	cmd.Env = serviceProcessEnv(
		"OMNARA_WEB_E2E_BASE_URL="+env.apiURL,
		"OMNARA_WEB_E2E_PROJECT_ID="+project.projectID,
		"OMNARA_WEB_E2E_ADMIN_EMAIL="+adminEmail,
		"OMNARA_WEB_E2E_VIEWER_EMAIL="+viewerEmail,
		"OMNARA_WEB_E2E_PASSWORD="+webE2EPassword,
		"OMNARA_WEB_E2E_PROVIDER_CONFIG="+webE2EProviderConfig,
		"OMNARA_WEB_E2E_MODEL_NAME="+webE2EModelName,
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
	email, projectRole string,
) {
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
			Role:   authz.OrgRoleMember,
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
}
