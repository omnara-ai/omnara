//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorReloadsCompiledModelOverridesFromModelContext(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	sourceYAML := `
instruction: Reload model options from the context agent config.
model:
  provider_config: openai-prod
  name: context-options-model
  default_max_output_tokens: 2345
  cache_retention: long
`
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel Context Model Options",
		"kernel-context-model-options-agent",
		sourceYAML,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
			IdempotencyKey: "kernel-context-model-options-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		launch.Agent.ID,
		kernelTestUserID,
		"hello",
		now.Add(2*time.Millisecond),
	)
	snapshot, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		kernelTestProjectID,
		launch.Agent.ID,
		turn.OpeningEventSequence,
	)
	if err != nil {
		t.Fatalf("capture agent config: %v", err)
	}
	modelClaim, err := fixture.Store.Execution().
		ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
			ProjectID:          kernelTestProjectID,
			AgentID:            launch.Agent.ID,
			RuntimeLockID:      turn.RuntimeLockID,
			OpeningInputIDs:    turn.InputIDs,
			AgentConfigID:      snapshot.AgentConfig.ID,
			InputEventSequence: turn.OpeningEventSequence,
		})
	if err != nil {
		t.Fatalf("claim model call context: %v", err)
	}
	modelClient := &sequenceKernelModel{providerModelSlug: "context-options-model"}
	resolver := &selectionRecordingResolver{
		client: modelClient,
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		snapshot.AgentConfig.CompiledDefinition,
		snapshot.AgentConfig.EffectiveDefinitionHash,
	)
	if err != nil {
		t.Fatalf("load model contract for context: %v", err)
	}
	selection := modelSelectionForContext(modelClaim.Context, contract.Model)
	if _, err := resolver.Resolve(ctx, selection); err != nil {
		t.Fatalf("resolve model client for context: %v", err)
	}
	if len(resolver.selections) != 1 {
		t.Fatalf("resolver selections = %d, want 1", len(resolver.selections))
	}
	resolvedSelection := resolver.selections[0]
	if resolvedSelection.Overrides.DefaultMaxOutputTokens == nil ||
		*resolvedSelection.Overrides.DefaultMaxOutputTokens != 2345 ||
		resolvedSelection.Overrides.CacheRetention != string(model.CacheRetentionLong) {
		t.Fatalf("selection overrides from context = %+v", resolvedSelection.Overrides)
	}
}

func TestAgentExecutorTargetDoesNotImplicitlyGrantMessagingTools(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	sourceYAML := `
instruction: Send messages through the connected integration.
model:
  provider_config: openai-prod
  name: implicit-integration-tool-model
