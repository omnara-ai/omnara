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
	native := githubJourneyProvider(t, &sends)
	configuration := map[string]any{
		"coreUrl": core.URL + "/api/v1", "githubUrl": native.URL, "token": token,
		"appID": testPublicID(t, publicid.KindIntegrationApp, app.ID),
	}
	for index, kind := range []string{"opened", "synchronize", "timeline", "review", "review"} {
		configuration["webhooks"] = []any{githubJourneyWebhook(t, kind)}
		result := runGitHubJourney(t, configuration)
		require.Equal(t, 1, result.Processed)
		require.Equal(t, []int{http.StatusAccepted}, result.WebhookStatuses)
		var count, agents int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT agent_id) FROM agent_inputs
WHERE project_id=$1 AND integration_target_binding_id IS NOT NULL`, project.ProjectUUID).Scan(&count, &agents))
		require.Equal(t, min(index+1, 4), count, "semantic duplicate callbacks must not repeat an input")
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
	child, err := project.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, project.ProjectUUID,
		install.ID, "repo:456:pr:7:comment:PRRC_1")
	require.NoError(t, err)
	require.Equal(t, root.ID, child.ParentChannelID)
	access, err := project.Store.Integrations().GetAgentChannelAccess(ctx, project.ProjectUUID, agentID, child.ID)
	require.NoError(t, err)
	require.True(t, access.Capabilities.Read)
	require.True(t, access.Capabilities.Send)
	var childInputs, completed int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs
WHERE agent_id=$1 AND integration_target_id=$2`, agentID, child.ID).Scan(&childInputs))
	require.Equal(t, 1, childInputs)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_event_receipts
WHERE integration_install_id=$1 AND state='completed'`, install.ID).Scan(&completed))
	require.Equal(t, 5, completed)
	owners, operations, payloads := prepareGitHubJourneyOperations(t, project, agentID, root, app.ID)
	configuration["operations"] = operations
	result := runGitHubJourney(t, configuration)
	require.Zero(t, result.Processed)
	require.Len(t, result.OperationResults, 2)
	for i, operation := range result.OperationResults {
		require.Equal(t, channelconnector.OperationCompleted, operation.Outcome)
		call, err := project.Store.Execution().CompleteChannelOperation(ctx, executionstore.CompleteChannelOperationInput{
			Prepared: owners[i], Payload: payloads[i], Result: operation,
		})
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStateCompleted, call.State)
	}
	require.Contains(t, string(result.OperationResults[0].Payload), "Published history")
	require.Contains(t, string(result.OperationResults[1].Payload), "IC_sent")
	require.EqualValues(t, 1, sends.Load())
	t.Run("durable_control_restart", func(t *testing.T) {
		exerciseGitHubControlRecovery(t, handler, pool, project, app.ID, install.ID, token)
	})
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

func githubJourneyProvider(t *testing.T, sends *atomic.Int32) *httptest.Server {
	t.Helper()
	identity := map[string]any{"id": "PR_selected", "number": 7, "repository": map[string]string{"id": "R_selected"}}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
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
		case "/graphql":
			var request struct {
				Query string `json:"query"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			data := map[string]any{}
			switch {
			case strings.Contains(request.Query, "query GitHubViewer"):
				data["viewer"] = map[string]string{"id": "U_bot", "login": "example[bot]"}
			case strings.Contains(request.Query, "query GitHubInboundComment"):
				data["node"] = map[string]any{"id": "PRRC_1", "state": "SUBMITTED", "replyTo": nil,
					"pullRequestReview": map[string]string{"id": "PRR_1", "state": "COMMENTED"}, "pullRequest": identity}
			case strings.Contains(request.Query, "query GitHubInboundThreads"):
				data["node"] = map[string]any{"id": "R_selected", "pullRequest": map[string]any{
					"id": "PR_selected", "number": 7, "repository": map[string]string{"id": "R_selected"},
					"reviewThreads": map[string]any{
						"nodes": []any{map[string]any{"id": "PRRT_1", "comments": map[string]any{
							"nodes": []any{map[string]string{"id": "PRRC_1", "state": "SUBMITTED"}},
						}}}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					},
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
		assert.NoError(t, json.NewEncoder(w).Encode(value))
	}))
	t.Cleanup(provider.Close)
	return provider
}

