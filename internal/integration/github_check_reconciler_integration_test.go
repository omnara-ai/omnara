//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestGitHubCheckReconcilerReadsOnePagePerTick(t *testing.T) {
	_, store, ids, integrationID := integrationProviderFixture(t, "github", "11", "22")
	ctx := t.Context()
	integrations := store.Integrations()
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#42"}
	for range 5 {
		require.NoError(t, integrations.MarkGitHubCheckRun(ctx, ids.ProjectID, integrationID, uuid.New(), address, false))
	}
	remaining := func() []integrationstore.GitHubCheckRun {
		t.Helper()
		runs, err := integrations.ListGitHubCheckRuns(ctx, uuid.Nil, uuid.Nil, 10)
		require.NoError(t, err)
		return runs
	}
	tracked := remaining()
	for _, run := range tracked {
		run.Done = true
		require.NoError(t, integrations.SaveGitHubCheckRun(ctx, run))
	}
	reconciler := &GitHubCheckReconciler{Store: store, pageSize: 2}
	tick := func(wantWorked bool) {
		t.Helper()
		pass, err := reconciler.Reconcile(ctx)
		require.NoError(t, err)
		require.Equal(t, wantWorked, pass.Worked)
	}
	tick(true)
	require.Len(t, remaining(), 3, "a tick finishes one page of idle checks")
	require.Equal(t, tracked[1].ID, reconciler.after)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := reconciler.Reconcile(canceled)
	require.Error(t, err)
	require.Equal(t, tracked[1].ID, reconciler.after, "a failed read keeps the cursor")
	tick(true)
	require.Len(t, remaining(), 1)
	require.Equal(t, tracked[3].ID, reconciler.after)
	tick(true)
	require.Empty(t, remaining())
	require.Equal(t, uuid.Nil, reconciler.after, "a short page wraps the cursor to the start")
	tick(false)
}

func TestGitHubCheckReconcilerRepublishesUnfinishedFinishedChecks(t *testing.T) {
	_, store, ids, integrationID := integrationProviderFixture(t, "github", "11", "22")
	ctx := t.Context()
	integrations := store.Integrations()
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#42"}
	require.NoError(t, integrations.MarkGitHubCheckRun(ctx, ids.ProjectID, integrationID, uuid.New(), address, false))
	tracked := func(claim uuid.UUID) integrationstore.GitHubCheckRun {
		t.Helper()
		runs, err := integrations.ListGitHubCheckRuns(ctx, claim, uuid.Nil, 10)
		require.NoError(t, err)
		require.Len(t, runs, 1)
		return runs[0]
	}
	run := tracked(uuid.Nil)
	run.Done = true
	require.NoError(t, integrations.SaveGitHubCheckRun(ctx, run))
	run = tracked(uuid.Nil)
	require.NoError(t, integrations.ClaimGitHubCheckRun(ctx, &run, uuid.New(), 0))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	reconciler := &GitHubCheckReconciler{
		Store: store, GitHub: github.Config{APIURL: server.URL, HTTPClient: server.Client()},
	}
	_, err := reconciler.Reconcile(ctx)
	require.Error(t, err)
	require.Positive(t, requests.Load(), "a finished check left unfinished is republished instead of deleted")
	require.True(t, tracked(reconciler.claim).Unfinished)
}

func TestGitHubCheckReconcilerPausesRateLimitedInstallations(t *testing.T) {
	_, store, ids, integrationID := integrationProviderFixture(t, "github", "11", "22")
	ctx := t.Context()
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#42"}
	track := func(integrationID uuid.UUID) {
		t.Helper()
		require.NoError(t, store.Integrations().MarkGitHubCheckRun(
			ctx, ids.ProjectID, integrationID, uuid.New(), address, false))
	}
	for range githubCheckRunConcurrency + 1 {
		track(integrationID)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	reconciler := &GitHubCheckReconciler{
		Store: store, GitHub: github.Config{APIURL: server.URL, HTTPClient: server.Client()},
	}
	pass, err := reconciler.Reconcile(ctx)
	require.Error(t, err)
	retrying := pass.Retrying
	require.Positive(t, retrying)
	require.LessOrEqual(t, retrying, githubCheckRunConcurrency, "checks queued behind a rate limit wait without failing")
	limited := requests.Load()
	require.Positive(t, limited)
	first, err := store.Integrations().GetIntegration(ctx, ids.ProjectID, integrationID)
	require.NoError(t, err)
	credential, err := store.Secrets().GetSecret(ctx, ids.OrgID, first.CredentialSecretID)
	require.NoError(t, err)
	shared, err := store.Integrations().CreateIntegration(ctx, integrationstore.SaveIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "reviews", IntegrationKind: integrationdefinition.GitHubPR,
	})
	require.NoError(t, err)
	shared, err = store.Integrations().ConfigureIntegration(ctx, integrationstore.ConfigureIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, IntegrationID: shared.ID, InstalledByUserID: ids.ProviderAdminUserID,
		Provider: integrationdefinition.ProviderGitHub, ProviderTenantID: "11", ProviderAccountRef: "22",
		ExpectedSetupRevision: shared.SetupRevision, CredentialAppID: 11,
		CredentialSecretID: credential.ID, CredentialVersionID: credential.CurrentVersionID,
	})
	require.NoError(t, err)
	track(shared.ID)
	pass, err = reconciler.Reconcile(ctx)
	require.NoError(t, err)
	require.False(t, pass.Worked)
	require.Equal(t, limited, requests.Load(), "checks on another integration sharing the rate-limited installation wait instead of calling GitHub")
	require.Equal(t, retrying, pass.Retrying)
}
