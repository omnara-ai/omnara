//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type integrationToolFixture struct {
	Pool               *pgxpool.Pool
	Store              *storage.Store
	User               identitystore.UserRecord
	Profile            executionstore.AgentProfileRecord
	Agent              executionstore.AgentRecord
	AgentConfig        executionstore.AgentConfigRecord
	Lock               executionstore.AgentRuntimeLockRecord
	ModelCallContextID storage.ID
	ModelOutputEventID storage.ID
	Install            integrationstore.IntegrationInstallRecord
	Target             integrationstore.IntegrationTargetRecord
	Now                time.Time
	WithMCP            bool
}

func toolsTestUserPrincipal(userID storage.ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

func integrationToolInteraction(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	toolCallID storage.ID,
	kind executionstore.AgentInteractionKind,
) executionstore.AgentInteractionRecord {
	t.Helper()
	interaction, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		toolCallID,
		kind,
	)
	if err != nil {
		t.Fatalf("get %s interaction: %v", kind, err)
	}
	if !found {
		t.Fatalf("%s interaction not found", kind)
	}
	return interaction
}

func immediateIntegrationBackgroundRunner(ctx context.Context) BackgroundRunner {
	return backgroundRunnerFunc(func(_ string, task func(context.Context) error) bool {
		_ = task(ctx)
		return true
	})
}

func TestIntegrationSendToolDispatchDeliversDistinctCalls(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "send-distinct")
	postCount := 0
	readbackCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.replies":
			readbackCount++
			writeToolTestJSON(w, map[string]any{"ok": true, "messages": []map[string]any{}})
		case "/chat.postMessage":
			postCount++
			writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
		default:
			t.Fatalf("unexpected integration provider path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	calls := []model.ToolCall{
		{
			ID:    "call_send_first",
			Name:  "send_integration_message",
			Input: json.RawMessage(`{"text":"hello"}`),
		},
		{
			ID:    "call_send_second",
			Name:  "send_integration_message",
			Input: json.RawMessage(`{"text":"hello"}`),
		},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	first := calls[0]
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	firstResult, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), first)
	if err != nil {
		t.Fatalf("dispatch first send: %v", err)
	}
	firstBody := integrationToolResultFromTestParts(t, firstResult.ContentParts)
	if firstBody.Code != "delivered" || firstBody.ProviderMessageID != "C123:222.333" {
		t.Fatalf("first send result = %+v", firstBody)
	}
	if postCount != 1 {
		t.Fatalf("post count after first send = %d, want 1", postCount)
	}

	second := calls[1]
	secondResult, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), second)
	if err != nil {
		t.Fatalf("dispatch second send: %v", err)
	}
	secondBody := integrationToolResultFromTestParts(t, secondResult.ContentParts)
	if secondBody.Code != "delivered" || secondBody.ProviderMessageID != "C123:222.333" {
		t.Fatalf("second send result = %+v", secondBody)
	}
	if postCount != 2 {
		t.Fatalf("post count after second send = %d, want 2", postCount)
	}
	if readbackCount != 0 {
		t.Fatalf("readback count = %d, want 0 for successful sends", readbackCount)
	}
}

