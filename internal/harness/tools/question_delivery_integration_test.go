//go:build integration

package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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

func enqueuePendingQuestionPresentations(t *testing.T, ctx context.Context, executor Executor) func() error {
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
	require.NoError(t, executor.interactionPresenter().EnqueuePending(ctx, runner))
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
	return wait
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
			require.Zero(t, enqueues, "dispatch must not schedule presentation")
			require.False(t, scope.Started())
			result, err = executor.Dispatch(ctx, f.turn(), call)
			require.NoError(t, err)
			require.Equal(t, DispatchDeferred, result.Disposition)
			require.Zero(t, enqueues, "tool replay must not recreate or enqueue the interaction")
			require.NoError(t, executor.interactionPresenter().EnqueuePending(ctx, runner))
			require.Equal(t, 1, enqueues, "the worker scan discovers the committed question")
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
			var attempted bool
			require.NoError(t, f.Pool.QueryRow(ctx,
				`SELECT presentation_attempted_at IS NOT NULL FROM agent_interactions WHERE id = $1`,
				interaction.ID).Scan(&attempted))
			require.False(t, attempted, "a lost queue entry must remain eligible for durable rediscovery")
			assertDispatchTestResultCount(t, ctx, f, call.ID, 0)
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

	rejected := make(chan struct{}, 1)
	observed := questionPromptRunner{t: t, trySubmit: func(label string, task func(context.Context) error) bool {
		accepted := runner.TrySubmit(label, task)
		if !accepted {
			select {
			case rejected <- struct{}{}:
			default:
			}
		}
		return accepted
	}}
	workerCtx, stopWorker := context.WithCancel(ctx)
	pollingDone := make(chan struct{})
	defer func() { stopWorker(); <-pollingDone }()
	presenter := executor.interactionPresenter()
	go func() { presenter.RunPending(workerCtx, observed); close(pollingDone) }()
	select {
	case <-rejected:
	case <-time.After(5 * time.Second):
		t.Fatal("pending presenter did not encounter the saturated runner")
	}
	var attempted bool
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT presentation_attempted_at IS NOT NULL FROM agent_interactions WHERE id = $1`,
		interaction.ID).Scan(&attempted))
	require.False(t, attempted, "saturation must not claim presentation")
	releaseCapacity()
	require.Eventually(t, func() bool {
		current, found, err := f.Store.Execution().GetAgentInteraction(
			ctx, toolsTestProjectID, f.Agent.ID, interaction.ID)
		return err == nil && found && len(current.PresentationReceipt) != 0
	}, 5*time.Second, 10*time.Millisecond)
	stopWorker()
	<-pollingDone
	current := integrationToolInteraction(t, ctx, f, toolID, executionstore.AgentInteractionKindQuestion)
	require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
	require.JSONEq(t, `{"provider":"slack","channel_id":"C123","message_id":"222.333"}`,
		string(current.PresentationReceipt))
	require.NoError(t, presenter.EnqueuePending(ctx, runner))
	drained := make(chan struct{})
	require.True(t, runner.Submit("drain", func(context.Context) error { close(drained); return nil }))
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("presentation queue did not drain")
	}
	require.EqualValues(t, 1, posts.Load(), "later discovery must not repost the question")
	assertDispatchTestToolState(t, ctx, f, call.ID, "waiting", false)
	assertDispatchTestResultCount(t, ctx, f, call.ID, 0)
}
