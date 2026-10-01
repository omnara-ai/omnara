//go:build integration

package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubPendingReviewJourney(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-draft", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	state, summary := "", ""
	comments := []map[string]any{}
	mutations := 0
	review := func() map[string]any {
		value := map[string]any{
			"id": 80, "node_id": "PRR_80", "state": state, "commit_id": "abc123", "body": summary,
			"user":             map[string]any{"id": 999, "login": "helper[bot]", "type": "Bot"},
			"html_url":         "https://github.com/octo/renamed/pull/7#pullrequestreview-80",
			"pull_request_url": "https://api.github.com/repos/octo/renamed/pulls/7",
		}
		if state == "COMMENTED" {
			value["submitted_at"] = time.Now().UTC()
		}
		return value
	}
	server := githubToolTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/octo/renamed/pulls/7/reviews" && r.Method == http.MethodGet:
			assert.Equal(t, "30", r.URL.Query().Get("per_page"))
			reviews := []any{}
			if state != "" {
				reviews = append(reviews, review())
			}
			writeToolTestJSON(w, reviews)
		case r.URL.Path == "/graphql":
			var input struct {
				Query     string
				Variables map[string]any
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) {
				return
			}
			assert.Contains(t, input.Query, "addPullRequestReviewThread")
			assert.Equal(t, "PENDING", state)
			args, ok := input.Variables["input"].(map[string]any)
			if !assert.True(t, ok) {
				return
			}
			assert.Equal(t, "PRR_80", args["pullRequestReviewId"])
			assert.NotContains(t, args, "pullRequestId")
			assert.Equal(t, "RIGHT", args["side"])
			assert.Equal(t, float64(7), args["startLine"])
			assert.Equal(t, "RIGHT", args["startSide"])
			id := 101 + len(comments)
			comments = append(comments, map[string]any{
				"id": id, "body": args["body"], "path": args["path"], "line": args["line"],
				"pull_request_review_id": 80, "pull_request_url": "https://api.github.com/repos/octo/renamed/pulls/7",
			})
			mutations++
			writeToolTestJSON(w, map[string]any{"data": map[string]any{"addPullRequestReviewThread": map[string]any{
				"thread": map[string]any{"comments": map[string]any{"nodes": []any{map[string]any{
					"fullDatabaseId": strconv.Itoa(id), "state": "PENDING", "pullRequestReview": map[string]string{"fullDatabaseId": "80"}, "url": fmt.Sprintf("https://github.com/octo/renamed/pull/7#discussion_r%d", id),
				}}}},
			}}})
		case r.URL.Path == "/repos/octo/renamed/pulls/7/reviews" && r.Method == http.MethodPost:
			var payload map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				return
			}
			assert.Equal(t, map[string]any{"commit_id": "abc123"}, payload)
			assert.Empty(t, state)
			state = "PENDING"
			mutations++
			writeToolTestJSON(w, review())
		case r.URL.Path == "/repos/octo/renamed/pulls/7/reviews/80" && r.Method == http.MethodGet:
			writeToolTestJSON(w, review())
		case r.URL.Path == "/repos/octo/renamed/pulls/7/reviews/80/comments" && r.Method == http.MethodGet:
			assert.Equal(t, "1", r.URL.Query().Get("per_page"))
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			assert.NoError(t, err)
			assert.GreaterOrEqual(t, page, 1)
			assert.LessOrEqual(t, page, len(comments))
			if page < len(comments) {
				w.Header().Set("Link", fmt.Sprintf(
					"<https://api.github.com/repositories/123/pulls/7/reviews/80/comments?page=%d&per_page=1>; rel=\"next\"",
					page+1))
			}
			writeToolTestJSON(w, comments[page-1:page])
		case r.URL.Path == "/repos/octo/renamed/pulls/7/reviews/80/events" && r.Method == http.MethodPost:
			var payload struct {
				Event string
				Body  string
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				return
			}
			assert.Equal(t, "COMMENT", payload.Event)
			assert.Equal(t, "PENDING", state)
			assert.Len(t, comments, 2)
			state, summary = "COMMENTED", payload.Body
			mutations++
			writeToolTestJSON(w, review())
		default:
			t.Errorf("unexpected provider operation %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	})
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	proposals := []model.ToolCall{}
	for i, args := range [][2]string{
		{"read", `{"section":"pending_review"}`},
		{"start_review", `{"commit_id":"abc123"}`},
		{"review_comment", `{"review_id":80,"body":"First finding","path":"file.go","line":9,` +
			`"side":"RIGHT","start_line":7,"start_side":"RIGHT"}`},
		{"review_comment", `{"review_id":80,"body":"Second finding","path":"file.go","line":9,` +
			`"side":"RIGHT","start_line":7,"start_side":"RIGHT"}`},
		{"read", `{"section":"pending_review"}`},
		{"read", `{"section":"review_comments","review_id":80,"page":1,"limit":1}`},
		{"read", `{"section":"review_comments","review_id":80,"page":2,"limit":1}`},
		{"submit_review", `{"review_id":80,"body":"Two findings to address."}`},
		{"read", `{"section":"review","review_id":80}`},
		{"read", `{"section":"pending_review"}`},
	} {
		proposals = append(proposals, model.ToolCall{
			ID: fmt.Sprintf("review-%d", i+1), Name: "int__chat__" + args[0], Input: json.RawMessage(args[1]),
		})
	}
	f.recordToolCalls(t, ctx, proposals, f.Now)
	sequence := 0
	run := func(operation, input string) (model.ToolCall, map[string]any) {
		t.Helper()
		call := proposals[sequence]
		sequence++
		require.Equal(t, "int__chat__"+operation, call.Name)
		require.JSONEq(t, input, string(call.Input))
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
		require.NoError(t, err)
		record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, string(result.ContentParts))
		return call, toolResultMapFromTestParts(t, result.ContentParts)
	}
	_, pending := run("read", `{"section":"pending_review"}`)
	require.Nil(t, pending["review"])
	_, started := run("start_review", `{"commit_id":"abc123"}`)
	require.Equal(t, float64(80), started["id"])
	require.Equal(t, "PENDING", started["state"])
	for _, body := range []string{"First finding", "Second finding"} {
		_, comment := run("review_comment", fmt.Sprintf(`{"review_id":80,"body":%q,"path":"file.go","line":9,"side":"RIGHT",`+
			`"start_line":7,"start_side":"RIGHT"}`, body))
		require.Equal(t, float64(80), comment["review_id"])
		require.Equal(t, "PENDING", comment["state"])
		require.Equal(t, "PENDING", state)
	}
	executor = Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	_, pending = run("read", `{"section":"pending_review"}`)
	pendingReview, ok := pending["review"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(80), pendingReview["id"])
	_, first := run("read", `{"section":"review_comments","review_id":80,"page":1,"limit":1}`)
	require.Equal(t, float64(2), first["next_page"])
	firstComments, ok := first["comments"].([]any)
	require.True(t, ok)
	require.Len(t, firstComments, 1)
	firstComment, ok := firstComments[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "First finding", firstComment["body"])
	require.Equal(t, "file.go", firstComment["path"])
	require.Equal(t, float64(9), firstComment["line"])
	_, last := run("read", `{"section":"review_comments","review_id":80,"page":2,"limit":1}`)
	require.NotContains(t, last, "next_page")
	require.Len(t, last["comments"], 1)
	lastComments, ok := last["comments"].([]any)
	require.True(t, ok)
	lastComment, ok := lastComments[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Second finding", lastComment["body"])
	require.Equal(t, "file.go", lastComment["path"])
	require.Equal(t, float64(9), lastComment["line"])
	call, published := run("submit_review", `{"review_id":80,"body":"Two findings to address."}`)
	require.Equal(t, "COMMENTED", published["state"])
	require.Equal(t, "Two findings to address.", summary)
	_, inspected := run("read", `{"section":"review","review_id":80}`)
	require.Equal(t, "COMMENTED", inspected["state"])
	require.Equal(t, summary, inspected["body"])
	_, pending = run("read", `{"section":"pending_review"}`)
	require.Nil(t, pending["review"])
	_, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
	require.NoError(t, err)
	require.Equal(t, 4, mutations, "tool replay must not create or publish duplicate reviews")
}

