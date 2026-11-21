//go:build integration

package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func githubAttachTestPullRequest(number int) map[string]any {
	return map[string]any{
		"id": 700 + number, "number": number, "html_url": fmt.Sprintf("https://github.com/octo/renamed/pull/%d", number),
		"base": map[string]any{"repo": map[string]any{"id": 123, "full_name": "octo/renamed"}},
	}
}

func githubAttachTestCall(id string, pullRequest int) model.ToolCall {
	return model.ToolCall{
		ID:    id,
		Name:  toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationAttachPullRequest),
		Input: json.RawMessage(fmt.Sprintf(`{"repository_id":123,"pull_request":%d}`, pullRequest)),
	}
}

func seedOtherGitHubOwner(t *testing.T, f integrationToolFixture, ref string) {
	t.Helper()
	ctx := t.Context()
	other, err := f.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: toolsTestProjectID, ProfileID: f.Profile.ID, AgentConfigID: f.Profile.CurrentConfigID,
		LaunchedBy: toolsTestUserPrincipal(f.User.ID), IdempotencyKey: "other-github-owner",
	})
	require.NoError(t, err)
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = f.Store.Integrations().EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
		ProjectID: toolsTestProjectID, AgentID: other.Agent.ID, IntegrationID: f.Install.ID,
		Address: integrationstore.ConversationAddress{Kind: "pull_request", Ref: ref}, LaunchKey: "default",
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

func githubAttachmentInput(f integrationToolFixture, pullRequest int) integrationstore.AgentConversationInput {
	return integrationstore.AgentConversationInput{
		CreateIntegrationSubscriptionInput: integrationstore.CreateIntegrationSubscriptionInput{
			OrgID: f.Agent.OrgID, ProjectID: f.Agent.ProjectID, IntegrationID: f.Install.ID, AgentID: f.Agent.ID,
			Conversation: json.RawMessage(fmt.Sprintf(`{"repository_id":123,"pull_request":%d}`, pullRequest)),
		},
		ExpectedConfigID: f.AgentConfig.ID, RuntimeLockID: f.Lock.ID,
	}
}

func seedGitHubAttachment(t *testing.T, f integrationToolFixture, pullRequest int) {
	t.Helper()
	require.NoError(t, f.Store.Integrations().AttachAgentConversation(t.Context(), githubAttachmentInput(f, pullRequest)))
}

func githubSubscribedPullRequests(t *testing.T, f integrationToolFixture) []string {
	t.Helper()
	var refs []string
	for _, subscription := range integrationToolSubscriptions(t, f) {
		require.Equal(t, f.Agent.ID, subscription.AgentID)
		refs = append(refs, subscription.Address.Ref)
	}
	return refs
}

func githubPullRequestOwner(t *testing.T, f integrationToolFixture, ref string) uuid.UUID {
	t.Helper()
	var agentID uuid.UUID
	err := f.Pool.QueryRow(t.Context(), `SELECT agent_id FROM integration_targets
		WHERE integration_id=$1 AND scope_ref=$2 AND launch_key='default'`, f.Install.ID, ref).Scan(&agentID)
	if err != nil {
		return uuid.Nil
	}
	return agentID
}

