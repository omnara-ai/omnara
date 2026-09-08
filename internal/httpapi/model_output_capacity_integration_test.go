//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestOptionalOutputCapacityCreationReplayAndPatch(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var hint *int
	var lookedUp string
	handler := newIntegrationServer(pool, func(server *Server) {
		server.fillMissingModelLimits = func(
			_ context.Context,
			models []modelprovider.DiscoveredModel,
		) []modelprovider.DiscoveredModel {
			lookedUp = models[0].Slug
			models[0].MaxOutputTokens = hint
			return models
		}
	})
	project := bootstrapPublicHTTPProject(t, handler, "optional-output-capacity")
	orgPath := "/api/v1/orgs/" + project.OrgID
	request := func(method, path, body string, status int) map[string]any {
		return requestJSONWithHeaders(t, handler, method, path, body, "", status, authHeaders(project.AdminToken))
	}
	secret := request(
		http.MethodPost,
		orgPath+"/secrets",
		`{"owner":{"kind":"org"},"name":"capacity-test-key","material":{"kind":"generic","value":"test-key"}}`,
		http.StatusCreated,
	)
	provider := createdModelProviderConfig(
		t,
		request(
			http.MethodPost,
			orgPath+"/model-provider-configs",
			`{"name":"capacity-provider","preset":"openai","credential_secret_id":"`+testutil.RequireType[string](
				t,
				secret["id"],
			)+`"}`,
			http.StatusCreated,
		),
	)
	modelsPath := orgPath + "/model-provider-configs/" + testutil.RequireType[string](t, provider["id"]) + "/models"
	for _, tc := range []struct {
		name string
		hint int
	}{
		{name: "conflicting-hint", hint: 16000},
		{name: "invalid-hint", hint: -1},
		{name: "context-exhausting-hint", hint: 128000},
	} {
		hint = new(tc.hint)
		created := request(http.MethodPost, modelsPath, fmt.Sprintf(
			`{"name":%q,"provider_model_slug":"capacity-model","context_window_tokens":128000,"default_max_output_tokens":32000}`,
			tc.name,
		), http.StatusCreated)
		if value, present := created["max_output_tokens"]; !present || value != nil ||
			created["default_max_output_tokens"] != float64(32000) {
			t.Fatalf("catalog hint overrode explicit intent: %+v", created)
		}
	}
	var unknownModelID string
	for _, known := range []bool{false, true} {
		name := "unknown"
		hint = nil
		if known {
			name = "discovered"
			hint = new(64000)
		}
		body := fmt.Sprintf(
			`{"name":%q,"provider_model_slug":"  capacity-model  ","context_window_tokens":128000,"default_max_output_tokens":32000}`,
			name,
		)
		created := request(http.MethodPost, modelsPath, body, http.StatusCreated)
		if lookedUp != "capacity-model" {
			t.Fatalf("catalog slug=%q", lookedUp)
		}
		if known && created["max_output_tokens"] != float64(64000) {
			t.Fatalf("discovered capacity=%v", created["max_output_tokens"])
		}
		if !known {
			unknownModelID = testutil.RequireType[string](t, created["id"])
			if unknownModelID == "" {
				t.Fatalf("created model ID=%v, want a nonempty string", created["id"])
			}
			if capacity, present := created["max_output_tokens"]; !present || capacity != nil {
				t.Fatalf("unknown capacity missing or non-null: %v", capacity)
			}
		}
		// A changed or newly available hint must not redefine an existing request.
		// This hint also conflicts with the explicit allowance for a new model.
		hint = new(16000)
		replay := request(http.MethodPost, modelsPath, body, http.StatusOK)
		if replay["id"] != created["id"] ||
			replay["current_revision_id"] != created["current_revision_id"] ||
			replay["max_output_tokens"] != created["max_output_tokens"] {
			t.Fatal("catalog refresh changed creation replay")
		}
		request(
			http.MethodPost,
			modelsPath,
			fmt.Sprintf(
				`{"name":%q,"provider_model_slug":"capacity-model","context_window_tokens":128000,"default_max_output_tokens":32000,"max_output_tokens":96000}`,
				name,
			),
			http.StatusConflict,
		)
	}

	// Exercise the patch lifecycle once, starting from the unknown-capacity model.
	modelPath := modelsPath + "/" + unknownModelID
	set := request(http.MethodPut, modelPath, `{"max_output_tokens":96000}`, http.StatusOK)
	preserved := request(http.MethodPut, modelPath, `{}`, http.StatusOK)
	if preserved["current_revision_id"] != set["current_revision_id"] ||
		preserved["max_output_tokens"] != float64(96000) {
		t.Fatal("omitted patch did not preserve capacity")
	}
	cleared := request(http.MethodPut, modelPath, `{"max_output_tokens":null}`, http.StatusOK)
	if value, present := cleared["max_output_tokens"]; !present ||
		value != nil ||
		cleared["default_max_output_tokens"] != float64(32000) ||
		cleared["current_revision_id"] == set["current_revision_id"] {
		t.Fatal("clear did not create a nullable revision while preserving allowance")
	}
	request(
		http.MethodPost,
		project.ProjectPath+"/model-grants",
		`{"configured_model_id":"`+unknownModelID+`"}`,
		http.StatusCreated,
	)
	config := request(
		http.MethodPost,
		project.ProjectPath+"/agent-configs",
		agentConfigSourceBody("instruction: Test.\nmodel:\n  provider_config: capacity-provider\n  name: unknown\n"),
		http.StatusCreated,
	)
	effective := testutil.RequireType[map[string]any](t, config["model"])
	if value, present := effective["max_output_tokens"]; !present || value != nil {
		t.Fatalf("effective unknown capacity=%v present=%v", value, present)
	}
}
