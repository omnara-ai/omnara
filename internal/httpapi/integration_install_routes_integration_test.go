//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func TestListIntegrationInstalls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)

	project := bootstrapPublicHTTPProject(t, handler, "list-installs")

	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"list-installs",
		"yaml",
		"name: Install Agent\ninstruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken,
		http.StatusCreated,
	)
	profileAlpha := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"list-installs-alpha",
		"Alpha",
		config["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)
	profileBeta := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"list-installs-beta",
		"Beta",
		config["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)
	profileAlphaID := profileAlpha["id"].(string)
	profileBetaID := profileBeta["id"].(string)

	installAlpha := createListInstallsFixture(
		t, ctx, project,
		mustPublicHTTPID(t, publicid.KindAgentProfile, profileAlphaID),
		"A-LIST-1", "T-LIST-1", "Alpha App",
	)
	installBeta := createListInstallsFixture(
		t, ctx, project,
		mustPublicHTTPID(t, publicid.KindAgentProfile, profileBetaID),
		"A-LIST-2", "T-LIST-2", "Beta App",
	)

	installsPath := project.ProjectPath + "/integration-installs"

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data := listed["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 integration installs, got %d: %+v", len(data), data)
	}
	if listed["next_cursor"] != nil {
		t.Fatalf("single full page should have null next_cursor, got %v", listed["next_cursor"])
	}

	first := data[0].(map[string]any)
	second := data[1].(map[string]any)
	if first["provider_agent_display_name"] != "Beta App" || second["provider_agent_display_name"] != "Alpha App" {
		t.Fatalf("expected newest-first ordering Beta App, Alpha App, got %+v", data)
	}
	wantAlphaID, err := publicid.Encode(publicid.KindIntegrationInstall, installAlpha.ID)
	if err != nil {
		t.Fatalf("encode install id: %v", err)
	}
	wantBetaID, err := publicid.Encode(publicid.KindIntegrationInstall, installBeta.ID)
	if err != nil {
		t.Fatalf("encode install id: %v", err)
	}
	if first["id"] != wantBetaID || second["id"] != wantAlphaID {
		t.Fatalf("unexpected install ids: %+v", data)
	}
	if first["agent_profile_id"] != profileBetaID || second["agent_profile_id"] != profileAlphaID {
		t.Fatalf("unexpected install profile bindings: %+v", data)
	}
	if first["provider"] != integrationstore.IntegrationProviderSlack || first["state"] != "active" {
		t.Fatalf("unexpected install provider/state: %+v", first)
	}
	if first["integration_kind"] != slack.IntegrationKindAgentProfile ||
		first["connection_mode"] != slack.ConnectionModeWebhook {
		t.Fatalf("unexpected install kind/mode: %+v", first)
	}
	if first["provider_tenant_id"] != "T-LIST-2" || first["provider_account_ref"] != "A-LIST-2" {
		t.Fatalf("unexpected install provider refs: %+v", first)
	}
	if _, ok := first["agent_id"]; ok {
		t.Fatalf("profile-bound install should omit agent_id: %+v", first)
	}
	for _, hidden := range []string{"credential_secret_id", "provider_config", "provider_identity", "provider_metadata", "installed_by_user_id"} {
		if _, ok := first[hidden]; ok {
			t.Fatalf("install response should not expose %s: %+v", hidden, first)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, first["created_at"].(string)); err != nil {
		t.Fatalf("parse install created_at: %v", err)
	}

	firstPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath+"?limit=1",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	firstPageData := firstPage["data"].([]any)
	if len(firstPageData) != 1 || firstPageData[0].(map[string]any)["id"] != wantBetaID {
		t.Fatalf("expected first page to hold only the Beta install, got %+v", firstPageData)
	}
	nextCursor, ok := firstPage["next_cursor"].(string)
	if !ok || nextCursor == "" {
		t.Fatalf("expected non-null next_cursor on first page, got %v", firstPage["next_cursor"])
	}
	secondPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath+"?limit=1&cursor="+nextCursor,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	secondPageData := secondPage["data"].([]any)
	if len(secondPageData) != 1 || secondPageData[0].(map[string]any)["id"] != wantAlphaID {
		t.Fatalf("expected second page to hold only the Alpha install, got %+v", secondPageData)
	}
	if secondPage["next_cursor"] != nil {
		t.Fatalf("final page should have null next_cursor, got %v", secondPage["next_cursor"])
	}

	filtered := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath+"?agent_profile_id="+profileAlphaID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	filteredData := filtered["data"].([]any)
	if len(filteredData) != 1 || filteredData[0].(map[string]any)["id"] != wantAlphaID {
		t.Fatalf("expected only the Alpha install for profile filter, got %+v", filteredData)
	}

	named := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath+"?name=Beta*",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	namedData := named["data"].([]any)
	if len(namedData) != 1 || namedData[0].(map[string]any)["id"] != wantBetaID {
		t.Fatalf("expected only the Beta install for name filter, got %+v", namedData)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath+"?agent_profile_id=not-a-profile-id",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath+"?cursor=not-a-valid-cursor",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		installsPath+"/"+wantAlphaID,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	afterDelete := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	afterDeleteData := afterDelete["data"].([]any)
	if len(afterDeleteData) != 1 || afterDeleteData[0].(map[string]any)["id"] != wantBetaID {
		t.Fatalf("expected only the Beta install after delete, got %+v", afterDeleteData)
	}

	otherOrg := bootstrapPublicHTTPProject(t, handler, "list-installs-other-org")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		installsPath,
		"",
		"",
		http.StatusNotFound,
		authHeaders(otherOrg.AdminToken),
	)
}

func createListInstallsFixture(
	t *testing.T,
	ctx context.Context,
	project publicHTTPProject,
	profileID storage.ID,
	appID, workspaceID, displayName string,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	credentialPayload, err := slack.CredentialPayload(slack.AppCredentials{
		BotToken:      "xoxb-" + appID,
		ClientID:      "client-id-" + appID,
		ClientSecret:  "client-secret-" + appID,
		SigningSecret: "signing-secret-" + appID,
	})
	if err != nil {
		t.Fatalf("build Slack credential payload: %v", err)
	}
	credentialSecret := createSlackHTTPInstallSecret(
		t,
		ctx,
		project,
		appID+"-credentials",
		credentialPayload,
	)
	install, err := project.Store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID:                    project.OrgUUID,
			ProjectID:                project.ProjectUUID,
			AgentProfileID:           profileID,
			InstalledByUserID:        project.AdminUserUUID,
			Provider:                 integrationstore.IntegrationProviderSlack,
			IntegrationKind:          slack.IntegrationKindAgentProfile,
			ConnectionMode:           slack.ConnectionModeWebhook,
			State:                    integrationstore.IntegrationInstallStateActive,
			ProviderTenantID:         workspaceID,
			ProviderAccountRef:       appID,
			ProviderAgentDisplayName: displayName,
			CredentialSecretID:       credentialSecret,
		},
	)
	if err != nil {
		t.Fatalf("create Slack install %s/%s: %v", appID, workspaceID, err)
	}
	return install
}
