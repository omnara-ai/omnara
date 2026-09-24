//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

type questionPromptRunner struct {
	t         *testing.T
	trySubmit func(string, func(context.Context) error) bool
}

func (r questionPromptRunner) Submit(string, func(context.Context) error) bool {
	r.t.Error("question presentation must not use blocking Submit")
	return false
}

func (r questionPromptRunner) TrySubmit(label string, task func(context.Context) error) bool {
	return r.trySubmit(label, task)
}

func newQuestionPresentationRunner(t *testing.T, ctx context.Context) (BackgroundRunner, func() error) {
	t.Helper()
	var tasks sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	runner := questionPromptRunner{t: t, trySubmit: func(_ string, task func(context.Context) error) bool {
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			if err := task(ctx); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}()
		return true
	}}
	wait := func() error {
		t.Helper()
		done := make(chan struct{})
		go func() { tasks.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("question background tasks did not finish")
		}
		mu.Lock()
		defer mu.Unlock()
		return errors.Join(failures...)
	}
	t.Cleanup(func() { _ = wait() })
	return runner, wait
}

func TestQuestionWaitSurvivesUndeliveredPromptAndRuntimeExpiry(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "runner-shutdown"
		if accepted {
			name = "worker-stopped"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "question-"+name)
			prepareInteractionPromptFixture(t, ctx, f)
			call := f.recordToolCall(t, ctx, "question-"+name, "ask_question",
				`{"questions":[{"prompt":"Continue?","options":[{"label":"Yes"},{"label":"No"}]}]}`, f.Now)
			toolID := f.toolCallID(t, ctx, call.ID)
			enqueues := 0
			runner := questionPromptRunner{t: t, trySubmit: func(string, func(context.Context) error) bool {
				enqueues++
				tool, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, toolID)
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolCallStateWaiting, tool.State)
				require.Equal(t, uuid.Nil, tool.RuntimeLockID)
				return accepted
			}}
			executor := Executor{Store: f.Store, BackgroundRunner: runner}
			scope := NewAsyncExecutionScope(nil)
			scope.Seal()
			result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, DispatchDeferred, result.Disposition)
			require.Equal(t, 1, enqueues, "presentation is offered only after the transaction commits")
			require.False(t, scope.Started())
			result, err = executor.Dispatch(ctx, f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, DispatchDeferred, result.Disposition)
			require.Equal(t, 1, enqueues, "tool replay must not recreate or enqueue the interaction")
			interaction := integrationToolInteraction(t, ctx, f, toolID, executionstore.AgentInteractionKindQuestion)
			_, err = f.Pool.Exec(ctx, `UPDATE agent_runtime_locks
 SET started_at = statement_timestamp() - interval '3 minutes',
     renewed_at = statement_timestamp() - interval '2 minutes',
     lease_expires_at = statement_timestamp() - interval '1 minute'
 WHERE id = $1`, f.Lock.ID)
			require.NoError(t, err)
			reaped, err := f.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
			require.NoError(t, err)
			require.EqualValues(t, 1, reaped)
			waiting, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, toolID)
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateWaiting, waiting.State)
			require.Empty(t, waiting.Outcome)
			require.Nil(t, waiting.CompletedAt)
			current := integrationToolInteraction(t, ctx, f, toolID, executionstore.AgentInteractionKindQuestion)
			require.Equal(t, interaction.ID, current.ID)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			require.Empty(t, current.PresentationReceipt)
			assertDispatchTestResultCount(t, ctx, f, call.ID, 0)
			resolveDashboardQuestion(t, ctx, f, current)
		})
	}
}

func TestQuestionDispatchDoesNotWaitForSaturatedPresentationQueue(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "question-saturation")
	prepareInteractionPromptFixture(t, ctx, f)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected question presentation request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		posts.Add(1)
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()
	runner, err := NewBackgroundExecutionRunner(ctx, nil, 1)
	require.NoError(t, err)
	release, started := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCapacity := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { releaseCapacity(); runner.Shutdown() }()
	require.True(t, runner.Submit("occupied", func(context.Context) error {
		close(started)
		<-release
		return nil
	}))
	<-started
	require.True(t, runner.Submit("queued", func(context.Context) error { return nil }))
	call := f.recordToolCall(t, ctx, "question-saturation", "ask_question",
		`{"questions":[{"prompt":"Continue?","options":[{"label":"Yes"},{"label":"No"}]}]}`, f.Now)
	toolID := f.toolCallID(t, ctx, call.ID)
	executor := Executor{Store: f.Store, BackgroundRunner: runner,
		IntegrationHTTPClient: integrationProviderTestClient(server)}
	dispatchCtx, cancelDispatch := context.WithCancel(ctx)
	defer cancelDispatch()
	returned := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := executor.Dispatch(dispatchCtx, f.turn(), call)
		returned <- struct {
			result Result
			err    error
		}{result, err}
	}()
	require.Eventually(t, func() bool {
		tool, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, toolID)
		return err == nil && tool.State == executionstore.ToolCallStateWaiting && tool.RuntimeLockID == uuid.Nil
	}, 5*time.Second, 10*time.Millisecond)
	select {
	case outcome := <-returned:
		require.NoError(t, outcome.err)
		require.Equal(t, DispatchDeferred, outcome.result.Disposition)
	case <-time.After(time.Second):
		t.Fatal("question dispatch remained blocked after durable wait with a full presentation queue")
	}
	cancelDispatch()
	interaction := integrationToolInteraction(t, ctx, f, toolID, executionstore.AgentInteractionKindQuestion)
	require.Zero(t, posts.Load())

	releaseCapacity()
	result, err := executor.Dispatch(ctx, f.turn(), call)
	require.NoError(t, err)
	require.Equal(t, DispatchDeferred, result.Disposition)

	drained := make(chan struct{})
	require.True(t, runner.Submit("drain", func(context.Context) error { close(drained); return nil }))
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("presentation queue did not drain")
	}
	require.Zero(t, posts.Load(), "a dropped presentation is not retried after capacity returns")
	assertDispatchTestToolState(t, ctx, f, call.ID, "waiting", false)
	assertDispatchTestResultCount(t, ctx, f, call.ID, 0)
	resolveDashboardQuestion(t, ctx, f, interaction)
}