func TestIntegrationSendToolUploadsArtifactWithSafeRetries(t *testing.T) {
	tests := []struct {
		name                   string
		uploadURLFailures      int
		completionRateLimits   int
		completionStatus       int
		loseAfterPath          string
		wantCode               string
		wantUploadRequests     int
		wantCompletionRequests int
	}{
		{name: "success", wantCode: "delivered", wantUploadRequests: 1, wantCompletionRequests: 1},
		{name: "upload URL transient failure", uploadURLFailures: 1, wantCode: "delivered", wantUploadRequests: 1, wantCompletionRequests: 1},
		{name: "completion rate limit", completionRateLimits: 1, wantCode: "delivered", wantUploadRequests: 1, wantCompletionRequests: 2},
		{name: "completion failure", completionStatus: http.StatusInternalServerError, wantCode: "delivery_unknown", wantUploadRequests: 1, wantCompletionRequests: 1},
		{name: "ownership lost before content upload", loseAfterPath: "/files.getUploadURLExternal"},
		{name: "ownership lost before completion", loseAfterPath: "/upload/v1/artifact", wantUploadRequests: 1},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			seed := "send-artifact-" + strconv.Itoa(index)
			fixture := newIntegrationToolFixtureWithMCP(
				t,
				ctx,
				seed,
				false,
				storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
			)
			artifactContent := []byte("artifact contents")
			artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
				ProjectID:      toolsTestProjectID,
				AgentID:        fixture.Agent.ID,
				ContentType:    "text/plain",
				Filename:       "report.txt",
				Content:        artifactContent,
				MaxBytes:       1024,
				IdempotencyKey: seed,
			})
			if err != nil {
				t.Fatalf("create artifact: %v", err)
			}
			artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
			if err != nil {
				t.Fatalf("encode artifact id: %v", err)
			}
			requests := make(map[string]int)
			loseOwnership := func() {
				if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
					ctx,
					toolsTestProjectID,
					fixture.Agent.ID,
					fixture.Lock.ID,
				); err != nil {
					t.Fatalf("release runtime lock: %v", err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests[r.URL.Path]++
				switch r.URL.Path {
				case "/files.getUploadURLExternal":
					if requests[r.URL.Path] <= tt.uploadURLFailures {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if err := r.ParseForm(); err != nil {
						t.Fatalf("parse upload URL request: %v", err)
					}
					if r.Form.Get("filename") != "report.txt" || r.Form.Get("length") != strconv.Itoa(len(artifactContent)) {
						t.Fatalf("upload URL form = %v", r.Form)
					}
					if tt.loseAfterPath == r.URL.Path {
						loseOwnership()
					}
					writeToolTestJSON(w, map[string]any{
						"ok":         true,
						"upload_url": "https://files.slack.com/upload/v1/artifact",
						"file_id":    "F123",
					})
				case "/upload/v1/artifact":
					if r.Header.Get("Authorization") != "" {
						t.Fatalf("file upload included authorization")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatalf("read uploaded artifact: %v", err)
					}
					if string(body) != string(artifactContent) {
						t.Fatalf("uploaded artifact = %q", body)
					}
					if tt.loseAfterPath == r.URL.Path {
						loseOwnership()
					}
					w.WriteHeader(http.StatusOK)
				case "/files.completeUploadExternal":
					var payload struct {
						Files []struct {
							ID    string `json:"id"`
							Title string `json:"title"`
						} `json:"files"`
						ChannelID      string `json:"channel_id"`
						ThreadTS       string `json:"thread_ts"`
						InitialComment string `json:"initial_comment"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode completion payload: %v", err)
					}
					if len(payload.Files) != 1 || payload.Files[0].ID != "F123" || payload.Files[0].Title != "report.txt" ||
						payload.ChannelID != "C123" || payload.ThreadTS != "111.222" ||
						payload.InitialComment != "here is the report" {
						t.Fatalf("completion payload = %+v", payload)
					}
					if requests[r.URL.Path] <= tt.completionRateLimits {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if tt.completionStatus != 0 {
						w.WriteHeader(tt.completionStatus)
						return
					}
					writeToolTestJSON(w, map[string]any{"ok": true})
				default:
					t.Fatalf("unexpected integration provider path %s", r.URL.Path)
				}
			}))
			defer server.Close()

			executor := Executor{
				Store:                 fixture.Store,
				IntegrationHTTPClient: integrationProviderTestClient(server),
				Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
			}
			if tt.loseAfterPath != "" {
				target, err := executor.currentIntegrationToolTarget(ctx, fixture.turn())
				if err != nil {
					t.Fatalf("resolve integration target: %v", err)
				}
				slackTarget, err := slackMessageTarget(target)
				if err != nil {
					t.Fatalf("resolve Slack target: %v", err)
				}
				_, err = executor.dispatchIntegrationArtifactSend(
					ctx,
					fixture.turn(),
					slackTarget,
					integrationMessageRequest{Text: "here is the report", ArtifactID: artifactID},
				)
				if !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
					t.Fatalf("artifact send error = %v, want ErrRuntimeLockInactive", err)
				}
			} else {
				call := fixture.recordToolCall(
					t,
					ctx,
					"call_"+seed,
					"send_integration_message",
					`{"text":"here is the report","artifact_id":"`+artifactID+`"}`,
					fixture.Now.Add(20*time.Second),
				)
				result, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), call)
				if err != nil {
					t.Fatalf("dispatch artifact send: %v", err)
				}
				body := integrationToolResultFromTestParts(t, result.ContentParts)
				if body.Code != tt.wantCode || body.ProviderMessageID != "" || body.TargetRef != fixture.Target.TargetRef {
					t.Fatalf("artifact send result = %+v", body)
				}
			}
			wantRequests := map[string]int{
				"/files.getUploadURLExternal":   1 + tt.uploadURLFailures,
				"/upload/v1/artifact":           tt.wantUploadRequests,
				"/files.completeUploadExternal": tt.wantCompletionRequests,
			}
			for path, want := range wantRequests {
				if requests[path] != want {
					t.Fatalf("requests[%q] = %d, want %d", path, requests[path], want)
				}
			}
		})
	}
}

func TestPostIntegrationRuntimeMessageUsesCurrentTarget(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "runtime-message")
	agentPublicID, err := publicid.Encode(publicid.KindAgent, fixture.Agent.ID)
	if err != nil {
		t.Fatalf("encode agent id: %v", err)
	}
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider path %s", r.URL.Path)
		}
		postCount++
		var payload struct {
			Channel  string `json:"channel"`
			ThreadTS string `json:"thread_ts"`
			Text     string `json:"text"`
			Metadata struct {
				EventType    string            `json:"event_type"`
				EventPayload map[string]string `json:"event_payload"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode runtime message payload: %v", err)
		}
		if payload.Channel != "C123" || payload.ThreadTS != "111.222" ||
			payload.Text != "I couldn't complete this request: model unavailable" {
			t.Fatalf("unexpected runtime message payload: %+v", payload)
		}
		if payload.Metadata.EventType != slack.MessageMarkerEventType ||
			payload.Metadata.EventPayload["agent_id"] != agentPublicID ||
			payload.Metadata.EventPayload["provider_call_id"] != "runtime_error:"+fixture.Lock.ID.String() ||
			payload.Metadata.EventPayload["target_ref"] != fixture.Target.TargetRef {
			t.Fatalf("unexpected runtime message metadata: %+v", payload.Metadata)
		}
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()

	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	if err := executor.PostIntegrationRuntimeMessage(
		ctx,
		fixture.turn(),
		"I couldn't complete this request: model unavailable",
	); err != nil {
		t.Fatalf("post runtime message: %v", err)
	}
	if postCount != 1 {
		t.Fatalf("post count = %d, want 1", postCount)
	}
}

func TestIntegrationSendToolRetriesShortRateLimit(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "send-rate-limit-retry")
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider path %s", r.URL.Path)
		}
		postCount++
		if postCount == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_send_rate_limit_retry",
		"send_integration_message",
		`{"text":"hello"}`,
		fixture.Now.Add(20*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch rate-limited send: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "delivered" || body.ProviderMessageID != "C123:222.333" {
		t.Fatalf("rate-limited send result = %+v", body)
	}
	if postCount != 2 {
		t.Fatalf("post count = %d, want 2", postCount)
	}
}

func TestIntegrationSendToolReadbackFailureAfterUnknownPost(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "readback-failure")
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.replies":
			w.WriteHeader(http.StatusInternalServerError)
		case "/chat.postMessage":
			postCount++
			writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123"})
		default:
			t.Fatalf("unexpected integration provider path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_readback_failure",
		"send_integration_message",
		`{"text":"hello"}`,
		fixture.Now.Add(20*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch readback-failing send: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "delivery_unknown" {
		t.Fatalf("readback-failing send result = %+v", body)
	}
	if postCount != 1 {
		t.Fatalf("post count = %d, want 1", postCount)
	}
}

func TestIntegrationSendToolTransientPostUsesReadback(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "transient-post-readback")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_transient_post",
		"send_integration_message",
		`{"text":"hello"}`,
		fixture.Now.Add(20*time.Second),
	)
	agentPublicID, err := publicid.Encode(publicid.KindAgent, fixture.Agent.ID)
	if err != nil {
		t.Fatalf("encode agent id: %v", err)
	}
	targetRef := fixture.Target.TargetRef
	postCount := 0
	readbackCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.replies":
			readbackCount++
			writeToolTestJSON(w, map[string]any{
				"ok": true,
				"messages": []map[string]any{{
					"channel": "C123",
					"ts":      "222.333",
					"metadata": map[string]any{
						"event_type": slack.MessageMarkerEventType,
						"event_payload": map[string]any{
							"agent_id":         agentPublicID,
							"provider_call_id": call.ID,
							"target_ref":       targetRef,
						},
					},
				}},
			})
		case "/chat.postMessage":
			postCount++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected integration provider path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch transient send: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "delivered" || body.ProviderMessageID != "C123:222.333" {
		t.Fatalf("transient send result = %+v", body)
	}
	if body.TargetRef != targetRef {
		t.Fatalf("target_ref = %q, want %q", body.TargetRef, targetRef)
	}
	if postCount != 1 || readbackCount != 1 {
		t.Fatalf("post/readback count = %d/%d, want 1/1", postCount, readbackCount)
	}
}

func TestIntegrationSendToolTokenRevokedReturnsIntegrationDisabled(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "token-revoked")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			writeToolTestJSON(w, map[string]any{"ok": false, "error": "token_revoked"})
		default:
			t.Fatalf("unexpected integration provider path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_token_revoked",
		"send_integration_message",
		`{"text":"hello"}`,
		fixture.Now.Add(20*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch token-revoked send: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "integration_disabled" {
		t.Fatalf("token-revoked result = %+v", body)
	}
}

func TestIntegrationSendToolUnknownPostRetriesAfterReadbackMiss(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "unknown-retry")
	postCount := 0
	readbackCount := 0
	var readbackForms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.replies":
			readbackCount++
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse readback form: %v", err)
			}
			readbackForms = append(readbackForms, r.Form)
			writeToolTestJSON(w, map[string]any{"ok": true, "messages": []map[string]any{}})
		case "/chat.postMessage":
			postCount++
			if postCount == 1 {
				writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123"})
				return
			}
			writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
		default:
			t.Fatalf("unexpected integration provider path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_send_unknown_retry",
		"send_integration_message",
		`{"text":"hello"}`,
		fixture.Now.Add(20*time.Second),
	)
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		fixture.toolCallID(t, ctx, call.ID),
	)
	if err != nil {
		t.Fatalf("get integration send tool call: %v", err)
	}
	wantOldest := strconv.FormatFloat(
		float64(toolCall.CreatedAt.Add(-time.Second).UnixNano())/float64(time.Second),
		'f',
		6,
		64,
	)
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch send: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "delivered" {
		t.Fatalf("send result = %+v", body)
	}
	if postCount != 2 {
		t.Fatalf("post count = %d, want 2", postCount)
	}
	if readbackCount != 1 {
		t.Fatalf("readback count = %d, want 1", readbackCount)
	}
	if len(readbackForms) != 1 || readbackForms[0].Get("oldest") != wantOldest {
		t.Fatalf("readback forms = %v, want oldest %q from tool call creation", readbackForms, wantOldest)
	}
}

