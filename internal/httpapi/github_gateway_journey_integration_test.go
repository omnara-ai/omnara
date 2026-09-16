//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real generated TS client/behavior, Go authenticated HTTP and PostgreSQL; only
// GitHub is a local fake. Credentials are ephemeral fixture material, never live.
func TestGitHubGatewayPRCommunicationJourney(t *testing.T) {
	if os.Getenv("OMNARA_TEST_GITHUB_GATEWAY_RUNNER") == "" {
		t.Skip("OMNARA_TEST_GITHUB_GATEWAY_RUNNER is required")
	}
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	core := httptest.NewUnstartedServer(nil)
	t.Cleanup(core.Close)
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{{
		ID: "github-journey", Token: token,
		Capabilities: []channelconnector.Capability{{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "github"}},
	}})
	require.NoError(t, err)
	handler := newIntegrationServer(pool, WithChannelConnectorAuthenticator(auth),
		WithInternalAPIOrigins([]string{"http://" + core.Listener.Addr().String()}))
	project := bootstrapPublicHTTPProject(t, handler, "github-native-journey")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	secret, _, err := project.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: project.OrgUUID, OwnerKind: secretstore.SecretOwnerOrg, Name: "GitHub journey credentials",
		Actor: httpUserPrincipal(project.AdminUserUUID),
		Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{
			"private_key": privateKey, "webhook_secret": "github-local-signature",
		}},
	})
	require.NoError(t, err)
	app, err := project.Store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: project.OrgUUID, Provider: "github", ProviderAppRef: "42", DisplayName: "GitHub fixture",
		ConnectorKey: channelconnector.BuiltInConnectorKey, State: integrationstore.IntegrationAppStateActive,
		CredentialSecretID: secret.ID,
	})
	require.NoError(t, err)
	profile := createSlackReadyHTTPProfile(t, handler, project, "github-reviewer", project.AdminToken)
	profileID := channelReceiptString(t, profile, "id")
	install, err := project.Store.Integrations().UpsertIntegrationInstall(
		ctx, integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: app.ID,
			InstalledBy: httpUserPrincipal(project.AdminUserUUID), Provider: "github",
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State: integrationstore.IntegrationInstallStateActive, ProviderTenantID: "123", ProviderAccountRef: "456",
			DisplayName: "example/project", ProviderIdentity: json.RawMessage(`{"repository_owner":"example",` +
				`"repository_name":"project","repository_node_id":"R_selected"}`),
		})
	require.NoError(t, err)
	setupPath := project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, install.ID) + "/launch-profile"
	requestJSONWithHeaders(t, handler, http.MethodPut, setupPath,
		workflowHTTPJSON(t, map[string]any{"agent_profile_id": profileID}), "",
		http.StatusOK, authHeaders(project.AdminToken))
	core.Config.Handler = handler
	core.Start()
	var sends atomic.Int32
	native := githubJourneyProvider(t, &sends, func(delegated bool) {
		var definitions int
		assert.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_channel_definitions
WHERE integration_install_id=$1 AND implementation_key='github_review_thread'`, install.ID).Scan(&definitions))
		want := 0
		if delegated {
			want = 1
		}
		assert.Equal(t, want, definitions, "generic child definition must exist before a delegated native write")
	})
	configuration := map[string]any{
		"coreUrl": core.URL + "/api/v1", "githubUrl": native.URL, "token": token,
		"appID": testPublicID(t, publicid.KindIntegrationApp, app.ID),
	}
	for index, kind := range []string{"opened", "synchronize", "timeline"} {
		configuration["webhooks"] = []any{githubJourneyWebhook(t, kind)}
		result := runGitHubJourney(t, configuration)
		require.Equal(t, 1, result.Processed)
		require.Equal(t, []int{http.StatusAccepted}, result.WebhookStatuses)
		var count, agents int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT agent_id) FROM agent_inputs
WHERE project_id=$1 AND integration_target_binding_id IS NOT NULL`, project.ProjectUUID).Scan(&count, &agents))
		require.Equal(t, index+1, count, "semantic duplicate callbacks must not repeat an input")
		require.Equal(t, 1, agents, "all PR communication belongs to the same default workflow")
	}
	delete(configuration, "webhooks")
	require.Zero(t, sends.Load(), "inbound communication must never mutate native content")
	var agentID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT agent_id FROM agent_inputs
