//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type integrationToolFixture struct {
	Pool               *pgxpool.Pool
	Store              *storage.Store
	User               identitystore.UserRecord
	Profile            executionstore.AgentProfileRecord
	Agent              executionstore.AgentRecord
	AgentConfig        executionstore.AgentConfigRecord
	Lock               executionstore.AgentRuntimeLockRecord
	ModelCallContextID uuid.UUID
	ModelOutputEventID uuid.UUID
	Install            integrationstore.ProjectIntegrationRecord
	Target             integrationstore.IntegrationTargetRecord
	Now                time.Time
	WithMCP            bool
	IntegrationTools   map[string]ToolSpec
}

func toolsTestUserPrincipal(userID uuid.UUID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

func approveToolPermissionForTest(
	t *testing.T,
	ctx context.Context,
	store *executionstore.Store,
	interaction executionstore.AgentInteractionRecord,
	userID uuid.UUID,
) {
	t.Helper()
	actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(userID))
	require.NoError(t, err)
	_, err = store.ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: interaction.ProjectID, AgentID: interaction.AgentID, ID: interaction.ID, Actor: actor,
		Resolution: interactionform.Resolution{
			Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
		},
	})
	require.NoError(t, err)
}

func integrationToolInteraction(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	toolCallID uuid.UUID,
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

func TestPostIntegrationRuntimeMessageUsesSelectedHandler(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "runtime-message")
	prepareInteractionPromptFixture(t, ctx, fixture)
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected integration provider path %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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
			t.Errorf("decode runtime message payload: %v", err)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		if payload.Channel != "C123" || payload.ThreadTS != "111.222" ||
			payload.Text != "I couldn't complete this request: model unavailable" {
			t.Errorf("unexpected runtime message payload: %+v", payload)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()

	executor := Executor{
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
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
	activateInteractionToolHandlers(t, ctx, fixture)
	require.NoError(t, executor.PostIntegrationRuntimeMessage(ctx, fixture.turn(), "dashboard only"))
	require.Equal(t, 1, postCount)

}

func TestIntegrationQuestionPromptDisabledTargetFallsBackToOmnara(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-disabled-target")
	prepareInteractionPromptFixture(t, ctx, fixture)
	if _, err := fixture.Store.Integrations().DisconnectProjectIntegration(
		ctx,
		integrationstore.DisconnectProjectIntegrationInput{
			ProjectID:             toolsTestProjectID,
			IntegrationID:         fixture.Install.ID,
			ExpectedSetupRevision: &fixture.Install.SetupRevision,
		},
	); err != nil {
		t.Fatalf("disable install: %v", err)
	}
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path == "/chat.postMessage" {
			postCount++
		}
		t.Errorf("unexpected integration provider post to %s", r.URL.Path)
		http.Error(w, "test handler failed", http.StatusInternalServerError)
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

func TestQuestionDispatchCommitsDurableWaitBeforePrompt(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-tx-background")
	prepareInteractionPromptFixture(t, ctx, fixture)
	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected integration provider post to %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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
		"call_question_transaction_background",
		"ask_question",
		`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		fixture.Now.Add(21*time.Second),
	)
	runner, waitPresentation := newQuestionPresentationRunner(t, ctx)
	executor := Executor{
		BackgroundRunner:      runner,
		Store:                 fixture.Store,
		IntegrationHTTPClient: integrationProviderTestClient(server),
	}
	result, err := executor.Dispatch(
		ctx,
		fixture.turn(),
		call,
	)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("question disposition = %d, want deferred", result.Disposition)
	}
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
		`SELECT state, coalesce(runtime_lock_id = $2, false) FROM tool_calls WHERE id = $1`,
		toolCallID,
		fixture.Lock.ID,
	).Scan(&state, &ownsRuntime); err != nil {
		t.Fatalf("load question during background prompt delivery: %v", err)
	}
	if state != "waiting" || ownsRuntime {
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
	require.NoError(t, waitPresentation())
	var released bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT runtime_lock_id IS NULL FROM tool_calls WHERE id = $1`,
		toolCallID,
	).Scan(&released); err != nil {
		t.Fatalf("load question after background prompt delivery: %v", err)
	}
	if !released {
		t.Fatal("question retained runtime ownership after background prompt delivery")
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
	prepareInteractionPromptFixture(t, ctx, fixture)
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected integration provider post to %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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
	prepareInteractionPromptFixture(t, ctx, fixture)
	var mu sync.Mutex
	var blocks any
	postCount := 0
	readbackCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode question prompt payload: %v", err)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			mu.Lock()
			postCount++
			blocks = payload["blocks"]
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			writeToolTestJSON(w, map[string]any{"ok": false})
		case "/conversations.replies":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse prompt readback form: %v", err)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			if r.Form.Get("oldest") != "" || r.Form.Get("limit") != "100" {
				t.Errorf("prompt readback bounds = %+v", r.Form)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			mu.Lock()
			readbackCount++
			readback := readbackCount
			promptBlocks := blocks
			mu.Unlock()
			if readback == 1 {
				if r.Form.Get("cursor") != "" {
					t.Errorf("first prompt readback cursor = %q", r.Form.Get("cursor"))
					http.Error(w, "test handler failed", http.StatusInternalServerError)
					return
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
				t.Errorf("second prompt readback cursor = %q", r.Form.Get("cursor"))
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			writeToolTestJSON(w, map[string]any{
				"ok": true,
				"messages": []map[string]any{{
					"ts":     "333.333",
					"blocks": promptBlocks,
				}},
			})
		default:
			t.Errorf("unexpected integration provider request to %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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

func TestIntegrationQuestionPromptUnknownOutcomeKeepsDashboardOpen(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-delivery-unknown")
	prepareInteractionPromptFixture(t, ctx, fixture)
	postCount := 0
	readbackCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			postCount++
			w.WriteHeader(http.StatusInternalServerError)
			writeToolTestJSON(w, map[string]any{"ok": false})
		case "/conversations.replies":
			readbackCount++
			writeToolTestJSON(w, map[string]any{"ok": true, "messages": []any{}})
		default:
			t.Errorf("unexpected integration provider request to %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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
	}
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatal("question did not remain deferred during background prompt delivery")
	}
	if postCount != 1 || readbackCount != 1 {
		t.Fatalf(
			"prompt posts/readbacks = %d/%d, want %d/%d",
			postCount,
			readbackCount,
			1,
			1,
		)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get delivery-unknown question tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting {
		t.Fatalf("delivery-unknown question tool = %+v, want waiting on dashboard", toolCall)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "question")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("delivery-unknown question interaction = %+v, want open", interaction)
	}
}

func TestIntegrationQuestionPromptDeliveryFailureKeepsDashboardOpen(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "question-delivery-failure")
	prepareInteractionPromptFixture(t, ctx, fixture)
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected integration provider post to %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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
	}
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch question: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatal("question did not remain deferred during background prompt delivery")
	}
	if postCount != 1 {
		t.Fatalf("post count = %d, want 1", postCount)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get failed question tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting {
		t.Fatalf("failed-delivery question tool = %+v, want waiting on dashboard", toolCall)
	}
	interaction := integrationToolInteraction(t, ctx, fixture, toolCallID, "question")
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("failed question interaction = %+v, want open", interaction)
	}
}

func TestIntegrationPermissionPromptDisabledTargetFallsBackToOmnara(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "permission-disabled-target")
	prepareInteractionPromptFixture(t, ctx, fixture)
	if _, err := fixture.Store.Integrations().DisconnectProjectIntegration(
		ctx,
		integrationstore.DisconnectProjectIntegrationInput{
			ProjectID:             toolsTestProjectID,
			IntegrationID:         fixture.Install.ID,
			ExpectedSetupRevision: &fixture.Install.SetupRevision,
		},
	); err != nil {
		t.Fatalf("disable install: %v", err)
	}
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path == "/chat.postMessage" {
			postCount++
		}
		t.Errorf("unexpected integration provider post to %s", r.URL.Path)
		http.Error(w, "test handler failed", http.StatusInternalServerError)
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
	prepareInteractionPromptFixture(t, ctx, fixture)
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
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected integration provider post to %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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
	prepareInteractionPromptFixture(t, ctx, fixture)
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected integration provider post to %s", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
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
	f := newIntegrationToolFixtureWithMCP(t, ctx, label, false)
	f.Target = seedToolContext(t, ctx, f.Pool, f.Store, f.Agent, f.Install,
		integrationstore.ConversationAddress{Kind: f.Target.ProviderRefKind, Ref: f.Target.ProviderRef})
	return f
}

func newIntegrationToolFixtureWithMCP(
	t *testing.T,
	ctx context.Context,
	label string,
	withMCP bool,
	storeOptions ...storage.Option,
) integrationToolFixture {
	return newIntegrationToolFixtureWithOptions(t, ctx, label, toolFixtureOptions{withMCP: withMCP}, storeOptions...)
}

type toolFixtureOptions struct {
	withMCP                bool
	withSubagents          bool
	withSlackIntegration   bool
	withDiscordIntegration bool
	withGitHubIntegration  bool
	githubPermission       string
	withToolContext        bool
	toolContextAddress     integrationstore.ConversationAddress
	slackPermission        string
}

func newIntegrationToolFixtureWithOptions(
	t *testing.T,
	ctx context.Context,
	label string,
	fixtureOptions toolFixtureOptions,
	storeOptions ...storage.Option,
) integrationToolFixture {
	t.Helper()
	withMCP := fixtureOptions.withMCP
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

	var install integrationstore.ProjectIntegrationRecord
	if fixtureOptions.withDiscordIntegration {
		install = createDiscordToolIntegration(t, ctx, store, user.ID)
	} else if fixtureOptions.withGitHubIntegration {
		install = createGitHubToolIntegration(t, ctx, store, user.ID)
	} else {
		install = createSlackToolIntegration(t, ctx, store, user.ID, "chat", label)
	}
	profile := createIntegrationToolProfile(t, ctx, store, user.ID, label, fixtureOptions)
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
	if fixtureOptions.withDiscordIntegration {
		address = integrationstore.ConversationAddress{Kind: "channel", Ref: "444"}
		if fixtureOptions.withToolContext {
			address = integrationstore.ConversationAddress{Kind: "thread", Ref: "444:555"}
		}
	} else if fixtureOptions.withGitHubIntegration {
		address = integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#7"}
	}
	if fixtureOptions.toolContextAddress.Kind != "" {
		address = fixtureOptions.toolContextAddress
	}
	origin := &executionstore.LaunchInputOrigin{IntegrationID: install.ID, Address: address}
	integrationActor, err := executionstore.IntegrationActorParams(install.ID, "U_FIXTURE", nil)
	require.NoError(t, err)
	actor := &integrationActor
	if fixtureOptions.withToolContext {
		origin = nil
		actor, err = executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(user.ID))
		require.NoError(t, err)
	}
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      toolsTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     toolsTestUserPrincipal(user.ID),
			IdempotencyKey: "tools-integration-launch-" + label,
			InitialInput: &executionstore.LaunchInitialInput{
				ContentBlocks:    json.RawMessage(`[{"type":"text","text":"send an integration reply"}]`),
				SemanticEventKey: "tools-integration-input-" + label,
				Actor:            actor,
				Origin:           origin,
			},
		},
	)
	if err != nil {
		t.Fatalf("launch integration tool agent: %v", err)
	}
	agent := launch.Agent
	target := launch.IntegrationTarget
	if fixtureOptions.withToolContext {
		target = seedToolContext(t, ctx, pool, store, agent, install, address)
	}
	input := launch.AgentInput
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
		[]uuid.UUID{input.ID},
		launch.AgentConfig.ID,
		admitted.Events[0].Sequence,
		uuid.Nil,
	)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(launch.AgentConfig.CompiledDefinition, &compiled))
	integrationID := install.ID
	prepared, err := agentconfig.PrepareIntegrationTools(compiled, map[uuid.UUID]agentconfig.IntegrationResolution{
		integrationID: {IntegrationID: integrationID, IntegrationType: install.IntegrationType},
	})
	require.NoError(t, err)
	integrationTools := map[string]ToolSpec{}
	for _, tool := range prepared {
		integrationTools[tool.Name] = ToolSpec{
			Type: tool.Type, Permission: tool.Permission, InputSchema: tool.InputSchema,
			Description: tool.Description, Deferred: tool.Deferred,
		}
	}
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
		IntegrationTools:   integrationTools,
	}
}

