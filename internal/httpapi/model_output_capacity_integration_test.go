//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestOutputCapacityCreationReplayAndPatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, format, variant string
	}{
		{"responses", "openai-responses", "default"},
		{"chat", "openai-chat-completions", "default"},
		{"messages", "anthropic-messages", "default"},
		{"bedrock-messages", "anthropic-messages", "bedrock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			handler := newIntegrationServer(pool, WithModelDiscoverer(func(
				_ context.Context, _ modelstore.ModelProviderConfigRecord, _ string, _ bool,
			) ([]modelprovider.DiscoveredModel, error) {
				return []modelprovider.DiscoveredModel{{
					Slug: "capacity-model", ContextWindowTokens: new(128000), MaxOutputTokens: new(64000),
				}}, nil
			}))
			project := bootstrapPublicHTTPProject(t, handler, "output-capacity")
			orgPath := "/api/v1/orgs/" + project.OrgID
			request := func(method, path, body string, status int) map[string]any {
				return requestJSONWithHeaders(t, handler, method, path, body, "", status, authHeaders(project.AdminToken))
			}
			secret := request(http.MethodPost, orgPath+"/secrets",
				`{"owner":{"kind":"org"},"name":"capacity-test-key","material":{"kind":"generic","value":"test-key"}}`,
				http.StatusCreated)
			providerResponse := request(http.MethodPost, orgPath+"/model-provider-configs", fmt.Sprintf(
				`{"name":"capacity-provider","api_format":%q,"api_variant":%q,"base_url":"https://example.test","credential_secret_id":%q}`,
				tc.format, tc.variant, testutil.RequireType[string](t, secret["id"]),
			), http.StatusCreated)
			catalog := testutil.RequireType[map[string]any](t, providerResponse["model_catalog"])
			discovered := testutil.RequireType[[]any](t, catalog["models"])
			require.Len(t, discovered, 1)
			require.Equal(t, float64(64000), testutil.RequireType[map[string]any](t, discovered[0])["max_output_tokens"])
			provider := createdModelProviderConfig(t, providerResponse)
			modelsPath := orgPath + "/model-provider-configs/" + testutil.RequireType[string](t, provider["id"]) + "/models"
			// Available discovery must not silently fill a capacity the caller omitted.
			withoutDefault := request(http.MethodPost, modelsPath,
				`{"name":"without-default","provider_model_slug":"capacity-model","context_window_tokens":128000}`,
				http.StatusCreated)
			require.Contains(t, withoutDefault, "max_output_tokens")
			require.Nil(t, withoutDefault["max_output_tokens"])
			require.Nil(t, withoutDefault["default_max_output_tokens"])
			var patchModelID, patchCreateBody string
			for _, known := range []bool{false, true} {
				name, capacity := "unknown", ""
				if known {
					name, capacity = "explicit", `,"max_output_tokens":64000`
				}
				body := fmt.Sprintf(
					`{"name":%q,"provider_model_slug":"  capacity-model  ","context_window_tokens":128000,"default_max_output_tokens":32000%s}`,
					name, capacity,
				)
				created := request(http.MethodPost, modelsPath, body, http.StatusCreated)
				id := testutil.RequireType[string](t, created["id"])
				require.NotEmpty(t, id)
				require.NotEmpty(t, testutil.RequireType[string](t, created["current_revision_id"]))
				require.Equal(t, "capacity-model", created["provider_model_slug"])
				require.Contains(t, created, "max_output_tokens")
				require.Equal(t, float64(32000), created["default_max_output_tokens"])
				if known {
					require.Equal(t, float64(64000), created["max_output_tokens"])
				} else {
					require.Nil(t, created["max_output_tokens"])
					patchModelID, patchCreateBody = id, body
				}
				replay := request(http.MethodPost, modelsPath, body, http.StatusOK)
				for _, field := range []string{"id", "current_revision_id", "max_output_tokens", "default_max_output_tokens"} {
					require.Equal(t, created[field], replay[field], "creation replay changed %s", field)
				}
				request(http.MethodPost, modelsPath, fmt.Sprintf(
					`{"name":%q,"provider_model_slug":"capacity-model","context_window_tokens":128000,"default_max_output_tokens":16000}`,
					name,
				), http.StatusConflict)
				request(http.MethodPost, modelsPath, fmt.Sprintf(
					`{"name":%q,"provider_model_slug":"capacity-model","context_window_tokens":128000,"default_max_output_tokens":32000,"max_output_tokens":96000}`,
					name,
				), http.StatusConflict)
			}

			modelPath := modelsPath + "/" + patchModelID
			set := request(http.MethodPut, modelPath, `{"max_output_tokens":96000}`, http.StatusOK)
			require.NotEmpty(t, testutil.RequireType[string](t, set["current_revision_id"]))
			for _, body := range []string{`{}`, `{"max_output_tokens":96000}`} {
				preserved := request(http.MethodPut, modelPath, body, http.StatusOK)
				require.Equal(t, set["current_revision_id"], preserved["current_revision_id"])
				require.Equal(t, float64(96000), preserved["max_output_tokens"])
				require.Equal(t, float64(32000), preserved["default_max_output_tokens"])
			}
			replay := request(http.MethodPost, modelsPath, patchCreateBody, http.StatusOK)
			require.Equal(t, set["current_revision_id"], replay["current_revision_id"])
			require.Equal(t, float64(96000), replay["max_output_tokens"])
			cleared := request(http.MethodPut, modelPath, `{"max_output_tokens":null}`, http.StatusOK)
			require.Contains(t, cleared, "max_output_tokens")
			require.Nil(t, cleared["max_output_tokens"])
			require.Equal(t, float64(32000), cleared["default_max_output_tokens"])
			require.NotEqual(t, set["current_revision_id"], cleared["current_revision_id"])
			replay = request(http.MethodPost, modelsPath, patchCreateBody, http.StatusOK)
			require.Equal(t, cleared["current_revision_id"], replay["current_revision_id"])
			require.Contains(t, replay, "max_output_tokens")
			require.Nil(t, replay["max_output_tokens"])
			renamed := request(http.MethodPut, modelPath, `{"name":"patch-model"}`, http.StatusOK)
			require.Equal(t, cleared["current_revision_id"], renamed["current_revision_id"])
			require.Equal(t, "patch-model", renamed["name"])
			require.Nil(t, renamed["max_output_tokens"])
			request(http.MethodPost, project.ProjectPath+"/model-grants",
				`{"configured_model_id":"`+patchModelID+`"}`, http.StatusCreated)
			config := request(http.MethodPost, project.ProjectPath+"/agent-configs",
				agentConfigSourceBody("instruction: Test.\nmodel:\n  provider_config: capacity-provider\n  name: patch-model\n"),
				http.StatusCreated)
			effective := testutil.RequireType[map[string]any](t, config["model"])
			require.Contains(t, effective, "max_output_tokens")
			require.Nil(t, effective["max_output_tokens"])
		})
	}
}
