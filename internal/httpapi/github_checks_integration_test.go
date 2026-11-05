//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGitHubChecksJourney(t *testing.T, seed string) (githubHTTPJourney, map[string]any) {
	t.Helper()
	f := newGitHubHTTPJourney(t, seed)
	base := map[string]any{
		"instruction": "Review this pull request.",
		"model":       map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
	}
	config := createPublicHTTPAgentConfig(t, f.handler, f.project, "checks-base", "json",
		integrationHTTPJSON(t, base), f.project.AdminToken, http.StatusCreated)
	profile := createPublicHTTPAgentProfile(t, f.handler, f.project, "checks-profile", "Review",
		testutil.RequireType[string](t, config["id"]), f.project.AdminToken, http.StatusCreated)
	requestJSONWithHeaders(t, f.handler, http.MethodPut,
		f.project.ProjectPath+"/integrations/"+testPublicID(t, publicid.KindIntegration, f.integration.ID),
		integrationHTTPJSON(t, map[string]any{"settings": map[string]any{"launcher": map[string]any{
			"trigger": "both", "profile": profile["id"],
		}}}), "", http.StatusOK, authHeaders(f.project.AdminToken))
	return f, profile
}

type githubCheckRunRequest struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
	DetailsURL string `json:"details_url"`
	Output     struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"output"`
}

func newGitHubChecksAPI(
	t *testing.T, head func() string, denyChecks bool,
	checkRun func(*http.Request, githubCheckRunRequest) any,
) github.Config {
	t.Helper()
	repository := github.Repository{ID: 1001, Name: "repository", Owner: github.User{Login: "owner"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any
		switch {
		case r.URL.Path == "/app/installations/456/access_tokens":
			var grant struct {
				Permissions map[string]string `json:"permissions"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&grant))
			result = map[string]any{"token": "token", "expires_at": "2099-01-01T00:00:00Z"}
			if grant.Permissions["checks"] != "" && denyChecks {
				w.WriteHeader(http.StatusUnprocessableEntity)
				result = map[string]any{"message": "The permissions requested are not granted to this installation."}
			}
		case r.URL.Path == "/installation/repositories":
			result = map[string]any{"total_count": 1, "repositories": []github.Repository{repository}}
		case r.URL.Path == "/repos/owner/repository/pulls/42":
			result = github.PullRequest{ID: 2042, Number: 42, Base: github.Branch{Repo: &repository},
				Head: github.Branch{SHA: head()}}
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repository/check-runs"):
			var body githubCheckRunRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			result = checkRun(r, body)
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
		assert.NoError(t, json.NewEncoder(w).Encode(result))
	}))
	t.Cleanup(server.Close)
	return github.Config{APIURL: server.URL, HTTPClient: server.Client()}
}