func TestIntegrationSetTargetToolDispatchUpdatesTarget(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "set-target")
	secondTarget, err := fixture.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID:            toolsTestProjectID,
		AgentID:              fixture.Agent.ID,
		IntegrationInstallID: fixture.Install.ID,
		ProviderRef:          "D456",
		ProviderRefKind:      "dm",
	})
	if err != nil {
		t.Fatalf("create second target: %v", err)
	}
	targetRef := secondTarget.TargetRef

	calls := []model.ToolCall{
		{
			ID:    "call_set_target",
			Name:  "set_integration_target",
			Input: json.RawMessage(`{"target_ref":"` + targetRef + `"}`),
		},
		{
			ID:    "call_set_target_current",
			Name:  "set_integration_target",
			Input: json.RawMessage(`{"target_ref":"` + targetRef + `"}`),
		},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(21*time.Second))
	call := calls[0]
	executor := Executor{Store: fixture.Store, Now: func() time.Time { return fixture.Now.Add(22 * time.Second) }}
	result, err := executor.Dispatch(
		ctx,
		fixture.turn(),
		call,
	)
	if err != nil {
		t.Fatalf("dispatch set target: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "target_set" || body.TargetRef != targetRef ||
		body.Provider != integrationstore.IntegrationProviderSlack {
		t.Fatalf("set target result = %+v", body)
	}
	targets, err := fixture.Store.Integrations().ListIntegrationTargets(ctx, toolsTestProjectID, fixture.Agent.ID)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	for _, target := range targets {
		if target.ID == secondTarget.ID && !target.IsCurrent {
			t.Fatalf("second target was not made current: %+v", targets)
		}
	}

	executor = Executor{Store: fixture.Store, Now: func() time.Time { return fixture.Now.Add(23 * time.Second) }}
	result, err = executor.Dispatch(
		ctx,
		fixture.turn(),
		calls[1],
	)
	if err != nil {
		t.Fatalf("dispatch current set target: %v", err)
	}
	body = integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "target_set" || body.TargetRef != targetRef ||
		body.Provider != integrationstore.IntegrationProviderSlack {
		t.Fatalf("current set target result = %+v", body)
	}
}

func TestIntegrationSendToolDisabledAndMissingTargetFailures(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "disabled-target")
	if _, err := fixture.Store.Integrations().DisableIntegrationInstall(
		ctx,
		integrationstore.DisableIntegrationInstallInput{
			ProjectID:           toolsTestProjectID,
			ID:                  fixture.Install.ID,
			ExpectedOAuthFlowID: &fixture.Install.LastOAuthFlowID,
		},
	); err != nil {
		t.Fatalf("disable install: %v", err)
	}
	calls := []model.ToolCall{
		{
			ID:    "call_disabled",
			Name:  "send_integration_message",
			Input: json.RawMessage(`{"text":"hello"}`),
		},
		{
			ID:    "call_missing",
			Name:  "send_integration_message",
			Input: json.RawMessage(`{"text":"hello"}`),
		},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(21*time.Second))
	call := calls[0]
	executor := Executor{Store: fixture.Store, Now: func() time.Time { return fixture.Now.Add(22 * time.Second) }}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch disabled send: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "integration_disabled" {
		t.Fatalf("disabled send result = %+v", body)
	}

	if err := storagetest.SeedAgentIntegrationTarget(
		ctx,
		fixture.Pool,
		toolsTestProjectID,
		fixture.Agent.ID,
		storage.NilID,
	); err != nil {
		t.Fatalf("clear target: %v", err)
	}
	missing := calls[1]
	executor = Executor{Store: fixture.Store, Now: func() time.Time { return fixture.Now.Add(25 * time.Second) }}
	result, err = dispatchAsyncToolToTerminal(
		t,
		ctx,
		executor,
		fixture.turn(),
		missing,
	)
	if err != nil {
		t.Fatalf("dispatch missing-target send: %v", err)
	}
	body = integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "missing_integration_target" {
		t.Fatalf("missing target result = %+v", body)
	}
}

