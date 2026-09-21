//go:build integration

package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

const githubJourneyWebhookSecret = "local-test-webhook-secret"

type githubHTTPJourney struct {
	handler  http.Handler
	project  publicHTTPProject
	app      integrationstore.ProjectAppRecord
	secretID string
}

func newGitHubHTTPJourney(t *testing.T, seed string, options ...Option) githubHTTPJourney {
	t.Helper()
	options = append([]Option{WithGitHubClientConfig(githubSetupTestConfig(t))}, options...)
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()), options...)
	project := bootstrapPublicHTTPProject(t, handler, seed)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	secretID := createAppSetupHTTPSecret(t, handler, project, "github-credentials", map[string]any{
		"kind": "github_app_credentials", "app_id": "123", "webhook_secret": githubJourneyWebhookSecret,
		"private_key": string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
		})),
	})
	app := githubHTTPJourneyApp(t, handler, project, secretID, "456")
	return githubHTTPJourney{handler, project, app, secretID}
}

func githubHTTPJourneyApp(
	t *testing.T, handler http.Handler, project publicHTTPProject, secretID, installationID string,
) integrationstore.ProjectAppRecord {
	t.Helper()
	app := createSetupHTTPApp(t, handler, project, "github-"+installationID, appdefinition.GitHubPR)
	body := appSetupHTTPBody("123", installationID)
	body["credential_secret_id"] = secretID
	body["expected_setup_revision"] = app.SetupRevision
	created := requestJSONWithHeaders(t, handler, http.MethodPost, appSetupPath(t, project, app),
		projectAppHTTPJSON(t, body), "", http.StatusOK, authHeaders(project.AdminToken))
	id := mustPublicHTTPID(t, publicid.KindProjectApp, testutil.RequireType[string](t, created["id"]))
	app, err := project.Store.Integrations().GetProjectApp(t.Context(), project.ProjectUUID, id)
	require.NoError(t, err)
	var identity github.AppIdentity
	require.NoError(t, json.Unmarshal(app.ProviderIdentity, &identity))
	require.Equal(t, int64(123), identity.AppID)
	require.Equal(t, installationID, strconv.FormatInt(identity.InstallationID, 10))
	require.Equal(t, int64(999), identity.BotUserID)
	require.Equal(t, "helper[bot]", identity.BotLogin)
	return app
}

func githubHTTPWebhook(
	t *testing.T, handler http.Handler, eventType, deliveryID, secret, raw string, want int,
) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/github/123/events", strings.NewReader(raw))
	r = r.WithContext(t.Context())
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(github.EventHeader, eventType)
	r.Header.Set(github.DeliveryHeader, deliveryID)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(raw))
	r.Header.Set(github.SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	// No bearer, browser session or SetPathValue: exercise the mounted HTTP stack.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, want, w.Code, w.Body.String())
}

