//go:build integration && servicee2e

package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/apps/slack"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/harness/worker"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedServiceSlackApp(
	t *testing.T, ctx context.Context, env *serviceE2EEnvironment, project deterministicProject, store *storage.Store,
) appstore.ProjectAppRecord {
	t.Helper()
	created := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/apps",
		map[string]any{"name": "chat", "app_type": appdefinition.SlackThread, "settings": map[string]any{}},
		"", project.adminToken, http.StatusCreated)
	appID, err := publicid.Decode(publicid.KindProjectApp, testutil.RequireType[string](t, created["id"]))
	require.NoError(t, err)
	projectID, err := publicid.Decode(publicid.KindProject, project.projectID)
	require.NoError(t, err)
	app, err := store.Apps().GetProjectApp(ctx, projectID, appID)
	require.NoError(t, err)
	userID, err := publicid.Decode(publicid.KindUser, project.adminUserID)
	require.NoError(t, err)
	credential, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: app.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: projectID,
		Name: "local-slack", Actor: identitystore.NewUserPrincipal(userID),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-local-only", ClientID: "local-client", ClientSecret: "local-client-secret",
			SigningSecret: "local-signing-secret",
		},
	})
	require.NoError(t, err)
	app, err = store.Apps().ConfigureProjectApp(ctx, appstore.ConfigureProjectAppInput{
		OrgID: app.OrgID, ProjectID: projectID, AppID: app.ID, ExpectedSetupRevision: app.SetupRevision,
		InstalledByUserID: userID, Provider: "slack", ProviderTenantID: "T123", ProviderAccountRef: "A123",
		ProviderAgentDisplayName: "Scheduled bot", CredentialSecretID: credential.ID, CredentialVersionID: version.ID,
		OAuthFlowID: uuid.Must(uuid.NewV7()), ProviderIdentity: json.RawMessage(`{"bot_user_id":"U_BOT"}`),
	})
	require.NoError(t, err)
	return app
}

func startServiceSlackWorkers(
	t *testing.T, ctx context.Context, store *storage.Store, client *http.Client, log *slog.Logger,
) {
	t.Helper()
	bus, err := notifications.NewRedisBus(integrationredis.OpenClient(t), log)
	require.NoError(t, err)
	kernelWorker := worker.NewWorker(store.Execution(), kernel.AgentExecutor{
		Store: store,
		ContextBuilder: modelcontext.Builder{
			Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Apps()),
		},
		ModelResolver:   modelprovider.Resolver{Models: store.Models(), Secrets: store.Secrets(), AllowLoopback: true},
		ToolExecutor:    tools.Executor{Store: store, AppHTTPClient: client, Log: log},
		StreamPublisher: bus, StreamLog: log,
	}, worker.Options{Log: log, Capacity: 1, ControlSubscriber: bus})
	router := apps.NewAppRouter(store.Execution(), store.Apps())
	slackProvider := apps.NewSlackAppInboxProvider(
		slack.OAuthConfig{HTTPClient: client}, store.Secrets(), store.Apps(), store.Execution(),
	)
	providers := map[string]apps.AppInboxProvider{"slack": slackProvider}
	launcher := apps.NewChatAppLauncher(store.Apps(), store.Execution(), providers)
	launches := apps.NewAppLaunchWorkflow(router, map[appdefinition.Type]apps.AppLauncher{
		appdefinition.SlackThread: launcher.Decide,
	})
	scheduled := apps.NewThreadAppScheduledHandler(router, store.Apps(), slackProvider)
	consumer := apps.NewAppInboxConsumer(router, store.Apps(), store.Artifacts(), providers,
		apps.InteractionPresenter{Store: store, HTTPClient: client}, launches,
		apps.WithAppScheduledHandlers(map[appdefinition.Type]apps.AppScheduledHandler{
			appdefinition.SlackThread: scheduled.Handle,
		}))
	appWorker := apps.NewAppInboxWorker(store.Apps(), consumer,
		apps.AppInboxWorkerOptions{Log: log, Capacity: 1})
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 2)
	go func() { done <- kernelWorker.Run(workerCtx) }()
	go func() { done <- appWorker.Run(workerCtx) }()
	t.Cleanup(func() {
		cancel()
		for range 2 {
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					assert.NoError(t, err)
				}
			case <-time.After(10 * time.Second):
				t.Error("hosted app worker did not stop")
			}
		}
	})
}

type serviceSlackTransport struct{ target *url.URL }

func (transport serviceSlackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "slack.com" {
		return nil, fmt.Errorf("unexpected hosted provider host %q", request.URL.Host)
	}
	local := request.Clone(request.Context())
	local.URL.Scheme, local.URL.Host = transport.target.Scheme, transport.target.Host
	local.Host = transport.target.Host
	return http.DefaultTransport.RoundTrip(local)
}

func sendServiceSlackReply(
	t *testing.T, ctx context.Context, env *serviceE2EEnvironment, eventID, threadTS, messageTS, text string,
) {
	t.Helper()
	body := mustJSON(map[string]any{
		"type": "event_callback", "team_id": "T123", "api_app_id": "A123", "event_id": eventID,
		"authorizations": []any{map[string]any{"team_id": "T123", "user_id": "U_BOT", "is_bot": true}},
		"event": map[string]any{"type": "message", "user": "U123", "team": "T123", "channel": "C123",
			"channel_type": "channel", "thread_ts": threadTS, "ts": messageTS, "text": text},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(env.apiURL, "/api/v1")+"/api/integrations/slack/events", strings.NewReader(string(body)))
	require.NoError(t, err)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("local-signing-secret"))
	_, err = mac.Write(append([]byte("v0:"+timestamp+":"), body...))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(slack.TimestampHeader, timestamp)
	request.Header.Set(slack.SignatureHeader, "v0="+hex.EncodeToString(mac.Sum(nil)))
	response := doServiceJSONRequest(t, request, http.StatusOK)
	require.Equal(t, "received", response["ok"])
}