func resolveDashboardQuestion(
	t *testing.T,
	ctx context.Context,
	f integrationToolFixture,
	interaction executionstore.AgentInteractionRecord,
) {
	t.Helper()
	actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(f.User.ID))
	require.NoError(t, err)
	resolved, err := f.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ID: interaction.ID, Actor: actor,
		Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{0}}}},
	})
	require.NoError(t, err)
	require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
	require.NotEqual(t, uuid.Nil, resolved.ResolvedByInputID)
}

func TestQuestionPresentationReplayAndTakeoverDoNotResend(t *testing.T) {
	for _, scenario := range []string{"sent", "provider_failure", "uncertain_publication", "receipt_write_failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "replay-"+scenario)
			prepareInteractionPromptFixture(t, ctx, f)
			call := model.ToolCall{ID: "question", Name: "ask_question", Input: json.RawMessage(
				`{"questions":[{"prompt":"Continue?","options":[{"label":"Yes"},{"label":"No"}]}]}`)}
			// A ready sibling gives the replacement worker work while the question waits.
			f.recordToolCalls(t, ctx, []model.ToolCall{call, {ID: "sibling", Name: "list_agents", Input: json.RawMessage(`{}`)}}, f.Now)
			if scenario == "receipt_write_failure" {
				_, err := f.Pool.Exec(ctx, `
				CREATE FUNCTION reject_presentation_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
				IF NEW.presentation_receipt IS NOT NULL THEN RAISE EXCEPTION 'receipt store unavailable';
				END IF; RETURN NEW; END $$;
				CREATE TRIGGER reject_presentation_receipt BEFORE UPDATE ON agent_interactions
				FOR EACH ROW EXECUTE FUNCTION reject_presentation_receipt()`)
				require.NoError(t, err)
			}
			var posts, readbacks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				switch r.URL.Path {
				case "/chat.postMessage":
					posts.Add(1)
					if scenario == "uncertain_publication" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if scenario == "provider_failure" {
						writeToolTestJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
						return
					}
					writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
				case "/conversations.replies":
					readbacks.Add(1)
					writeToolTestJSON(w, map[string]any{"ok": true, "messages": []any{}, "has_more": false})
				default:
					t.Errorf("unexpected presentation request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			runner, wait := newQuestionPresentationRunner(t, ctx)
			enqueues := 0
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server),
				BackgroundRunner: questionPromptRunner{t: t, trySubmit: func(label string, task func(context.Context) error) bool {
					enqueues++
					return runner.TrySubmit(label, task)
				}}}
			result, err := executor.Dispatch(ctx, f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, DispatchDeferred, result.Disposition)
			if scenario == "sent" {
				require.NoError(t, wait())
			} else {
				require.Error(t, wait())
			}
			if scenario == "receipt_write_failure" {
				_, err := f.Pool.Exec(ctx, `DROP TRIGGER reject_presentation_receipt ON agent_interactions`)
				require.NoError(t, err)
			}
			result, err = executor.Dispatch(ctx, f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, DispatchDeferred, result.Disposition)
			require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, toolsTestProjectID, f.Agent.ID, f.Lock.ID))
			claimInput := toolsTestClaimInput()
			claimInput.WorkerProcessID = uuid.New()
			claim, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, claimInput)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentWorkTool, claim.Kind)
			require.NotEqual(t, f.Lock.ID, claim.RuntimeLock.ID)
			f.Lock = claim.RuntimeLock
			result, err = executor.Dispatch(ctx, f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, DispatchDeferred, result.Disposition)
			_ = wait()
			require.Equal(t, 1, enqueues, "replay and takeover must not offer another presentation")
			require.EqualValues(t, 1, posts.Load())
			if scenario == "uncertain_publication" {
				require.EqualValues(t, 1, readbacks.Load())
			} else {
				require.Zero(t, readbacks.Load())
			}
			interaction := integrationToolInteraction(
				t, ctx, f, f.toolCallID(t, ctx, call.ID), executionstore.AgentInteractionKindQuestion,
			)
			require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
			require.Equal(t, scenario == "sent", len(interaction.PresentationReceipt) != 0)
			assertDispatchTestToolState(t, ctx, f, call.ID, "waiting", false)
			assertDispatchTestResultCount(t, ctx, f, call.ID, 0)
			resolveDashboardQuestion(t, ctx, f, interaction)
		})
	}
}
