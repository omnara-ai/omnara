//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
)

func TestCreateAgentConfigReportsFieldLevelIssues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-issues")

	response := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-config-issues-schema",
		"yaml",
		"instruction: Help.\nmodel:\n  provider_config: openai-prod\ntools:\n  run_command:\n    enabled: \"yes\"\n",
		project.AdminToken,
		http.StatusBadRequest,
	)
	if response["code"] != "invalid_request" {
		t.Fatalf("code = %v, want invalid_request", response["code"])
	}
	issues, ok := response["issues"].([]any)
	if !ok || len(issues) != 2 {
		t.Fatalf("issues = %v, want two issues", response["issues"])
	}
	first := issues[0].(map[string]any)
	if first["path"] != "/model/name" || first["line"] != float64(2) || first["column"] != float64(1) {
		t.Fatalf("first issue = %v, want /model/name at 2:1", first)
	}
	second := issues[1].(map[string]any)
	if second["path"] != "/tools/run_command/enabled" || second["line"] != float64(6) {
		t.Fatalf("second issue = %v, want /tools/run_command/enabled on line 6", second)
	}

	unknownModel := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-config-issues-model",
		"yaml",
		"instruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: no-such-model\n",
		project.AdminToken,
		http.StatusBadRequest,
	)
	modelIssues, ok := unknownModel["issues"].([]any)
	if !ok || len(modelIssues) != 1 {
		t.Fatalf("issues = %v, want one issue", unknownModel["issues"])
	}
	modelIssue := modelIssues[0].(map[string]any)
	if modelIssue["path"] != "/model/name" || modelIssue["line"] != float64(4) {
		t.Fatalf("model issue = %v, want /model/name on line 4", modelIssue)
	}

	syntax := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-config-issues-syntax",
		"yaml",
		"instruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: [\n",
		project.AdminToken,
		http.StatusBadRequest,
	)
	syntaxIssues, ok := syntax["issues"].([]any)
	if !ok || len(syntaxIssues) != 1 {
		t.Fatalf("issues = %v, want one issue", syntax["issues"])
	}
	syntaxIssue := syntaxIssues[0].(map[string]any)
	if syntaxIssue["path"] != "" || syntaxIssue["line"] != float64(4) {
		t.Fatalf("syntax issue = %v, want root issue on line 4", syntaxIssue)
	}
}