func TestGitHubDiscardReviewIsExplicitAndReplaySafe(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-discard", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	deletes := 0
	server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octo/renamed/pulls/7/reviews/80", r.URL.Path)
		switch r.Method {
		case http.MethodGet:
		case http.MethodDelete:
			deletes++
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
		writeToolTestJSON(w, map[string]any{
			"id": 80, "state": "PENDING", "commit_id": "abc123",
			"pull_request_url": "https://api.github.com/repos/octo/renamed/pulls/7",
		})
	})
	call := f.recordToolCall(t, ctx, "discard", "int__chat__discard_review", `{"review_id":80}`, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"id": float64(80), "deleted": true},
		toolResultMapFromTestParts(t, result.ContentParts))
	replay, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
	require.NoError(t, err)
	require.JSONEq(t, string(result.ContentParts), string(replay.ContentParts))
	require.Equal(t, 1, deletes)
}

func TestGitHubReviewSelectorsRejectAmbiguousArguments(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-choice", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	calls := []model.ToolCall{
		{ID: "neither", Name: "int__chat__review_comment", Input: json.RawMessage(
			`{"body":"finding","path":"a.go","line":1,"side":"RIGHT"}`)},
		{ID: "both", Name: "int__chat__review_comment", Input: json.RawMessage(
			`{"body":"finding","path":"a.go","line":1,"side":"RIGHT","commit_id":"abc","review_id":80}`)},
		{ID: "wrong-section", Name: "int__chat__read", Input: json.RawMessage(
			`{"section":"discussion_comments","review_id":80}`)},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid arguments reached GitHub: %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
		require.NoError(t, err)
		body := toolResultMapFromTestParts(t, result.ContentParts)
		require.Equal(t, "integration_tool_failed", body["code"])
		if call.Name == "int__chat__read" {
			require.Contains(t, body["message"], "review_id is only supported")
		} else {
			require.Contains(t, body["message"], "exactly one of commit_id")
		}
	}
}