func prepareGitHubJourneyOperations(
	t *testing.T, project publicHTTPProject, agentID uuid.UUID,
	target integrationstore.IntegrationTargetRecord, appID uuid.UUID,
) ([]executionstore.PreparedChannelOperation, []map[string]any, []json.RawMessage) {
	t.Helper()
	ctx, store := t.Context(), project.Store
	agent, err := store.Execution().GetAgentInProject(ctx, project.ProjectUUID, agentID)
	require.NoError(t, err)
	work, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, agentID, work.RuntimeLock.AgentID)
	inputs := make([]uuid.UUID, len(work.Model.AdmittedInputTurn.Inputs))
	for i, input := range work.Model.AdmittedInputTurn.Inputs {
		inputs[i] = input.ID
	}
	require.NotEmpty(t, inputs)
	claim := claimNormalModelCallForHTTPTest(t, ctx, store, project.ProjectUUID, agentID, work.RuntimeLock,
		inputs, agent.CurrentConfigID, work.Model.AdmittedInputTurn.Events[len(inputs)-1].Sequence)
	channelID := testPublicID(t, publicid.KindIntegrationTarget, target.ID)
	proposals := []model.ToolCall{
		{ID: "github-journey-read", Name: toolcatalog.ToolNameReadChannel,
			Input: json.RawMessage(fmt.Sprintf(`{"channel_id":%q,"limit":3}`, channelID))},
		{ID: "github-journey-send", Name: toolcatalog.ToolNameSendChannelMessage,
			Input: json.RawMessage(fmt.Sprintf(`{"channel_id":%q,"message":{"text":"Explicit agent reply"}}`, channelID))},
	}
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, store, project.ProjectUUID, claim.Context)
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "github-journey-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls(proposals)})
	require.NoError(t, err)
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, RuntimeLockID: work.RuntimeLock.ID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: response,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ProviderCallID: proposals[0].ID, Type: toolcatalog.ToolTypeBuiltIn},
				{ProviderCallID: proposals[1].ID, Type: toolcatalog.ToolTypeBuiltIn},
			},
		})
	require.NoError(t, err)
	require.Len(t, calls, 2)
	prepared := make([]executionstore.PreparedChannelOperation, 2)
	operations := make([]map[string]any, 2)
	payloads := make([]json.RawMessage, 2)
	for i, call := range calls {
		_, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, ID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
		})
		require.NoError(t, err)
		owner := executionstore.PrepareChannelOperationInput{
			ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
				ProjectID: project.ProjectUUID, AgentID: agentID, ToolCallID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
			}, TurnID: call.TurnID, ChannelID: target.ID, Operation: integrationstore.ChannelBindingOperationRead,
		}
		if i == 1 {
			owner.Operation = integrationstore.ChannelBindingOperationSend
		}
		_, err = store.Execution().ExecuteToolCall(ctx, owner.ExecuteToolCallInput,
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.StartToolCallAsync(), nil
			})
		require.NoError(t, err)
		prepared[i], err = store.Execution().PrepareChannelOperation(ctx, owner)
		require.NoError(t, err)
		destination := channelconnector.OperationDestination{
			ImplementationKey: "github_pr", ProviderRef: target.ProviderRef, ProviderRefKind: target.ProviderRefKind,
			ProviderMetadata: json.RawMessage(`{}`),
		}
		if i == 0 {
			_, err = store.Execution().RecheckChannelOperation(ctx, prepared[i])
			require.NoError(t, err)
			payloads[i], err = json.Marshal(channelconnector.ReadPayload{Destination: destination, Limit: 3})
		} else {
			_, params, prepareErr := store.Execution().PrepareChannelSend(ctx, prepared[i])
			require.NoError(t, prepareErr)
			payloads[i], err = json.Marshal(channelconnector.SendPayload{
				Destination: destination, Message: channelconnector.Message{Text: "Explicit agent reply"}, Params: params,
			})
		}
		require.NoError(t, err)
		operations[i] = map[string]any{
			"kind": string(owner.Operation), "request_id": testPublicID(t, publicid.KindToolCall, call.ID), "payload": payloads[i],
			"scope": channelconnector.OperationScope{
				ProjectID: project.ProjectID, IntegrationAppID: testPublicID(t, publicid.KindIntegrationApp, appID),
				IntegrationInstallID: testPublicID(t, publicid.KindIntegrationInstall, target.IntegrationInstallID),
				AgentID:              testPublicID(t, publicid.KindAgent, agentID), ChannelID: channelID,
			},
		}
	}
	return prepared, operations, payloads
}