func TestGitHubAttachPullRequest(t *testing.T) {
	for _, test := range []struct {
		name        string
		pullRequest int
		seed        func(*testing.T, integrationToolFixture)
		check       func(*testing.T, integrationToolFixture)
	}{
		{
			name: "attaches a reachable pull request", pullRequest: 9,
			check: func(t *testing.T, f integrationToolFixture) {
				require.Equal(t, []string{"123#9"}, githubSubscribedPullRequests(t, f))
				require.Equal(t, uuid.Nil, githubPullRequestOwner(t, f, "123#9"), "attaching never claims mentions")
			},
		},
		{
			name: "earlier pull requests stay attached", pullRequest: 8,
			seed: func(t *testing.T, f integrationToolFixture) {
				seedGitHubAttachment(t, f, 9)
			},
			check: func(t *testing.T, f integrationToolFixture) {
				require.ElementsMatch(t, []string{"123#8", "123#9"}, githubSubscribedPullRequests(t, f))
			},
		},
		{
			name: "attaching again does not duplicate", pullRequest: 8,
			seed: func(t *testing.T, f integrationToolFixture) {
				seedGitHubAttachment(t, f, 8)
			},
			check: func(t *testing.T, f integrationToolFixture) {
				require.Equal(t, []string{"123#8"}, githubSubscribedPullRequests(t, f))
			},
		},
		{
			name: "attaches its launch pull request", pullRequest: 7,
			check: func(t *testing.T, f integrationToolFixture) {
				require.Equal(t, []string{"123#7"}, githubSubscribedPullRequests(t, f))
			},
		},
		{
			name: "another agent keeps its pull request", pullRequest: 10,
			seed: func(t *testing.T, f integrationToolFixture) {
				seedOtherGitHubOwner(t, f, "123#10")
			},
			check: func(t *testing.T, f integrationToolFixture) {
				require.Equal(t, []string{"123#10"}, githubSubscribedPullRequests(t, f))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-attach", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true,
			})
			if test.seed != nil {
				test.seed(t, f)
			}
			path := fmt.Sprintf("/repos/octo/renamed/pulls/%d", test.pullRequest)
			server := githubToolTestServer(t, "read", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, path, r.URL.Path)
				writeToolTestJSON(w, githubAttachTestPullRequest(test.pullRequest))
			})
			call := githubAttachTestCall("attach", test.pullRequest)
			f.recordToolCalls(t, ctx, []model.ToolCall{call}, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
			want := map[string]any{"repository_id": float64(123), "pull_request": float64(test.pullRequest)}
			if test.pullRequest != 7 {
				want["url"] = fmt.Sprintf("https://github.com/octo/renamed/pull/%d", test.pullRequest)
			}
			require.Subset(t, toolResultMapFromTestParts(t, result.ContentParts), want)
			test.check(t, f)
		})
	}
}

func TestGitHubAttachPullRequestRechecksAuthorityInTransaction(t *testing.T) {
	for _, scenario := range []string{"config changed", "lease lost"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-assign-race", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true,
			})
			server := githubToolTestServer(t, "read", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/repos/octo/renamed/pulls/8", r.URL.Path)
				writeToolTestJSON(w, githubAttachTestPullRequest(8))
			})
			call := githubAttachTestCall("attach", 8)
			f.recordToolCalls(t, ctx, []model.ToolCall{call}, f.Now)
			hold, err := f.Pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = hold.Rollback(ctx) }()
			require.NoError(t, integrationstore.LockConversationTx(ctx, hold, f.Agent.ProjectID, f.Install.ID,
				integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#8"}))
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			scope := NewAsyncExecutionScope(nil)
			_, err = executor.Dispatch(WithAsyncExecutionScope(ctx, scope), f.turn(), call)
			require.NoError(t, err)
			scope.Seal()
			require.Eventually(t, func() bool {
				var waiting int
				err := f.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks
					WHERE locktype = 'advisory' AND NOT granted
					  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&waiting)
				return err == nil && waiting > 0
			}, 5*time.Second, 10*time.Millisecond, "attaching must wait on the conversation lock")
			switch scenario {
			case "config changed":
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat), []byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				delete(source.Tools, call.Name)
				changeIntegrationToolConfig(t, ctx, f, source)
			case "lease lost":
				require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, f.Agent.ProjectID, f.Agent.ID, f.Lock.ID))
			}
			require.NoError(t, hold.Rollback(ctx))
			select {
			case <-scope.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for attach")
			}
			require.Empty(t, integrationToolSubscriptions(t, f), "a revoked call must not attach")
			if scenario == "config changed" {
				record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Contains(t, toolResultMapFromTestParts(t, record.ResultContentParts)["message"], "agent config changed")
			}
		})
	}
}