`
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel Implicit Integration Tool",
		"kernel-implicit-integration-tool-agent",
		sourceYAML,
	)
	secret, _, err := fixture.Store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:          kernelTestOrgID,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: kernelTestProjectID,
			Name:           "kernel-implicit-integration-tool-credentials",
			Material: secrets.SlackAppCredentialsMaterial{
				AccessToken:   "xoxb-test",
				ClientID:      "client-id",
				ClientSecret:  "client-secret",
				SigningSecret: "signing-secret",
			},
			Actor: kernelTestUserPrincipal(kernelTestUserID),
		},
	)
	if err != nil {
		t.Fatalf("create integration credential secret: %v", err)
	}
	app, err := fixture.Store.Integrations().
		CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
			OrgID: kernelTestOrgID, ProjectID: kernelTestProjectID,
			Name: "implicit", AppType: appdefinition.SlackThread,
		})
	require.NoError(t, err)
	install, err := fixture.Store.Integrations().
		ConfigureProjectApp(ctx, integrationstore.ConfigureProjectAppInput{
			OrgID:                 kernelTestOrgID,
			ProjectID:             kernelTestProjectID,
			AppID:                 app.ID,
			ExpectedSetupRevision: app.SetupRevision,
			InstalledByUserID:     kernelTestUserID,
			Provider:              integrationstore.IntegrationProviderSlack,
			ProviderTenantID:      "T_IMPLICIT_TOOL",
			ProviderAccountRef:    "A_IMPLICIT_TOOL",
			CredentialSecretID:    secret.ID,
			CredentialVersionID:   secret.CurrentVersionID,
			OAuthFlowID:           uuid.Must(uuid.NewV7()),
			ProviderIdentity:      json.RawMessage(`{"bot_user_id":"B_IMPLICIT_TOOL"}`),
		})
	require.NoError(t, err)
	actor, err := executionstore.AppActorParams(install.ID, "U_FIXTURE", nil)
	require.NoError(t, err)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
			IdempotencyKey: "kernel-implicit-integration-tool-agent",
			InitialInput: &executionstore.LaunchInitialInput{
				ContentBlocks:    json.RawMessage(`[{"type":"text","text":"hello"}]`),
				SemanticEventKey: "kernel-origin",
				Actor:            &actor,
				Origin: &executionstore.LaunchInputOrigin{
					AppID: install.ID,
					Address: integrationstore.ConversationAddress{
						Kind: "thread",
						Ref:  "CIMPLICITTOOL:1.0",
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	require.NotEqual(t, uuid.Nil, launch.IntegrationTarget.ID)
	specs, err := (AgentExecutor{Store: fixture.Store}).modelContextToolRuntime(
		ctx,
		kernelTestProjectID,
		launch.Agent.ID,
		executionstore.ModelCallContextRecord{AgentConfigID: profile.CurrentConfigID},
		now,
	)
	if err != nil {
		t.Fatalf("reload model context tool runtime: %v", err)
	}
	for _, spec := range specs {
		require.False(
			t,
			toolcatalog.UsesAppToolNamespace(spec.Name),
			"attribution must not grant app tools",
		)
	}
	selection, err := fixture.Store.Execution().
		GetInteractionSelection(ctx, kernelTestProjectID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(
		t,
		executionstore.InteractionSelection{},
		selection,
		"an origin without a handler remains dashboard only",
	)
}

func TestAgentExecutorRecordsErrorWhenModelGrantUnavailableBeforeContextCreation(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	sourceYAML := `
instruction: Keep model grants enforced.
model:
  provider_config: openai-prod
  name: unavailable-grant-model
