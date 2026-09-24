//go:build integration

package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type githubDeploymentResponse struct {
	status int
	err    error
}

func sendGitHubDeploymentWebhook(
	t *testing.T, server *httptest.Server, delivery, raw string,
) <-chan githubDeploymentResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost,
		server.URL+"/api/integrations/github/123/events", strings.NewReader(raw))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(github.EventHeader, "issue_comment")
	r.Header.Set(github.DeliveryHeader, delivery)
	mac := hmac.New(sha256.New, []byte(githubJourneyWebhookSecret))
	_, _ = mac.Write([]byte(raw))
	r.Header.Set(github.SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	done := make(chan githubDeploymentResponse, 1)
	go func() {
		response, err := server.Client().Do(r)
		if err != nil {
			done <- githubDeploymentResponse{err: err}
			return
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, response.Body)
		done <- githubDeploymentResponse{status: response.StatusCode, err: err}
	}()
	return done
}

func waitGitHubDeploymentResponse(t *testing.T, done <-chan githubDeploymentResponse) githubDeploymentResponse {
	t.Helper()
	select {
	case response := <-done:
		return response
	case <-time.After(10 * time.Second):
		t.Fatal("webhook request did not finish")
		return githubDeploymentResponse{}
	}
}

func TestGitHubHTTPDeploymentDrainPreservesReceipts(t *testing.T) {
	f := newGitHubHTTPJourney(t, "github-deployment-drain")
	ctx := t.Context()
	pool := integrationPoolForHandler(t, f.handler)
	old := httptest.NewServer(f.handler)
	t.Cleanup(old.Close)
	next := httptest.NewServer(newIntegrationServer(pool))
	t.Cleanup(next.Close)
	raw := githubHTTPComment(t, 42, 3001, "in flight during drain")
	lock, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer lock.Rollback(ctx)
	_, err = lock.Exec(ctx, `INSERT INTO integration_inbox(project_id,integration_id,receipt_key,payload)
		VALUES($1,$2,'github:issue_comment:draining',$3)`, f.integration.ProjectID, f.integration.ID, []byte(raw))
	require.NoError(t, err)
	response := sendGitHubDeploymentWebhook(t, old, "draining", raw)
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "InsertIntegrationInboxReceipt", 1)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox WHERE integration_id=$1`,
		f.integration.ID).Scan(&count))
	require.Zero(t, count, "the receipt is not durably visible while intake is blocked")
	select {
	case result := <-response:
		t.Fatalf("webhook acknowledged before durable commit: %+v", result)
	default:
	}
	draining := make(chan struct{})
	old.Config.RegisterOnShutdown(func() { close(draining) })
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	shutdown := make(chan error, 1)
	go func() { shutdown <- old.Config.Shutdown(shutdownCtx) }()
	select {
	case <-draining:
	case <-shutdownCtx.Done():
		t.Fatal("old HTTP server did not start draining")
	}
	nextRaw := githubHTTPComment(t, 42, 3002, "accepted by replacement")
	accepted := waitGitHubDeploymentResponse(t, sendGitHubDeploymentWebhook(t, next, "replacement", nextRaw))
	require.NoError(t, accepted.err)
	require.Equal(t, http.StatusNoContent, accepted.status)
	var saved []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM integration_inbox
		WHERE integration_id=$1 AND receipt_key='github:issue_comment:replacement'`, f.integration.ID).Scan(&saved))
	require.Equal(t, nextRaw, string(saved), "replacement acknowledges committed signed bytes")
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown finished before the in-flight receipt committed: %v", err)
	case result := <-response:
		t.Fatalf("draining request returned before durable commit: %+v", result)
	default:
	}
	require.NoError(t, lock.Rollback(ctx))
	accepted = waitGitHubDeploymentResponse(t, response)
	require.NoError(t, accepted.err)
	require.Equal(t, http.StatusNoContent, accepted.status)
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM integration_inbox
		WHERE integration_id=$1 AND receipt_key='github:issue_comment:draining'`, f.integration.ID).Scan(&saved))
	require.Equal(t, raw, string(saved), "old server acknowledges only after the real transaction commits")
	select {
	case err := <-shutdown:
		require.NoError(t, err)
	case <-shutdownCtx.Done():
		t.Fatal("old HTTP server did not finish draining")
	}
	accepted = waitGitHubDeploymentResponse(t, sendGitHubDeploymentWebhook(t, next, "draining", raw))
	require.NoError(t, accepted.err)
	require.Equal(t, http.StatusNoContent, accepted.status)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox WHERE integration_id=$1`,
		f.integration.ID).Scan(&count))
	require.Equal(t, 2, count, "redelivery to the replacement must preserve the original receipt")
}

type githubLostDeploymentResponse struct {
	http.ResponseWriter
	dropped bool
	err     error
}

func (w *githubLostDeploymentResponse) WriteHeader(status int) {
	if status != http.StatusNoContent {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	conn, _, err := http.NewResponseController(w.ResponseWriter).Hijack()
	w.err = err
	if err == nil {
		w.dropped = true
		w.err = conn.Close()
	}
}

func (w *githubLostDeploymentResponse) Write(body []byte) (int, error) {
	if w.dropped {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

func TestGitHubHTTPDeploymentLostResponseReplayDeduplicates(t *testing.T) {
	f := newGitHubHTTPJourney(t, "github-deployment-lost-response")
	pool := integrationPoolForHandler(t, f.handler)
	dropped := make(chan error, 1)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lost := &githubLostDeploymentResponse{ResponseWriter: w}
		f.handler.ServeHTTP(lost, r)
		dropped <- lost.err
	}))
	t.Cleanup(old.Close)
	next := httptest.NewServer(newIntegrationServer(pool))
	t.Cleanup(next.Close)
	raw := githubHTTPComment(t, 42, 3001, "response lost after commit")
	response := waitGitHubDeploymentResponse(t, sendGitHubDeploymentWebhook(t, old, "lost", raw))
	require.Error(t, response.err, "caller must observe an actual dropped HTTP response")
	select {
	case err := <-dropped:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("mounted handler did not finish")
	}
	var receipt integrationstore.IntegrationInboxRecord
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT id FROM integration_inbox
		WHERE integration_id=$1 AND receipt_key='github:issue_comment:lost'`, f.integration.ID).Scan(&receipt.ID))
	require.Empty(t, f.consume(t, raw), "this integration has no launcher or subscriptions")
	old.Close()
	for range 2 {
		response = waitGitHubDeploymentResponse(t, sendGitHubDeploymentWebhook(t, next, "lost", raw))
		require.NoError(t, response.err)
		require.Equal(t, http.StatusNoContent, response.status)
	}
	saved, err := f.project.Store.Integrations().GetIntegrationInbox(t.Context(), f.integration.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, raw, string(saved.Payload))
	require.Equal(t, integrationstore.IntegrationInboxCompleted, saved.State)
	var count, attempts int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*),sum(attempt_count)
		FROM integration_inbox WHERE integration_id=$1`, f.integration.ID).Scan(&count, &attempts))
	require.Equal(t, 1, count)
	require.Equal(t, 1, attempts, "redelivery cannot cause a second consumption")
}
