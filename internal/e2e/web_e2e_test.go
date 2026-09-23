//go:build integration && servicee2e && webe2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
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
	// Chromium maps this hostname to local TLS so secure-cookie and callback checks stay enabled.
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
	providerFixture := webE2EVerifiedAppSetupFixture(t, store, orgID, projectID, adminUserID)
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
	onboardingProfileID, err := publicid.Decode(
		publicid.KindAgentProfile,
		onboardingProject.agentID,
	)
	if err != nil {
		t.Fatalf("decode onboarding profile id: %v", err)
	}
	if err := store.Execution().
		DeleteAgentProfile(ctx, onboardingProjectID, onboardingProfileID); err != nil {
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
	switchOrg, err := store.Organizations().
		CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
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
		if _, err := store.Identity().
			CreateOrgInvitation(ctx, identitystore.CreateOrgInvitationInput{
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

func webE2EVerifiedAppSetupFixture(
	t *testing.T,
	store *storage.Store,
	orgID, projectID, userID uuid.UUID,
) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /apps/{app_id}/setup", func(w http.ResponseWriter, r *http.Request) {
		appID, err := publicid.Decode(publicid.KindProjectApp, r.PathValue("app_id"))
		if err != nil {
			http.Error(w, "invalid app id", http.StatusBadRequest)
			return
		}
		app, err := store.Apps().GetProjectApp(r.Context(), projectID, appID)
		if err != nil || app.OrgID != orgID {
			http.Error(
				w,
				"browser app was not persisted in the fixture project",
				http.StatusNotFound,
			)
			return
		}
		var body openapi.ConfigureProjectAppRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil ||
			(app.Provider != "github" && app.Provider != "discord") ||
			body.ProviderTenantId != "111" ||
			(app.Provider == "github" && (body.ProviderAccountRef == nil || *body.ProviderAccountRef != "222")) ||
			(app.Provider == "discord" && body.ProviderAccountRef != nil) {
			http.Error(w, "invalid provider browser fixture request", http.StatusBadRequest)
			return
		}
		if body.ExpectedSetupRevision != app.SetupRevision {
			http.Error(w, "app setup changed", http.StatusConflict)
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
		input := appstore.ConfigureProjectAppInput{
			OrgID: orgID, ProjectID: projectID, AppID: app.ID,
			ExpectedSetupRevision: body.ExpectedSetupRevision, InstalledByUserID: userID,
			Provider: app.Provider, ProviderTenantID: "111", ProviderAccountRef: "222",
			CredentialSecretID: secretID, CredentialVersionID: secret.CurrentVersionID,
			ProviderConfig: app.ProviderConfig,
		}
		if app.Provider == "github" {
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
		configured, err := store.Apps().ConfigureProjectApp(r.Context(), input)
		if err != nil {
			t.Errorf("configure verified browser app: %v", err)
			http.Error(w, "could not configure verified app", http.StatusInternalServerError)
			return
		}
		id, err := publicid.Encode(publicid.KindProjectApp, configured.ID)
		if err != nil {
			t.Errorf("encode provider fixture app: %v", err)
			http.Error(w, "could not encode app", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"id": id}); err != nil {
			t.Errorf("write provider fixture response: %v", err)
		}
	})
	fixture := httptest.NewServer(mux)
	t.Cleanup(fixture.Close)
	return fixture.URL
}

func TestWebE2EVerifiedAppSetupFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "web-app-setup-fixture")
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(
		t,
		ctx,
		"web-app-setup-fixture",
		webE2EProviderConfig,
		webE2EModelName,
	)
	fixtureURL := webE2EVerifiedAppSetupFixture(t, storage.NewStore(env.db),
		uuid.MustParse(mustDecodeServiceE2EPublicID(t, publicid.KindOrganization, project.orgID)),
		uuid.MustParse(mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)),
		uuid.MustParse(mustDecodeServiceE2EPublicID(t, publicid.KindUser, project.adminUserID)))
	for _, test := range []struct{ provider, appType string }{{"github", "github_pr"}, {"discord", "discord_thread"}} {
		provider := test.provider
		app := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/apps", map[string]any{
			"name":     "browser-" + provider,
			"app_type": test.appType,
			"settings": map[string]any{},
		}, "", project.adminToken, http.StatusCreated)
		appID := testutil.RequireType[string](t, app["id"])
		material := map[string]any{"kind": "generic", "value": "local-discord-token"}
		if provider == "github" {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			material = map[string]any{
				"kind":           "github_app_credentials",
				"app_id":         "111",
				"webhook_secret": "local-webhook-secret",
				"private_key": string(
					pem.EncodeToMemory(
						&pem.Block{
							Type:  "RSA PRIVATE KEY",
							Bytes: x509.MarshalPKCS1PrivateKey(key),
						},
					),
				),
			}
		}
		secret := env.requestJSON(
			t,
			ctx,
			http.MethodPost,
			"/api/v1/orgs/"+project.orgID+"/secrets",
			map[string]any{
				"name":     provider + "-browser-credentials",
				"owner":    map[string]any{"kind": "project", "project_id": project.projectID},
				"material": material,
			},
			"",
			project.adminToken,
			http.StatusCreated,
		)
		setup := map[string]any{
			"expected_setup_revision": app["setup_revision"], "credential_secret_id": secret["id"],
			"provider_tenant_id": "111", "provider_account_ref": "222",
		}
		if provider == "discord" {
			delete(setup, "provider_account_ref")
			setup["provider_config"] = map[string]any{
				"public_key": strings.Repeat("ab", 32),
			}
		}
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			fixtureURL+"/apps/"+appID+"/setup",
			strings.NewReader(string(mustJSON(setup))),
		)
		require.NoError(t, err)
		result := doServiceJSONRequest(t, request, http.StatusOK)
		require.Equal(t, appID, result["id"])
		configured := env.requestJSON(
			t,
			ctx,
			http.MethodGet,
			project.projectPath+"/apps/"+appID,
			nil,
			"",
			project.adminToken,
			http.StatusOK,
		)
		require.Equal(t, "active", configured["state"])
		require.Equal(t, test.appType, configured["app_type"])
		require.Equal(t, secret["id"], configured["credential_secret_id"])
		require.Equal(t, float64(2), configured["setup_revision"])
		if provider == "discord" {
			require.Equal(
				t,
				strings.Repeat("ab", 32),
				testutil.RequireType[map[string]any](t, configured["provider_config"])["public_key"],
			)
		}
		stale, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			fixtureURL+"/apps/"+appID+"/setup",
			strings.NewReader(string(mustJSON(setup))),
		)
		require.NoError(t, err)
		response, err := http.DefaultClient.Do(stale)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(
			t,
			http.StatusConflict,
			response.StatusCode,
			"fixture must fence an old browser setup revision",
		)
	}
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