`
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel Unavailable Grant",
		"kernel-unavailable-grant-agent",
		sourceYAML,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
			IdempotencyKey: "kernel-unavailable-grant-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	botToken := attachKernelSlackHandler(
		t,
		ctx,
		fixture,
		launch.Agent.ID,
		"unavailable-grant",
		"CUNAVAILABLEGRANT:1.0",
	)
	config, found, err := fixture.Store.Execution().
		GetAgentConfig(ctx, kernelTestProjectID, profile.CurrentConfigID)
	if err != nil {
		t.Fatalf("load agent config: %v", err)
	}
	if !found {
		t.Fatalf("agent config not found")
	}
	configuredModelID := configuredModelIDForKernelConfig(t, ctx, fixture.Store, config)
	grant, err := fixture.Store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		kernelTestOrgID,
		kernelTestProjectID,
		configuredModelID,
	)
	if err != nil {
		t.Fatalf("load active model grant: %v", err)
	}
	if _, err := fixture.Store.Models().DeleteProjectModelGrant(
		ctx,
		kernelTestOrgID,
		kernelTestProjectID,
		grant.ID,
	); err != nil {
		t.Fatalf("revoke model grant: %v", err)
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		launch.Agent.ID,
		kernelTestUserID,
		"hello",
		now.Add(3*time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "unavailable-grant-model",
		responses: []model.Response{
			{
				ID:         "resp_unreachable",
				Content:    []model.ResponsePart{{Type: "text", Text: "should not respond"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	var postedMessage struct {
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
	}
	postCount := 0
	postedPath := ""
	postedAuthorization := ""
	var postedDecodeErr error
	integrationHTTPClient := kernelSlackRuntimeHTTPClient(t, "unavailable-grant",
		func(req *http.Request) (*http.Response, error) {
			postCount++
			postedPath = req.URL.Path
			postedAuthorization = req.Header.Get("Authorization")
			postedDecodeErr = json.NewDecoder(req.Body).Decode(&postedMessage)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"ok":true,"channel":"CUNAVAILABLEGRANT","ts":"2.0"}`,
				)),
				Request: req,
			}, nil
		},
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor: tools.Executor{
			Store:                 fixture.Store,
			IntegrationHTTPClient: integrationHTTPClient,
		},
		Now: func() time.Time { return now.Add(4 * time.Millisecond) },
	}
	err = executor.ExecuteModelWork(ctx, turn)
	if err != nil {
		t.Fatalf("execute turn after model grant unavailable: %v", err)
	}
	if len(modelClient.responses) != 1 {
		t.Fatalf(
			"model responded after unavailable grant; remaining responses=%d",
			len(modelClient.responses),
		)
	}
	if postedDecodeErr != nil {
		t.Fatalf("decode Slack runtime message: %v", postedDecodeErr)
	}
	if postCount != 1 {
		t.Fatalf("Slack runtime message post count = %d, want 1", postCount)
	}
	if postedPath != "/api/chat.postMessage" {
		t.Fatalf("Slack runtime message path = %q", postedPath)
	}
	if postedAuthorization != "Bearer "+botToken {
		t.Fatalf("Slack runtime message authorization = %q", postedAuthorization)
	}
	if postedMessage.Channel != "CUNAVAILABLEGRANT" ||
		postedMessage.ThreadTS != "1.0" ||
		postedMessage.Text != slack.AgentRequestFailureMessage {
		t.Fatalf("Slack runtime message = %+v", postedMessage)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		fixture,
		launch.Agent.ID,
		turn.TurnID,
		"auth",
		"model_grant_unavailable",
	)
	var providerRequestID, providerResponseID string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT context.provider_request_id,
		       context.provider_response_id
		FROM model_call_contexts context
		WHERE context.project_id = $1 AND context.agent_id = $2
		ORDER BY context.created_at DESC, context.id DESC
		LIMIT 1`, kernelTestProjectID, launch.Agent.ID).Scan(
		&providerRequestID,
		&providerResponseID,
	); err != nil {
		t.Fatalf("load unavailable-grant attempt evidence: %v", err)
	}
	if providerRequestID != "" || providerResponseID != "" {
		t.Fatalf(
			"pre-send normal response evidence = request %q response %q, want empty",
			providerRequestID,
			providerResponseID,
		)
	}
}

type kernelSlackRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kernelSlackRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func kernelSlackRuntimeHTTPClient(
	t *testing.T,
	identifier string,
	postMessage kernelSlackRoundTripFunc,
) *http.Client {
	t.Helper()
	identity, err := json.Marshal(map[string]any{
		"ok": true, "team_id": "T_" + identifier,
		"user_id": "B_KERNEL_TEST", "bot_id": "BOT_KERNEL_TEST",
	})
	require.NoError(t, err)
	return &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "Bearer xoxb-"+identifier, req.Header.Get("Authorization"))
		if req.URL.Path != "/api/auth.test" {
			return postMessage(req)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(identity))),
			Request:    req,
		}, nil
	})}
}

func attachKernelSlackHandler(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	agentID uuid.UUID,
	identifier, providerRef string,
) string {
	t.Helper()
	botToken := "xoxb-" + identifier
	secret, _, err := fixture.Store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:          kernelTestOrgID,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: kernelTestProjectID,
			Name:           "kernel-" + identifier + "-integration-credentials",
			Material: secrets.SlackAppCredentialsMaterial{
				AccessToken:   botToken,
				ClientID:      "client-id",
				ClientSecret:  "client-secret",
				SigningSecret: "signing-secret",
			},
			Actor: kernelTestUserPrincipal(kernelTestUserID),
		},
	)
	if err != nil {
		t.Fatalf("create integration credential secret: %v", err)
	}
	const handlerKey = "runtime-notifications"
	app, err := fixture.Store.Integrations().
		CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
			OrgID: kernelTestOrgID, ProjectID: kernelTestProjectID,
			Name: handlerKey, AppType: appdefinition.SlackThread,
		})
	require.NoError(t, err)
	install, err := fixture.Store.Integrations().
		ConfigureProjectApp(ctx, integrationstore.ConfigureProjectAppInput{
			OrgID:                 kernelTestOrgID,
			ProjectID:             kernelTestProjectID,
			AppID:                 app.ID,
			ExpectedSetupRevision: app.SetupRevision,
			InstalledByUserID:     kernelTestUserID,
			Provider:              integrationstore.IntegrationProviderSlack,
			ProviderTenantID:      "T_" + identifier,
			ProviderAccountRef:    "A_" + identifier,
			CredentialSecretID:    secret.ID,
			CredentialVersionID:   secret.CurrentVersionID,
			OAuthFlowID:           uuid.Must(uuid.NewV7()),
			ProviderIdentity:      json.RawMessage(`{"bot_user_id":"B_KERNEL_TEST"}`),
		})
	require.NoError(t, err)
	config := fixture.currentAgentConfig(t, ctx, agentID)
	source, err := agentconfig.ParseSource(
		agentconfig.SourceFormat(config.SourceFormat),
		[]byte(config.Source),
	)
	require.NoError(t, err)
	appID := install.ID
	channel, thread, found := strings.Cut(providerRef, ":")
	require.True(t, found, "runtime-message fixture requires a Slack thread")
	if source.InteractionHandlers == nil {
		source.InteractionHandlers = make(map[string]agentconfig.AgentConfigAppCapabilitySource)
	}
	source.InteractionHandlers[handlerKey] = agentconfig.AgentConfigAppCapabilitySource{}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	configuredModel := currentConfiguredModelForKernelConfig(t, ctx, fixture.Store, config)
	compiled, err := agentconfig.Compile(
		agentconfig.SourceFormatJSON,
		raw,
		agentconfig.CompileOptions{
			ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
				return resolvedKernelAgentConfigModel(configuredModel), nil
			},
			ResolveAppName: func(name string) (agentconfig.AppResolution, error) {
				require.Equal(t, install.Name, name)
				return agentconfig.AppResolution{
					AppID:   appID,
					AppType: install.AppType,
				}, nil
			},
		},
	)
	require.NoError(t, err)
	_, err = fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID: kernelTestProjectID, Source: string(raw), SourceFormat: "json",
			ConfiguredModelID: config.ConfiguredModelID, CompiledDefinition: compiled.CanonicalJSON,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:                 agentID,
		ExpectedCurrentConfigID: config.ID,
		ActorType:               "user",
		ActorID:                 kernelTestUserID,
		IdempotencyKey:          "kernel-handler-" + identifier,
	})
	require.NoError(t, err)

	tx, err := fixture.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, integrationstore.LockAppsTx(ctx, tx, kernelTestProjectID, nil, install.ID))
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: providerRef}
	require.NoError(
		t,
		integrationstore.LockConversationTx(ctx, tx, kernelTestProjectID, install.ID, address),
	)
	_, err = tx.Exec(
		ctx,
		"SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE",
		kernelTestProjectID,
		agentID,
	)
	require.NoError(t, err)
	target, err := fixture.Store.Integrations().
		EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
			ProjectID: kernelTestProjectID, AgentID: agentID, AppID: install.ID,
			Address: address,
		})
	require.NoError(t, err)
	selection, err := fixture.Store.Execution().SelectInteractionDestinationForOriginTx(
		ctx, tx, kernelTestProjectID, agentID, target.ID,
	)
	require.NoError(t, err)
	require.Equal(t, target.ID, selection.IntegrationTargetID)
	require.Equal(t, handlerKey, selection.HandlerKey)
	expected, err := json.Marshal(map[string]string{"channel_id": channel, "thread_ts": thread})
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(selection.Args))
	require.NoError(t, tx.Commit(ctx))
	return botToken
}

func TestAgentExecutorSettlesTurnWhenConfiguredModelWasDeleted(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/deleted-configured-model", now)
	config := fixture.currentAgentConfig(t, ctx, agentID)
	configuredModel := currentConfiguredModelForKernelConfig(t, ctx, fixture.Store, config)
	grant, err := fixture.Store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		kernelTestOrgID,
		kernelTestProjectID,
		configuredModel.ID,
	)
	if err != nil {
		t.Fatalf("load configured-model grant: %v", err)
	}
	if _, err := fixture.Store.Models().DeleteProjectModelGrant(
		ctx,
		kernelTestOrgID,
		kernelTestProjectID,
		grant.ID,
	); err != nil {
		t.Fatalf("revoke configured-model grant: %v", err)
	}
	if _, err := fixture.Store.Models().DeleteConfiguredModel(
		ctx,
		kernelTestOrgID,
		configuredModel.ID,
	); err != nil {
		t.Fatalf("delete configured model: %v", err)
	}

	work := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue",
		now.Add(time.Millisecond),
	)
	executor := AgentExecutor{
		Store: fixture.Store,
		ModelResolver: modelprovider.Resolver{
			Models:  fixture.Store.Models(),
			Secrets: fixture.Store.Secrets(),
		},
		ToolExecutor: tools.Executor{Store: fixture.Store},
		Now:          func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, work); err != nil {
		t.Fatalf("execute turn after configured model deletion: %v", err)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		fixture,
		agentID,
		work.TurnID,
		"invalid_request",
		"configured_model_revision_unavailable",
	)
}

func TestAgentExecutorAllowsPreparedAttemptAcrossGrantReplacement(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"finish the prepared request",
		fixture.Now.Add(time.Second),
	)
	config := fixture.currentAgentConfig(t, ctx, agentID)
	configuredModelID := configuredModelIDForKernelConfig(t, ctx, fixture.Store, config)
	originalGrantID := currentProjectModelGrantIDForKernelConfig(t, ctx, fixture.Store, config)

	var replaceOnce sync.Once
	var replacementGrant modelstore.ProjectModelGrantRecord
	var replacementErr error
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		afterPrepare: func() {
			replaceOnce.Do(func() {
				if _, replacementErr = fixture.Store.Models().DeleteProjectModelGrant(
					ctx,
					kernelTestOrgID,
					kernelTestProjectID,
					originalGrantID,
				); replacementErr != nil {
					return
				}
				replacementGrant, replacementErr = fixture.Store.Models().CreateProjectModelGrant(
					ctx,
					modelstore.CreateProjectModelGrantInput{
						OrgID:             kernelTestOrgID,
						ProjectID:         kernelTestProjectID,
						ConfiguredModelID: configuredModelID,
					},
				)
			})
		},
		responses: []model.Response{
			{
				ID: "resp_after_replacement_grant",
				Content: []model.ResponsePart{
					{Type: model.ResponsePartTypeText, Text: "prepared request completed"},
				},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute grant replacement race: %v", err)
	}
	if replacementErr != nil {
		t.Fatalf("replace model grant after request preparation: %v", replacementErr)
	}
	if replacementGrant.ID == uuid.Nil || replacementGrant.ID == originalGrantID {
		t.Fatalf("replacement grant = %+v, original=%s", replacementGrant, originalGrantID)
	}
	if modelClient.preparedCount() != 1 || modelClient.respondedCount() != 1 {
		t.Fatalf(
			"prepared/responded = %d/%d, want one completed attempt",
			modelClient.preparedCount(),
			modelClient.respondedCount(),
		)
	}
	var succeeded int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND input_event_sequence = $3
  AND state = 'succeeded'
  AND attempt_number = 1`,
		kernelTestProjectID,
		agentID,
		turn.OpeningEventSequence,
	).Scan(&succeeded); err != nil {
		t.Fatalf("count completed prepared attempt: %v", err)
	}
	if succeeded != 1 {
		t.Fatalf("completed prepared attempts = %d, want 1", succeeded)
	}
}