WHERE project_id=$1 AND integration_target_binding_id IS NOT NULL LIMIT 1`, project.ProjectUUID).Scan(&agentID))
	root, err := project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx, project.ProjectUUID, install.ID, "repo:456:pr:7")
	require.NoError(t, err)
	var definitions int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_channel_definitions
WHERE integration_install_id=$1 AND implementation_key='github_review_thread'`, install.ID).Scan(&definitions))
	require.Zero(t, definitions, "first inline send must work without an inbound thread or preseeded child definition")
	exerciseGitHubUndelegatedComment(t, project, app.ID, agentID, root, configuration, &sends)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_channel_definitions
WHERE integration_install_id=$1 AND implementation_key='github_review_thread'`, install.ID).Scan(&definitions))
	require.Zero(t, definitions, "a sender without reply grants must not preseed the child definition")
	var targets int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_targets
WHERE integration_install_id=$1`, install.ID).Scan(&targets))
	require.Equal(t, 1, targets, "only the PR is registered before the first delegated inline send")
	agentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, agentID)
	requestJSONWithHeaders(t, handler, http.MethodPost, agentPath+"/channel-bindings",
		workflowHTTPJSON(t, map[string]any{
			"channel_id":           testPublicID(t, publicid.KindIntegrationTarget, root.ID),
			"grants":               map[string]bool{"receive": false, "read": false, "send": true},
			"reply_channel_grants": map[string]bool{"receive": true, "read": false, "send": true},
		}), "", http.StatusOK, authHeaders(project.AdminToken))
	exerciseGitHubImmediateComments(t, project, app.ID, agentID, root, configuration, &sends)

	// Inbound native review-thread communication comes only after the first
	// explicit comment created its own registered child through completion.
	for range 2 {
		configuration["webhooks"] = []any{githubJourneyWebhook(t, "review")}
		result := runGitHubJourney(t, configuration)
		require.Equal(t, 1, result.Processed)
		require.Equal(t, []int{http.StatusAccepted}, result.WebhookStatuses)
	}
	delete(configuration, "webhooks")
	child, err := project.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, project.ProjectUUID,
		install.ID, "repo:456:pr:7:comment:PRRC_1")
	require.NoError(t, err)
	require.Equal(t, root.ID, child.ParentChannelID)
	access, err := project.Store.Integrations().GetAgentChannelAccess(ctx, project.ProjectUUID, agentID, child.ID)
	require.NoError(t, err)
	require.True(t, access.Capabilities.Read)
	require.True(t, access.Capabilities.Send)
	var inputs, agents, childInputs, completed int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT agent_id) FROM agent_inputs
WHERE project_id=$1 AND integration_target_binding_id IS NOT NULL`, project.ProjectUUID).Scan(&inputs, &agents))
	require.Equal(t, 4, inputs, "the repeated signed callback must not create another semantic input")
	require.Equal(t, 1, agents)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs
WHERE agent_id=$1 AND integration_target_id=$2`, agentID, child.ID).Scan(&childInputs))
	require.Equal(t, 1, childInputs)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_event_receipts