func TestGitHubAttachPullRequestRequiresCredentialAccess(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-assign-credential", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	server := githubToolTestServer(t, "read", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octo/renamed/pulls/8", r.URL.Path)
		_, err := f.Pool.Exec(ctx, `UPDATE secrets SET deleted_at=now() WHERE id=$1`, f.Install.CredentialSecretID)
		assert.NoError(t, err)
		writeToolTestJSON(w, githubAttachTestPullRequest(8))
	})
	call := githubAttachTestCall("attach", 8)
	f.recordToolCalls(t, ctx, []model.ToolCall{call}, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
	require.NoError(t, err)
	record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	require.Contains(t, toolResultMapFromTestParts(t, result.ContentParts)["message"], "integration access changed")
	require.Empty(t, integrationToolSubscriptions(t, f), "revoked credential access must not attach")
}

func githubCommentCall(id, target string) model.ToolCall {
	return model.ToolCall{
		ID:    id,
		Name:  toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationDiscussionComment),
		Input: json.RawMessage(`{"body":"Ready"` + target + `}`),
	}
}

func TestGitHubToolsChooseAttachedPullRequest(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-choose-pr", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	seedOtherGitHubOwner(t, f, "123#8")
	seedGitHubAttachment(t, f, 8)
	calls := []model.ToolCall{
		githubCommentCall("unnamed", ""),
		githubCommentCall("attached", `,"repository_id":123,"pull_request":8`),
		githubCommentCall("launch", `,"repository_id":123,"pull_request":7`),
		githubCommentCall("unattached", `,"repository_id":123,"pull_request":9`),
	}
	failures := map[string]string{
		"unnamed":    "required when multiple pull requests are attached",
		"unattached": "pull request 9 in repository 123 is not attached to this agent",
	}
	var posted []string
	server := githubToolTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/octo/renamed/pulls/8" {
			writeToolTestJSON(w, githubAttachTestPullRequest(8))
			return
		}
		posted = append(posted, r.URL.Path)
		writeToolTestJSON(w, map[string]any{"id": 501, "html_url": "https://github.com/octo/renamed/pull/8#c"})
	})
	f.recordToolCalls(t, ctx, calls, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
		require.NoError(t, err)
		record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
		require.NoError(t, err)
		if message, failed := failures[call.ID]; failed {
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome, call.ID)
			require.Contains(t, toolResultMapFromTestParts(t, result.ContentParts)["message"], message)
			continue
		}
		require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, call.ID)
	}
	require.Equal(t, []string{"/repos/octo/renamed/issues/8/comments", "/repos/octo/renamed/issues/7/comments"}, posted,
		"a call names one of several attached PRs, including one another agent handles")
}

func githubCheckRuns(t *testing.T, f integrationToolFixture) []integrationstore.GitHubCheckRun {
	t.Helper()
	runs, err := f.Store.Integrations().ListGitHubCheckRuns(t.Context(), uuid.New(), uuid.Nil, 100)
	require.NoError(t, err)
	return runs
}

func TestGitHubToolStopsWhenItsPullRequestIsDetached(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-detached-mid-call", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	seedGitHubAttachment(t, f, 8)
	call := githubCommentCall("comment", `,"repository_id":123,"pull_request":8`)
	var posted atomic.Bool
	server := githubToolTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/octo/renamed/pulls/8" {
			assert.NoError(t, f.Store.Integrations().DetachAgentConversation(ctx, githubAttachmentInput(f, 8)))
			writeToolTestJSON(w, githubAttachTestPullRequest(8))
			return
		}
		posted.Store(true)
		writeToolTestJSON(w, map[string]any{"id": 501, "html_url": "https://github.com/octo/renamed/pull/8#c"})
	})
	f.recordToolCalls(t, ctx, []model.ToolCall{call}, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
	require.NoError(t, err)
	record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	require.Contains(t, toolResultMapFromTestParts(t, result.ContentParts)["message"],
		"pull request 8 in repository 123 is no longer attached to this agent")
	require.False(t, posted.Load(), "a call stops once its pull request is detached")
}