func (f githubHTTPJourney) consume(t *testing.T, raw string) []integration.AppSlotAdmission {
	t.Helper()
	inbox := f.project.Store.Integrations()
	receipt, found, err := inbox.ClaimIntegrationInbox(t.Context(), integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project.ProjectUUID, AppID: f.app.ID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, raw, string(receipt.Payload), "acknowledged receipt retains exact signed bytes")
	router := integration.NewAppRouter(f.project.Store.Execution(), inbox)
	consumer := integration.NewAppInboxConsumer(
		router, inbox, nil,
		map[string]integration.AppInboxProvider{"github": integration.GitHubAppInboxProvider{}},
		nil,
		integration.NewAppLaunchWorkflow(router, map[appdefinition.Type]integration.AppLauncher{
			appdefinition.GitHubPR: integration.EverySlotAppLauncher,
		}),
	)
	results, err := consumer.Consume(t.Context(), receipt.Lease())
	require.NoError(t, err)
	completed, err := inbox.GetIntegrationInbox(t.Context(), f.project.ProjectUUID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
	return results
}

func githubHTTPComment(t *testing.T, number int, commentID int64, text string) string {
	t.Helper()
	return projectAppHTTPJSON(t, map[string]any{
		"action": "created", "installation": map[string]any{"id": 456},
		"repository": map[string]any{"id": 1001, "full_name": "owner/repository"},
		"issue": map[string]any{"id": 2001, "number": number, "pull_request": map[string]any{
			"url": "https://api.github.com/repos/owner/repository/pulls/" + strconv.Itoa(number),
		}},
		"comment": map[string]any{"id": commentID, "body": text,
			"user": map[string]any{"id": 71, "login": "human", "type": "User"}},
		"sender": map[string]any{"id": 71, "login": "human", "type": "User"},
	})
}

func githubHTTPPullRequest(t *testing.T, action string) string {
	t.Helper()
	repository := map[string]any{"id": 1001, "full_name": "owner/repository"}
	payload := map[string]any{
		"action": action, "installation": map[string]any{"id": 456}, "repository": repository,
		"pull_request": map[string]any{
			"id": 2001, "number": 42, "title": "Review this change", "body": "Please review",
			"base": map[string]any{"repo": repository}, "head": map[string]any{"sha": strings.Repeat("b", 40)},
		},
		"sender": map[string]any{"id": 71, "login": "human", "type": "User"},
	}
	if action == "synchronize" {
		payload["before"], payload["after"] = strings.Repeat("a", 40), strings.Repeat("b", 40)
	}
	return projectAppHTTPJSON(t, payload)
}

func TestGitHubHTTPReceiptConsumerLaunchAndFollowupJourney(t *testing.T) {
	t.Parallel()
	for _, trigger := range []string{"mention", "pull_request_opened"} {
		t.Run(trigger, func(t *testing.T) {
			t.Parallel()
			f := newGitHubHTTPJourney(t, "github-"+strings.ReplaceAll(trigger, "_", "-"))
			appRef := testPublicID(t, publicid.KindProjectApp, f.app.ID)
			base := map[string]any{
				"instruction": "Review this pull request.",
				"model":       map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
			}
			config := createPublicHTTPAgentConfig(t, f.handler, f.project, "review-base", "json",
				projectAppHTTPJSON(t, base), f.project.AdminToken, http.StatusCreated)
			profile := createPublicHTTPAgentProfile(t, f.handler, f.project, "review-profile", "Review",
				testutil.RequireType[string](t, config["id"]), f.project.AdminToken, http.StatusCreated)
			app := map[string]any{
				"name": f.app.Name, "app_type": appdefinition.GitHubPR,
				"settings": map[string]any{"launcher": map[string]any{
					"trigger": trigger, "scope_kind": "repository", "scope_ref": "1001",
					"slots": []any{map[string]any{"key": "reviewer", "agent_profile_id": profile["id"]}},
				}},
			}
			requestJSONWithHeaders(t, f.handler, http.MethodPut, f.project.ProjectPath+"/apps/"+appRef,
				projectAppHTTPJSON(t, app), "", http.StatusOK, authHeaders(f.project.AdminToken))
			raw, eventType := githubHTTPComment(t, 42, 3001, "@helper please review"), "issue_comment"
			if trigger == "pull_request_opened" {
				raw, eventType = githubHTTPPullRequest(t, "opened"), "pull_request"
			}
			githubHTTPWebhook(t, f.handler, eventType, "initial", "wrong-secret", raw, http.StatusUnauthorized)
			var receipts int
			pool := integrationPoolForHandler(t, f.handler)
			require.NoError(t, pool.QueryRow(t.Context(),
				`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.app.ID).Scan(&receipts))
			require.Zero(t, receipts)
			// A failed initial persistence returns 503 and stores nothing. GitHub
			// does not retry automatically: these are manual redeliveries carrying
			// the same X-GitHub-Delivery value, through the mounted HTTP stack.
			_, err := pool.Exec(t.Context(),
				`ALTER TABLE integration_inbox ADD CONSTRAINT github_test_fail_receipt CHECK (false)`)
			require.NoError(t, err)
			githubHTTPWebhook(t, f.handler, eventType, "initial", githubJourneyWebhookSecret,
				raw, http.StatusServiceUnavailable)
			require.NoError(t, pool.QueryRow(t.Context(),
				`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.app.ID).Scan(&receipts))
			require.Zero(t, receipts)
			_, err = pool.Exec(t.Context(), `ALTER TABLE integration_inbox DROP CONSTRAINT github_test_fail_receipt`)
			require.NoError(t, err)
			for range 2 {
				githubHTTPWebhook(t, f.handler, eventType, "initial", githubJourneyWebhookSecret, raw, http.StatusNoContent)
				require.NoError(t, pool.QueryRow(t.Context(),
					`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.app.ID).Scan(&receipts))
				require.Equal(t, 1, receipts, "manual redelivery must accept once and then deduplicate")
			}
			results := f.consume(t, raw)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].Launch)
			require.True(t, results[0].Launch.Created)
			agentID := results[0].Launch.Agent.ID
			firstInput := results[0].Launch.AgentInput.ID
			require.Equal(t, "1001#42", results[0].Launch.IntegrationTarget.ProviderRef)
			// A different delivery/header label can create a receipt, never another
			// input or agent for the same signed semantic event.
			githubHTTPWebhook(t, f.handler, "pull_request_review", "relabeled", githubJourneyWebhookSecret,
				raw, http.StatusNoContent)
			f.consume(t, raw)
			var agents, inputs int
			require.NoError(t, pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.project.ProjectUUID).Scan(&agents))
			require.Equal(t, 1, agents)
			require.NoError(t, pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`, agentID).Scan(&inputs))
			require.Equal(t, 1, inputs)
			followup := githubHTTPComment(t, 42, 3002, "Please include a regression test")
			githubHTTPWebhook(t, f.handler, "issue_comment", "followup", githubJourneyWebhookSecret,
				followup, http.StatusNoContent)
			results = f.consume(t, followup)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].Input)
			require.Equal(t, agentID, results[0].Input.AgentInput.AgentID)
			require.NotEqual(t, firstInput, results[0].Input.AgentInput.ID)
			require.Equal(t, executionstore.DeliveryModeSteering, results[0].Input.AgentInput.DeliveryMode)
			var review map[string]any
			require.NoError(t, json.Unmarshal([]byte(githubHTTPPullRequest(t, "submitted")), &review))
			review["review"] = map[string]any{
				"id": 5001, "state": "changes_requested", "body": "Please handle the edge case",
				"user": map[string]any{"id": 71, "login": "human", "type": "User"},
			}
			reviewRaw := projectAppHTTPJSON(t, review)
			githubHTTPWebhook(t, f.handler, "pull_request_review", "review", githubJourneyWebhookSecret,
				reviewRaw, http.StatusNoContent)
			results = f.consume(t, reviewRaw)
			require.Len(t, results, 1)
			require.Equal(t, agentID, results[0].Input.AgentInput.AgentID)
			require.Equal(t, executionstore.DeliveryModeSteering, results[0].Input.AgentInput.DeliveryMode)
			commit := githubHTTPPullRequest(t, "synchronize")
			githubHTTPWebhook(t, f.handler, "pull_request", "commit", githubJourneyWebhookSecret,
				commit, http.StatusNoContent)
			results = f.consume(t, commit)
			require.Len(t, results, 1)
			require.Equal(t, executionstore.DeliveryModeQueued, results[0].Input.AgentInput.DeliveryMode)
			// Renaming does not lose the subscription; reusing that name for a different
			// repository ID cannot reach this agent or this repository launcher.
			renamed := strings.ReplaceAll(githubHTTPComment(t, 42, 3003, "renamed repository"),
				"owner/repository", "new-owner/new-name")
			githubHTTPWebhook(t, f.handler, "issue_comment", "renamed", githubJourneyWebhookSecret,
				renamed, http.StatusNoContent)
			require.Len(t, f.consume(t, renamed), 1)
			reused := strings.ReplaceAll(githubHTTPComment(t, 42, 3004, "@helper wrong repository"),
				`"id":1001`, `"id":1002`)
			githubHTTPWebhook(t, f.handler, "issue_comment", "reused", githubJourneyWebhookSecret,
				reused, http.StatusNoContent)
			require.Empty(t, f.consume(t, reused))
			self := strings.ReplaceAll(githubHTTPComment(t, 42, 3005, "@helper self event"),
				`{"id":71,"login":"human","type":"User"}`, `{"id":999,"login":"helper[bot]","type":"Bot"}`)
			githubHTTPWebhook(t, f.handler, "issue_comment", "self", githubJourneyWebhookSecret,
				self, http.StatusNoContent)
			require.Empty(t, f.consume(t, self))
		})
	}
}

