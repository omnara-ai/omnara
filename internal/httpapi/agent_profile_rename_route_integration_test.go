//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
)

func TestRenameAgentProfileRoute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)

	project := bootstrapPublicHTTPProject(t, handler, "rename-profile")

	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"rename-profile",
		"yaml",
		"instruction: Help out.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken,
		http.StatusCreated,
	)
	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"rename-profile",
		"Original",
		config["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)
	profilePath := project.ProjectPath + "/agent-profiles/" + profile["id"].(string)

	renamed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		profilePath,
		`{"name":"Renamed"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if renamed["name"] != "Renamed" || renamed["id"] != profile["id"] ||
		renamed["current_config_id"] != profile["current_config_id"] {
		t.Fatalf("renamed profile = %+v, want name Renamed with unchanged config", renamed)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		profilePath,
		`{"name":"   "}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	fetched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		profilePath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if fetched["name"] != "Renamed" {
		t.Fatalf("fetched profile name = %v, want Renamed", fetched["name"])
	}
}
