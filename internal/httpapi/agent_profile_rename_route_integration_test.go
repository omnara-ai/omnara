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
		`{"name":"Renamed  研究 🚀"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if renamed["name"] != "Renamed  研究 🚀" || renamed["id"] != profile["id"] ||
		renamed["current_config_id"] != profile["current_config_id"] {
		t.Fatalf("renamed profile = %+v, want exact name with unchanged config", renamed)
	}

	for _, body := range []string{`{"name":"   "}`, `{"name":" Renamed"}`} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPatch,
			profilePath,
			body,
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}

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
	if fetched["name"] != "Renamed  研究 🚀" {
		t.Fatalf("fetched profile name = %v, want exact renamed value", fetched["name"])
	}
}
