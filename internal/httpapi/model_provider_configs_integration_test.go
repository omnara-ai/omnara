//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestModelProviderConfigRoutesBackAgentConfigCompilation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "model-provider-configs")

	secret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"openai-provider-key","material":{"kind":"generic","value":"sk-test"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secretID := testutil.RequireType[string](t, secret["id"])
	createResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"openai-secondary","preset":"openai","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	providerConfig := createdModelProviderConfig(t, createResponse)
	if testutil.RequireType[map[string]any](t, createResponse["model_catalog"])["status"] != "failed" {
		t.Fatalf("create with stubbed discoverer should report failed discovery: %+v", createResponse)
	}
	providerConfigID := testutil.RequireType[string](t, providerConfig["id"])
	if providerConfig["management_kind"] != string(management.Tenant) {
		t.Fatalf("unexpected tenant provider management fields: %+v", providerConfig)
	}
	if providerConfig["api_format"] != "openai-responses" || providerConfig["base_url"] != "https://api.openai.com/v1" ||
		providerConfig["endpoint_path"] != "/responses" ||
		providerConfig["auth_kind"] != "bearer_token" ||
		len(testutil.RequireType[map[string]any](t, providerConfig["auth_options"])) != 0 ||
		providerConfig["request_timeout_ms"] != float64(modelstore.DefaultModelProviderRequestTimeoutMS) ||
		providerConfig["idle_timeout_ms"] != float64(modelstore.DefaultModelProviderIdleTimeoutMS) {
		t.Fatalf("preset did not materialize OpenAI provider config: %+v", providerConfig)
	}
	openRouterConfig := createdModelProviderConfig(t, requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"openrouter-secondary","preset":"openrouter","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	))
	openRouterConfigID := testutil.RequireType[string](t, openRouterConfig["id"])
	if openRouterConfig["api_format"] != "openai-chat-completions" ||
		openRouterConfig["api_variant"] != "openrouter" ||
		openRouterConfig["base_url"] != "https://openrouter.ai/api/v1" ||
		openRouterConfig["endpoint_path"] != "/chat/completions" ||
		openRouterConfig["auth_kind"] != "bearer_token" {
		t.Fatalf("preset did not materialize OpenRouter provider config: %+v", openRouterConfig)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"invalid-preset-mix","preset":"openai","api_format":"openai-responses","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"invalid-preset-path","preset":"openai","endpoint_path":"/custom","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"invalid-preset-auth","preset":"openai","auth_kind":"bearer_token","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	updatedSecret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"openai-provider-key-2","material":{"kind":"generic","value":"sk-test-2"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	updatedProviderConfig := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"base_url":"`+"https://proxy.example.test/v1"+`","endpoint_path":"/custom-responses","request_timeout_ms":30000,"auth_kind":"api_key_header","auth_options":{"header_name":"x-api-key"},"credential_secret_id":"`+testutil.RequireType[string](t, updatedSecret["id"])+`"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updatedProviderConfig["id"] != providerConfigID ||
		updatedProviderConfig["base_url"] != "https://proxy.example.test/v1" ||
		updatedProviderConfig["endpoint_path"] != "/custom-responses" ||
		updatedProviderConfig["auth_kind"] != "api_key_header" ||
		testutil.RequireType[map[string]any](t, updatedProviderConfig["auth_options"])["header_name"] != "x-api-key" ||
		updatedProviderConfig["credential_secret_id"] != testutil.RequireType[string](t, updatedSecret["id"]) ||
		updatedProviderConfig["request_timeout_ms"] != float64(30000) {
		t.Fatalf("provider config update mismatch: %+v", updatedProviderConfig)
	}
	patchedProviderConfig := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"base_url":"https://proxy2.example.test/v1"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if patchedProviderConfig["base_url"] != "https://proxy2.example.test/v1" ||
		patchedProviderConfig["endpoint_path"] != "/custom-responses" ||
		patchedProviderConfig["auth_kind"] != "api_key_header" ||
		testutil.RequireType[map[string]any](t, patchedProviderConfig["auth_options"])["header_name"] != "x-api-key" ||
		patchedProviderConfig["credential_secret_id"] != testutil.RequireType[string](t, updatedSecret["id"]) ||
		patchedProviderConfig["request_timeout_ms"] != float64(30000) {
		t.Fatalf("provider config patch should preserve omitted options: %+v", patchedProviderConfig)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"endpoint_path":"responses"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"auth_kind":"bearer_token","auth_options":{"header_name":"authorization"}}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"api_format":"anthropic-messages","base_url":"https://api.anthropic.com/v1","credential_secret_id":"`+testutil.RequireType[string](t, updatedSecret["id"])+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"request_timeout_ms":0}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	resetAuthProviderConfig := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"auth_kind":"bearer_token"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if resetAuthProviderConfig["auth_kind"] != "bearer_token" ||
		len(testutil.RequireType[map[string]any](t, resetAuthProviderConfig["auth_options"])) != 0 {
		t.Fatalf("auth_kind patch should reset omitted auth_options to defaults: %+v", resetAuthProviderConfig)
	}
	unknownConfiguredModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models",
		`{"name":"missing-output-ceiling","provider_model_slug":"missing-output-ceiling","context_window_tokens":128000}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if unknownConfiguredModel["max_output_tokens"] != nil ||
		unknownConfiguredModel["default_max_output_tokens"] != nil {
		t.Fatalf("unknown configured model limits = %+v", unknownConfiguredModel)
	}
	configuredModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models",
		`{"name":"gpt-test","provider_model_slug":"gpt-test","context_window_tokens":128000,"max_output_tokens":8192,"default_max_output_tokens":4096,"supports_tools":true,"supports_reasoning":true,"default_reasoning_effort":"high","supported_reasoning_efforts":["low","medium","high"],"input_modalities":["text"],"output_modalities":["text"],"api_variant_options":{"temperature":0.2}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	configuredModelID := testutil.RequireType[string](t, configuredModel["id"])
	if configuredModel["management_kind"] != string(management.Tenant) ||
		configuredModel["name"] != "gpt-test" || configuredModel["provider_model_slug"] != "gpt-test" ||
		configuredModel["supports_tools"] != true ||
		configuredModel["supports_reasoning"] != true ||
		configuredModel["default_reasoning_effort"] != "high" ||
		testutil.RequireType[[]any](t, configuredModel["supported_reasoning_efforts"])[2] != "high" ||
		testutil.RequireType[map[string]any](t, configuredModel["api_variant_options"])["temperature"] != 0.2 {
		t.Fatalf("configured model capability/options response mismatch: %+v", configuredModel)
	}
	if _, ok := configuredModel["default_cache_retention"]; ok {
		t.Fatalf("unset cache retention must be omitted from the response: %+v", configuredModel)
	}
	openRouterModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+openRouterConfigID+"/models",
		`{"name":"openrouter-model","provider_model_slug":"openrouter/model","context_window_tokens":128000,"max_output_tokens":8192,"default_max_output_tokens":4096,"api_variant_options":{"provider":{"only":["anthropic"],"require_parameters":true,"data_collection":"deny","sort":{"by":"latency","partition":"model"},"preferred_max_latency":{"p90":900},"max_price":{"prompt":"0.01","image":0.02}}}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if testutil.RequireType[map[string]any](
		t, testutil.RequireType[map[string]any](t, openRouterModel["api_variant_options"])["provider"],
	)["require_parameters"] != true {
		t.Fatalf("openrouter configured model api_variant_options create mismatch: %+v", openRouterModel)
	}
	updatedOpenRouterModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+openRouterConfigID+"/models/"+testutil.RequireType[string](t, openRouterModel["id"]),
		`{"api_variant_options":{"unknown":true}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if testutil.RequireType[map[string]any](t, updatedOpenRouterModel["api_variant_options"])["unknown"] != true {
		t.Fatalf("openrouter configured model should accept arbitrary flat options: %+v", updatedOpenRouterModel)
	}
	updatedOptionsModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models/"+configuredModelID,
		`{"api_variant_options":{"provider":{"only":["openai"]}}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if testutil.RequireType[[]any](
		t,
		testutil.RequireType[map[string]any](
			t, testutil.RequireType[map[string]any](t, updatedOptionsModel["api_variant_options"])["provider"],
		)["only"],
	)[0] != "openai" {
		t.Fatalf("configured model should accept provider passthrough options: %+v", updatedOptionsModel)
	}
	updatedConfiguredModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models/"+configuredModelID,
		`{"context_window_tokens":128000,"max_output_tokens":8192,"default_max_output_tokens":2048,"supports_tools":true,"supports_reasoning":true,"default_reasoning_effort":"medium","supported_reasoning_efforts":["low","medium","high"],"input_modalities":["text"],"output_modalities":["text"]}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updatedConfiguredModel["id"] != configuredModelID || updatedConfiguredModel["name"] != "gpt-test" ||
		updatedConfiguredModel["provider_model_slug"] != "gpt-test" ||
		updatedConfiguredModel["default_max_output_tokens"] != float64(2048) ||
		updatedConfiguredModel["default_reasoning_effort"] != "medium" ||
		testutil.RequireType[[]any](
			t,
			testutil.RequireType[map[string]any](
				t, testutil.RequireType[map[string]any](t, updatedConfiguredModel["api_variant_options"])["provider"],
			)["only"],
		)[0] != "openai" {
		t.Fatalf("configured model update mismatch: %+v", updatedConfiguredModel)
	}
	patchedConfiguredModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models/"+configuredModelID,
		`{"max_output_tokens":96000}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if patchedConfiguredModel["max_output_tokens"] != float64(96000) ||
		patchedConfiguredModel["default_max_output_tokens"] != float64(2048) ||
		patchedConfiguredModel["supports_tools"] != true ||
		patchedConfiguredModel["supports_reasoning"] != true ||
		patchedConfiguredModel["default_reasoning_effort"] != "medium" ||
		testutil.RequireType[[]any](t, patchedConfiguredModel["supported_reasoning_efforts"])[1] != "medium" {
		t.Fatalf("configured model patch should preserve omitted fields: %+v", patchedConfiguredModel)
	}
	for _, body := range []string{
		`{"name":null}`,
		`{"provider_model_slug":null}`,
		`{"context_window_tokens":null}`,
		`{"default_cache_retention":null}`,
		`{"supports_tools":null}`,
		`{"supports_reasoning":null}`,
		`{"default_reasoning_effort":null}`,
		`{"supported_reasoning_efforts":null}`,
		`{"input_modalities":null}`,
		`{"output_modalities":null}`,
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models/"+configuredModelID,
			body,
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}
	ungrantedConfiguredModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models",
		`{"name":"gpt-ungranted","provider_model_slug":"gpt-ungranted","context_window_tokens":128000,"max_output_tokens":8192,"default_max_output_tokens":4096}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-configs",
		agentConfigSourceBody(`instruction: Test.
model:
  provider_config: openai-secondary
  name: gpt-ungranted
`),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	grantBody := `{"configured_model_id":"` + configuredModelID + `","context_window_tokens":64000,"max_output_tokens":4096,"default_max_output_tokens":2048,"supports_tools":true,"supports_reasoning":true,"default_reasoning_effort":"medium","supported_reasoning_efforts":["low","medium"],"input_modalities":["text"],"output_modalities":["text"]}`
	grant := testutil.RequireType[map[string]any](
		t,
		requestJSONWithHeaders(
			t, handler, http.MethodPost, project.ProjectPath+"/model-grants", grantBody, "", http.StatusCreated,
			authHeaders(project.AdminToken),
		)["grant"],
	)
	if grant["configured_model_id"] != configuredModelID {
		t.Fatalf("unexpected model grant response: %+v", grant)
	}
	if grant["context_window_tokens"] != float64(64000) || grant["max_output_tokens"] != float64(4096) ||
		grant["default_max_output_tokens"] != float64(2048) ||
		grant["supports_tools"] != true ||
		grant["supports_reasoning"] != true ||
		grant["default_reasoning_effort"] != "medium" ||
		testutil.RequireType[[]any](t, grant["supported_reasoning_efforts"])[1] != "medium" ||
		testutil.RequireType[[]any](t, grant["input_modalities"])[0] != "text" ||
		testutil.RequireType[[]any](t, grant["output_modalities"])[0] != "text" {
		t.Fatalf("model grant overlay response mismatch: %+v", grant)
	}
	if _, ok := grant["metadata"]; ok {
		t.Fatalf("model grant response should not include metadata: %+v", grant)
	}
	duplicate := requestJSONWithHeaders(
		t, handler, http.MethodPost, project.ProjectPath+"/model-grants", grantBody, "", http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	if duplicate["code"] != "conflict" {
		t.Fatalf("duplicate grant error = %v, want conflict", duplicate)
	}
	grantPath := project.ProjectPath + "/model-grants/" + testutil.RequireType[string](t, grant["id"])
	patchedGrant := testutil.RequireType[map[string]any](t, requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		grantPath,
		`{"max_output_tokens":2048,"context_window_tokens":null,"supports_tools":false,"default_cache_retention":null}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)["grant"])
	if patchedGrant["id"] != grant["id"] ||
		patchedGrant["max_output_tokens"] != float64(2048) ||
		patchedGrant["context_window_tokens"] != nil ||
		patchedGrant["supports_tools"] != false ||
		patchedGrant["supports_reasoning"] != true ||
		patchedGrant["default_max_output_tokens"] != float64(2048) ||
		patchedGrant["default_reasoning_effort"] != "medium" {
		t.Fatalf("patched model grant mismatch: %+v", patchedGrant)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		grantPath,
		`{}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		grantPath,
		`{"default_cache_retention":"forever"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		grantPath,
		`{"context_window_tokens":256000}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		grantPath,
		`{"input_modalities":["audio"]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		project.ProjectPath+"/model-grants/"+
			testPublicID(t, publicid.KindProjectModelGrant, httpTestID("missing-model-grant")),
		`{"max_output_tokens":1024}`,
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	restoredGrant := testutil.RequireType[map[string]any](t, requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		grantPath,
		`{"max_output_tokens":4096,"context_window_tokens":64000,"supports_tools":true}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)["grant"])
	if restoredGrant["max_output_tokens"] != float64(4096) ||
		restoredGrant["context_window_tokens"] != float64(64000) ||
		restoredGrant["supports_tools"] != true {
		t.Fatalf("restored model grant mismatch: %+v", restoredGrant)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/model-grants",
		`{"configured_model_id":"`+configuredModelID+`","metadata":{"purpose":"coding"}}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/model-grants",
		`{"configured_model_id":"`+configuredModelID+`","context_window_tokens":256000}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/model-grants",
		`{"configured_model_id":"`+configuredModelID+`","input_modalities":["audio"]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	config := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-configs",
		agentConfigSourceBody(`instruction: Test.
model:
  provider_config: openai-secondary
  name: gpt-test
`),
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	model := testutil.RequireType[map[string]any](t, config["model"])
	if model["provider_config"] != "openai-secondary" || model["name"] != "gpt-test" ||
		model["configured_model_id"] != configuredModelID ||
		model["api_format"] != "openai-responses" {
		t.Fatalf("agent config model projection mismatch: %+v", model)
	}
	if model["context_window_tokens"] != float64(64000) || model["max_output_tokens"] != float64(4096) ||
		model["default_max_output_tokens"] != float64(2048) ||
		model["supports_tools"] != true ||
		model["supports_reasoning"] != true ||
		model["default_reasoning_effort"] != "medium" ||
		testutil.RequireType[[]any](t, model["supported_reasoning_efforts"])[1] != "medium" {
		t.Fatalf("agent config model effective options mismatch: %+v", model)
	}
	if model["current_revision_id"] != patchedConfiguredModel["current_revision_id"] {
		t.Fatalf("agent config did not project current configured model revision: %+v", model)
	}
	renamedConfiguredModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models/"+configuredModelID,
		`{"name":"gpt-renamed"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if renamedConfiguredModel["id"] != configuredModelID || renamedConfiguredModel["name"] != "gpt-renamed" ||
		renamedConfiguredModel["current_revision_id"] != patchedConfiguredModel["current_revision_id"] {
		t.Fatalf(
			"configured model rename should keep identity and current revision: before=%+v after=%+v",
			patchedConfiguredModel,
			renamedConfiguredModel,
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-configs",
		agentConfigSourceBody(`instruction: Test.
model:
  provider_config: openai-secondary
  name: gpt-test
`),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	renamedConfig := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-configs",
		agentConfigSourceBody(`instruction: Test.
model:
  provider_config: openai-secondary
  name: gpt-renamed
`),
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	renamedCompiledModel := testutil.RequireType[map[string]any](t, renamedConfig["model"])
	if renamedCompiledModel["name"] != "gpt-renamed" || renamedCompiledModel["configured_model_id"] != configuredModelID {
		t.Fatalf("renamed configured model projection mismatch: %+v", renamedCompiledModel)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models/"+testutil.RequireType[string](t, ungrantedConfiguredModel["id"]),
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID+"/models/"+configuredModelID,
		"",
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		"",
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/model-grants/"+testutil.RequireType[string](t, grant["id"]),
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-configs",
		agentConfigSourceBody(`instruction: Test.
model:
  provider_config: openai-secondary
  name: gpt-renamed
`),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	configuredModelUUID := mustPublicHTTPID(t, publicid.KindConfiguredModel, configuredModelID)
	providerConfigUUID := mustPublicHTTPID(t, publicid.KindModelProviderConfig, providerConfigID)
	if _, err := pool.Exec(
		ctx,
		`UPDATE configured_models SET deleted_at = now(), updated_at = now() WHERE org_id = $1 AND id = $2`,
		project.OrgUUID,
		configuredModelUUID,
	); err != nil {
		t.Fatalf("force archive configured model: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE model_provider_configs SET deleted_at = now(), updated_at = now() WHERE org_id = $1 AND id = $2`,
		project.OrgUUID,
		providerConfigUUID,
	); err != nil {
		t.Fatalf("force archive provider config: %v", err)
	}
	archivedConfig := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agent-configs/"+testutil.RequireType[string](t, config["id"]),
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	archivedModel := testutil.RequireType[map[string]any](t, archivedConfig["model"])
	if archivedModel["provider_config"] != "openai-secondary" || archivedModel["name"] != "gpt-renamed" ||
		archivedModel["configured_model_id"] != configuredModelID ||
		archivedModel["api_format"] != "openai-responses" {
		t.Fatalf("archived model agent config projection mismatch: %+v", archivedModel)
	}
	if archivedModel["current_revision_id"] != patchedConfiguredModel["current_revision_id"] {
		t.Fatalf("archived model agent config current revision mismatch: %+v", archivedModel)
	}
}

func TestModelProviderConfigRoutesRejectLocalEndpointsOutsideInsecureDev(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "model-provider-egress-policy")
	secret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"provider-key","material":{"kind":"generic","value":"sk-test"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secretID := testutil.RequireType[string](t, secret["id"])

	providerConfig := createdModelProviderConfig(t, requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"openai-public","api_format":"openai-responses","base_url":"https://api.openai.com/v1","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	))
	providerConfigID := testutil.RequireType[string](t, providerConfig["id"])
	for _, body := range []string{
		`{"name":"local-http","api_format":"openai-responses","base_url":"http://localhost:8080/v1","credential_secret_id":"` + secretID + `"}`,
		`{"name":"loopback-https","api_format":"openai-responses","base_url":"https://127.0.0.1:8443/v1","credential_secret_id":"` + secretID + `"}`,
		`{"name":"loopback-ipv6-https","api_format":"openai-responses","base_url":"https://[::1]:8443/v1","credential_secret_id":"` + secretID + `"}`,
		`{"name":"private-https","api_format":"openai-responses","base_url":"https://10.0.0.10/v1","credential_secret_id":"` + secretID + `"}`,
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
			body,
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+providerConfigID,
		`{"base_url":"http://localhost:8080/v1"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	devHandler := mustNewServer(
		t,
		project.Store,
		WithSecretKeyWrapper(integrationKeyWrapper()),
		WithAllowInsecureModelProviderEndpoints(),
		WithModelDiscoverer(func(
			context.Context,
			modelstore.ModelProviderConfigRecord,
			string,
			bool,
		) ([]modelprovider.DiscoveredModel, error) {
			return nil, errors.New("model discovery is disabled in integration tests")
		}),
	).Handler()
	localHTTP := createdModelProviderConfig(t, requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"local-http-dev","api_format":"openai-responses","base_url":"http://localhost:8080/v1","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	))
	if localHTTP["base_url"] != "http://localhost:8080/v1" {
		t.Fatalf("local dev base_url = %v, want http://localhost:8080/v1", localHTTP["base_url"])
	}
	loopbackHTTPS := createdModelProviderConfig(t, requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"loopback-https-dev","api_format":"openai-responses","base_url":"https://127.0.0.1:8443/v1","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	))
	if loopbackHTTPS["base_url"] != "https://127.0.0.1:8443/v1" {
		t.Fatalf("loopback dev base_url = %v, want https://127.0.0.1:8443/v1", loopbackHTTPS["base_url"])
	}
	loopbackIPv6HTTPS := createdModelProviderConfig(t, requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"loopback-ipv6-https-dev","api_format":"openai-responses","base_url":"https://[::1]:8443/v1","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	))
	if loopbackIPv6HTTPS["base_url"] != "https://[::1]:8443/v1" {
		t.Fatalf("loopback IPv6 dev base_url = %v, want https://[::1]:8443/v1", loopbackIPv6HTTPS["base_url"])
	}
	requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"private-dev","api_format":"openai-responses","base_url":"https://10.0.0.10/v1","credential_secret_id":"`+secretID+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
}

func TestCreateModelProviderConfigRunsModelDiscovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "model-provider-discovery")

	modelsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"gpt-a","context_length":65536,"max_output_tokens":2048},
			{"id":"gpt-b"}
		]}`))
	}))
	defer modelsServer.Close()

	devHandler := mustNewServer(
		t,
		project.Store,
		WithSecretKeyWrapper(integrationKeyWrapper()),
		WithAllowInsecureModelProviderEndpoints(),
	).Handler()

	goodSecret := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"discovery-good-key","material":{"kind":"generic","value":"sk-good"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	created := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"discovery-ok","api_format":"openai-responses","base_url":"`+modelsServer.URL+`/v1","credential_secret_id":"`+testutil.RequireType[string](t, goodSecret["id"])+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	discovery := testutil.RequireType[map[string]any](t, created["model_catalog"])
	if discovery["status"] != "ok" {
		t.Fatalf("discovery should succeed against local models endpoint: %+v", created)
	}
	models := testutil.RequireType[[]any](t, discovery["models"])
	if len(models) != 2 ||
		testutil.RequireType[map[string]any](t, models[0])["slug"] != "gpt-a" ||
		testutil.RequireType[map[string]any](t, models[1])["slug"] != "gpt-b" {
		t.Fatalf("discovery models mismatch: %+v", discovery)
	}
	firstModel := testutil.RequireType[map[string]any](t, models[0])
	if firstModel["context_window_tokens"] != float64(65536) ||
		firstModel["max_output_tokens"] != float64(2048) {
		t.Fatalf("discovery model token limits mismatch: %+v", firstModel)
	}

	badSecret := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"discovery-bad-key","material":{"kind":"generic","value":"sk-bad"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	failedCreate := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"discovery-failed","api_format":"openai-responses","base_url":"`+modelsServer.URL+`/v1","credential_secret_id":"`+testutil.RequireType[string](t, badSecret["id"])+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	failedDiscovery := testutil.RequireType[map[string]any](t, failedCreate["model_catalog"])
	if failedDiscovery["status"] != "failed" {
		t.Fatalf("discovery with a bad key should fail: %+v", failedCreate)
	}
	if message, _ := failedDiscovery["error"].(string); !strings.Contains(message, "invalid api key") {
		t.Fatalf("discovery error should carry the provider message: %+v", failedDiscovery)
	}
	failedConfigID := testutil.RequireType[string](t, createdModelProviderConfig(t, failedCreate)["id"])
	requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+failedConfigID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
}

func TestGetModelCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "model-catalog")

	modelsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"gpt-a","context_length":65536,"max_output_tokens":2048},
			{"id":"gpt-b"}
		]}`))
	}))
	defer modelsServer.Close()

	devHandler := mustNewServer(
		t,
		project.Store,
		WithSecretKeyWrapper(integrationKeyWrapper()),
		WithAllowInsecureModelProviderEndpoints(),
	).Handler()

	createProvider := func(name, keyName, keyValue string) string {
		secret := requestJSONWithHeaders(
			t,
			devHandler,
			http.MethodPost,
			"/api/v1/orgs/"+project.OrgID+"/secrets",
			`{"owner":{"kind":"org"},"name":"`+keyName+`","material":{"kind":"generic","value":"`+keyValue+`"}}`,
			"",
			http.StatusCreated,
			authHeaders(project.AdminToken),
		)
		created := requestJSONWithHeaders(
			t,
			devHandler,
			http.MethodPost,
			"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
			`{"name":"`+name+`","api_format":"openai-responses","base_url":"`+modelsServer.URL+`/v1","credential_secret_id":"`+testutil.RequireType[string](t, secret["id"])+`"}`,
			"",
			http.StatusCreated,
			authHeaders(project.AdminToken),
		)
		return testutil.RequireType[string](t, createdModelProviderConfig(t, created)["id"])
	}

	goodConfigID := createProvider("catalog-ok", "catalog-good-key", "sk-good")
	catalog := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+goodConfigID+"/model-catalog",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if catalog["status"] != "ok" {
		t.Fatalf("model catalog should succeed against local models endpoint: %+v", catalog)
	}
	models := testutil.RequireType[[]any](t, catalog["models"])
	if len(models) != 2 ||
		testutil.RequireType[map[string]any](t, models[0])["slug"] != "gpt-a" ||
		testutil.RequireType[map[string]any](t, models[1])["slug"] != "gpt-b" {
		t.Fatalf("model catalog models mismatch: %+v", catalog)
	}
	firstModel := testutil.RequireType[map[string]any](t, models[0])
	if firstModel["context_window_tokens"] != float64(65536) ||
		firstModel["max_output_tokens"] != float64(2048) {
		t.Fatalf("model catalog token limits mismatch: %+v", firstModel)
	}

	badConfigID := createProvider("catalog-failed", "catalog-bad-key", "sk-bad")
	failedCatalog := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+badConfigID+"/model-catalog",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if failedCatalog["status"] != "failed" {
		t.Fatalf("model catalog with a bad key should fail: %+v", failedCatalog)
	}
	if message, _ := failedCatalog["error"].(string); !strings.Contains(message, "invalid api key") {
		t.Fatalf("model catalog error should carry the provider message: %+v", failedCatalog)
	}

	awsSecret := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"catalog-aws-credentials","material":{"kind":"aws_credentials","access_key_id":"AKIAEXAMPLE","secret_access_key":"secret"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	genericSecret := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"catalog-sigv4-wrong-kind","material":{"kind":"generic","value":"key"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"catalog-sigv4-wrong-kind","api_format":"openai-chat-completions","api_variant":"bedrock","base_url":"https://bedrock-mantle.us-west-2.api.aws/v1","auth_kind":"sigv4","auth_options":{"service":"bedrock-mantle","region":"us-west-2"},"credential_secret_id":"`+genericSecret["id"].(string)+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	createdSigV4 := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs",
		`{"name":"catalog-bedrock-sigv4","api_format":"openai-chat-completions","api_variant":"bedrock","base_url":"https://bedrock-mantle.us-west-2.api.aws/v1","auth_kind":"sigv4","auth_options":{"service":"bedrock-mantle","region":"us-west-2"},"credential_secret_id":"`+awsSecret["id"].(string)+`"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	sigV4Catalog := createdSigV4["model_catalog"].(map[string]any)
	if sigV4Catalog["status"] != "ok" || len(sigV4Catalog["models"].([]any)) != 0 {
		t.Fatalf("SigV4 Bedrock catalog should succeed without probing: %+v", sigV4Catalog)
	}
	sigV4ConfigID := createdModelProviderConfig(t, createdSigV4)["id"].(string)
	requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+sigV4ConfigID,
		`{"credential_secret_id":"`+genericSecret["id"].(string)+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	updatedSigV4 := requestJSONWithHeaders(
		t,
		devHandler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/model-provider-configs/"+sigV4ConfigID,
		`{"auth_kind":"bearer_token","auth_options":{},"credential_secret_id":"`+genericSecret["id"].(string)+`"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updatedSigV4["auth_kind"] != "bearer_token" ||
		updatedSigV4["credential_secret_id"] != genericSecret["id"] {
		t.Fatalf("SigV4 provider auth update mismatch: %+v", updatedSigV4)
	}
}

func agentConfigSourceBody(source string) string {
	raw, err := json.Marshal(map[string]string{"source_format": "yaml", "source": source})
	if err != nil {
		panic(err)
	}
	return string(raw)
}
