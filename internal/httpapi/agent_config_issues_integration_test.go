//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
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
	first := testutil.RequireType[map[string]any](t, issues[0])
	if first["path"] != "/model/name" || first["line"] != float64(2) || first["column"] != float64(1) {
		t.Fatalf("first issue = %v, want /model/name at 2:1", first)
	}
	second := testutil.RequireType[map[string]any](t, issues[1])
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
	modelIssue := testutil.RequireType[map[string]any](t, modelIssues[0])
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
	syntaxIssue := testutil.RequireType[map[string]any](t, syntaxIssues[0])
	if syntaxIssue["path"] != "" || syntaxIssue["line"] != float64(4) {
		t.Fatalf("syntax issue = %v, want root issue on line 4", syntaxIssue)
	}
}

func TestCreateAgentConfigValidatesWebhookSigningSecretValue(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	handler := newIntegrationServer(openIntegrationDB(t, ctx))
	project := bootstrapPublicHTTPProject(t, handler, "webhook-secret")
	for i, value := range []string{"invalid", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))} {
		secret, _, err := project.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID: project.OrgUUID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: project.ProjectUUID,
			Name: fmt.Sprintf("webhook-key-%d", i), Material: secrets.GenericMaterial{Value: value},
			Actor: httpUserPrincipal(project.AdminUserUUID),
		})
		require.NoError(t, err)
		id := testPublicID(t, publicid.KindSecret, secret.ID)
		source := "instruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n" +
			"event_webhook:\n  url: https://example.com/events\n  signing_secret_id: " + id + "\n"
		status := http.StatusCreated
		if i == 0 {
			status = http.StatusBadRequest
		}
		response := createPublicHTTPAgentConfig(
			t, handler, project, "webhook-secret", "yaml", source, project.AdminToken, status,
		)
		if i == 0 {
			issues := testutil.RequireType[[]any](t, response["issues"])
			require.Len(t, issues, 1)
			issue := testutil.RequireType[map[string]any](t, issues[0])
			require.Equal(t, "/event_webhook/signing_secret_id", issue["path"])
		}
	}
}