func TestAgentExecutorAllowsPreparedAttemptAcrossCredentialRotation(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"finish the prepared request",
		fixture.Now.Add(time.Second),
	)
	config := fixture.currentAgentConfig(t, ctx, agentID)
	configuredModel := currentConfiguredModelForKernelConfig(t, ctx, fixture.Store, config)
	provider, err := fixture.Store.Models().GetModelProviderConfig(
		ctx,
		config.OrgID,
		configuredModel.ModelProviderConfigID,
	)
	if err != nil {
		t.Fatalf("load model provider: %v", err)
	}
	credential, err := fixture.Store.Secrets().
		GetSecret(ctx, config.OrgID, provider.CredentialSecretID)
	if err != nil {
		t.Fatalf("load model credential: %v", err)
	}

	var rotateOnce sync.Once
	var rotatedVersion secretstore.SecretVersionRecord
	var rotationErr error
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		afterPrepare: func() {
			rotateOnce.Do(func() {
				_, rotatedVersion, rotationErr = fixture.Store.Secrets().CreateSecretVersion(
					ctx,
					secretstore.CreateSecretVersionInput{
						OrgID:    kernelTestOrgID,
						SecretID: provider.CredentialSecretID,
						Material: secrets.GenericMaterial{Value: "rotated-kernel-model-key"},
						Actor:    kernelTestUserPrincipal(kernelTestUserID),
					},
				)
			})
		},
		responses: []model.Response{
			{
				ID: "resp_after_credential_rotation",
				Content: []model.ResponsePart{
					{Type: model.ResponsePartTypeText, Text: "prepared request completed"},
				},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute credential rotation race: %v", err)
	}
	if rotationErr != nil {
		t.Fatalf("rotate credential after request preparation: %v", rotationErr)
	}
	if rotatedVersion.ID == uuid.Nil || rotatedVersion.ID == credential.CurrentVersionID {
		t.Fatalf(
			"credential version was not rotated: initial=%s rotated=%s",
			credential.CurrentVersionID,
			rotatedVersion.ID,
		)
	}
	if modelClient.preparedCount() != 1 || modelClient.respondedCount() != 1 {
		t.Fatalf(
			"prepared/responded = %d/%d, want one completed attempt",
			modelClient.preparedCount(),
			modelClient.respondedCount(),
		)
	}
	var succeeded int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND input_event_sequence = $3
  AND state = 'succeeded'
  AND attempt_number = 1`,
		kernelTestProjectID,
		agentID,
		turn.OpeningEventSequence,
	).Scan(&succeeded); err != nil {
		t.Fatalf("count completed prepared attempt: %v", err)
	}
	if succeeded != 1 {
		t.Fatalf("completed prepared attempts = %d, want 1", succeeded)
	}
}

func TestAgentExecutorRecordsErrorWhenCurrentModelLacksRequiredToolSupport(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	sourceYAML := `
instruction: Use the configured tool.
model:
  provider_config: openai-prod
  name: tool-support-model
tools:
  run_command: {}
`
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel Tool Support",
		"kernel-tool-support-agent",
		sourceYAML,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
			IdempotencyKey: "kernel-tool-support-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	config, found, err := fixture.Store.Execution().
		GetAgentConfig(ctx, kernelTestProjectID, profile.CurrentConfigID)
	if err != nil {
		t.Fatalf("load agent config: %v", err)
	}
	if !found {
		t.Fatalf("agent config not found")
	}
	configuredModel := currentConfiguredModelForKernelConfig(t, ctx, fixture.Store, config)
	supportsTools := false
	if _, err := fixture.Store.Models().PatchConfiguredModel(
		ctx,
		modelstore.PatchConfiguredModelInput{
			OrgID:                 kernelTestOrgID,
			ModelProviderConfigID: configuredModel.ModelProviderConfigID,
			ID:                    configuredModel.ID,
			SupportsTools:         &supportsTools,
		},
	); err != nil {
		t.Fatalf("disable model tool support: %v", err)
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		launch.Agent.ID,
		kernelTestUserID,
		"hello",
		now.Add(3*time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "tool-support-model",
		capabilities:      model.Capabilities{SupportsTools: &supportsTools},
		responses: []model.Response{
			{
				ID:         "resp_unreachable",
				Content:    []model.ResponsePart{{Type: "text", Text: "should not respond"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(4 * time.Millisecond) },
	}
	err = executor.ExecuteModelWork(ctx, turn)
	if err != nil {
		t.Fatalf("execute turn with unsupported required tools: %v", err)
	}
	if modelClient.preparedCount() != 0 {
		t.Fatalf(
			"model prepared %d requests after tool-support rejection, want 0",
			modelClient.preparedCount(),
		)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		fixture,
		launch.Agent.ID,
		turn.TurnID,
		"invalid_request",
		"required_tools_unsupported",
	)
}
