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

func githubAssignTestPullRequest(number int) map[string]any {
	return map[string]any{
		"id": 700 + number, "number": number, "html_url": fmt.Sprintf("https://github.com/octo/renamed/pull/%d", number),
		"base": map[string]any{"repo": map[string]any{"id": 123, "full_name": "octo/renamed"}},
	}
}

func githubAssignTestCall(id string, pullRequest int) model.ToolCall {
	return model.ToolCall{
		ID:    id,
		Name:  toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationAssignPullRequest),
		Input: json.RawMessage(fmt.Sprintf(`{"repository_id":123,"pull_request":%d}`, pullRequest)),
	}
}

func seedOtherGitHubOwner(t *testing.T, f integrationToolFixture, ref string) uuid.UUID {
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
	return other.Agent.ID
}

func seedGitHubAssignment(t *testing.T, f integrationToolFixture, pullRequest int) {
	t.Helper()
	_, err := f.Store.Integrations().CreateAssignedIntegrationSubscription(t.Context(),
		integrationstore.CreateAssignedIntegrationSubscriptionInput{
			CreateIntegrationSubscriptionInput: integrationstore.CreateIntegrationSubscriptionInput{
				OrgID: f.Agent.OrgID, ProjectID: f.Agent.ProjectID, IntegrationID: f.Install.ID, AgentID: f.Agent.ID,
				Conversation: json.RawMessage(fmt.Sprintf(`{"repository_id":123,"pull_request":%d}`, pullRequest)),
			},
			ExpectedConfigID: f.AgentConfig.ID, RuntimeLockID: f.Lock.ID,
		})
	require.NoError(t, err)
}