func TestIntegrationQuestionPromptDisabledTargetFallsBackToOmnara(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-disabled-target")
	if _, err := fixture.Store.Integrations().DisableIntegrationInstall(
		ctx,
		integrationstore.DisableIntegrationInstallInput{
			ProjectID:           toolsTestProjectID,
			ID:                  fixture.Install.ID,
			ExpectedOAuthFlowID: &fixture.Install.LastOAuthFlowID,
		},
	); err != nil {
		t.Fatalf("disable install: %v", err)
	}
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			postCount++
		}
		t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_question_disabled_target",
		"ask_question",
		`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("question disposition = %d, want deferred", result.Disposition)
	}
	if postCount != 0 {
		t.Fatalf("post count = %d, want 0", postCount)
	}
	toolCallID := fixture.toolCallID(t, ctx, "call_question_disabled_target")
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get question tool call: %v", err)
	}
	if toolCall.State != "waiting" {
		t.Fatalf("question tool call state = %q, want waiting", toolCall.State)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "question")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("question interaction = %+v, want open", interaction)
	}
}

func TestQuestionDispatchCommitsBeforePromptAndReleasesAsyncOwnership(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-transaction-async")
	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
		}
		postCount++
		requestStarted <- struct{}{}
		<-releaseResponse
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()
	responseReleased := false
	defer func() {
		if !responseReleased {
			close(releaseResponse)
		}
	}()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_question_transaction_async",
		"ask_question",
		`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(
		WithAsyncExecutionScope(ctx, scope),
		fixture.turn(),
		call,
	)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("question disposition = %d, want deferred", result.Disposition)
	}
	scope.Seal()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("question prompt delivery did not start")
	}

	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	var state string
	var ownsRuntime bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT state, runtime_lock_id = $2 FROM tool_calls WHERE id = $1`,
		toolCallID,
		fixture.Lock.ID,
	).Scan(&state, &ownsRuntime); err != nil {
		t.Fatalf("load question during async prompt delivery: %v", err)
	}
	if state != "running" || !ownsRuntime {
		t.Fatalf("question during prompt delivery state=%q owns_runtime=%v", state, ownsRuntime)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "question")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("question interaction = %+v, want open", interaction)
	}
	concurrentResult, err := executor.Dispatch(ctx, fixture.turn(), call)
	if err != nil {
		t.Fatalf("concurrent question dispatch: %v", err)
	}
	if concurrentResult.Disposition != DispatchDeferred {
		t.Fatalf("concurrent question dispatch result = %+v, want deferred", concurrentResult)
	}

	responseReleased = true
	close(releaseResponse)
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("question async prompt delivery did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("question async prompt delivery: %v", err)
	}
	var released bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT runtime_lock_id IS NULL FROM tool_calls WHERE id = $1`,
		toolCallID,
	).Scan(&released); err != nil {
		t.Fatalf("load question after async prompt delivery: %v", err)
	}
	if !released {
		t.Fatal("question retained runtime ownership after async prompt delivery")
	}
	releasedResult, err := executor.Dispatch(ctx, fixture.turn(), call)
	if err != nil {
		t.Fatalf("released question dispatch: %v", err)
	}
	if releasedResult.Disposition != DispatchDeferred {
		t.Fatalf("released question dispatch result = %+v, want deferred", releasedResult)
	}
	if postCount != 1 {
		t.Fatalf("post count after released dispatch = %d, want 1", postCount)
	}
}

func TestIntegrationQuestionPromptRetriesShortRateLimit(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-rate-limit-retry")
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
		}
		postCount++
		if postCount == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_question_rate_limit_retry",
		"ask_question",
		`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("question disposition = %d, want deferred", result.Disposition)
	}
	if postCount != 2 {
		t.Fatalf("post count = %d, want 2", postCount)
	}
	interaction := integrationToolInteraction(
		t,
		ctx,
		fixture,
		fixture.toolCallID(t, ctx, "call_question_rate_limit_retry"),
		"question",
	)
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("question interaction = %+v, want open", interaction)
	}
}

func TestIntegrationQuestionPromptUnknownPostUsesReadback(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-unknown-readback")
	var mu sync.Mutex
	var blocks any
	postCount := 0
	readbackCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode question prompt payload: %v", err)
			}
			mu.Lock()
			postCount++
			blocks = payload["blocks"]
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			writeToolTestJSON(w, map[string]any{"ok": false})
		case "/conversations.replies":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse prompt readback form: %v", err)
			}
			if r.Form.Get("oldest") != "" || r.Form.Get("limit") != "100" {
				t.Fatalf("prompt readback bounds = %+v", r.Form)
			}
			mu.Lock()
			readbackCount++
			readback := readbackCount
			promptBlocks := blocks
			mu.Unlock()
			if readback == 1 {
				if r.Form.Get("cursor") != "" {
					t.Fatalf("first prompt readback cursor = %q", r.Form.Get("cursor"))
				}
				writeToolTestJSON(w, map[string]any{
					"ok": true,
					"messages": []map[string]any{
						{"ts": "111.111"},
						{"ts": "222.222"},
					},
					"response_metadata": map[string]any{"next_cursor": "page-2"},
				})
				return
			}
			if r.Form.Get("cursor") != "page-2" {
				t.Fatalf("second prompt readback cursor = %q", r.Form.Get("cursor"))
			}
			writeToolTestJSON(w, map[string]any{
				"ok": true,
				"messages": []map[string]any{{
					"ts":     "333.333",
					"blocks": promptBlocks,
				}},
			})
		default:
			t.Fatalf("unexpected integration provider request to %s", r.URL.Path)
		}
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_question_unknown_readback",
		"ask_question",
		`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatal("question dispatch was not deferred after readback")
	}
	mu.Lock()
	posts := postCount
	readbacks := readbackCount
	mu.Unlock()
	if posts != 1 || readbacks != 2 {
		t.Fatalf("prompt posts/readbacks = %d/%d, want 1/2", posts, readbacks)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get question tool call after readback: %v", err)
	}
	if toolCall.State != "waiting" {
		t.Fatalf("question tool call after readback = %+v", toolCall)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "question")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("question interaction after readback = %+v", interaction)
	}
}

