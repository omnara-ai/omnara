//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledSlackReadbackNeverReposts(t *testing.T) {
	for _, test := range []scheduledSlackReadbackCase{
		{
			name: "delayed visibility launches", status: http.StatusInternalServerError, visible: true,
			retainAttempt: true, attempts: 2, wantPosts: 1, wantReads: 2, wantAgents: 1,
		},
		{
			name: "exhaustion fails without launching", status: http.StatusInternalServerError,
			retainAttempt: true, attempts: integrationstore.IntegrationInboxMaxAttempts,
			wantPosts: 1, wantReads: integrationstore.IntegrationInboxMaxAttempts,
		},
		{
			name: "5xx ratelimited retains uncertain attempt", status: http.StatusInternalServerError, code: "ratelimited",
			retainAttempt: true, attempts: integrationstore.IntegrationInboxMaxAttempts,
			wantPosts: 1, wantReads: integrationstore.IntegrationInboxMaxAttempts,
		},
		{
			name:   "5xx channel_not_found retains uncertain attempt",
			status: http.StatusInternalServerError, code: "channel_not_found",
			retainAttempt: true, attempts: integrationstore.IntegrationInboxMaxAttempts,
			wantPosts: 1, wantReads: integrationstore.IntegrationInboxMaxAttempts,
		},
		{
			name: "429 permits a later send", status: http.StatusTooManyRequests, code: "ratelimited",
			attempts: 2, wantPosts: 2, wantAgents: 1,
		},
		{
			name: "channel_not_found fails immediately", status: http.StatusOK, code: "channel_not_found",
			terminal: true, attempts: 1, wantPosts: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) { testScheduledSlackReadback(t, test) })
	}
}

type scheduledSlackReadbackCase struct {
	name          string
	status        int
	code          string
	visible       bool
	terminal      bool
	retainAttempt bool
	attempts      int
	wantPosts     int
	wantReads     int
	wantAgents    int
}

func testScheduledSlackReadback(t *testing.T, test scheduledSlackReadbackCase) {
	t.Helper()
	f := newScheduledJourney(t)
	_, err := f.pool.Exec(t.Context(),
		`UPDATE project_apps SET provider_identity='{"bot_user_id":"UBOT"}' WHERE id=$1`, f.appID)
	require.NoError(t, err)
	app, err := f.store.Integrations().GetProjectAppByID(t.Context(), f.appID)
	require.NoError(t, err)
	receipt := f.fire()
	var posts, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth.test":
			_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
		case "/chat.postMessage":
			attempt := posts.Add(1)
			if test.status == http.StatusTooManyRequests && attempt > 1 {
				_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"100.1"}`))
				return
			}
			// A 5xx body cannot prove non-delivery, even when its Slack error code
			// would ordinarily mean a definite rate limit or destination rejection.
			w.WriteHeader(test.status)
			if test.code != "" {
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": test.code}))
			}
		case "/conversations.history":
			if reads.Add(1) == 1 || !test.visible {
				_, _ = w.Write([]byte(`{"ok":true,"messages":[]}`))
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "messages": []any{map[string]any{
					"user": "UBOT", "ts": "100.1", "metadata": map[string]any{
						"event_type":    "omnara_scheduled_thread",
						"event_payload": map[string]string{"receipt_id": receipt.ID.String()},
					},
				}},
			}))
		default:
			t.Errorf("unexpected Slack request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	access := &slackInboxTestAccess{appSetup: app, version: uuid.New()}
	f.consumer.providers["slack"] = NewSlackAppInboxProvider(
		slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, access, access, nil,
	)
	worker := NewAppInboxWorker(f.store.Integrations(), f.consumer, AppInboxWorkerOptions{})
	err = worker.consume(t.Context(), receipt)
	if test.terminal {
		require.ErrorIs(t, err, ErrScheduledLaunchFailed)
	} else {
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrScheduledLaunchFailed)
	}
	saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	if test.terminal {
		require.Equal(t, integrationstore.IntegrationInboxFailed, saved.State,
			"ErrScheduledLaunchFailed must fail the receipt on its first attempt")
	} else {
		require.Equal(t, integrationstore.IntegrationInboxPending, saved.State)
	}
	require.Equal(t, 1, saved.AttemptCount)
	require.Empty(t, saved.Plan)
	preparation, err := saved.ScheduledPreparation()
	require.NoError(t, err)
	require.Equal(t, test.retainAttempt, preparation.AttemptedAt != nil,
		"only a definite non-delivery may clear the durable attempt")
	require.Nil(t, preparation.Root)
	var agents int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Zero(t, agents, "a rejected or uncertain opening cannot launch an agent")
	for range test.attempts - 1 {
		_, err = f.pool.Exec(t.Context(),
			`UPDATE integration_inbox SET available_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
		require.NoError(t, err)
		retried := f.claim()
		err := worker.consume(t.Context(), retried)
		if test.wantAgents > 0 {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
	saved, err = f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	if test.wantAgents > 0 {
		require.Equal(t, integrationstore.IntegrationInboxCompleted, saved.State)
	} else {
		require.Equal(t, integrationstore.IntegrationInboxFailed, saved.State)
	}
	require.Equal(t, test.attempts, saved.AttemptCount)
	finalPreparation, err := saved.ScheduledPreparation()
	require.NoError(t, err)
	if test.retainAttempt {
		require.Equal(t, preparation.AttemptedAt, finalPreparation.AttemptedAt,
			"retry and exhaustion must preserve the original publication attempt")
	}
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Equal(t, test.wantAgents, agents)
	worked, err := worker.RunOnce(t.Context())
	require.NoError(t, err)
	require.False(t, worked, "completed or failed receipts must not be automatically reclaimed")
	require.EqualValues(t, test.wantPosts, posts.Load())
	require.EqualValues(t, test.wantReads, reads.Load())
}
