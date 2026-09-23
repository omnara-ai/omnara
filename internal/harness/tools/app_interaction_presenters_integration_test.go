//go:build integration

package tools

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestInteractionPresenterLateReceiptAfterCancelDismisses(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "late-presentation")
	prepareInteractionPromptFixture(t, ctx, f)
	started, release, dismissed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			close(started)
			<-release
			writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
		case "/chat.update":
			writeToolTestJSON(w, map[string]any{"ok": true})
			dismissed <- struct{}{}
		default:
			t.Errorf("unexpected provider call %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	call := f.recordToolCall(
		t,
		ctx,
		"late-question",
		"ask_question",
		`{"questions":[{"prompt":"Proceed?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		f.Now,
	)
	executor := Executor{
		Store:         f.Store,
		AppHTTPClient: appProviderTestClient(server),
	}
	_, err := executor.Dispatch(ctx, f.turn(), call)
	require.NoError(t, err)
	waitPresentation := enqueuePendingQuestionPresentations(t, ctx, executor)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("presentation did not start")
	}
	interaction := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID), "question")
	_, err = f.Store.Execution().
		CancelAgent(ctx, executionstore.CancelAgentInput{ProjectID: toolsTestProjectID, AgentID: f.Agent.ID})
	require.NoError(t, err)
	released = true
	close(release)
	select {
	case <-dismissed:
	case <-time.After(5 * time.Second):
		t.Fatal("late confirmed prompt was not dismissed")
	}
	require.NoError(t, waitPresentation())
	current, found, err := f.Store.Execution().
		GetAgentInteraction(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentInteractionStateCanceled, current.State)
	require.JSONEq(
		t,
		`{"provider":"slack","channel_id":"C123","message_id":"222.333"}`,
		string(current.PresentationReceipt),
	)
}

func TestInteractionPresenterRechecksAppBeforeRetry(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "presentation-revoked")
	prepareInteractionPromptFixture(t, ctx, f)
	started, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		calls.Add(1)
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected provider request %s", r.URL.Path)
		}
		started <- struct{}{}
		<-release
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	call := f.recordToolCall(t, ctx, "revoked-question", "ask_question",
		`{"questions":[{"prompt":"Proceed?","options":[{"label":"Yes"},{"label":"No"}]}]}`, f.Now)
	executor := Executor{
		Store:         f.Store,
		AppHTTPClient: appProviderTestClient(server),
	}
	_, err := executor.Dispatch(ctx, f.turn(), call)
	require.NoError(t, err)
	waitPresentation := enqueuePendingQuestionPresentations(t, ctx, executor)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("presentation did not start")
	}
	_, err = f.Store.Apps().
		DisconnectProjectApp(ctx, appstore.DisconnectProjectAppInput{
			ProjectID:             toolsTestProjectID,
			AppID:                 f.Install.ID,
			ExpectedSetupRevision: &f.Install.SetupRevision,
		})
	require.NoError(t, err)
	released = true
	close(release)
	require.ErrorIs(t, waitPresentation(), storeerr.ErrUnauthorized)
	require.EqualValues(t, 1, calls.Load(), "revocation must prevent the safe rate-limit retry")
	current := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID), "question")
	require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
	require.Empty(t, current.PresentationReceipt)
}