func TestIntegrationQuestionPromptUnknownOutcomeFailsTool(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-delivery-unknown")
	postCount := 0
	readbackCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			postCount++
			w.WriteHeader(http.StatusInternalServerError)
			writeToolTestJSON(w, map[string]any{"ok": false})
		case "/conversations.replies":
			readbackCount++
			writeToolTestJSON(w, map[string]any{"ok": true, "messages": []any{}})
		default:
			t.Fatalf("unexpected integration provider request to %s", r.URL.Path)
		}
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_question_delivery_unknown",
		"ask_question",
		`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatal("question did not begin async prompt delivery")
	}
	if postCount != integrationMessageSendAttempts || readbackCount != integrationMessageSendAttempts {
		t.Fatalf(
			"prompt posts/readbacks = %d/%d, want %d/%d",
			postCount,
			readbackCount,
			integrationMessageSendAttempts,
			integrationMessageSendAttempts,
		)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get delivery-unknown question tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted ||
		toolCall.Outcome != executionstore.ToolResultOutcomeFailed ||
		!strings.Contains(string(toolCall.ResultContentParts), "delivery outcome is unknown") {
		t.Fatalf("delivery-unknown question tool = %+v, want failed execution", toolCall)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "question")
	if interaction.State != executionstore.AgentInteractionStateCanceled {
		t.Fatalf("delivery-unknown question interaction = %+v, want canceled", interaction)
	}
}

func TestIntegrationQuestionPromptDeliveryFailureFailsTool(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-delivery-failure")
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
		}
		postCount++
		writeToolTestJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer server.Close()

	call := fixture.recordToolCall(
		t,
		ctx,
		"call_question_delivery_failure",
		"ask_question",
		`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatal("question did not begin async prompt delivery")
	}
	if postCount != 1 {
		t.Fatalf("post count = %d, want 1", postCount)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get failed question tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted ||
		toolCall.Outcome != executionstore.ToolResultOutcomeFailed ||
		!strings.Contains(string(toolCall.ResultContentParts), "channel_not_found") {
		t.Fatalf("failed-delivery question tool = %+v, want failed execution", toolCall)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "question")
	if interaction.State != executionstore.AgentInteractionStateCanceled {
		t.Fatalf("failed question interaction = %+v, want canceled", interaction)
	}
}

func TestIntegrationSetTargetPermissionUsesResolvedAuthorizationInput(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "set-target-permission")
	currentTarget, err := fixture.Store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            toolsTestProjectID,
			AgentID:              fixture.Agent.ID,
			IntegrationInstallID: fixture.Install.ID,
			ProviderRef:          "D456",
			ProviderRefKind:      "dm",
		},
	)
	if err != nil {
		t.Fatalf("create current integration target: %v", err)
	}
	if err := storagetest.SeedAgentIntegrationTarget(
		ctx,
		fixture.Pool,
		toolsTestProjectID,
		fixture.Agent.ID,
		currentTarget.ID,
	); err != nil {
		t.Fatalf("set current integration target: %v", err)
	}
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
		}
		postCount++
		var payload struct {
			Channel string `json:"channel"`
			Text    string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode set-target permission prompt: %v", err)
		}
		if payload.Channel != "D456" {
			t.Fatalf("permission prompt channel = %q, want current target D456", payload.Channel)
		}
		if !strings.Contains(payload.Text, "set_integration_target") ||
			!strings.Contains(payload.Text, fixture.Target.TargetRef) {
			t.Fatalf("set-target permission prompt text = %q", payload.Text)
		}
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "D456", "ts": "222.333"})
	}))
	defer server.Close()

	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"set_integration_target": {
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		},
	}
	call := fixture.recordPendingToolCall(
		t,
		ctx,
		"call_set_target_permission",
		"set_integration_target",
		`{"target_ref":"  `+strings.ToUpper(fixture.Target.TargetRef)+`  "}`,
		fixture.Now.Add(22*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		BackgroundRunner:      immediateIntegrationBackgroundRunner(ctx),
		Now:                   func() time.Time { return fixture.Now.Add(23 * time.Second) },
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, call); err != nil {
		t.Fatalf("prepare set-target permission: %v", err)
	}
	if postCount != 1 {
		t.Fatalf("set-target permission post count = %d, want 1", postCount)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "permission")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("set-target permission interaction = %+v, want open", interaction)
	}
	permissionRequest, err := toolpermission.ParseRequest(interaction.Request)
	if err != nil {
		t.Fatalf("parse set-target permission request: %v", err)
	}
	wantAuthorizationInput := `{"target_ref":"` + fixture.Target.TargetRef + `"}`
	if string(permissionRequest.Authorization.Input) != wantAuthorizationInput {
		t.Fatalf(
			"set-target authorization input = %s, want %s",
			permissionRequest.Authorization.Input,
			wantAuthorizationInput,
		)
	}
	resolution := interactionform.Resolution{
		Answers: []interactionform.Answer{{
			OptionIndices: []int{toolpermission.AllowOptionIndex},
		}},
	}
	actor, err := executionstore.OmnaraActorParams(
		toolsTestOrgID,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.User.ID},
	)
	if err != nil {
		t.Fatalf("build set-target permission actor: %v", err)
	}
	if _, err := fixture.Store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID:  toolsTestProjectID,
			AgentID:    fixture.Agent.ID,
			ID:         interaction.ID,
			Resolution: resolution,
			Actor:      actor,
		},
	); err != nil {
		t.Fatalf("approve set-target permission: %v", err)
	}
	result, err := executor.Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatalf("dispatch approved set-target call: %v", err)
	}
	body := integrationToolResultFromTestParts(t, result.ContentParts)
	if body.Code != "target_set" || body.TargetRef != fixture.Target.TargetRef {
		t.Fatalf("approved set-target result = %+v", body)
	}
	targets, err := fixture.Store.Integrations().ListIntegrationTargets(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
	)
	if err != nil {
		t.Fatalf("list targets after approved set-target call: %v", err)
	}
	for _, target := range targets {
		if target.ID == fixture.Target.ID && !target.IsCurrent {
			t.Fatalf("approved target was not made current: %+v", targets)
		}
	}
}

