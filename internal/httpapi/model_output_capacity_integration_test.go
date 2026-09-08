//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestOutputCapacityCreationReplayAndPatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, format, variant string
		requiresCapacity      bool
	}{
		{"responses", "openai-responses", "default", false},
		{"chat", "openai-chat-completions", "default", false},
		{"messages", "anthropic-messages", "default", true},
		{"bedrock-messages", "anthropic-messages", "bedrock", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			var hint *int
			var lookedUp string
			handler := newIntegrationServer(pool, func(server *Server) {
				server.fillMissingModelLimits = func(
					_ context.Context, models []modelprovider.DiscoveredModel,
				) []modelprovider.DiscoveredModel {
					lookedUp = models[0].Slug
					models[0].MaxOutputTokens = hint
					return models
				}
			})
			project := bootstrapPublicHTTPProject(t, handler, "output-capacity")
			orgPath := "/api/v1/orgs/" + project.OrgID
			request := func(method, path, body string, status int) map[string]any {
				return requestJSONWithHeaders(t, handler, method, path, body, "", status, authHeaders(project.AdminToken))
			}
			secret := request(http.MethodPost, orgPath+"/secrets",
				`{"owner":{"kind":"org"},"name":"capacity-test-key","material":{"kind":"generic","value":"test-key"}}`,
				http.StatusCreated)
			provider := createdModelProviderConfig(t, request(http.MethodPost, orgPath+"/model-provider-configs", fmt.Sprintf(
				`{"name":"capacity-provider","api_format":%q,"api_variant":%q,"base_url":"https://example.test","credential_secret_id":%q}`,
				tc.format, tc.variant, testutil.RequireType[string](t, secret["id"]),
			), http.StatusCreated))
			modelsPath := orgPath + "/model-provider-configs/" + testutil.RequireType[string](t, provider["id"]) + "/models"
			for _, discovery := range []struct {
				name string
				hint *int
			}{
				{"absent-hint", nil}, {"conflicting-hint", new(16000)},
				{"invalid-hint", new(-1)}, {"context-exhausting-hint", new(128000)},
			} {
				hint = discovery.hint
				status := http.StatusCreated
				if tc.requiresCapacity {
					status = http.StatusBadRequest
				}
				created := request(http.MethodPost, modelsPath, fmt.Sprintf(
					`{"name":%q,"provider_model_slug":"capacity-model","context_window_tokens":128000,"default_max_output_tokens":32000}`,
					discovery.name,
				), status)
				if !tc.requiresCapacity {
					require.Contains(t, created, "max_output_tokens")
					require.Nil(t, created["max_output_tokens"])
					require.Equal(t, float64(32000), created["default_max_output_tokens"])
				}
			}
			withoutDefaultBody := `{"name":"discovered-without-default","provider_model_slug":"capacity-model","context_window_tokens":128000}`
			if tc.requiresCapacity {
				hint = nil
				request(http.MethodPost, modelsPath, withoutDefaultBody, http.StatusBadRequest)
			}
			hint = new(64000)
			withoutDefault := request(http.MethodPost, modelsPath, withoutDefaultBody, http.StatusCreated)
			require.Equal(t, float64(64000), withoutDefault["max_output_tokens"])
			require.Nil(t, withoutDefault["default_max_output_tokens"], "discovery must not impose a request allowance")
			var patchModelID string
			for _, known := range []bool{false, true} {
				if !known && tc.requiresCapacity {
					continue
				}
				name := "unknown"
				hint = nil
				if known {
					name, hint = "discovered", new(64000)
				}
				body := fmt.Sprintf(
					`{"name":%q,"provider_model_slug":"  capacity-model  ","context_window_tokens":128000,"default_max_output_tokens":32000}`,
					name,
				)
				created := request(http.MethodPost, modelsPath, body, http.StatusCreated)
				require.Equal(t, "capacity-model", lookedUp)
				id := testutil.RequireType[string](t, created["id"])
				require.NotEmpty(t, id)
				require.NotEmpty(t, testutil.RequireType[string](t, created["current_revision_id"]))
				require.Contains(t, created, "max_output_tokens")
				if known {
					require.Equal(t, float64(64000), created["max_output_tokens"])
				} else {
					require.Nil(t, created["max_output_tokens"])
				}
				if patchModelID == "" {
					patchModelID = id
				}
				for _, replayHint := range []*int{new(96000), nil, new(-1), new(16000), new(128000)} {
					hint = replayHint
					replay := request(http.MethodPost, modelsPath, body, http.StatusOK)
					for _, field := range []string{"id", "current_revision_id", "max_output_tokens", "default_max_output_tokens"} {
						require.Equal(t, created[field], replay[field], "creation replay changed %s", field)
					}
				}
				hint = nil
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
			for _, body := range []string{`{}`, `{"max_output_tokens":96000}`, `{"name":"patch-model"}`} {
				preserved := request(http.MethodPut, modelPath, body, http.StatusOK)
				require.Equal(t, set["current_revision_id"], preserved["current_revision_id"])
				require.Equal(t, float64(96000), preserved["max_output_tokens"])
				require.Equal(t, float64(32000), preserved["default_max_output_tokens"])
			}
			if tc.requiresCapacity {
				request(http.MethodPut, modelPath, `{"name":"rejected-rename","max_output_tokens":null}`, http.StatusBadRequest)
				preserved := request(http.MethodPut, modelPath, `{}`, http.StatusOK)
				require.Equal(t, "patch-model", preserved["name"])
				require.Equal(t, set["current_revision_id"], preserved["current_revision_id"])
				require.Equal(t, float64(96000), preserved["max_output_tokens"])
				return
			}
			cleared := request(http.MethodPut, modelPath, `{"max_output_tokens":null}`, http.StatusOK)
			require.Contains(t, cleared, "max_output_tokens")
			require.Nil(t, cleared["max_output_tokens"])
			require.Equal(t, float64(32000), cleared["default_max_output_tokens"])
			require.NotEqual(t, set["current_revision_id"], cleared["current_revision_id"])
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