func githubAssignedPullRequest(t *testing.T, f integrationToolFixture) string {
	t.Helper()
	address, found, err := f.Store.Integrations().
		GetAgentIntegrationConversation(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Install.ID)
	require.NoError(t, err)
	require.True(t, found)
	return address.Ref
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

func TestGitHubAssignPullRequest(t *testing.T) {
	for _, test := range []struct {
		name        string
		pullRequest int
		seed        func(*testing.T, integrationToolFixture) uuid.UUID
		check       func(*testing.T, integrationToolFixture, uuid.UUID)
	}{
		{
			name: "assigns and owns a reachable pull request", pullRequest: 9,
			check: func(t *testing.T, f integrationToolFixture, _ uuid.UUID) {
				require.Equal(t, []string{"123#9"}, githubSubscribedPullRequests(t, f))
				require.Equal(t, f.Agent.ID, githubPullRequestOwner(t, f, "123#9"))
			},
		},
		{
			name: "earlier pull requests stay attached", pullRequest: 8,
			seed: func(t *testing.T, f integrationToolFixture) uuid.UUID {
				seedGitHubAssignment(t, f, 9)
				return uuid.Nil
			},
			check: func(t *testing.T, f integrationToolFixture, _ uuid.UUID) {
				require.ElementsMatch(t, []string{"123#8", "123#9"}, githubSubscribedPullRequests(t, f))
				require.Equal(t, f.Agent.ID, githubPullRequestOwner(t, f, "123#8"))
				require.Equal(t, f.Agent.ID, githubPullRequestOwner(t, f, "123#9"))
			},
		},
		{
			name: "assigning again does not duplicate", pullRequest: 8,
			seed: func(t *testing.T, f integrationToolFixture) uuid.UUID {
				seedGitHubAssignment(t, f, 8)
				return uuid.Nil
			},
			check: func(t *testing.T, f integrationToolFixture, _ uuid.UUID) {
				require.Equal(t, []string{"123#8"}, githubSubscribedPullRequests(t, f))
			},
		},
		{
			name: "an existing attachment becomes the owner", pullRequest: 7,
			check: func(t *testing.T, f integrationToolFixture, _ uuid.UUID) {
				require.Equal(t, f.Agent.ID, githubPullRequestOwner(t, f, "123#7"))
			},
		},
		{
			name: "another agent keeps its pull request", pullRequest: 10,
			seed: func(t *testing.T, f integrationToolFixture) uuid.UUID {
				return seedOtherGitHubOwner(t, f, "123#10")
			},
			check: func(t *testing.T, f integrationToolFixture, other uuid.UUID) {
				require.Equal(t, []string{"123#10"}, githubSubscribedPullRequests(t, f))
				require.Equal(t, other, githubPullRequestOwner(t, f, "123#10"))
			},
		},
		{
			name: "a planned launch keeps its reservation", pullRequest: 11,
			seed: func(t *testing.T, f integrationToolFixture) uuid.UUID {
				var reservation uuid.UUID
				require.NoError(t, f.Pool.QueryRow(t.Context(), `INSERT INTO integration_inbox
					(project_id, integration_id, receipt_key, payload, state, plan)
					VALUES ($1, $2, 'planned-launch', 'x'::bytea, 'queued', jsonb_build_object('recipients',
						jsonb_build_object('launch', jsonb_build_object('launch_claim', jsonb_build_object(
							'integration_id', $3::text, 'launch_key', 'default',
							'address', jsonb_build_object('kind', 'pull_request', 'ref', '123#11'))))))
					RETURNING id`, f.Agent.ProjectID, f.Install.ID, f.Install.ID.String()).Scan(&reservation))
				return reservation
			},
			check: func(t *testing.T, f integrationToolFixture, reservation uuid.UUID) {
				require.Equal(t, uuid.Nil, githubPullRequestOwner(t, f, "123#11"))
				var state string
				require.NoError(t, f.Pool.QueryRow(t.Context(),
					`SELECT state FROM integration_inbox WHERE id=$1`, reservation).Scan(&state))
				require.Equal(t, "queued", state)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-assign", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true,
			})
			var seeded uuid.UUID
			if test.seed != nil {
				seeded = test.seed(t, f)
			}
			path := fmt.Sprintf("/repos/octo/renamed/pulls/%d", test.pullRequest)
			server := githubToolTestServer(t, "read", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, path, r.URL.Path)
				writeToolTestJSON(w, githubAssignTestPullRequest(test.pullRequest))
			})
			call := githubAssignTestCall("assign", test.pullRequest)
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
			require.Equal(t, fmt.Sprintf("123#%d", test.pullRequest), githubAssignedPullRequest(t, f))
			test.check(t, f, seeded)
		})
	}
}

func TestGitHubAssignPullRequestMustBeCalledAlone(t *testing.T) {
	comment := model.ToolCall{
		ID: "comment", Name: toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationDiscussionComment),
		Input: json.RawMessage(`{"body":"Ready"}`),
	}
	for _, test := range []struct {
		name  string
		calls []model.ToolCall
	}{
		{"comment then assign", []model.ToolCall{comment, githubAssignTestCall("assign", 8)}},
		{"assign then comment", []model.ToolCall{githubAssignTestCall("assign", 8), comment}},
		{"two assignments", []model.ToolCall{githubAssignTestCall("assign", 8), githubAssignTestCall("assign-again", 9)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-assign-alone", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true,
			})
			var posts atomic.Int32
			server := githubToolTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/repos/octo/renamed/issues/7/comments", r.URL.Path)
				posts.Add(1)
				writeToolTestJSON(w, map[string]any{"id": 501, "html_url": "https://github.com/octo/renamed/pull/7#c"})
			})
			f.recordToolCalls(t, ctx, test.calls, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			for _, call := range test.calls {
				result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
				require.NoError(t, err)
				record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				if call.ID == comment.ID {
					require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
					continue
				}
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Contains(t, toolResultMapFromTestParts(t, result.ContentParts)["message"],
					"without other chat tool calls in the same response")
			}
			require.Equal(t, "123#7", githubAssignedPullRequest(t, f), "a rejected assignment must not move the PR")
			require.Empty(t, integrationToolSubscriptions(t, f))
			if test.calls[0].ID == comment.ID || test.calls[1].ID == comment.ID {
				require.EqualValues(t, 1, posts.Load(), "the comment must post on the assigned PR")
			}
		})
	}
}