func seedToolContext(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *storage.Store,
	agent executionstore.AgentRecord, integration integrationstore.ProjectIntegrationRecord,
	address integrationstore.ConversationAddress,
) integrationstore.IntegrationTargetRecord {
	t.Helper()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	target, err := store.Integrations().EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
		ProjectID: agent.ProjectID, AgentID: agent.ID, IntegrationID: integration.ID, Address: address,
	})
	require.NoError(t, err)
	err = store.Integrations().AssignAgentIntegrationConversationTx(
		ctx,
		tx,
		agent.ProjectID,
		agent.ID,
		integration.ID,
		address,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	return target
}

func (f *integrationToolFixture) turn() Turn {
	turn := Turn{
		OrgID:              toolsTestOrgID,
		ProjectID:          toolsTestProjectID,
		AgentID:            f.Agent.ID,
		SourceEventID:      f.ModelOutputEventID,
		RuntimeLockID:      f.Lock.ID,
		ModelCallContextID: f.ModelCallContextID,
		Tools: map[string]ToolSpec{
			"ask_question": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
			"int__chat__post_message": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
			"set_interaction_handler": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
		},
	}
	for name, tool := range f.IntegrationTools {
		turn.Tools[name] = tool
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
	if f.ModelOutputEventID != uuid.Nil {
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

func (f *integrationToolFixture) toolCallID(t *testing.T, ctx context.Context, providerCallID string) uuid.UUID {
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
	userID uuid.UUID,
	label string,
	fixtureOptions toolFixtureOptions,
) executionstore.AgentProfileRecord {
	t.Helper()
	withMCP := fixtureOptions.withMCP
	sourceYAML := `instruction: Reply to users.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
`
	if fixtureOptions.withSlackIntegration || fixtureOptions.withDiscordIntegration {
		permission := fixtureOptions.slackPermission
		if permission == "" {
			permission = toolpermission.ModeAlwaysAllow
		}
		sourceYAML += "  int__chat__read: {}\n" +
			"  int__chat__post_message:\n    permission: {mode: " + permission + "}\n"
	}
	if fixtureOptions.withGitHubIntegration {
		permission := fixtureOptions.githubPermission
		if permission == "" {
			permission = toolpermission.ModeAlwaysAllow
		}
		for _, operation := range []string{"read", "discussion_comment", "inline_comment", "reply"} {
			sourceYAML += "  int__chat__" + operation + ":\n    permission: {mode: " + permission + "}\n"
		}
	}
	if withMCP {
		sourceYAML += `mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
      parameters: {}
`
	}
	if fixtureOptions.withSubagents {
		sourceYAML += `subagents:
  fork:
    type: self
    instruction:
      append: You are a fork.
`
	}
	compiled := compileToolsAgentYAMLResolved(t, ctx, store, userID, sourceYAML)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               toolsTestProjectID,
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
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
	userID uuid.UUID,
	sourceYAML string,
) agentconfig.Result {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse agent config source: %v", err)
	}
	provider := storagefixture.EnsureModelProvider(t, ctx, store.Models(), store.Secrets(),
		storagefixture.ModelProviderInput{OrgID: toolsTestOrgID, UserID: userID, Name: source.Model.ProviderConfig})
	configuredModel := storagefixture.EnsureModelAccess(t, ctx, store.Models(), toolsTestProjectID,
		storagefixture.DefaultModelInput(toolsTestOrgID, provider.ID, source.Model.Name))
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveIntegrationName: func(name string) (agentconfig.IntegrationResolution, error) {
			return resolveToolsIntegrationName(ctx, store, name)
		},
		ResolveModelSelection: func(
			providerConfigName string,
			configuredModelName string,
		) (agentconfig.ResolvedModelSelection, error) {
			return resolvedToolsAgentConfigModel(configuredModel), nil
		},
		ResolveMachineName: func(machineName string) (uuid.UUID, error) {
			machineID, err := store.Execution().ResolveAgentConfigMachineName(ctx, toolsTestProjectID, machineName)
			if err != nil {
				return uuid.Nil, err
			}
			return machineID, nil
		},
		ResolveMachinePoolName: func(machinePoolName string) (uuid.UUID, error) {
			machinePoolID, err := store.Execution().ResolveAgentConfigMachinePoolName(
				ctx,
				toolsTestOrgID,
				toolsTestProjectID,
				machinePoolName,
			)
			if err != nil {
				return uuid.Nil, err
			}
			return machinePoolID, nil
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
				ID:   uuid.Must(publicid.Decode(publicid.KindSkill, skillID)),
				Name: records[0].Name,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent config: %v", err)
	}
	return compiled
}

func resolvedToolsAgentConfigModel(
	configuredModel modelstore.ConfiguredModelRecord,
) agentconfig.ResolvedModelSelection {
	supportsTools := configuredModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID,
		SupportsTools:     &supportsTools,
	}
}

func parseConfiguredModelID(t *testing.T, compiled agentconfig.Result) uuid.UUID {
	t.Helper()
	return compiled.Compiled.Model.ConfiguredModelID
}

func resolveToolsIntegrationName(
	ctx context.Context,
	store *storage.Store,
	name string,
) (agentconfig.IntegrationResolution, error) {
	result, err := store.Integrations().ListProjectIntegrations(ctx, integrationstore.ListProjectIntegrationsInput{
		ProjectID: toolsTestProjectID, NamePattern: name, Limit: 100,
	})
	if err != nil {
		return agentconfig.IntegrationResolution{}, err
	}
	for _, integration := range result.Integrations {
		if integration.Name != name || integration.State != integrationstore.ProjectIntegrationStateActive {
			continue
		}
		return agentconfig.IntegrationResolution{
			IntegrationID:   integration.ID,
			IntegrationType: integration.IntegrationType,
		}, nil
	}
	return agentconfig.IntegrationResolution{}, storeerr.ErrNotFound
}

func createSlackToolIntegration(
	t *testing.T, ctx context.Context, store *storage.Store, userID uuid.UUID, name, label string,
) integrationstore.ProjectIntegrationRecord {
	t.Helper()
	secret, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          toolsTestOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: toolsTestProjectID,
		Name:           "tools-integration-" + label + "-credentials",
		Actor:          toolsTestUserPrincipal(userID),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken:   "xoxb-test",
			ClientID:      "client-id",
			ClientSecret:  "client-secret",
			SigningSecret: "signing-secret",
		},
	})
	require.NoError(t, err)
	integration, err := store.Integrations().CreateProjectIntegration(ctx, integrationstore.SaveProjectIntegrationInput{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Name: name, IntegrationType: integrationdefinition.SlackThread,
	})
	require.NoError(t, err)
	integration, err = store.Integrations().ConfigureProjectIntegration(
		ctx,
		integrationstore.ConfigureProjectIntegrationInput{
			OrgID:                 toolsTestOrgID,
			ProjectID:             toolsTestProjectID,
			IntegrationID:         integration.ID,
			ExpectedSetupRevision: integration.SetupRevision,
			InstalledByUserID:     userID,
			Provider:              integrationdefinition.ProviderSlack,
			ProviderTenantID:      "T123",
			ProviderAccountRef:    "A123",
			CredentialSecretID:    secret.ID,
			CredentialVersionID:   version.ID,
			OAuthFlowID:           uuid.Must(uuid.NewV7()),
			ProviderIdentity:      json.RawMessage(`{"bot_user_id":"B123"}`),
		},
	)
	require.NoError(t, err)
	return integration
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

func dispatchToolAndDrainAsync(
	t *testing.T,
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
) (Result, error) {
	t.Helper()
	var waitPresentation func() error
	if call.Name == toolcatalog.ToolNameAskQuestion && executor.BackgroundRunner == nil {
		executor.BackgroundRunner, waitPresentation = newQuestionPresentationRunner(t, ctx)
	}
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
	if waitPresentation != nil {
		if err := waitPresentation(); err != nil {
			t.Logf("question presentation failed; dashboard remains available: %v", err)
		}
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
	userID uuid.UUID,
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