WHERE integration_install_id=$1 AND state='completed'`, install.ID).Scan(&completed))
	require.Equal(t, 5, completed)
	require.EqualValues(t, 5, sends.Load(), "inbound callbacks and replay cannot publish native content")
	t.Run("durable_control_restart", func(t *testing.T) {
		exerciseGitHubControlRecovery(t, handler, pool, project, app.ID, install.ID, token)
	})
}

func exerciseGitHubUndelegatedComment(
	t *testing.T, project publicHTTPProject, appID, agentID uuid.UUID,
	root integrationstore.IntegrationTargetRecord, configuration map[string]any, sends *atomic.Int32,
) {
	t.Helper()
	channelID := testPublicID(t, publicid.KindIntegrationTarget, root.ID)
	proposal := model.ToolCall{ID: "github-no-grants", Name: toolcatalog.ToolNameSendChannelMessage,
		Input: json.RawMessage(workflowHTTPJSON(t, map[string]any{
			"channel_id": channelID, "message": map[string]string{"text": "Published without delegation"},
			"params": map[string]any{"commit_id": strings.Repeat("b", 40), "path": "main.go", "line": 12, "side": "RIGHT"},
		}))}
	runtimeID, owners, calls := prepareGitHubJourneyOperations(t, project, agentID, root, []model.ToolCall{proposal})
	require.Nil(t, owners[0].Binding().ReplyChannelGrants)
	result, completed := runGitHubJourneyOperations(t, project, appID, configuration, owners, calls)
	publication, err := channelconnector.DecodeSendResult(result.OperationResults[0].Payload)
	require.NoError(t, err)
	require.Equal(t, channelconnector.MessagePublished, publication.Publication)
	require.Equal(t, channelconnector.MessageAtDestination, publication.MessageChannel)
	require.Equal(t, "PRRC_no_grants", publication.MessageID)
	require.Nil(t, publication.ReplyChannel)
	require.Nil(t, publication.ContinuationError)
	message := githubJourneySendResult(t, completed[0])
	require.Equal(t, channelconnector.MessagePublished, message.Message.Publication)
	require.Equal(t, channelID, message.Message.ChannelID)
	require.Empty(t, message.Message.ReplyChannelID)
	require.Nil(t, message.ContinuationError)
	require.EqualValues(t, 1, sends.Load())
	require.NoError(t, project.Store.Execution().ReleaseAgentRuntimeLock(
		t.Context(), project.ProjectUUID, agentID, runtimeID))
}

// First publication starts from only the PR definition; completion must resolve
// the provider's returned child, delegate exactly the preconfigured grants, and
// make that public channel usable by a subsequent normal model tool call.
func exerciseGitHubImmediateComments(
	t *testing.T, project publicHTTPProject, appID, agentID uuid.UUID,
	root integrationstore.IntegrationTargetRecord, configuration map[string]any, sends *atomic.Int32,
) {
	t.Helper()
	ctx := t.Context()
	channelID := testPublicID(t, publicid.KindIntegrationTarget, root.ID)
	send := func(id, text string, params map[string]any) model.ToolCall {
		return model.ToolCall{ID: id, Name: toolcatalog.ToolNameSendChannelMessage,
			Input: json.RawMessage(workflowHTTPJSON(t, map[string]any{
				"channel_id": channelID, "message": map[string]string{"text": text}, "params": params,
			}))}
	}
	proposals := []model.ToolCall{
		send("github-inline", "Immediate inline finding", map[string]any{
			"commit_id": strings.Repeat("b", 40), "path": "main.go", "line": 12, "side": "RIGHT",
			"start_line": 10, "start_side": "RIGHT",
		}),
		send("github-file", "Immediate file finding", map[string]any{
			"commit_id": strings.Repeat("b", 40), "path": "README.md", "subject_type": "file",
		}),
		{ID: "github-read", Name: toolcatalog.ToolNameReadChannel,
			Input: json.RawMessage(fmt.Sprintf(`{"channel_id":%q,"limit":3}`, channelID))},
		send("github-timeline", "Explicit agent reply", map[string]any{}),
		send("github-invalid-line", "Must not publish", map[string]any{
			"commit_id": strings.Repeat("b", 40), "path": "main.go", "line": 0, "side": "RIGHT",
		}),
	}
	runtimeID, owners, calls := prepareGitHubJourneyOperations(t, project, agentID, root, proposals)
	_, _, err := project.Store.Execution().PrepareChannelSend(ctx, owners[4])
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest, "the published definition must validate persisted generic params")
	require.EqualValues(t, 1, sends.Load(), "invalid params are rejected before any native write")
	parts, err := executionstore.ToolResultContentParts(json.RawMessage(`{"error":"invalid line"}`))
	require.NoError(t, err)
	_, err = project.Store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
		ProjectID: project.ProjectUUID, AgentID: agentID, ID: calls[4].ID, RuntimeLockID: runtimeID,
		Outcome: executionstore.ToolResultOutcomeFailed, ResultContentParts: parts,
	})
	require.NoError(t, err)
	result, completed := runGitHubJourneyOperations(t, project, appID, configuration, owners[:4], calls[:4])
	require.EqualValues(t, 4, sends.Load(), "one inline, one file comment, one timeline comment")
	require.Contains(t, string(result.OperationResults[2].Payload), "Published history")
	for _, index := range []int{0, 1, 3} {
		publication, err := channelconnector.DecodeSendResult(result.OperationResults[index].Payload)
		require.NoError(t, err)
		require.Equal(t, channelconnector.MessagePublished, publication.Publication)
		require.Nil(t, publication.ContinuationError)
		require.Equal(t, channelconnector.MessagePublished, githubJourneySendResult(t, completed[index]).Message.Publication)
	}
	for index, id := range []string{"PRRC_inline", "PRRC_file"} {
		publication, err := channelconnector.DecodeSendResult(result.OperationResults[index].Payload)
		require.NoError(t, err)
		require.Equal(t, channelconnector.MessageAtReplyChannel, publication.MessageChannel)
		require.NotNil(t, publication.ReplyChannel)
		require.Equal(t, "repo:456:pr:7:comment:"+id, publication.ReplyChannel.ProviderRef)
		child, err := project.Store.Integrations().GetIntegrationTargetByProviderRef(
			ctx, project.ProjectUUID, root.IntegrationInstallID, publication.ReplyChannel.ProviderRef)
		require.NoError(t, err, "core completion must register the first returned native child")
		require.Equal(t, root.ID, child.ParentChannelID)
		require.JSONEq(t, fmt.Sprintf(`{"thread_id":%q}`, strings.Replace(id, "PRRC_", "PRRT_", 1)),
			string(child.ProviderMetadata))
		access, err := project.Store.Integrations().GetAgentChannelAccess(ctx, project.ProjectUUID, agentID, child.ID)
		require.NoError(t, err)
		require.True(t, access.Capabilities.Send)
		require.False(t, access.Capabilities.Read, "parent read access must not widen explicit reply grants")
		binding, err := project.Store.Integrations().GetActiveReceiveBindingForTarget(
			ctx, project.ProjectUUID, agentID, child.ID)
		require.NoError(t, err)
		require.Nil(t, binding.ReplyChannelGrants, "continuation does not delegate onward")
		message := githubJourneySendResult(t, completed[index])
		require.Nil(t, message.ContinuationError)
		require.Equal(t, testPublicID(t, publicid.KindIntegrationTarget, child.ID), message.Message.ChannelID)
		require.Equal(t, id, message.Message.MessageID)
	}
	require.NoError(t, project.Store.Execution().ReleaseAgentRuntimeLock(ctx, project.ProjectUUID, agentID, runtimeID))
	child, err := project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx, project.ProjectUUID, root.IntegrationInstallID, "repo:456:pr:7:comment:PRRC_inline")
	require.NoError(t, err)
	channelID = testPublicID(t, publicid.KindIntegrationTarget, child.ID)
	runtimeID, owners, calls = prepareGitHubJourneyOperations(t, project, agentID, child, []model.ToolCall{
		send("github-thread-reply", "Published thread reply", map[string]any{}),
	})
	result, completed = runGitHubJourneyOperations(t, project, appID, configuration, owners, calls)
	publication, err := channelconnector.DecodeSendResult(result.OperationResults[0].Payload)
	require.NoError(t, err)
	require.Equal(t, channelconnector.MessagePublished, publication.Publication)
	require.Equal(t, channelconnector.MessageAtDestination, publication.MessageChannel)
	require.Equal(t, "PRRC_reply", publication.MessageID)
	require.Nil(t, publication.ReplyChannel)
	message := githubJourneySendResult(t, completed[0])
	require.Nil(t, message.ContinuationError)
	require.Equal(t, channelconnector.MessagePublished, message.Message.Publication)
	require.Equal(t, channelID, message.Message.ChannelID)
	require.Equal(t, "Published thread reply", message.Message.Content.Text)
	require.EqualValues(t, 5, sends.Load(), "the reply posts once to the native root database ID")
	require.NoError(t, project.Store.Execution().ReleaseAgentRuntimeLock(ctx, project.ProjectUUID, agentID, runtimeID))
}

func githubJourneySendResult(t *testing.T, record executionstore.ToolCallRecord) channelconnector.SendMessageResult {
	t.Helper()
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(record.ResultContentParts, &parts))
	require.Len(t, parts, 1)
	require.Equal(t, "structured_data", parts[0].Type)
	var result channelconnector.SendMessageResult
	require.NoError(t, json.Unmarshal(parts[0].Value, &result))
	return result
}

type githubJourneyResult struct {
	Processed        int                                `json:"processed"`
	WebhookStatuses  []int                              `json:"webhook_statuses"`
	OperationResults []channelconnector.OperationResult `json:"operation_results"`
	ControlResults   []struct {
		Outcome            string `json:"outcome"`
		LastInstallationID string `json:"last_installation_id"`
	} `json:"control_results"`
}

func runGitHubJourney(t *testing.T, configuration map[string]any) githubJourneyResult {
	t.Helper()
	node := os.Getenv("OMNARA_TEST_NODE")
	if node == "" {
		node = "node"
	}
	input, err := json.Marshal(configuration)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, os.Getenv("OMNARA_TEST_GITHUB_GATEWAY_RUNNER"))
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "GitHub journey: %s", output)
	var result githubJourneyResult
	require.NoError(t, json.Unmarshal(output, &result))
	return result
}

func githubJourneyWebhook(t *testing.T, kind string) map[string]string {
	t.Helper()
	author := map[string]any{"id": 17, "node_id": "U_human", "login": "human", "type": "User"}
	pr := map[string]any{
		"id": 22, "node_id": "PR_selected", "number": 7, "title": "Review", "body": "Original description",
		"state": "open", "draft": true, "updated_at": "2026-09-15T12:00:00Z",
		"head": map[string]string{"sha": strings.Repeat("b", 40)},
		"base": map[string]string{"sha": strings.Repeat("a", 40)},
	}
	body := map[string]any{
		"action": kind, "installation": map[string]int{"id": 123}, "sender": author, "pull_request": pr,
		"repository": map[string]any{"id": 456, "node_id": "R_selected", "name": "project",
			"owner": map[string]string{"login": "example"}},
	}
	event := "pull_request"
	switch kind {
	case "control":
		event = "installation_repositories"
		body = map[string]any{"action": "added", "installation": map[string]int{"id": 123, "app_id": 42}}
	case "synchronize":
		body["before"], body["after"] = strings.Repeat("a", 40), strings.Repeat("b", 40)
	case "timeline", "review":
		body["action"] = "created"
		body["comment"] = map[string]any{
			"id": 33, "node_id": "PRRC_1", "user": author, "body": "Original signed comment",
			"created_at": "2026-09-15T12:00:00Z", "updated_at": "2026-09-15T12:00:00Z",
			"pull_request_review_id": 44, "path": "main.go", "line": 12, "side": "RIGHT",
			"commit_id": strings.Repeat("a", 40),
		}
		event = "pull_request_review_comment"
		if kind == "timeline" {
			event = "issue_comment"
			pr["pull_request"] = map[string]string{"url": "https://api.github.invalid/ignored"}
			body["issue"] = pr
			delete(body, "pull_request")
		}
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte("github-local-signature"))
	_, err = mac.Write(raw)
	require.NoError(t, err)
	return map[string]string{"event": event, "delivery": uuid.NewString(),
		"body": string(raw), "signature": "sha256=" + hex.EncodeToString(mac.Sum(nil))}
}

func githubJourneyProvider(t *testing.T, sends *atomic.Int32, beforeWrite func(bool)) *httptest.Server {
	t.Helper()
	identity := map[string]any{"id": "PR_selected", "number": 7, "repository": map[string]string{"id": "R_selected"}}
	var mu sync.Mutex
	comments := map[string]map[string]any{
		"PRRC_1": githubJourneyPublishedComment("PRRC_1", "33", "Original signed comment", "main.go", 12, ""),
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var value any
		status := http.StatusOK
		switch r.URL.Path {
		case "/app/installations/123/access_tokens":
			var request map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			assert.Equal(t, []any{float64(456)}, request["repository_ids"])
			assert.Equal(t, map[string]any{"pull_requests": "write", "issues": "read", "metadata": "read"},
				request["permissions"])
			value = map[string]any{
				"token": "local-native-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			}
		case "/repositories/456":
			value = map[string]any{
				"id": 456, "node_id": "R_selected", "name": "project", "owner": map[string]string{"login": "example"},
			}
		case "/repos/example/project/pulls/7/comments", "/repos/example/project/pulls/7/comments/9007199254740993/replies":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer local-native-token", r.Header.Get("Authorization"))
			var request map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			id, databaseID, rootID, path := "PRRC_inline", "9007199254740993", "", "main.go"
			var line any = 12
			expected := map[string]any{"body": "Immediate inline finding", "commit_id": strings.Repeat("b", 40),
				"path": path, "line": float64(12), "side": "RIGHT", "start_line": float64(10), "start_side": "RIGHT"}
			switch {
			case strings.HasSuffix(r.URL.Path, "/replies"):
				id, databaseID, rootID = "PRRC_reply", "9007199254740997", "PRRC_inline"
				assert.Contains(t, comments, rootID)
				expected = map[string]any{"body": "Published thread reply"}
			case request["body"] == "Published without delegation":
				id, databaseID = "PRRC_no_grants", "9007199254740991"
				expected["body"] = "Published without delegation"
				delete(expected, "start_line")
				delete(expected, "start_side")
				beforeWrite(false)
			case request["subject_type"] == "file":
				id, databaseID, path, line = "PRRC_file", "9007199254740995", "README.md", nil
				expected = map[string]any{"body": "Immediate file finding", "commit_id": strings.Repeat("b", 40),
					"path": path, "subject_type": "file"}
				beforeWrite(true)
			default:
				beforeWrite(true)
			}
			assert.Equal(t, expected, request, "native REST accepts only placement/body fields, never staged-review selectors")
			assert.NotContains(t, comments, id, "a publication must not be retried")
			comments[id] = githubJourneyPublishedComment(id, databaseID, fmt.Sprint(request["body"]), path, line, rootID)
			sends.Add(1)
			status = http.StatusCreated
			value = map[string]any{"id": json.Number(databaseID), "node_id": id, "body": request["body"],
				"path": path, "line": line, "commit_id": strings.Repeat("b", 40), "pull_request_review_id": 44,
				"created_at": "2026-09-15T12:00:00Z", "user": map[string]string{"login": "example[bot]"}}
		case "/graphql":
			var request struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			data := map[string]any{}
			switch {
			case strings.Contains(request.Query, "query GitHubViewer"):
				data["viewer"] = map[string]string{"id": "U_bot", "login": "example[bot]"}
			case strings.Contains(request.Query, "query GitHubCommentIdentity"),
				strings.Contains(request.Query, "query GitHubInboundComment"):
				id := fmt.Sprint(request.Variables["comment"])
				if strings.Contains(request.Query, "query GitHubInboundComment") {
					assert.NotEqual(t, "PRRC_no_grants", id, "no thread lookup without delegation")
				}
				assert.Contains(t, comments, id, "identity reads must follow actual publication")
				data["node"] = comments[id]
			case strings.Contains(request.Query, "query GitHubThreadIdentity"):
				threadID := fmt.Sprint(request.Variables["thread"])
				id := strings.Replace(threadID, "PRRT_", "PRRC_", 1)
				assert.Contains(t, comments, id)
				data["node"] = map[string]any{"id": threadID, "pullRequest": identity,
					"comments": map[string]any{"nodes": []any{comments[id]}}}
			case strings.Contains(request.Query, "query GitHubPendingReviews"):
				assert.Equal(t, "example[bot]", request.Variables["author"])
				data["node"] = map[string]any{"id": "R_selected", "pullRequest": map[string]any{
					"id": "PR_selected", "number": 7, "repository": map[string]string{"id": "R_selected"},
					"reviews": map[string]any{"nodes": []any{}},
				}}
			case strings.Contains(request.Query, "query GitHubInboundThreads"):
				assert.Contains(t, comments, "PRRC_inline", "no thread scan before a delegated comment is published")
				threads := make([]any, 0, len(comments))
				for id, comment := range comments {
					if comment["replyTo"] != nil {
						continue
					}
					threads = append(threads, map[string]any{"id": strings.Replace(id, "PRRC_", "PRRT_", 1),
						"comments": map[string]any{"nodes": []any{map[string]string{"id": id, "state": "SUBMITTED"}}}})
				}
				data["node"] = map[string]any{"id": "R_selected", "pullRequest": map[string]any{
					"id": "PR_selected", "number": 7, "repository": map[string]string{"id": "R_selected"},
					"reviewThreads": map[string]any{"nodes": threads,
						"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}},
				}}
			case strings.Contains(request.Query, "query GitHubPullRequest"):
				data["node"] = map[string]any{"id": "R_selected", "pullRequest": map[string]any{
					"id": "PR_selected", "number": 7, "repository": map[string]string{"id": "R_selected"},
					"title": "Review", "state": "OPEN", "headRefOid": strings.Repeat("b", 40),
				}}
			case strings.Contains(request.Query, "query GitHubTimeline"):
				data["node"] = map[string]any{"id": "R_selected", "pullRequest": map[string]any{
					"id": "PR_selected", "number": 7, "repository": map[string]string{"id": "R_selected"},
					"timelineItems": map[string]any{"nodes": []any{map[string]any{
						"__typename": "IssueComment", "id": "IC_history", "body": "Published history",
						"author": map[string]string{"login": "human"}, "createdAt": "2026-09-15T12:00:00Z",
					}}, "pageInfo": map[string]any{"hasPreviousPage": false, "startCursor": "cursor-one"}},
				}}
			case strings.Contains(request.Query, "mutation GitHubTimelineComment"):
				sends.Add(1)
				data["addComment"] = map[string]any{"commentEdge": map[string]any{"node": map[string]any{
					"id": "IC_sent", "body": "Explicit agent reply", "author": map[string]string{"login": "example[bot]"},
					"createdAt": "2026-09-15T12:00:00Z",
				}}}
			default:
				t.Errorf("unexpected native operation: %s", request.Query)
				http.Error(w, "unsupported", http.StatusBadRequest)
				return
			}
			value = map[string]any{"data": data}
		default:
			t.Errorf("unexpected native endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		assert.NoError(t, json.NewEncoder(w).Encode(value))
	}))
	t.Cleanup(provider.Close)
	return provider
}

func githubJourneyPublishedComment(id, databaseID, text, path string, line any, rootID string) map[string]any {
	var replyTo any
	if rootID != "" {
		replyTo = map[string]string{"id": rootID}
	}
	return map[string]any{
		"id": id, "fullDatabaseId": databaseID, "body": text, "state": "SUBMITTED", "path": path, "line": line,
		"author": map[string]string{"login": "example[bot]"}, "createdAt": "2026-09-15T12:00:00Z",
		"originalCommit": map[string]string{"oid": strings.Repeat("b", 40)}, "replyTo": replyTo,
		"pullRequestReview": map[string]string{"id": "PRR_" + id, "state": "COMMENTED"},
		"pullRequest": map[string]any{"id": "PR_selected", "number": 7,
			"repository": map[string]string{"id": "R_selected"}},
	}
}

func prepareGitHubJourneyOperations(
	t *testing.T, project publicHTTPProject, agentID uuid.UUID,
	target integrationstore.IntegrationTargetRecord, proposals []model.ToolCall,
) (uuid.UUID, []executionstore.PreparedChannelOperation, []executionstore.ToolCallRecord) {
	t.Helper()
	ctx, store := t.Context(), project.Store
	work, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, agentID, work.RuntimeLock.AgentID)
	require.Equal(t, executionstore.AgentWorkModel, work.Kind)
	snapshot, err := store.Execution().CaptureAgentConfigForModelContext(ctx, project.ProjectUUID, agentID)
	require.NoError(t, err)
	claim, err := store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID: project.ProjectUUID, AgentID: agentID, RuntimeLockID: work.RuntimeLock.ID,
		OpeningInputIDs: work.Model.InputIDs, AgentConfigID: snapshot.AgentConfig.ID,
		InputEventSequence:       snapshot.InputEventSequence,
		SourceModelCallContextID: work.Model.SourceModelCallContextID,
		SourceModelOutputID:      work.Model.SourceModelOutputID,
	})
	require.NoError(t, err)
	require.True(t, claim.Claimed)
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, store, project.ProjectUUID, claim.Context)
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "github-journey-" + uuid.NewString(), StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls(proposals)})
	require.NoError(t, err)
	bindings := make([]executionstore.ToolCallBindingInput, len(proposals))
	for i, proposal := range proposals {
		bindings[i] = executionstore.ToolCallBindingInput{ProviderCallID: proposal.ID, Type: toolcatalog.ToolTypeBuiltIn}
	}
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, RuntimeLockID: work.RuntimeLock.ID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: response, ToolCallBindings: bindings,
		})
	require.NoError(t, err)
	require.Len(t, calls, len(proposals))
	prepared := make([]executionstore.PreparedChannelOperation, len(calls))
	for i, call := range calls {
		_, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, ID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
		})
		require.NoError(t, err)
		owner := executionstore.PrepareChannelOperationInput{
			ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
				ProjectID: project.ProjectUUID, AgentID: agentID, ToolCallID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
			}, TurnID: call.TurnID, ChannelID: target.ID, Operation: integrationstore.ChannelBindingOperationSend,
		}
		if call.Name == toolcatalog.ToolNameReadChannel {
			owner.Operation = integrationstore.ChannelBindingOperationRead
		}
		_, err = store.Execution().ExecuteToolCall(ctx, owner.ExecuteToolCallInput,
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.StartToolCallAsync(), nil
			})
		require.NoError(t, err)
		prepared[i], err = store.Execution().PrepareChannelOperation(ctx, owner)
		require.NoError(t, err)
	}
	return work.RuntimeLock.ID, prepared, calls
}

func runGitHubJourneyOperations(
	t *testing.T, project publicHTTPProject, appID uuid.UUID, configuration map[string]any,
	prepared []executionstore.PreparedChannelOperation, calls []executionstore.ToolCallRecord,
) (githubJourneyResult, []executionstore.ToolCallRecord) {
	t.Helper()
	ctx := t.Context()
	operations := make([]map[string]any, len(calls))
	payloads := make([]json.RawMessage, len(calls))
	for i, call := range calls {
		access := prepared[i].Access()
		var input struct {
			Message channelconnector.Message `json:"message"`
			Limit   int                      `json:"limit"`
		}
		require.NoError(t, json.Unmarshal(call.Input, &input))
		destination := channelconnector.OperationDestination{
			ImplementationKey: access.ImplementationKey, ProviderRef: access.ProviderRef,
			ProviderRefKind: access.ProviderRefKind, ProviderMetadata: access.ProviderMetadata,
		}
		var payload any
		kind := channelconnector.OperationSend
		if call.Name == toolcatalog.ToolNameReadChannel {
			kind = channelconnector.OperationRead
			_, err := project.Store.Execution().RecheckChannelOperation(ctx, prepared[i])
			require.NoError(t, err)
			payload = channelconnector.ReadPayload{Destination: destination, Limit: input.Limit}
		} else {
			_, params, err := project.Store.Execution().PrepareChannelSend(ctx, prepared[i])
			require.NoError(t, err)
			message := channelconnector.SendPayload{Destination: destination, Message: input.Message, Params: params}
			if grants := prepared[i].Binding().ReplyChannelGrants; grants != nil {
				message.ReplyChannelGrants = &channelconnector.ChannelGrants{
					Receive: grants.ReceiveAllowed, Read: grants.ReadAllowed, Send: grants.SendAllowed,
				}
			}
			payload = message
		}
		var err error
		payloads[i], err = json.Marshal(payload)
		require.NoError(t, err)
		operations[i] = map[string]any{
			"kind": kind, "request_id": testPublicID(t, publicid.KindToolCall, call.ID), "payload": payloads[i],
			"scope": channelconnector.OperationScope{
				ProjectID: project.ProjectID, IntegrationAppID: testPublicID(t, publicid.KindIntegrationApp, appID),
				IntegrationInstallID: testPublicID(t, publicid.KindIntegrationInstall, access.IntegrationInstallID),
				AgentID:              testPublicID(t, publicid.KindAgent, call.AgentID),
				ChannelID:            testPublicID(t, publicid.KindIntegrationTarget, access.ChannelID),
			},
		}
	}
	configuration["operations"] = operations
	defer delete(configuration, "operations")
	result := runGitHubJourney(t, configuration)
	require.Zero(t, result.Processed)
	require.Len(t, result.OperationResults, len(calls))
	completed := make([]executionstore.ToolCallRecord, len(calls))
	for i, operation := range result.OperationResults {
		require.Equal(t, channelconnector.OperationCompleted, operation.Outcome)
		var err error
		completed[i], err = project.Store.Execution().CompleteChannelOperation(ctx,
			executionstore.CompleteChannelOperationInput{
				Prepared: prepared[i], Payload: payloads[i], Result: operation,
			})
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStateCompleted, completed[i].State)
		require.Equal(t, executionstore.ToolResultOutcomeSucceeded, completed[i].Outcome)
	}
	return result, completed
}
