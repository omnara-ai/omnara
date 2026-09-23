//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type appLaunchHTTPFixture struct {
	handler                             http.Handler
	project                             publicHTTPProject
	configID, profileID, appID, appName string
	launchToken                         string
}

func newAppLaunchHTTPFixture(t *testing.T, seed string) appLaunchHTTPFixture {
	t.Helper()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, seed)
	app := createSlackHTTPApp(t, t.Context(), project, "A123", "T123", "Support")
	config := createPublicHTTPAgentConfig(t, handler, project, seed, "yaml",
		"instruction: Help with tickets.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken, http.StatusCreated)
	configID := testutil.RequireType[string](t, config["id"])
	profile := createPublicHTTPAgentProfile(t, handler, project, seed, "Ticket support", configID,
		project.AdminToken, http.StatusCreated)
	return appLaunchHTTPFixture{
		handler: handler, project: project, configID: configID, appName: app.Name,
		profileID: testutil.RequireType[string](t, profile["id"]), appID: testPublicID(t, publicid.KindProjectApp, app.ID),
		launchToken: customIntegrationHTTPKey(t, handler, project, "launch-manager", "admin"),
	}
}

func (f appLaunchHTTPFixture) body() map[string]any {
	initial := customIntegrationHTTPInput()
	initial["actor"] = map[string]any{"provider_tenant_id": "customer-directory", "provider_user_id": "requester-7"}
	body := hostedLaunchHTTPCapabilities(f.appName, f.appID)
	body["config"], body["profile"], body["initial_input"] = f.configID, f.profileID, initial
	return body
}

