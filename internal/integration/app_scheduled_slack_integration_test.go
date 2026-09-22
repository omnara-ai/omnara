//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledSlackPublicationOutcomes(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		retry  bool
	}{
		{name: "server error", status: http.StatusInternalServerError},
		{
			name: "5xx ratelimited is uncertain", status: http.StatusInternalServerError,
			body: `{"ok":false,"error":"ratelimited"}`,
		},
		{
			name: "5xx channel_not_found is uncertain", status: http.StatusInternalServerError,
			body: `{"ok":false,"error":"channel_not_found"}`,
		},
		{name: "429 permits retry", status: http.StatusTooManyRequests, retry: true},
		{
			name: "ratelimited permits retry", status: http.StatusOK,
			body: `{"ok":false,"error":"ratelimited"}`, retry: true,
		},
		{
			name: "permanent rejection", status: http.StatusOK,
			body: `{"ok":false,"error":"channel_not_found"}`,
		},
		{
			name: "transient error is uncertain", status: http.StatusOK,
			body: `{"ok":false,"error":"internal_error"}`,
		},
		{name: "missing message ID", status: http.StatusOK, body: `{"ok":true,"channel":"C123"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newScheduledJourney(t)
			_, err := f.pool.Exec(t.Context(),
				`UPDATE project_apps SET provider_identity='{"bot_user_id":"UBOT"}' WHERE id=$1`, f.appID)
			require.NoError(t, err)
			receipt := f.fire()
			event, err := receipt.ScheduledEvent()
			require.NoError(t, err)
			app, err := f.store.Integrations().GetProjectAppByID(t.Context(), f.appID)
			require.NoError(t, err)
			launch, err := appdefinition.PrepareThreadSchedule(app.AppType, event.Settings, event.Occurrence)
			require.NoError(t, err)
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/auth.test" {
					_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
					return
				}
				if !assert.Equal(t, "/chat.postMessage", r.URL.Path, "scheduled openings must not read history") ||
					!assert.Equal(t, http.MethodPost, r.Method) {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				var body map[string]any
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, "C123", body["channel"])
				assert.NotEmpty(t, body["text"])
				assert.NotContains(t, body, "thread_ts")
				assert.NotContains(t, body, "metadata")
				if posts.Add(1) == 1 {
					assert.Equal(t, launch.OpeningMessage, body["text"])
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(test.body))
					return
				}
				_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"100.1"}`))
			}))
			defer server.Close()
			access := &slackInboxTestAccess{appSetup: app, version: uuid.New()}
			f.handler.provider = NewSlackAppInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, access, access, nil,
			)
			worker := NewAppInboxWorker(f.store.Integrations(), f.consumer, AppInboxWorkerOptions{})
			err = worker.consume(t.Context(), receipt)
			saved, readErr := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, readErr)
			if test.retry {
				require.Error(t, err)
				require.NotErrorIs(t, err, ErrScheduledActionFailed)
				require.Equal(t, integrationstore.IntegrationInboxPending, saved.State)
			} else {
				require.ErrorIs(t, err, ErrScheduledActionFailed)
				require.Equal(t, integrationstore.IntegrationInboxFailed, saved.State)
			}
			require.Equal(t, 1, saved.AttemptCount)
			require.Empty(t, saved.Plan)
			var agents int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Zero(t, agents)
			require.EqualValues(t, 1, posts.Load())

			var next integrationstore.IntegrationInboxRecord
			if test.retry {
				_, err = f.pool.Exec(t.Context(),
					`UPDATE integration_inbox SET available_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
				require.NoError(t, err)
				next = f.claim()
				require.Equal(t, receipt.ID, next.ID)
				require.NotEqual(t, receipt.ClaimToken, next.ClaimToken)
			} else {
				worked, err := worker.RunOnce(t.Context())
				require.NoError(t, err)
				require.False(t, worked, "terminal publication failure must not be automatically retried")
				require.EqualValues(t, 1, posts.Load())
				next = f.fire()
				require.NotEqual(t, receipt.ID, next.ID, "a failed occurrence must not block the next one")
			}
			require.NoError(t, worker.consume(t.Context(), next))
			saved, err = f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, next.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, saved.State)
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Equal(t, 1, agents)
			require.EqualValues(t, 2, posts.Load())
			worked, err := worker.RunOnce(t.Context())
			require.NoError(t, err)
			require.False(t, worked)
			require.EqualValues(t, 2, posts.Load(), "completed receipts must not post again")
		})
	}
}