func TestGitHubCheckFollowsPullRequestOwner(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-check-owner", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	seedGitHubAttachment(t, f, 8)
	require.NoError(t, f.Store.Integrations().MarkGitHubCheckRun(ctx, f.Agent.ProjectID, f.Install.ID, f.Agent.ID,
		integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#8"}, false))
	require.True(t, githubCheckRuns(t, f)[0].Eligible)
	seedOtherGitHubOwner(t, f, "123#8")
	require.False(t, githubCheckRuns(t, f)[0].Eligible, "a new owner's check replaces a subscriber's")
}

func TestGitHubAttachRestartsTheAgentsCheck(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-attach-restarts-check", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	require.NoError(t, f.Store.Integrations().MarkGitHubCheckRun(ctx, f.Agent.ProjectID, f.Install.ID, f.Agent.ID,
		integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#8"}, false))
	require.False(t, githubCheckRuns(t, f)[0].Restart)
	seedGitHubAttachment(t, f, 8)
	run := githubCheckRuns(t, f)[0]
	require.True(t, run.Restart, "a new subscription restarts the agent's existing check")
	require.NoError(t, f.Store.Integrations().SaveGitHubCheckRun(ctx, run))
	seedGitHubAttachment(t, f, 8)
	require.False(t, githubCheckRuns(t, f)[0].Restart, "an existing subscription leaves the check alone")
	seedGitHubAttachment(t, f, 9)
	require.Len(t, githubCheckRuns(t, f), 1, "subscribing never creates a check")
}

func TestGitHubListAttachedPullRequests(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-list-attached", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	seedGitHubAttachment(t, f, 9)
	seedGitHubAttachment(t, f, 8)
	list := func(id, input string) model.ToolCall {
		return model.ToolCall{
			ID:    id,
			Name:  toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationListAttachedPullRequests),
			Input: json.RawMessage(input),
		}
	}
	calls := []model.ToolCall{list("first", `{"limit":2}`), list("rest", `{"offset":2}`)}
	f.recordToolCalls(t, ctx, calls, f.Now)
	var pages []map[string]any
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, Executor{Store: f.Store}, f.turn(), call)
		require.NoError(t, err)
		pages = append(pages, toolResultMapFromTestParts(t, result.ContentParts))
	}
	pullRequests := func(numbers ...int) []any {
		var page []any
		for _, number := range numbers {
			page = append(page, map[string]any{"repository_id": float64(123), "pull_request": float64(number)})
		}
		return page
	}
	require.Equal(t, []map[string]any{
		{"pull_requests": pullRequests(7, 9), "has_more": true, "next_offset": float64(2)},
		{"pull_requests": pullRequests(8), "has_more": false},
	}, pages, "the launch pull request first, then attachments oldest first")
}

func TestGitHubDetachPullRequest(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-detach", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	seedGitHubAttachment(t, f, 8)
	require.NoError(t, f.Store.Integrations().MarkGitHubCheckRun(ctx, f.Agent.ProjectID, f.Install.ID, f.Agent.ID,
		integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#8"}, false))
	require.Len(t, githubCheckRuns(t, f), 1)
	require.True(t, githubCheckRuns(t, f)[0].Eligible)
	detach := func(id string, pullRequest int) model.ToolCall {
		return model.ToolCall{
			ID:    id,
			Name:  toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationDetachPullRequest),
			Input: json.RawMessage(fmt.Sprintf(`{"repository_id":123,"pull_request":%d}`, pullRequest)),
		}
	}
	calls := []model.ToolCall{detach("detach", 8), detach("again", 8), detach("launch", 7)}
	f.recordToolCalls(t, ctx, calls, f.Now)
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, Executor{Store: f.Store}, f.turn(), call)
		require.NoError(t, err)
		record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
		require.NoError(t, err)
		if call.ID == "launch" {
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
			require.Contains(t, toolResultMapFromTestParts(t, result.ContentParts)["message"],
				"can't detach the conversation it was launched for")
			continue
		}
		require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, call.ID)
	}
	require.Empty(t, integrationToolSubscriptions(t, f))
	require.False(t, githubCheckRuns(t, f)[0].Eligible, "the detached agent's check gets finished")
}