func TestGitHubHTTPExistingAgentSubscriptionJourney(t *testing.T) {
	t.Parallel()
	f := newGitHubHTTPJourney(t, "github-existing")
	source := map[string]any{
		"instruction": "Review this pull request.",
		"model":       map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
	}
	config := createPublicHTTPAgentConfig(t, f.handler, f.project, "existing-config", "json",
		projectAppHTTPJSON(t, source), f.project.AdminToken, http.StatusCreated)
	profile := createPublicHTTPAgentProfile(t, f.handler, f.project, "existing-profile", "Existing reviewer",
		testutil.RequireType[string](t, config["id"]), f.project.AdminToken, http.StatusCreated)
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, map[string]any{"profile": profile["id"], "config": config["id"]}),
		"existing-reviewer", http.StatusCreated, authHeaders(f.project.AdminToken))
	publicAgentID := testutil.RequireType[string](t,
		testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	requestJSONWithHeaders(t, f.handler, http.MethodPost,
		f.project.ProjectPath+"/apps/"+testPublicID(t, publicid.KindProjectApp, f.app.ID)+"/subscriptions",
		projectAppHTTPJSON(t, map[string]any{
			"agent_id": publicAgentID, "type": "pull_request",
			"conversation": map[string]any{"repository_id": 1001, "pull_request": 42},
			"events":       []string{"discussion_comment", "review_comment", "commit"},
		}), "", http.StatusCreated, authHeaders(f.project.AdminToken))
	raw := githubHTTPComment(t, 42, 4001, "Human steering without a mention")
	githubHTTPWebhook(t, f.handler, "issue_comment", "existing", githubJourneyWebhookSecret, raw, http.StatusNoContent)
	results := f.consume(t, raw)
	require.Len(t, results, 1)
	require.Nil(t, results[0].Launch)
	require.Equal(t, mustPublicHTTPID(t, publicid.KindAgent, publicAgentID), results[0].Input.AgentInput.AgentID)
	require.Equal(t, executionstore.DeliveryModeSteering, results[0].Input.AgentInput.DeliveryMode)
}

