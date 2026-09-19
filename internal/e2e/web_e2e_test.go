//go:build integration && servicee2e && webe2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
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
	apiURL, err := url.Parse(env.apiURL)
	if err != nil {
		t.Fatal(err)
	}
	// Keep callback URL validation and secure browser cookies enabled. Chromium
	// resolves this test-only hostname to the local TLS proxy; no public server
	// or provider traffic is involved.
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(apiURL))
	t.Cleanup(proxy.Close)
	env.publicURL = strings.Replace(proxy.URL, "127.0.0.1", "app.omnara.test", 1)
	env.publicURLHost = strings.TrimPrefix(env.publicURL, "https://")
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
		MaxOutputTokens:        new(8192),
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
	providerFixture := webE2EVerifiedConnectionFixture(t, store, orgID, projectID, adminUserID)
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
	onboardingProject := env.bootstrapProjectViaAPI(
		t,
		ctx,
		"web-onboarding",
		webE2EProviderConfig,
		webE2EModelName,
	)
	onboardingOrgID, err := publicid.Decode(publicid.KindOrganization, onboardingProject.orgID)
	if err != nil {
		t.Fatalf("decode onboarding organization id: %v", err)
	}
	onboardingProjectID, err := publicid.Decode(publicid.KindProject, onboardingProject.projectID)
	if err != nil {
		t.Fatalf("decode onboarding project id: %v", err)
	}
	onboardingProfileID, err := publicid.Decode(publicid.KindAgentProfile, onboardingProject.agentID)
	if err != nil {
		t.Fatalf("decode onboarding profile id: %v", err)
	}
	if err := store.Execution().DeleteAgentProfile(ctx, onboardingProjectID, onboardingProfileID); err != nil {
		t.Fatalf("delete bootstrapped onboarding profile: %v", err)
	}
	onboardingEmail := "web-onboarding-" + env.seed + "@example.com"
	createWebE2EUser(
		t,
		ctx,
		store,
		onboardingOrgID,
		onboardingProjectID,
		onboardingEmail,
		authz.OrgRoleAdmin,
		authz.ProjectRoleAdmin,
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
	for _, invitationOrgID := range []uuid.UUID{
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
		"OMNARA_WEB_E2E_BASE_URL="+env.publicURL,
		"OMNARA_WEB_E2E_PROJECT_ID="+project.projectID,
		"OMNARA_WEB_E2E_ORG_NAME="+webE2EOrgName,
		"OMNARA_WEB_E2E_SWITCH_ORG_NAME="+webE2ESwitchOrgName,
		"OMNARA_WEB_E2E_ADMIN_EMAIL="+adminEmail,
		"OMNARA_WEB_E2E_VIEWER_EMAIL="+viewerEmail,
		"OMNARA_WEB_E2E_INVITEE_EMAIL="+inviteeEmail,
		"OMNARA_WEB_E2E_ONBOARDING_EMAIL="+onboardingEmail,
		"OMNARA_WEB_E2E_PASSWORD="+webE2EPassword,
		"OMNARA_WEB_E2E_PROVIDER_CONFIG="+webE2EProviderConfig,
		"OMNARA_WEB_E2E_MODEL_NAME="+webE2EModelName,
		"OMNARA_WEB_E2E_UNGRANTED_MODEL="+webE2EUngrantedModel,
		"OMNARA_WEB_E2E_PROVIDER_FIXTURE="+providerFixture,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run Playwright: %v\n%s", err, output)
	}
	t.Logf("Playwright output:\n%s", output)
}

// The real API binary has no provider-client test switch. Playwright replaces
// only GitHub/Discord connection creation with this loopback fixture: it seeds verified
// identity using the secret just saved through the real public API. Discovery
// itself is covered by HTTP integration tests with local provider servers.
func webE2EVerifiedConnectionFixture(
	t *testing.T,
	store *storage.Store,
	orgID, projectID, userID uuid.UUID,
) string {
	t.Helper()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body openapi.SaveIntegrationConnectionRequest
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil ||
			(body.Provider != "github" && body.Provider != "discord") ||
			body.ProviderTenantId != "111" || body.ProviderAccountRef != "222" {
			http.Error(w, "invalid provider browser fixture request", http.StatusBadRequest)
			return
		}
		secretID, err := publicid.Decode(publicid.KindSecret, body.CredentialSecretId)
		if err != nil {
			http.Error(w, "invalid credential id", http.StatusBadRequest)
			return
		}
		secret, err := store.Secrets().GetSecret(r.Context(), orgID, secretID)
		if err != nil {
			http.Error(w, "browser credential was not persisted", http.StatusBadRequest)
			return
		}
		input := integrationstore.SaveIntegrationConnectionInput{
			OrgID: orgID, ProjectID: projectID, InstalledByUserID: userID,
			Provider: string(body.Provider), ProviderTenantID: "111", ProviderAccountRef: "222",
			State:              integrationstore.IntegrationConnectionStateActive,
			CredentialSecretID: secretID, CredentialVersionID: secret.CurrentVersionID,
		}
		if body.Provider == "github" {
			input.CredentialAppID = 111
			input.ProviderIdentity = json.RawMessage(`{
				"app_id":111,"installation_id":222,"app_slug":"web-fixture","bot_user_id":444,"bot_login":"web-fixture[bot]"
			}`)
		} else {
			input.ProviderIdentity = json.RawMessage(`{"application_id":"111","bot_user_id":"222"}`)
		}
		if body.ProviderConfig != nil {
			input.ProviderConfig, err = json.Marshal(body.ProviderConfig)
			if err != nil {
				http.Error(w, "invalid fixture provider config", http.StatusBadRequest)
				return
			}
		}
		if body.ProviderAgentDisplayName != nil {
			input.ProviderAgentDisplayName = *body.ProviderAgentDisplayName
		}
		connection, err := store.Integrations().CreateIntegrationConnection(r.Context(), input)
		if err != nil {
			t.Errorf("seed verified provider browser connection: %v", err)
			http.Error(w, "could not seed verified connection", http.StatusInternalServerError)
			return
		}
		id, err := publicid.Encode(publicid.KindIntegrationConnection, connection.ID)
		if err != nil {
			t.Errorf("encode provider fixture connection: %v", err)
			http.Error(w, "could not encode connection", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"id": id}); err != nil {
			t.Errorf("write provider fixture response: %v", err)
		}
	}))
	t.Cleanup(fixture.Close)
	return fixture.URL
}

func createWebE2EUser(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID uuid.UUID,
	email, orgRole, projectRole string,
) uuid.UUID {
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