func TestIntegrationPermissionPromptDisabledTargetFallsBackToOmnara(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "permission-disabled-target")
	if _, err := fixture.Store.Integrations().DisableIntegrationInstall(
		ctx,
		integrationstore.DisableIntegrationInstallInput{
			ProjectID:           toolsTestProjectID,
			ID:                  fixture.Install.ID,
			ExpectedOAuthFlowID: &fixture.Install.LastOAuthFlowID,
		},
	); err != nil {
		t.Fatalf("disable install: %v", err)
	}
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			postCount++
		}
		t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
	}))
	defer server.Close()

	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"list_processes": {
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		},
	}
	call := fixture.recordPendingToolCall(
		t,
		ctx,
		"call_permission_disabled_target",
		"list_processes",
		`{}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		BackgroundRunner:      immediateIntegrationBackgroundRunner(ctx),
		Now:                   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, call); err != nil {
		t.Fatalf("prepare permission: %v", err)
	}
	if postCount != 0 {
		t.Fatalf("post count = %d, want 0", postCount)
	}
	toolCallID := fixture.toolCallID(t, ctx, "call_permission_disabled_target")
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get permission tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateAwaitingPermission {
		t.Fatalf("permission tool call = %+v, want awaiting permission", toolCall)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "permission")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("permission interaction = %+v, want open", interaction)
	}
}

func TestIntegrationPermissionPromptDeliveryDoesNotBlockOmnaraPrompt(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "permission-delivery-failure")
	runner, err := NewBackgroundExecutionRunner(ctx, nil, 1)
	if err != nil {
		t.Fatalf("new background runner: %v", err)
	}
	defer runner.Shutdown()
	postStarted := make(chan struct{})
	postFinished := make(chan struct{})
	releasePost := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releasePost) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
		}
		close(postStarted)
		<-releasePost
		writeToolTestJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
		close(postFinished)
	}))
	defer server.Close()
	defer release()

	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"list_processes": {
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		},
	}
	call := fixture.recordPendingToolCall(
		t,
		ctx,
		"call_permission_delivery_failure",
		"list_processes",
		`{}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		BackgroundRunner:      runner,
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	prepareDone := make(chan error, 1)
	go func() {
		prepareDone <- executor.PrepareToolCallPermission(ctx, turn, call)
	}()
	select {
	case <-postStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("integration permission prompt copy did not start")
	}
	select {
	case err := <-prepareDone:
		if err != nil {
			t.Fatalf("prepare permission: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("permission preparation waited for the integration copy")
	}
	toolCallID := fixture.toolCallID(t, ctx, "call_permission_delivery_failure")
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get permission tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateAwaitingPermission {
		t.Fatalf("permission tool call = %+v, want awaiting permission", toolCall)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "permission")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("permission interaction = %+v, want open", interaction)
	}
	release()
	select {
	case <-postFinished:
	case <-time.After(time.Second):
		t.Fatal("integration permission prompt copy did not finish")
	}
}

func TestIntegrationExistingPermissionPromptDoesNotRedeliver(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "permission-existing-prompt")
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected integration provider post to %s", r.URL.Path)
		}
		postCount++
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()

	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"list_processes": {
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		},
	}
	call := fixture.recordPendingToolCall(
		t,
		ctx,
		"call_permission_existing_prompt",
		"list_processes",
		`{}`,
		fixture.Now.Add(21*time.Second),
	)
	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
		BackgroundRunner:      immediateIntegrationBackgroundRunner(ctx),
		Now:                   func() time.Time { return fixture.Now.Add(22 * time.Second) },
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, call); err != nil {
		t.Fatalf("prepare permission: %v", err)
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, call); err != nil {
		t.Fatalf("prepare existing permission: %v", err)
	}
	if postCount != 1 {
		t.Fatalf("post count = %d, want 1", postCount)
	}
	if _, err := fixture.Pool.Exec(
		ctx,
		`UPDATE agent_runtime_locks
SET started_at = statement_timestamp() - interval '3 minutes',
    renewed_at = statement_timestamp() - interval '2 minutes',
    lease_expires_at = statement_timestamp() - interval '1 minute'
WHERE id = $1`,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("expire runtime lock: %v", err)
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, call); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("existing permission with inactive runtime error = %v, want runtime lock inactive", err)
	}
	if postCount != 1 {
		t.Fatalf("post count after inactive check = %d, want 1", postCount)
	}
}

func newIntegrationToolFixture(t *testing.T, ctx context.Context, label string) integrationToolFixture {
	return newIntegrationToolFixtureWithMCP(t, ctx, label, false)
}

func newIntegrationToolFixtureWithMCP(
	t *testing.T,
	ctx context.Context,
	label string,
	withMCP bool,
	storeOptions ...storage.Option,
) integrationToolFixture {
	t.Helper()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	options := []storage.Option{
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
		storage.WithMachinePoolProviders(toolsTestMachinePoolProviders{}),
	}
	options = append(options, storeOptions...)
	store := storage.NewStore(pool, options...)
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "tools-integration-" + label + "@example.com",
			DisplayName: "Tools Integration " + label,
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`
INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
VALUES ($1, 'Tools Integration Org', $2, $3, $3)
`,
		toolsTestOrgID,
		"tools-integration-org-"+label,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Tools Integration Project', $3, $4, $4)
`,
		toolsTestProjectID,
		toolsTestOrgID,
		"tools-integration-project-"+label,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ensureIntegrationToolsProjectAdmin(t, ctx, store, user.ID, now)

	profile := createIntegrationToolProfile(t, ctx, store, user.ID, label, now.Add(time.Second), withMCP)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      toolsTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     toolsTestUserPrincipal(user.ID),
			IdempotencyKey: "tools-integration-launch-" + label,
		},
	)
	if err != nil {
		t.Fatalf("launch integration tool agent: %v", err)
	}
	agent := launch.Agent
	install := createIntegrationToolInstall(t, ctx, store, profile.ID, user.ID, label, now.Add(3*time.Second))
	target, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            toolsTestProjectID,
			AgentID:              agent.ID,
			IntegrationInstallID: install.ID,
			ProviderRef:          "C123:111.222",
			ProviderRefKind:      "thread",
		},
	)
	if err != nil {
		t.Fatalf("create integration target: %v", err)
	}
	if err := storagetest.SeedAgentIntegrationTarget(
		ctx,
		pool,
		toolsTestProjectID,
		agent.ID,
		target.ID,
	); err != nil {
		t.Fatalf("set integration target: %v", err)
	}
	producer, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(user.ID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      toolsTestProjectID,
			AgentID:        agent.ID,
			Actor:          producer,
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"send an integration reply"}]`),
			IdempotencyKey: "tools-integration-input-" + label,
		},
	)
	if err != nil {
		t.Fatalf("create agent input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || len(claim.Model.AdmittedInputTurn.Inputs) != 1 ||
		claim.Model.AdmittedInputTurn.Inputs[0].ID != input.ID {
		t.Fatalf(
			"claim input found=%v executable=%v input=%+v want %s",
			found,
			claim.Kind == executionstore.AgentWorkModel,
			claim.Model.AdmittedInputTurn.Inputs,
			input.ID,
		)
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	modelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		store,
		toolsTestProjectID,
		agent.ID,
		lock,
		[]storage.ID{input.ID},
		launch.AgentConfig.ID,
		admitted.Events[0].Sequence,
		storage.NilID,
	)
	return integrationToolFixture{
		Pool:               pool,
		Store:              store,
		User:               user,
		Profile:            profile,
		Agent:              agent,
		AgentConfig:        launch.AgentConfig,
		Lock:               lock,
		ModelCallContextID: modelCall.Context.ID,
		Install:            install,
		Target:             target,
		Now:                now,
		WithMCP:            withMCP,
	}
}

func (f integrationToolFixture) turn() Turn {
	turn := Turn{
		ProjectID:          toolsTestProjectID,
		AgentID:            f.Agent.ID,
		SourceEventID:      f.ModelOutputEventID,
		RuntimeLockID:      f.Lock.ID,
		ModelCallContextID: f.ModelCallContextID,
		Tools: map[string]ToolSpec{
			"ask_question": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
			"send_integration_message": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
			"set_integration_target": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
		},
	}
	if f.WithMCP {
		turn.Tools[toolcatalog.MCPRuntimeToolName("docs", "greet")] = ToolSpec{
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			Type:       toolcatalog.ToolTypeMCP,
		}
	}
	return turn
}

func (f *integrationToolFixture) recordToolCall(
	t *testing.T,
	ctx context.Context,
	providerCallID, name, rawInput string,
	at time.Time,
) model.ToolCall {
	t.Helper()
	call := model.ToolCall{ID: providerCallID, Name: name, Input: json.RawMessage(rawInput)}
	f.recordToolCalls(t, ctx, []model.ToolCall{call}, at)
	return call
}

func (f *integrationToolFixture) recordPendingToolCall(
	t *testing.T,
	ctx context.Context,
	providerCallID, name, rawInput string,
	at time.Time,
) model.ToolCall {
	t.Helper()
	call := model.ToolCall{ID: providerCallID, Name: name, Input: json.RawMessage(rawInput)}
	f.recordPendingToolCalls(t, ctx, []model.ToolCall{call}, at)
	return call
}

func (f *integrationToolFixture) recordToolCalls(
	t *testing.T,
	ctx context.Context,
	calls []model.ToolCall,
	at time.Time,
) {
	t.Helper()
	f.recordPendingToolCalls(t, ctx, calls, at)
	for _, call := range calls {
		if _, err := f.Store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     toolsTestProjectID,
				AgentID:       f.Agent.ID,
				ID:            f.toolCallID(t, ctx, call.ID),
				RuntimeLockID: f.Lock.ID,
			},
		); err != nil {
			t.Fatalf("mark tool call %s allowed: %v", call.ID, err)
		}
	}
}

