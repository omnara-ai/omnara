//go:build integration

package dbmigrate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Uses the existing build:journey bundle and the same runner environment as the
// HTTP Slack journeys. Only configuration responses and Slack itself are fake;
// migration, channel access, generated TS decoding and gateway dispatch are real.
func TestPostgresMigratedSlackDMSendsThroughGateway(t *testing.T) {
	runner := os.Getenv("OMNARA_TEST_SLACK_GATEWAY_RUNNER")
	if runner == "" {
		t.Skip("OMNARA_TEST_SLACK_GATEWAY_RUNNER is required for the migration gateway journey")
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 37))
	fixture := seedLegacyChannelMigrationFixture(t, ctx, db)
	// Configure a live pre-cutover DM, with a syntactically valid Slack address.
	// The shared fixture's archived agent and D_EXISTING placeholder exercise SQL
	// preservation elsewhere; neither represents an active provider send journey.
	_, err := db.ExecContext(ctx, `UPDATE agents SET state = 'active', archived_at = NULL WHERE id = $1`,
		fixture.dmAgentID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE integration_targets SET provider_ref = 'D0123456789' WHERE id = $1`,
		fixture.dmTargetID)
	require.NoError(t, err)
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 38))
	require.Equal(t, int64(38), currentPostgresMigrationVersion(t, ctx, db))

	projectID, agentID, channelID := uuid.MustParse(fixture.projectID), uuid.MustParse(fixture.dmAgentID),
		uuid.MustParse(fixture.dmTargetID)
	store := storage.NewStore(pool).Integrations()
	access, err := store.GetAgentChannelAccess(ctx, projectID, agentID, channelID)
	require.NoError(t, err)
	require.Equal(t, channelID, access.ChannelID)
	require.True(t, access.Active)
	require.True(t, access.Capabilities.Send)
	require.False(t, access.Capabilities.CreatesReplyChannel)
	require.Equal(t, "D0123456789", access.ProviderRef)
	require.Equal(t, "dm", access.ProviderRefKind)
	binding, err := store.GetActiveSendBindingForTarget(ctx, projectID, agentID, channelID)
	require.NoError(t, err)
	require.Equal(t, agentID, binding.AgentID)
	require.Equal(t, channelID, binding.IntegrationTargetID)
	var currentChannel string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT integration_target_id::text FROM agents
WHERE id = $1 AND state = 'active' AND archived_at IS NULL`, agentID).Scan(&currentChannel))
	require.Equal(t, fixture.dmTargetID, currentChannel)

	app, err := store.GetIntegrationApp(ctx, uuid.MustParse(fixture.orgID), access.IntegrationAppID)
	require.NoError(t, err)
	install, err := store.GetIntegrationInstall(ctx, projectID, access.IntegrationInstallID)
	require.NoError(t, err)
	scope := channelconnector.OperationScope{
		ProjectID:            migratedSlackPublicID(t, publicid.KindProject, projectID),
		AgentID:              migratedSlackPublicID(t, publicid.KindAgent, agentID),
		ChannelID:            migratedSlackPublicID(t, publicid.KindIntegrationTarget, channelID),
		IntegrationAppID:     migratedSlackPublicID(t, publicid.KindIntegrationApp, app.ID),
		IntegrationInstallID: migratedSlackPublicID(t, publicid.KindIntegrationInstall, install.ID),
	}
	core := migratedSlackConfigurationServer(t, app, install, scope)
	defer core.Close()
	posts := make(chan map[string]json.RawMessage, 2)
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected Slack call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.Equal(t, "Bearer migration-fake-slack-token", r.Header.Get("Authorization"))
		var post map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&post); err != nil {
			t.Errorf("decode Slack post: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case posts <- post:
		default:
			t.Error("unexpected repeated Slack publication")
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"ok":true,"channel":"D0123456789","ts":"1730000000.000002"}`))
		assert.NoError(t, err)
	}))
	defer slack.Close()
	payload := channelconnector.SendPayload{
		Destination: channelconnector.OperationDestination{
			ImplementationKey: access.ImplementationKey, ProviderRef: access.ProviderRef,
			ProviderRefKind: access.ProviderRefKind, ProviderMetadata: access.ProviderMetadata,
		},
		Message: channelconnector.Message{Text: "The migrated DM still works."}, Params: json.RawMessage(`{}`),
	}
	input, err := json.Marshal(map[string]any{
		"coreUrl": core.URL, "slackUrl": slack.URL, "token": "migration-fake-core-token",
		"send": map[string]any{"request_id": "migrated-dm-send", "scope": scope, "payload": payload},
	})
	require.NoError(t, err)
	node := os.Getenv("OMNARA_TEST_NODE")
	if node == "" {
		node = "node"
	}
	command := exec.CommandContext(ctx, node, runner)
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "gateway journey: %s", output)
	var result struct {
		Processed  int                              `json:"processed"`
		SendResult channelconnector.OperationResult `json:"send_result"`
	}
	require.NoError(t, json.Unmarshal(output, &result))
	require.Zero(t, result.Processed, "sending must not need inbound receipt replay")
	require.Equal(t, "migrated-dm-send", result.SendResult.RequestID)
	require.Equal(t, channelconnector.OperationCompleted, result.SendResult.Outcome)
	published, err := channelconnector.DecodeSendResult(result.SendResult.Payload)
	require.NoError(t, err)
	require.Equal(t, channelconnector.MessagePublished, published.Publication)
	require.Equal(t, channelconnector.MessageAtDestination, published.MessageChannel)
	require.Equal(t, "1730000000.000002", published.MessageID)
	require.Nil(t, published.ReplyChannel, "persistent DMs must not become threads")
	require.Len(t, posts, 1)
	post := <-posts
	require.JSONEq(t, `"D0123456789"`, string(post["channel"]))
	require.JSONEq(t, `"The migrated DM still works."`, string(post["text"]))
	require.NotContains(t, post, "thread_ts")
	after, err := store.GetActiveSendBindingForTarget(ctx, projectID, agentID, channelID)
	require.NoError(t, err)
	require.Equal(t, binding.ID, after.ID, "sending preserves the migrated binding")
}

func migratedSlackPublicID(t *testing.T, kind publicid.Kind, id uuid.UUID) string {
	t.Helper()
	value, err := publicid.Encode(kind, id)
	require.NoError(t, err)
	return value
}

func migratedSlackConfigurationServer(
	t *testing.T, app integrationstore.IntegrationAppRecord, install integrationstore.IntegrationInstallRecord,
	scope channelconnector.OperationScope,
) *httptest.Server {
	t.Helper()
	appPath := "/channel-connector/apps/" + scope.IntegrationAppID
	responses := map[string]any{
		appPath + "/configuration": map[string]any{"app": map[string]any{
			"id": scope.IntegrationAppID, "provider": app.Provider, "connector_key": app.ConnectorKey,
			"provider_app_ref": app.ProviderAppRef, "display_name": app.DisplayName,
			"provider_config": app.ProviderConfig, "provider_metadata": app.ProviderMetadata,
			"configuration_revision": app.ConfigurationRevision, "updated_at": app.UpdatedAt,
		}},
		appPath + "/installations/" + scope.IntegrationInstallID + "/configuration": map[string]any{
			"integration_app_id": scope.IntegrationAppID, "app_configuration_revision": app.ConfigurationRevision,
			"install": map[string]any{
				"id": scope.IntegrationInstallID, "project_id": scope.ProjectID,
				"provider_account_ref": install.ProviderAccountRef, "provider_tenant_id": install.ProviderTenantID,
				"display_name": install.DisplayName, "provider_config": install.ProviderConfig,
				"provider_identity": install.ProviderIdentity, "metadata": install.Metadata,
				"configuration_revision": install.ConfigurationRevision, "updated_at": install.UpdatedAt,
			},
			"credential": map[string]any{
				"kind": "slack_app_credentials", "payload": map[string]string{"access_token": "migration-fake-slack-token"},
			},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer migration-fake-core-token", r.Header.Get("Authorization"))
		if r.Method == http.MethodPost && r.URL.Path == "/channel-connector/events/claim-next" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, ok := responses[r.URL.Path]
		if r.Method != http.MethodGet || !ok {
			t.Errorf("unexpected core call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(body))
	}))
}