func TestGitHubCheckReconcilerTracksAgentActivity(t *testing.T) {
	t.Parallel()
	f, profile := newGitHubChecksJourney(t, "github-checks")
	sha := func(c string) string { return strings.Repeat(c, 40) }
	var mu sync.Mutex
	var calls []string
	var titles []string
	var firstSummary, checkName string
	head := sha("b")
	var onClose func()
	setHead := func(commit string) {
		mu.Lock()
		defer mu.Unlock()
		head = commit
	}
	api := newGitHubChecksAPI(t, func() string {
		mu.Lock()
		defer mu.Unlock()
		return head
	}, false, func(r *http.Request, body githubCheckRunRequest) any {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, strings.TrimSpace(fmt.Sprint(r.Method, " ", r.URL.Path, " ",
			body.Status, body.Conclusion, " ", body.HeadSHA, " ", body.DetailsURL)))
		titles = append(titles, body.Output.Title)
		if len(calls) == 1 {
			firstSummary, checkName = body.Output.Summary, body.Name
		}
		if hook := onClose; hook != nil && r.Method == http.MethodPatch {
			onClose = nil
			hook()
		}
		return map[string]any{"id": len(calls)}
	})
	reconciler := &integrationruntime.GitHubCheckReconciler{
		Store: f.project.Store, GitHub: api, PublicURL: "https://omnara.test",
	}
	reconcile := func() { requireReconciled(t, reconciler) }
	makeIdle := func(agentID any) {
		pool := integrationPoolForHandler(t, f.handler)
		_, err := pool.Exec(t.Context(), `DELETE FROM agent_wakeups WHERE agent_id=$1`, agentID)
		require.NoError(t, err)
		_, err = pool.Exec(t.Context(),
			`UPDATE agent_execution_state SET logical_ready_at=NULL, incomplete_tools=false WHERE agent_id=$1`, agentID)
		require.NoError(t, err)
	}

	raw := githubHTTPPullRequest(t, 42, "opened")
	githubHTTPWebhook(t, f.handler, "pull_request", "opened", githubJourneyWebhookSecret, raw, http.StatusNoContent)
	results := f.consume(t, raw)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Launch)
	agentID := results[0].Launch.Agent.ID
	details := "https://omnara.test/projects/" + testPublicID(t, publicid.KindProject, f.project.ProjectUUID) +
		"/agents/" + testPublicID(t, publicid.KindAgent, agentID) + "/chat"
	created := func(state, commit string) string {
		return "POST /repos/owner/repository/check-runs " + state + " " + commit + " " + details
	}
	closed := func(id int) string { return fmt.Sprintf("PATCH /repos/owner/repository/check-runs/%d neutral", id) }
	deliver := func(eventType, deliveryID, raw string) {
		githubHTTPWebhook(t, f.handler, eventType, deliveryID, githubJourneyWebhookSecret, raw, http.StatusNoContent)
		f.consume(t, raw)
	}
	push := func(deliveryID, commit string) {
		setHead(commit)
		deliver("pull_request", deliveryID,
			strings.ReplaceAll(githubHTTPPullRequest(t, 42, "synchronize"), sha("b"), commit))
	}
	reconcile()
	reconcile()
	deliver("issue_comment", "followup", githubHTTPComment(t, 42, 3002, "@helper please include a regression test"))
	reconcile()
	push("synchronize", sha("c"))
	reconcile()
	makeIdle(agentID)
	reconcile()
	reconcile()
	deliver("issue_comment", "review", githubHTTPComment(t, 42, 3003, "@helper do a full review"))
	reconcile()
	push("last-push", sha("d"))
	makeIdle(agentID)
	reconcile()
	require.Equal(t, []string{
		created("in_progress", sha("b")), closed(1),
		created("in_progress", sha("c")), closed(3),
		created("in_progress", sha("c")), closed(5),
		created("completedneutral", sha("d")),
	}, calls)

	deliver("issue_comment", "fix", githubHTTPComment(t, 42, 3004, "@helper please fix the test"))
	reconcile()
	repository := map[string]any{"id": 1001, "full_name": "owner/repository"}
	botPush := integrationHTTPJSON(t, map[string]any{
		"action": "synchronize", "installation": map[string]any{"id": 456}, "repository": repository,
		"before": sha("d"), "after": sha("e"),
		"pull_request": map[string]any{
			"id": 2042, "number": 42, "title": "Review this change", "body": "Please review",
			"base": map[string]any{"repo": repository}, "head": map[string]any{"sha": sha("e")},
		},
		"sender": map[string]any{"id": 999, "login": "helper[bot]", "type": "Bot"},
	})
	setHead(sha("e"))
	githubHTTPWebhook(t, f.handler, "pull_request", "bot-push", githubJourneyWebhookSecret, botPush, http.StatusNoContent)
	var botReceipts int
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT count(*) FROM integration_inbox WHERE receipt_key = 'github:pull_request:bot-push'`).Scan(&botReceipts))
	require.Zero(t, botReceipts, "the bot's own push must not become agent input")
	reconcile()
	require.Equal(t, []string{
		created("in_progress", sha("d")), closed(8), created("in_progress", sha("e")),
	}, calls[7:])

	integrations := f.project.Store.Integrations()
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#42"}
	pool := integrationPoolForHandler(t, f.handler)
	makeIdle(agentID)
	setHook := func(hook func()) {
		mu.Lock()
		defer mu.Unlock()
		onClose = hook
	}
	setHook(func() {
		_, err := pool.Exec(t.Context(),
			`UPDATE agent_execution_state SET logical_ready_at=statement_timestamp() WHERE agent_id=$1`, agentID)
		assert.NoError(t, err)
		assert.NoError(t, integrations.MarkGitHubCheckRun(
			t.Context(), f.project.ProjectUUID, f.integration.ID, agentID, address, false))
	})
	reconcile()
	reconcile()
	require.Equal(t, []string{closed(10), created("in_progress", sha("e"))}, calls[10:],
		"work starting while the check closes gets a fresh check")

	require.Equal(t, []string{
		"Working", "Superseded", "Working", "Finished", "Working", "Superseded",
		"Finished", "Working", "Superseded", "Working", "Finished", "Working",
	}, titles)
	require.Equal(t, f.integration.ProviderAgentDisplayName+" ("+f.integration.Name+")", checkName)
	require.Contains(t, firstSummary, "Open in Omnara: "+details)
	require.Contains(t, firstSummary, testPublicID(t, publicid.KindAgent, agentID))
	require.Contains(t, firstSummary, "- **Profile:** `"+testutil.RequireType[string](t, profile["id"])+"`")
	require.Contains(t, firstSummary, "`"+f.integration.Name+"`")

	makeIdle(agentID)
	reconcile()
	reconcile()
	require.Equal(t, []string{closed(12)}, calls[12:])
	runs, err := integrations.ListGitHubCheckRuns(t.Context(), uuid.Nil, uuid.Nil, 10)
	require.NoError(t, err)
	require.Empty(t, runs, "a finished check stops being tracked")

	for range 2 {
		require.NoError(t, integrations.MarkGitHubCheckRun(
			t.Context(), f.project.ProjectUUID, f.integration.ID, uuid.New(), address, false))
	}
	first, err := integrations.ListGitHubCheckRuns(t.Context(), uuid.Nil, uuid.Nil, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	second, err := integrations.ListGitHubCheckRuns(t.Context(), uuid.Nil, first[0].ID, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Greater(t, second[0].ID.String(), first[0].ID.String())
	rest, err := integrations.ListGitHubCheckRuns(t.Context(), uuid.Nil, second[0].ID, 1)
	require.NoError(t, err)
	require.Empty(t, rest)
}

func TestGitHubCheckReconcilersShareOneCheck(t *testing.T) {
	t.Parallel()
	f, _ := newGitHubChecksJourney(t, "github-check-claims")
	entered, release := make(chan struct{}), make(chan struct{})
	var creates, closes atomic.Int32
	api := newGitHubChecksAPI(t, func() string { return strings.Repeat("b", 40) }, false,
		func(r *http.Request, _ githubCheckRunRequest) any {
			switch {
			case r.Method == http.MethodPost:
				id := creates.Add(1)
				if id == 1 {
					close(entered)
					<-release
				}
				return map[string]any{"id": id}
			case r.URL.Path == "/repos/owner/repository/check-runs/1":
				closes.Add(1)
				return map[string]any{"id": 1}
			}
			t.Errorf("unexpected check run request: %s %s", r.Method, r.URL.Path)
			return nil
		})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	raw := githubHTTPPullRequest(t, 42, "opened")
	githubHTTPWebhook(t, f.handler, "pull_request", "opened", githubJourneyWebhookSecret, raw, http.StatusNoContent)
	require.Len(t, f.consume(t, raw), 1)
	reconciler := func() *integrationruntime.GitHubCheckReconciler {
		return &integrationruntime.GitHubCheckReconciler{
			Store: f.project.Store, GitHub: api,
		}
	}
	first, second := reconciler(), reconciler()
	done := make(chan error, 1)
	go func() {
		_, err := first.Reconcile(t.Context())
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("first reconciler finished without creating a check: %v", err)
	}
	requireReconciled(t, second)
	unblock.Do(func() { close(release) })
	require.NoError(t, <-done)
	requireReconciled(t, second)
	require.Equal(t, int32(1), creates.Load(), "only the reconciler holding the claim may publish the check")

	integrations := f.project.Store.Integrations()
	runs, err := integrations.ListGitHubCheckRuns(t.Context(), uuid.Nil, uuid.Nil, 1)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.NoError(t, integrations.ClaimGitHubCheckRun(t.Context(), &runs[0], uuid.New(), 0))
	requireReconciled(t, second)
	require.Equal(t, int32(1), closes.Load(), "a claim abandoned by a lost owner forces a repair")
	require.Equal(t, int32(2), creates.Load())
	runs, err = integrations.ListGitHubCheckRuns(t.Context(), uuid.Nil, uuid.Nil, 1)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.False(t, runs[0].Unfinished)
	require.Equal(t, int64(2), runs[0].Run)
}

func TestGitHubCheckReconcilerUpdatesTheWaitingCheck(t *testing.T) {
	t.Parallel()
	f, _ := newGitHubChecksJourney(t, "github-check-waiting")
	ctx := t.Context()
	agentID, _ := createHTTPStructuredQuestionInteraction(t, ctx, integrationPoolForHandler(t, f.handler),
		f.project.Store, f.project.OrgUUID, f.project.ProjectUUID, time.Now())
	require.NoError(t, f.project.Store.Integrations().MarkGitHubCheckRun(ctx, f.project.ProjectUUID, f.integration.ID,
		agentID, integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#42"}, false))
	var mu sync.Mutex
	var calls []string
	api := newGitHubChecksAPI(t, func() string { return strings.Repeat("b", 40) }, false,
		func(r *http.Request, body githubCheckRunRequest) any {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, strings.TrimSpace(fmt.Sprint(r.Method, " ", r.URL.Path, " ", body.Status, body.Conclusion)))
			return map[string]any{"id": 1}
		})
	reconciler := &integrationruntime.GitHubCheckReconciler{Store: f.project.Store, GitHub: api}
	requireReconciled(t, reconciler)
	requestJSONWithHeaders(t, f.handler, http.MethodPost,
		f.project.ProjectPath+"/agents/"+testPublicID(t, publicid.KindAgent, agentID)+"/archive", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	requireReconciled(t, reconciler)
	require.Equal(t, []string{
		"POST /repos/owner/repository/check-runs completedaction_required",
		"PATCH /repos/owner/repository/check-runs/1 neutral",
	}, calls, "finishing after a question updates the waiting check instead of adding another")
}

func requireReconciled(t *testing.T, reconciler *integrationruntime.GitHubCheckReconciler) {
	t.Helper()
	_, err := reconciler.Reconcile(t.Context())
	require.NoError(t, err)
}

func TestGitHubCheckTrackingSurvivesReceiptReplay(t *testing.T) {
	t.Parallel()
	f, _ := newGitHubChecksJourney(t, "github-check-replay")
	ctx := t.Context()
	integrations := f.project.Store.Integrations()
	raw := githubHTTPPullRequest(t, 42, "opened")
	githubHTTPWebhook(t, f.handler, "pull_request", "opened", githubJourneyWebhookSecret, raw, http.StatusNoContent)
	results := f.consume(t, raw)
	require.Len(t, results, 1)
	runs, err := integrations.ListGitHubCheckRuns(ctx, uuid.Nil, uuid.Nil, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.NoError(t, integrations.DeleteGitHubCheckRun(ctx, runs[0]))
	_, err = integrationPoolForHandler(t, f.handler).Exec(ctx, `UPDATE integration_inbox
		SET state = 'queued', completed_at = NULL, claim_token = NULL, claim_expires_at = NULL,
		    next_attempt_at = statement_timestamp()
		WHERE project_id = $1 AND receipt_key = 'github:pull_request:opened'`, f.project.ProjectUUID)
	require.NoError(t, err)
	replayed := f.consume(t, raw)
	require.Len(t, replayed, 1)
	require.False(t, replayed[0].Launch.Created, "the replay finds the agent admitted before the crash")
	runs, err = integrations.ListGitHubCheckRuns(ctx, uuid.Nil, uuid.Nil, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1, "a replayed receipt still tracks its admitted agent")
	require.Equal(t, results[0].Launch.Agent.ID, runs[0].AgentID)
}

func TestGitHubCheckStopsTrackingWithoutChecksPermission(t *testing.T) {
	t.Parallel()
	f, _ := newGitHubChecksJourney(t, "github-check-denied")
	var creates atomic.Int32
	api := newGitHubChecksAPI(t, func() string { return strings.Repeat("b", 40) }, true,
		func(*http.Request, githubCheckRunRequest) any {
			creates.Add(1)
			return map[string]any{"id": 1}
		})
	raw := githubHTTPPullRequest(t, 42, "opened")
	githubHTTPWebhook(t, f.handler, "pull_request", "opened", githubJourneyWebhookSecret, raw, http.StatusNoContent)
	require.Len(t, f.consume(t, raw), 1)
	requireReconciled(t, &integrationruntime.GitHubCheckReconciler{Store: f.project.Store, GitHub: api})
	runs, err := f.project.Store.Integrations().ListGitHubCheckRuns(t.Context(), uuid.Nil, uuid.Nil, 10)
	require.NoError(t, err)
	require.Empty(t, runs, "an App without the Checks permission stops tracking without retrying")
	require.Zero(t, creates.Load())
}