func TestGitHubDraftCommentFailureGuidance(t *testing.T) {
	for _, scenario := range []struct {
		name, response, code, hint string
	}{
		{"rejected", `{"data":{"addPullRequestReviewThread":null},` +
			`"errors":[{"type":"UNPROCESSABLE","message":"private-detail"}]}`,
			"permanent_failure", "check path, line, side"},
		{"unknown", `{"data":{"addPullRequestReviewThread":{"thread":null}}}`,
			"delivery_unknown", "review commit diff"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-guidance", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true,
			})
			mutations := 0
			server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					assert.Equal(t, "/repos/octo/renamed/pulls/7/reviews/80", r.URL.Path)
					writeToolTestJSON(w, map[string]any{
						"id": 80, "node_id": "PRR_80", "state": "PENDING", "commit_id": "abc123",
						"pull_request_url": "https://api.github.com/repos/octo/renamed/pulls/7",
					})
					return
				}
				assert.Equal(t, "/graphql", r.URL.Path)
				mutations++
				fmt.Fprint(w, scenario.response)
			})
			call := f.recordToolCall(t, ctx, "finding", "int__chat__review_comment",
				`{"review_id":80,"body":"finding","path":"a.go","line":1,"side":"RIGHT"}`, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			require.Equal(t, scenario.code, body["code"])
			require.Contains(t, body["message"], scenario.hint)
			require.NotContains(t, body["message"], "private-detail")
			if scenario.code == "delivery_unknown" {
				require.Contains(t, body["message"], "Read back the comment or review before retrying")
			}
			require.Equal(t, 1, mutations)
		})
	}
}