func (f appLaunchHTTPFixture) counts(t *testing.T) [6]int {
	t.Helper()
	var counts [6]int
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM agent_configs WHERE project_id=$1),
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content'),
		(SELECT count(*) FROM app_targets WHERE project_id=$1),
		(SELECT count(*) FROM app_subscriptions WHERE project_id=$1),
		(SELECT count(*) FROM actors WHERE project_id=$1)`, f.project.ProjectUUID).
		Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5]))
	return counts
}

func TestPublicAppLaunchAtomicRollbackAndReplay(t *testing.T) {
	t.Parallel()
	f := newAppLaunchHTTPFixture(t, "app-launch-atomic")
	ctx := t.Context()
	pool := integrationPoolForHandler(t, f.handler)
	before := f.counts(t)
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_launch_initial() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.input_kind='content' THEN RAISE EXCEPTION 'fixture rejects initial input'; END IF;
		RETURN NEW; END $$;
		CREATE TRIGGER reject_launch_initial BEFORE INSERT ON agent_inputs
		FOR EACH ROW EXECUTE FUNCTION reject_launch_initial()`)
	require.NoError(t, err)
	body := projectAppHTTPJSON(t, f.body())
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		body, "atomic-launch", http.StatusInternalServerError, authHeaders(f.launchToken))
	require.Equal(t, before, f.counts(t), "failed input must leave no config, agent, input, target, subscription or actor")
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_launch_initial ON agent_inputs; DROP FUNCTION reject_launch_initial()`)
	require.NoError(t, err)
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		body, "atomic-launch", http.StatusCreated, authHeaders(f.launchToken))
	agent := testutil.RequireType[map[string]any](t, launched["agent"])
	config := testutil.RequireType[map[string]any](t, launched["agent_config"])
	input := testutil.RequireType[map[string]any](t, launched["agent_input"])
	require.NotEqual(t, f.configID, config["id"])
	require.Equal(t, config["id"], agent["current_config_id"])
	agentID := mustPublicHTTPID(t, publicid.KindAgent, testutil.RequireType[string](t, agent["id"]))
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, testutil.RequireType[string](t, config["id"]))
	inputID := mustPublicHTTPID(t, publicid.KindAgentInput, testutil.RequireType[string](t, input["id"]))
	stored, found, err := f.project.Store.Execution().GetAgentConfig(ctx, f.project.ProjectUUID, configID)
	require.NoError(t, err)
	require.True(t, found)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(stored.CompiledDefinition, &compiled))
	appUUID := mustPublicHTTPID(t, publicid.KindProjectApp, f.appID)
	require.Equal(t, appUUID, compiled.InteractionHandlers[f.appName].AppID)
	toolName := toolcatalog.AppToolName(f.appName, toolcatalog.AppOperationRead)
	require.Equal(t, appUUID, compiled.Tools[toolName].AppID)
	publicCompiled := testutil.RequireType[map[string]any](t, config["compiled_definition"])
	publicTools := testutil.RequireType[map[string]any](t, publicCompiled["tools"])
	publicHandlers := testutil.RequireType[map[string]any](t, publicCompiled["interaction_handlers"])
	require.Equal(t, f.appID, testutil.RequireType[map[string]any](t, publicTools[toolName])["app_id"])
	require.Equal(t, f.appID, testutil.RequireType[map[string]any](t, publicHandlers[f.appName])["app_id"])
	var provider, actorTenant, actorUser string
	var noTarget bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT input.app_target_id IS NULL,
  actor.provider, actor.provider_tenant_id, actor.provider_user_id
  FROM agent_inputs input JOIN actors actor ON actor.id=input.actor_id WHERE input.id=$1`, inputID).
		Scan(&noTarget, &provider, &actorTenant, &actorUser))
	require.True(t, noTarget, "ordinary initial input has no provider routing origin")
	require.Equal(t, "external", provider)
	require.Equal(t, "customer-directory", actorTenant)
	require.Equal(t, "requester-7", actorUser)
	selection, err := f.project.Store.Execution().GetInteractionSelection(ctx, f.project.ProjectUUID, agentID)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, selection.AppTargetID)
	profile := requestJSONWithHeaders(t, f.handler, http.MethodGet,
		f.project.ProjectPath+"/agent-profiles/"+f.profileID, "", "", http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, f.configID, testutil.RequireType[map[string]any](t, profile["current_config"])["id"])
	after := f.counts(t)
	for _, i := range []int{0, 1, 2, 4} {
		require.Equal(t, before[i]+1, after[i], "one new config, agent, content input and subscription")
	}
	require.Equal(t, before[3], after[3],
		"ordinary external input and configured capabilities create no attribution target")
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, f.project.ProjectPath+"/apps/"+f.appID,
		"", "", http.StatusNoContent, authHeaders(f.project.AdminToken))
	after = f.counts(t)
	require.Zero(t, after[4], "app deletion retires its subscriptions")
	replayed := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		body, "atomic-launch", http.StatusOK, authHeaders(f.launchToken))
	require.Len(t, replayed, 1)
	require.Equal(t, agent["id"], testutil.RequireType[map[string]any](t, replayed["agent"])["id"])
	require.Equal(t, after, f.counts(t), "replay must not create or activate anything")
	changed := f.body()
	changed["initial_input"] = map[string]any{
		"content_blocks": []any{map[string]any{"type": "text", "text": "changed\x00"}},
	}
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, changed), "atomic-launch", http.StatusOK, authHeaders(f.launchToken))
	require.Equal(t, after, f.counts(t))
}

func TestPublicAppLaunchAuthorizationAndAppBoundary(t *testing.T) {
	t.Parallel()
	f := newAppLaunchHTTPFixture(t, "app-launch-auth")
	before := f.counts(t)
	operator := customIntegrationHTTPKey(t, f.handler, f.project, "operator", "operator")
	viewer := customIntegrationHTTPKey(t, f.handler, f.project, "viewer", "viewer")
	body := projectAppHTTPJSON(t, f.body())
	for _, token := range []string{operator, viewer} {
		requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
			body, "denied", http.StatusForbidden, authHeaders(token))
	}
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		body, "denied", http.StatusUnauthorized, nil)
	other := projectAppHTTPSecondProject(t, f.handler, f.project)
	otherApp := createSlackHTTPApp(t, t.Context(), other, "A999", "T999", "Other support")
	for _, name := range []string{otherApp.Name, "missing-app"} {
		request := f.body()
		for key, value := range hostedLaunchHTTPCapabilities(name, testPublicID(t, publicid.KindProjectApp, otherApp.ID)) {
			request[key] = value
		}
		requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
			projectAppHTTPJSON(t, request), "denied-app", http.StatusBadRequest, authHeaders(f.launchToken))
	}
	require.Equal(t, before, f.counts(t))
	plain := f.body()
	removeLaunchHTTPCapabilities(plain)
	created := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, plain), "operator-launch", http.StatusCreated, authHeaders(operator))
	require.Equal(t, f.configID, testutil.RequireType[map[string]any](t, created["agent_config"])["id"])
	require.Equal(t, before[0], f.counts(t)[0])
	require.Equal(t, before[4], f.counts(t)[4], "ordinary input creates no subscription")
}