func (f *integrationToolFixture) recordPendingToolCalls(
	t *testing.T,
	ctx context.Context,
	calls []model.ToolCall,
	at time.Time,
) {
	t.Helper()
	if f.ModelOutputEventID != storage.NilID {
		t.Fatal("integration tool fixture already published its complete tool proposal batch")
	}
	if len(calls) == 0 {
		t.Fatal("integration tool fixture requires at least one tool proposal")
	}
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(calls))
	for _, call := range calls {
		toolType := toolcatalog.ToolTypeBuiltIn
		if toolcatalog.IsMCPRuntimeToolName(call.Name) {
			toolType = toolcatalog.ToolTypeMCP
		}
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ProviderCallID: call.ID,
			Type:           toolType,
		})
	}
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         "resp_tools_integration_" + f.ModelCallContextID.String(),
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls(calls),
		},
	)
	if err != nil {
		t.Fatalf("build integration tool provider response: %v", err)
	}
	modelOutputEvent, records, err := f.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          toolsTestProjectID,
			AgentID:            f.Agent.ID,
			ModelCallContextID: f.ModelCallContextID,
			RuntimeLockID:      f.Lock.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings:   bindings,
		},
	)
	if err != nil {
		t.Fatalf("record integration tool proposal batch: %v", err)
	}
	if len(records) != len(calls) {
		t.Fatalf("recorded integration tool calls = %d, want %d", len(records), len(calls))
	}
	f.ModelOutputEventID = modelOutputEvent.ID
}

func (f integrationToolFixture) toolCallID(t *testing.T, ctx context.Context, providerCallID string) storage.ID {
	t.Helper()
	record, found, err := f.Store.Execution().GetToolCallByProviderCall(
		ctx,
		toolsTestProjectID,
		f.Agent.ID,
		f.ModelCallContextID,
		providerCallID,
	)
	if err != nil {
		t.Fatalf("get tool call %s: %v", providerCallID, err)
	}
	if !found {
		t.Fatalf("tool call %s not found", providerCallID)
	}
	return record.ID
}

func createIntegrationToolProfile(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	label string,
	now time.Time,
	withMCP bool,
) executionstore.AgentProfileRecord {
	t.Helper()
	sourceYAML := `instruction: Reply to users.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
  send_integration_message: {}
  set_integration_target: {}
`
	if withMCP {
		sourceYAML += `mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
      parameters: {}
`
	}
	compiled := compileToolsAgentYAMLResolved(t, ctx, store, userID, sourceYAML, now)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               toolsTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create integration tool config: %v", err)
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       toolsTestProjectID,
		Name:            "Integration Tool Agent " + label,
		CurrentConfigID: config.ID,
		IdempotencyKey:  "tools-integration-profile-" + label,
	})
	if err != nil {
		t.Fatalf("create integration tool profile: %v", err)
	}
	return profile
}

func compileToolsAgentYAMLResolved(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	sourceYAML string,
	now time.Time,
) agentconfig.Result {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse agent config source: %v", err)
	}
	configuredModel := ensureToolsModelSelection(
		t,
		ctx,
		store,
		userID,
		source.Model.ProviderConfig,
		source.Model.Name,
		now,
	)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(
			providerConfigName string,
			configuredModelName string,
		) (agentconfig.ResolvedModelSelection, error) {
			return resolvedToolsAgentConfigModel(configuredModel), nil
		},
		ResolveMachineName: func(machineName string) (string, error) {
			machineID, err := store.Execution().ResolveAgentConfigMachineName(ctx, toolsTestProjectID, machineName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachine, machineID)
		},
		ResolveMachinePoolName: func(machinePoolName string) (string, error) {
			machinePoolID, err := store.Execution().ResolveAgentConfigMachinePoolName(
				ctx,
				toolsTestOrgID,
				toolsTestProjectID,
				machinePoolName,
			)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachinePool, machinePoolID)
		},
		ResolveSkillID: func(skillID string) (agentconfig.SkillResolution, error) {
			records, _, err := store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
				OrgID:     toolsTestOrgID,
				ProjectID: toolsTestProjectID,
				IDs:       []string{skillID},
			})
			if err != nil {
				return agentconfig.SkillResolution{}, err
			}
			if len(records) != 1 {
				return agentconfig.SkillResolution{}, storeerr.ErrNotFound
			}
			return agentconfig.SkillResolution{
				PublicID: skillID,
				Name:     records[0].Name,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent config: %v", err)
	}
	return compiled
}

func resolvedToolsAgentConfigModel(configuredModel modelstore.ConfiguredModelRecord) agentconfig.ResolvedModelSelection {
	supportsTools := configuredModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID.String(),
		SupportsTools:     &supportsTools,
	}
}