func TestGitHubUnnamedCallRejectsSiblingAttachmentChanges(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-unnamed-siblings", toolFixtureOptions{
		withGitHubIntegration: true,
	})
	seedGitHubAttachment(t, f, 8)
	seedGitHubAttachment(t, f, 9)
	detach := model.ToolCall{
		ID:    "detach",
		Name:  toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationDetachPullRequest),
		Input: json.RawMessage(`{"repository_id":123,"pull_request":8}`),
	}
	calls := []model.ToolCall{githubCommentCall("comment", ""), detach}
	f.recordToolCalls(t, ctx, calls, f.Now)
	var posted atomic.Int32
	server := githubToolTestServer(t, "", func(w http.ResponseWriter, _ *http.Request) {
		posted.Add(1)
		writeToolTestJSON(w, map[string]any{"id": 501})
	})
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	for _, call := range []model.ToolCall{detach, calls[0]} {
		_, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
		require.NoError(t, err)
	}
	record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, "comment"))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	require.Contains(t, toolResultMapFromTestParts(t, record.ResultContentParts)["message"],
		"required when this response attaches or detaches pull requests")
	require.Zero(t, posted.Load(), "a sibling detach can't pick an unnamed call's pull request")
}

func TestGitHubApprovedUnnamedCallKeepsItsPullRequest(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-approval-keeps-pr", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true, githubPermission: toolpermission.ModeAlwaysAsk,
	})
	call := githubCommentCall("comment", "")
	call = f.recordPendingToolCall(t, ctx, call.ID, call.Name, string(call.Input), f.Now)
	var posted []string
	server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
		posted = append(posted, r.URL.Path)
		writeToolTestJSON(w, map[string]any{"id": 501, "html_url": "https://github.com/octo/renamed/pull/7#c"})
	})
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	turn := f.turn()
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
	permission := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID),
		executionstore.AgentInteractionKindPermission)
	seedGitHubAttachment(t, f, 9)
	actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(f.User.ID))
	require.NoError(t, err)
	_, err = f.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: f.Agent.ProjectID, AgentID: f.Agent.ID, ID: permission.ID, Actor: actor,
		Resolution: interactionform.Resolution{
			Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
		},
	})
	require.NoError(t, err)
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
	_, err = dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
	require.NoError(t, err)
	record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, string(record.ResultContentParts))
	require.Equal(t, []string{"/repos/octo/renamed/issues/7/comments"}, posted,
		"attaching another pull request doesn't change an approved call's pull request")
}

func TestGitHubToolApprovalRejectsChangedDestination(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-approval-destination", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true, githubPermission: toolpermission.ModeAlwaysAsk,
	})
	_, err := f.Pool.Exec(ctx, `DELETE FROM integration_states WHERE kind='agent_conversation' AND key=$1`,
		f.Agent.ID.String())
	require.NoError(t, err)
	seedGitHubAttachment(t, f, 8)
	call := githubCommentCall("comment", "")
	call = f.recordPendingToolCall(t, ctx, call.ID, call.Name, string(call.Input), f.Now)
	var posts atomic.Int32
	server := githubToolTestServer(t, "write", func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		writeToolTestJSON(w, map[string]any{"id": 501})
	})
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	turn := f.turn()
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
	permission := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID),
		executionstore.AgentInteractionKindPermission)
	require.NoError(t, f.Store.Integrations().DetachAgentConversation(ctx, githubAttachmentInput(f, 8)))
	seedGitHubAttachment(t, f, 9)
	actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(f.User.ID))
	require.NoError(t, err)
	_, err = f.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: f.Agent.ProjectID, AgentID: f.Agent.ID, ID: permission.ID, Actor: actor,
		Resolution: interactionform.Resolution{
			Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
		},
	})
	require.NoError(t, err)
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
	_, err = dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
	require.NoError(t, err)
	record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	require.Zero(t, posts.Load(), "a comment approved for #8 must not post on #9")
}