func TestPublicAppLaunchWithoutProfile(t *testing.T) {
	t.Parallel()
	f := newAppLaunchHTTPFixture(t, "app-launch-no-profile")
	body := f.body()
	delete(body, "profile")
	encoded := projectAppHTTPJSON(t, body)
	before := f.counts(t)
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		encoded, "pinned-launch", http.StatusCreated, authHeaders(f.launchToken))
	agent := testutil.RequireType[map[string]any](t, launched["agent"])
	config := testutil.RequireType[map[string]any](t, launched["agent_config"])
	require.NotEqual(t, f.configID, config["id"])
	require.Equal(t, config["id"], agent["current_config_id"])
	agentID := mustPublicHTTPID(t, publicid.KindAgent, testutil.RequireType[string](t, agent["id"]))
	var noProfile bool
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT agent_profile_id IS NULL FROM agents WHERE id=$1`, agentID).Scan(&noProfile))
	require.True(t, noProfile)
	after := f.counts(t)
	for _, i := range []int{0, 1, 2, 4} {
		require.Equal(t, before[i]+1, after[i], "one config, agent, input and subscription")
	}
	replayed := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		encoded, "pinned-launch", http.StatusOK, authHeaders(f.launchToken))
	require.Equal(t, agent["id"], testutil.RequireType[map[string]any](t, replayed["agent"])["id"])
	require.Equal(t, after, f.counts(t))
}

func TestPublicAppLaunchRejectsInvalidAttachmentsAndInput(t *testing.T) {
	t.Parallel()
	f := newAppLaunchHTTPFixture(t, "app-launch-invalid")
	before := f.counts(t)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		key  string
	}{
		{"message-and-input", func(body map[string]any) { body["message"] = "" }, "invalid"},
		{"origin", func(body map[string]any) {
			initial := testutil.RequireType[map[string]any](t, body["initial_input"])
			initial["origin"] = map[string]any{
				"app_id":  f.appID,
				"address": map[string]any{"kind": "channel", "ref": "C123"},
			}
		}, "invalid"},
		{"empty-input", func(body map[string]any) {
			body["initial_input"] = map[string]any{"content_blocks": []any{}}
		}, "invalid"},
		{"media", func(body map[string]any) {
			body["initial_input"] = map[string]any{"content_blocks": []any{map[string]any{
				"type": "media", "mime_type": "text/plain", "data": "YQ==",
			}}}
		}, "invalid"},
		{"unknown-app", func(body map[string]any) {
			for key, value := range hostedLaunchHTTPCapabilities("missing-app", f.appID) {
				body[key] = value
			}
		}, "invalid"},
		{"unknown-handler-field", func(body map[string]any) {
			body["interaction_handlers"] = map[string]any{
				f.appName: map[string]any{"unexpected": true},
			}
		}, "invalid"},
	} {
		body := f.body()
		tc.edit(body)
		response := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
			projectAppHTTPJSON(t, body), tc.key, http.StatusBadRequest, authHeaders(f.launchToken))
		require.NotEmpty(t, response["error"], tc.name)
	}
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, f.body()), "user-actor", http.StatusBadRequest, f.project.adminBrowserAuthHeaders())
	require.Equal(t, before, f.counts(t))
}

func TestPublicAppLaunchPreservesPinnedProfileConfigContract(t *testing.T) {
	t.Parallel()
	f := newAppLaunchHTTPFixture(t, "app-launch-profile")
	otherConfig := createPublicHTTPAgentConfig(t, f.handler, f.project, "unrelated-config", "yaml",
		"instruction: A different base.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		f.project.AdminToken, http.StatusCreated)
	otherID := testutil.RequireType[string](t, otherConfig["id"])
	body := f.body()
	body["config"] = otherID
	before := f.counts(t)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, body), "foreign-profile-config", http.StatusNotFound, authHeaders(f.launchToken))
	require.Equal(t, before, f.counts(t), "derivation must not bypass profile membership")
	requestJSONWithHeaders(t, f.handler, http.MethodPost,
		f.project.ProjectPath+"/agent-profiles/"+f.profileID+"/config",
		projectAppHTTPJSON(t, map[string]any{"config": otherID, "expected_current_config_id": f.configID}),
		"retarget-profile", http.StatusOK, authHeaders(f.project.AdminToken))
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, f.body()), "historical-profile-config", http.StatusCreated, authHeaders(f.launchToken))
}

func TestPublicAppLaunchInitialInputReplay(t *testing.T) {
	t.Parallel()
	for _, withApp := range []bool{false, true} {
		name := "without-app"
		if withApp {
			name = "with-app"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newAppLaunchHTTPFixture(t, "launch-replay-"+name)
			body := f.body()
			if !withApp {
				removeLaunchHTTPCapabilities(body)
			}
			before := f.counts(t)
			encoded := projectAppHTTPJSON(t, body)
			launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
				encoded, "launch-event", http.StatusCreated, authHeaders(f.launchToken))
			agent := testutil.RequireType[map[string]any](t, launched["agent"])
			after := f.counts(t)
			require.Equal(t, before[2]+1, after[2])
			if !withApp {
				require.Equal(t, before[0], after[0], "ordinary initial input does not derive a config")
				require.Equal(t, before[4], after[4], "ordinary initial input creates no subscription")
			}
			replay := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
				encoded, "launch-event", http.StatusOK, authHeaders(f.launchToken))
			require.Equal(t, agent["id"], testutil.RequireType[map[string]any](t, replay["agent"])["id"])
			require.Equal(t, after, f.counts(t))
			path := f.project.ProjectPath + "/agents/" + testutil.RequireType[string](t, agent["id"]) + "/inputs"
			input := requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
				projectAppHTTPJSON(t, body["initial_input"]), "launch-event", http.StatusCreated, authHeaders(f.launchToken))
			require.NotEqual(t, testutil.RequireType[map[string]any](t, launched["agent_input"])["id"],
				testutil.RequireType[map[string]any](t, input["agent_input"])["id"])
			replay = requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
				projectAppHTTPJSON(t, body["initial_input"]), "launch-event", http.StatusOK, authHeaders(f.launchToken))
			require.Equal(t, input["agent_input"], replay["agent_input"])
			require.Equal(t, after[2]+1, f.counts(t)[2])
		})
	}
}

func hostedLaunchHTTPCapabilities(appName, appID string) map[string]any {
	conversation := map[string]any{"channel_id": "C123", "thread_ts": "123.456"}
	return map[string]any{
		"tools": map[string]any{toolcatalog.AppToolName(appName, toolcatalog.AppOperationRead): map[string]any{}},
		"subscriptions": []any{map[string]any{
			"app_id": appID, "type": "thread_messages", "conversation": conversation, "events": []string{"message"},
		}},
		"interaction_handlers": map[string]any{appName: map[string]any{}},
	}
}

func removeLaunchHTTPCapabilities(body map[string]any) {
	delete(body, "tools")
	delete(body, "subscriptions")
	delete(body, "interaction_handlers")
}

func TestPublicSubscriptionOnlyLaunchPreservesConfigAndDetachedReplay(t *testing.T) {
	t.Parallel()
	f := newAppLaunchHTTPFixture(t, "subscription-launch")
	body := f.body()
	delete(body, "tools")
	delete(body, "interaction_handlers")
	before := f.counts(t)
	for _, role := range []string{"viewer", "operator"} {
		token := customIntegrationHTTPKey(t, f.handler, f.project, "subscription-launch-"+role, role)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
			projectAppHTTPJSON(t, body), "subscription-launch", http.StatusForbidden, authHeaders(token))
	}
	require.Equal(t, before, f.counts(t), "subscription-only launch still requires manage")
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, body), "subscription-launch", http.StatusCreated, authHeaders(f.launchToken))
	agent := testutil.RequireType[map[string]any](t, launched["agent"])
	require.Equal(t, f.configID, agent["current_config_id"])
	require.Equal(t, f.configID, testutil.RequireType[map[string]any](t, launched["agent_config"])["id"])
	after := f.counts(t)
	require.Equal(t, before[0], after[0], "subscriptions alone must not derive an empty config")
	require.Equal(t, before[1]+1, after[1])
	require.Equal(t, before[2]+1, after[2])
	require.Equal(t, before[4]+1, after[4])
	path := f.project.ProjectPath + "/apps/" + f.appID + "/subscriptions"
	page := requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, authHeaders(f.launchToken))
	data := testutil.RequireType[[]any](t, page["data"])
	require.Len(t, data, 1)
	subscription := testutil.RequireType[map[string]any](t, data[0])
	require.Equal(t, agent["id"], subscription["agent_id"])
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path+"/"+testutil.RequireType[string](t, subscription["id"]),
		"", "", http.StatusNoContent, authHeaders(f.launchToken))
	after = f.counts(t)
	body["subscriptions"] = []any{map[string]any{"app_id": f.appID, "type": "missing", "conversation": map[string]any{}}}
	replay := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, body), "subscription-launch", http.StatusOK, authHeaders(f.launchToken))
	require.Equal(t, agent["id"], testutil.RequireType[map[string]any](t, replay["agent"])["id"])
	require.Equal(t, after, f.counts(t), "launch replay cannot restore a detached subscription")
}

func TestPublicSubscriptionLaunchValidationRollsBackAllAttachments(t *testing.T) {
	t.Parallel()
	f := newAppLaunchHTTPFixture(t, "subscription-launch-invalid")
	other := projectAppHTTPSecondProject(t, f.handler, f.project)
	foreign := createSlackHTTPApp(t, t.Context(), other, "AFOREIGN", "TFOREIGN", "Foreign")
	before := f.counts(t)
	for _, tc := range []struct {
		name   string
		status int
		edit   func(map[string]any)
	}{
		{"unknown-app", http.StatusNotFound, func(a map[string]any) {
			a["app_id"] = testPublicID(t, publicid.KindProjectApp, uuid.New())
		}},
		{"foreign-app", http.StatusNotFound, func(a map[string]any) {
			a["app_id"] = testPublicID(t, publicid.KindProjectApp, foreign.ID)
		}},
		{"wrong-id-kind", http.StatusBadRequest, func(a map[string]any) { a["app_id"] = f.configID }},
		{"unknown-type", http.StatusBadRequest, func(a map[string]any) { a["type"] = "missing" }},
		{"empty-conversation", http.StatusBadRequest, func(a map[string]any) { a["conversation"] = map[string]any{} }},
		{"invalid-event", http.StatusBadRequest, func(a map[string]any) { a["events"] = []string{"commit"} }},
	} {
		t.Logf("invalid subscription case: %s", tc.name)
		body := f.body()
		delete(body, "tools")
		delete(body, "interaction_handlers")
		bad := map[string]any{
			"app_id": f.appID, "type": "thread_messages", "conversation": map[string]any{"channel_id": "COTHER"},
		}
		tc.edit(bad)
		body["subscriptions"] = append(testutil.RequireType[[]any](t, body["subscriptions"]), bad)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
			projectAppHTTPJSON(t, body), "invalid-subscription", tc.status, authHeaders(f.launchToken))
		require.Equal(t, before, f.counts(t), "failed validation must roll back all launch resources: %s", tc.name)
	}
	body := f.body()
	attachments := make([]any, 101)
	for i := range attachments {
		attachments[i] = testutil.RequireType[[]any](t, body["subscriptions"])[0]
	}
	body["subscriptions"] = attachments
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectAppHTTPJSON(t, body), "too-many-subscriptions", http.StatusBadRequest, authHeaders(f.launchToken))
	require.Equal(t, before, f.counts(t))
}