func TestGitHubToolApprovalRejectsChangedAssignment(t *testing.T) {
	for _, reassign := range []bool{false, true} {
		t.Run(fmt.Sprintf("reassign=%t", reassign), func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-assign-approval", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true, githubPermission: toolpermission.ModeAlwaysAsk,
			})
			call := f.recordPendingToolCall(t, ctx, "comment",
				toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationDiscussionComment),
				`{"body":"Ready"}`, f.Now)
			var posts atomic.Int32
			server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				assert.Equal(t, "/repos/octo/renamed/issues/7/comments", r.URL.Path)
				writeToolTestJSON(w, map[string]any{"id": 501, "html_url": "https://github.com/octo/renamed/pull/7#c"})
			})
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			turn := f.turn()
			require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
			permission := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID),
				executionstore.AgentInteractionKindPermission)
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
			if reassign {
				tx, err := f.Pool.Begin(ctx)
				require.NoError(t, err)
				require.NoError(t, f.Store.Integrations().AssignAgentIntegrationConversationTx(ctx, tx,
					f.Agent.ProjectID, f.Agent.ID, f.Install.ID,
					integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#8"}))
				require.NoError(t, tx.Commit(ctx))
			}
			_, err = dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			if reassign {
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Zero(t, posts.Load(), "an approval for one pull request must not post to another")
				return
			}
			require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
			require.EqualValues(t, 1, posts.Load())
		})
	}
}

func TestGitHubAssignPullRequestRechecksAuthorityInTransaction(t *testing.T) {
	for _, scenario := range []string{"config changed", "lease lost"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-assign-race", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true,
			})
			server := githubToolTestServer(t, "read", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/repos/octo/renamed/pulls/8", r.URL.Path)
				writeToolTestJSON(w, githubAssignTestPullRequest(8))
			})
			call := githubAssignTestCall("assign", 8)
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
			}, 5*time.Second, 10*time.Millisecond, "assignment must wait on the conversation lock")
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
				t.Fatal("timed out waiting for assignment")
			}
			address, found, err := f.Store.Integrations().
				GetAgentIntegrationConversation(ctx, f.Agent.ProjectID, f.Agent.ID, f.Install.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "123#7", address.Ref, "a revoked call must not move the assignment")
			require.Empty(t, integrationToolSubscriptions(t, f), "a revoked call must not subscribe")
			if scenario == "config changed" {
				record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Contains(t, toolResultMapFromTestParts(t, record.ResultContentParts)["message"], "agent config changed")
			}
		})
	}
}

func TestGitHubAssignPullRequestRequiresCredentialAccess(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-assign-credential", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	server := githubToolTestServer(t, "read", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octo/renamed/pulls/8", r.URL.Path)
		_, err := f.Pool.Exec(ctx, `UPDATE secrets SET deleted_at=now() WHERE id=$1`, f.Install.CredentialSecretID)
		assert.NoError(t, err)
		writeToolTestJSON(w, githubAssignTestPullRequest(8))
	})
	call := githubAssignTestCall("assign", 8)
	f.recordToolCalls(t, ctx, []model.ToolCall{call}, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
	require.NoError(t, err)
	record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	require.Contains(t, toolResultMapFromTestParts(t, result.ContentParts)["message"], "integration access changed")
	address, found, err := f.Store.Integrations().
		GetAgentIntegrationConversation(ctx, f.Agent.ProjectID, f.Agent.ID, f.Install.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "123#7", address.Ref, "revoked credential access must not move the assignment")
	require.Empty(t, integrationToolSubscriptions(t, f), "revoked credential access must not subscribe")
}