func ensureToolsModelSelection(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	providerConfigName, configuredModelName string,
	now time.Time,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	providerConfig, err := store.Models().GetModelProviderConfigByName(ctx, toolsTestOrgID, providerConfigName)
	if err != nil {
		if !storeerr.IsNotFound(err) {
			t.Fatalf("load model provider config %q: %v", providerConfigName, err)
		}
		secret, err := ensureToolsProviderCredential(t, ctx, store, userID, providerConfigName)
		if err != nil {
			t.Fatalf("ensure provider credential: %v", err)
		}
		providerConfig, err = store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
			OrgID:              toolsTestOrgID,
			Name:               providerConfigName,
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			APIVariant:         "default",
			BaseURL:            "https://api.openai.com/v1",
			CredentialSecretID: secret.ID,
		})
		if err != nil {
			t.Fatalf("create model provider config %q: %v", providerConfigName, err)
		}
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 toolsTestOrgID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  configuredModelName,
		ProviderModelSlug:     configuredModelName,
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             toolsTestOrgID,
		ProjectID:         toolsTestProjectID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	return configuredModel
}

func ensureToolsProviderCredential(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	providerConfigName string,
) (secretstore.SecretRecord, error) {
	t.Helper()
	name := "tools-provider-" + providerConfigName
	secret, err := store.Secrets().GetSecretByOwnerName(
		ctx,
		toolsTestOrgID,
		secretstore.SecretOwnerOrg,
		storage.NilID,
		storage.NilID,
		name,
	)
	if err == nil {
		return secret, nil
	}
	if !storeerr.IsNotFound(err) {
		return secretstore.SecretRecord{}, err
	}
	secret, _, err = store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     toolsTestOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      name,
		Material:  secrets.GenericMaterial{Value: "test-key"},
		Actor:     toolsTestUserPrincipal(userID),
	})
	return secret, err
}

func intPtrForToolsTest(value int) *int {
	return &value
}

func parseConfiguredModelID(t *testing.T, compiled agentconfig.Result) storage.ID {
	t.Helper()
	id, err := storage.ParseID(compiled.Compiled.Model.ConfiguredModelID)
	if err != nil {
		t.Fatalf("parse compiled configured model id: %v", err)
	}
	return id
}

func createIntegrationToolInstall(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentProfileID, userID storage.ID,
	label string,
	now time.Time,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationToolInstallInput(
			agentProfileID,
			userID,
			createIntegrationToolSecrets(t, ctx, store, userID, label, now),
			now,
		),
	)
	if err != nil {
		t.Fatalf("create integration install: %v", err)
	}
	return install
}

func createIntegrationToolSecrets(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	label string,
	now time.Time,
) storage.ID {
	t.Helper()
	secret, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:          toolsTestOrgID,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: toolsTestProjectID,
			Name:           "tools-integration-" + label + "-credentials",
			Material: secrets.SlackAppCredentialsMaterial{
				AccessToken:   "xoxb-test",
				ClientID:      "client-id",
				ClientSecret:  "client-secret",
				SigningSecret: "signing-secret",
			},
			Actor: toolsTestUserPrincipal(userID),
		},
	)
	if err != nil {
		t.Fatalf("create integration credential secret: %v", err)
	}
	return secret.ID
}

func integrationToolInstallInput(
	agentProfileID, userID storage.ID,
	credentialSecretID storage.ID,
	now time.Time,
) integrationstore.UpsertIntegrationInstallInput {
	return integrationstore.UpsertIntegrationInstallInput{
		OrgID:              toolsTestOrgID,
		ProjectID:          toolsTestProjectID,
		AgentProfileID:     agentProfileID,
		InstalledByUserID:  userID,
		Provider:           integrationstore.IntegrationProviderSlack,
		IntegrationKind:    slack.IntegrationKindAgentProfile,
		ConnectionMode:     slack.ConnectionModeWebhook,
		State:              integrationstore.IntegrationInstallStateActive,
		ProviderTenantID:   "T123",
		ProviderAccountRef: "A123",
		CredentialSecretID: credentialSecretID,
		ProviderIdentity:   json.RawMessage(`{"bot_user_id":"B123"}`),
		ProviderMetadata:   json.RawMessage(`{}`),
	}
}

func integrationToolKeyWrapper(t *testing.T) secrets.KeyWrapper {
	t.Helper()
	wrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create test key wrapper: %v", err)
	}
	return wrapper
}

func integrationToolMustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func dispatchToolAndDrainAsync(
	t *testing.T,
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
) (Result, error) {
	t.Helper()
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), turn, call)
	scope.Seal()
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async tool dispatch")
	}
	if err != nil {
		return result, err
	}
	return result, scope.Err()
}

func dispatchAsyncToolToTerminal(
	t *testing.T,
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
) (Result, error) {
	t.Helper()
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, turn, call)
	if err != nil || result.Disposition != DispatchDeferred {
		return result, err
	}
	return executor.Dispatch(ctx, turn, call)
}

func integrationToolResultFromTestParts(t *testing.T, parts json.RawMessage) integrationToolResult {
	t.Helper()
	var decoded []struct {
		Type  string                `json:"type"`
		Value integrationToolResult `json:"value"`
	}
	if err := json.Unmarshal(parts, &decoded); err != nil {
		t.Fatalf("decode result parts: %v raw=%s", err, parts)
	}
	for _, part := range decoded {
		if part.Type == "structured_data" {
			return part.Value
		}
	}
	t.Fatalf("missing structured result in %s", parts)
	return integrationToolResult{}
}

func toolResultMapFromTestParts(t *testing.T, parts json.RawMessage) map[string]any {
	t.Helper()
	var decoded []struct {
		Type  string         `json:"type"`
		Value map[string]any `json:"value"`
	}
	if err := json.Unmarshal(parts, &decoded); err != nil {
		t.Fatalf("decode result parts: %v raw=%s", err, parts)
	}
	for _, part := range decoded {
		if part.Type == "structured_data" {
			return part.Value
		}
	}
	t.Fatalf("missing structured result in %s", parts)
	return nil
}

func ensureIntegrationToolsProjectAdmin(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	now time.Time,
) {
	t.Helper()
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: toolsTestOrgID, UserID: userID, Role: "admin"},
	); err != nil {
		t.Fatalf("add integration tools org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     toolsTestOrgID,
			ProjectID: toolsTestProjectID,
			UserID:    userID,
			Role:      "admin",
		},
	); err != nil {
		t.Fatalf("add integration tools project membership: %v", err)
	}
}