func TestGitHubHTTPSharedAppCredentialsAndInstallationIsolation(t *testing.T) {
	t.Parallel()
	f := newGitHubHTTPJourney(t, "github-shared-app")
	ctx := t.Context()
	second := projectAppHTTPSecondProject(t, f.handler, f.project)
	secretPath := "/api/v1/orgs/" + f.project.OrgID + "/secrets/" + f.secretID
	grant := requestJSONWithHeaders(t, f.handler, http.MethodPost, secretPath+"/grants",
		projectAppHTTPJSON(t, map[string]any{"target_project_id": second.ProjectID}),
		"", http.StatusCreated, authHeaders(f.project.AdminToken))
	secondApp := githubHTTPJourneyApp(t, f.handler, second, f.secretID, "457")
	inbox := f.project.Store.Integrations()
	candidates, err := inbox.ListGitHubWebhookCredentialApps(ctx, "123", 16)
	require.NoError(t, err)
	require.Len(t, candidates, 1, "shared credential is decrypted at most once for an App ping")
	require.Equal(t, f.app.CredentialSecretID, candidates[0].CredentialSecretID)
	credential, err := f.project.Store.Secrets().ReadProjectAvailableSecretPayload(ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			SecretID: f.app.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
		})
	require.NoError(t, err)
	material := map[string]any{
		"kind": "github_app_credentials", "app_id": "123", "webhook_secret": githubJourneyWebhookSecret,
		"private_key": credential.Payload[secrets.KeyPrivateKey],
	}
	separateSecret := createAppSetupHTTPSecret(t, f.handler, f.project, "second-credential", material)
	separate := githubHTTPJourneyApp(t, f.handler, f.project, separateSecret, "458")
	candidates, err = inbox.ListGitHubWebhookCredentialApps(ctx, "123", 16)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	limited, err := inbox.ListGitHubWebhookCredentialApps(ctx, "123", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, candidates[0].ID, limited[0].ID, "bounded lookup has stable ordering")
	material["app_id"] = "124"
	otherSecret := createAppSetupHTTPSecret(t, f.handler, f.project, "other-app-credential", material)
	otherBody := appSetupHTTPBody("124", "459")
	otherBody["credential_secret_id"] = otherSecret
	otherApp := createSetupHTTPApp(t, f.handler, f.project, "other-github", appdefinition.GitHubPR)
	otherBody["expected_setup_revision"] = otherApp.SetupRevision
	requestJSONWithHeaders(t, f.handler, http.MethodPost, appSetupPath(t, f.project, otherApp),
		projectAppHTTPJSON(t, otherBody), "", http.StatusOK, authHeaders(f.project.AdminToken))
	candidates, err = inbox.ListGitHubWebhookCredentialApps(ctx, "123", 16)
	require.NoError(t, err)
	require.Len(t, candidates, 2, "a different App cannot become a credential candidate")
	for _, candidate := range candidates {
		require.Equal(t, "123", candidate.ProviderTenantID)
	}
	ping := `{"zen":"Keep it logically awesome.","hook":{"id":701}}`
	githubHTTPWebhook(t, f.handler, "ping", "ping", githubJourneyWebhookSecret, ping, http.StatusNoContent)
	githubHTTPWebhook(t, f.handler, "ping", "bad-ping", "wrong-secret", ping, http.StatusUnauthorized)
	unknown := `{"action":"created","installation":{"id":9999,"app_id":123}}`
	githubHTTPWebhook(t, f.handler, "installation", "unmanaged", githubJourneyWebhookSecret,
		unknown, http.StatusNoContent)
	var count int
	pool := integrationPoolForHandler(t, f.handler)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox`).Scan(&count))
	require.Zero(t, count, "App health/unmanaged installation callbacks do not choose a project")
	appPath := f.project.ProjectPath + "/apps/" + testPublicID(t, publicid.KindProjectApp, f.app.ID)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, appPath+"/disconnect",
		"", "", http.StatusOK, authHeaders(f.project.AdminToken))
	githubHTTPWebhook(t, f.handler, "issue_comment", "disabled", githubJourneyWebhookSecret,
		githubHTTPComment(t, 42, 3001, "@helper disabled installation"), http.StatusNoContent)
	// The same physical App URL still durably accepts the second installation's
	// lifecycle event, scoped only to its project, and normalization ignores it.
	installed := `{"action":"created","installation":{"id":457,"app_id":123}}`
	githubHTTPWebhook(t, f.handler, "installation", "managed", githubJourneyWebhookSecret,
		installed, http.StatusNoContent)
	f2 := githubHTTPJourney{handler: f.handler, project: second, app: secondApp, secretID: f.secretID}
	require.Empty(t, f2.consume(t, installed))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_inbox WHERE project_id=$1`, f.project.ProjectUUID).Scan(&count))
	require.Zero(t, count)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete,
		f.project.ProjectPath+"/apps/"+
			testPublicID(t, publicid.KindProjectApp, separate.ID),
		"", "", http.StatusNoContent, authHeaders(f.project.AdminToken))
	candidates, err = inbox.ListGitHubWebhookCredentialApps(ctx, "123", 16)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	// Disabled credentials can serve App health even after the other project's
	// grant is revoked. The known installation cannot borrow that authorization.
	requestJSONWithHeaders(t, f.handler, http.MethodDelete,
		secretPath+"/grants/"+testutil.RequireType[string](t, grant["id"]),
		"", "", http.StatusNoContent, authHeaders(f.project.AdminToken))
	candidates, err = inbox.ListGitHubWebhookCredentialApps(ctx, "123", 16)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, f.app.ID, candidates[0].ID)
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, candidates[0].State)
	githubHTTPWebhook(t, f.handler, "ping", "disabled-ping", githubJourneyWebhookSecret, ping, http.StatusNoContent)
	githubHTTPWebhook(t, f.handler, "installation", "revoked", githubJourneyWebhookSecret,
		installed, http.StatusUnauthorized)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, appPath,
		"", "", http.StatusNoContent, authHeaders(f.project.AdminToken))
	candidates, err = inbox.ListGitHubWebhookCredentialApps(ctx, "123", 16)
	require.NoError(t, err)
	require.Empty(t, candidates, "deleted and no-longer-authorized references cannot verify a callback")
	githubHTTPWebhook(t, f.handler, "ping", "unavailable-ping", githubJourneyWebhookSecret, ping, http.StatusUnauthorized)
}
