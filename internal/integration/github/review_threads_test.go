package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testReviewThread() map[string]any {
	return map[string]any{
		"id": "PRRT_1", "isResolved": false, "isOutdated": true, "path": "file.go", "line": nil,
		"comments": map[string]any{"nodes": []any{map[string]any{"fullDatabaseId": "9007199254740995"}}},
	}
}

func testReviewThreadsResponse(threads []any, more bool, cursor any) map[string]any {
	return map[string]any{"data": map[string]any{"node": map[string]any{
		"__typename": "PullRequest", "id": "PR_7", "fullDatabaseId": "7", "number": 42,
		"reviewThreads": map[string]any{
			"nodes": threads, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor},
		},
	}}}
}

func TestReviewThreadsUseVerifiedNodeAndCursorPages(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/graphql", r.URL.Path)
		assert.Equal(t, "Bearer installation-token", r.Header.Get("Authorization"))
		var input struct {
			Query     string `json:"query"`
			Variables struct {
				ID    string  `json:"id"`
				First int     `json:"first"`
				After *string `json:"after"`
			} `json:"variables"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.True(t, strings.HasPrefix(input.Query, "query "))
		assert.Contains(t, input.Query, "node(id: $id)")
		assert.Contains(t, input.Query, "fullDatabaseId")
		assert.Contains(t, input.Query, "comments(first: 1)")
		assert.Equal(t, "PR_7", input.Variables.ID)
		assert.Equal(t, 1, input.Variables.First)
		thread := testReviewThread()
		more, cursor := true, "opaque-cursor"
		if input.Variables.After != nil {
			assert.Equal(t, cursor, *input.Variables.After)
			thread["id"], thread["isResolved"], thread["isOutdated"], thread["line"] = "PRRT_2", true, false, 12
			thread["comments"] = map[string]any{"nodes": []any{}}
			more, cursor = false, "last-cursor"
		}
		_ = json.NewEncoder(w).Encode(testReviewThreadsResponse([]any{thread}, more, cursor))
	}))
	scope := testScope()
	scope.Owner, scope.Repository = "untrusted-owner", "untrusted-name"
	first, err := client.ListReviewThreads(t.Context(), scope, ReviewThreadsOptions{Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load(), "do not traverse later pages automatically")
	require.Equal(t, "opaque-cursor", first.NextCursor)
	require.Len(t, first.Threads, 1)
	require.False(t, first.Threads[0].IsResolved)
	require.True(t, first.Threads[0].IsOutdated)
	require.Nil(t, first.Threads[0].Line)
	require.Equal(t, int64(9007199254740995), first.Threads[0].CommentID, "REST ID must retain 64-bit precision")
	last, err := client.ListReviewThreads(t.Context(), scope, ReviewThreadsOptions{Limit: 1, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Empty(t, last.NextCursor)
	require.Len(t, last.Threads, 1)
	require.True(t, last.Threads[0].IsResolved)
	require.False(t, last.Threads[0].IsOutdated)
	require.Equal(t, 12, *last.Threads[0].Line)
	require.Zero(t, last.Threads[0].CommentID, "a thread can have no available comments")
	require.EqualValues(t, 2, calls.Load())
}

func TestReviewThreadsRejectPartialMalformedAndWrongScopeResponses(t *testing.T) {
	for _, scenario := range []string{
		"errors with data", "null node", "wrong numeric ID", "wrong PR", "wrong type", "missing ID",
		"missing threads", "too many threads", "missing page info", "missing cursor", "repeated cursor", "oversized cursor",
		"missing resolved", "missing outdated", "missing comment ID", "overflow comment ID", "extra comments",
		"missing comments", "null thread", "invalid JSON",
	} {
		t.Run(scenario, func(t *testing.T) {
			thread := testReviewThread()
			body := testReviewThreadsResponse([]any{thread}, false, nil)
			data := testutil.RequireType[map[string]any](t, body["data"])
			node := testutil.RequireType[map[string]any](t, data["node"])
			connection := testutil.RequireType[map[string]any](t, node["reviewThreads"])
			page := testutil.RequireType[map[string]any](t, connection["pageInfo"])
			code := InvalidResponse
			switch scenario {
			case "errors with data":
				body["errors"] = []any{map[string]any{"message": "private-provider-detail installation-token"}}
			case "null node":
				data["node"] = nil
			case "wrong numeric ID":
				node["fullDatabaseId"], code = "8", ScopeMismatch
			case "wrong PR":
				node["number"], code = 43, ScopeMismatch
			case "wrong type":
				node["__typename"], code = "Issue", ScopeMismatch
			case "missing ID":
				delete(node, "fullDatabaseId")
			case "missing threads":
				delete(connection, "nodes")
			case "too many threads":
				connection["nodes"] = []any{thread, thread}
			case "missing page info":
				delete(connection, "pageInfo")
			case "missing cursor":
				page["hasNextPage"] = true
			case "repeated cursor":
				page["hasNextPage"], page["endCursor"] = true, "previous"
			case "oversized cursor":
				page["hasNextPage"], page["endCursor"] = true, strings.Repeat("x", 4097)
			case "missing resolved":
				delete(thread, "isResolved")
			case "missing outdated":
				delete(thread, "isOutdated")
			case "missing comment ID":
				thread["comments"] = map[string]any{"nodes": []any{map[string]any{}}}
			case "overflow comment ID":
				thread["comments"] = map[string]any{"nodes": []any{map[string]any{"fullDatabaseId": "9223372036854775808"}}}
			case "extra comments":
				thread["comments"] = map[string]any{"nodes": []any{map[string]any{}, map[string]any{}}}
			case "missing comments":
				delete(thread, "comments")
			case "null thread":
				connection["nodes"] = []any{nil}
			}
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, _ *http.Request) {
				if scenario == "invalid JSON" {
					fmt.Fprint(w, `{"data":`)
					return
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			result, err := client.ListReviewThreads(t.Context(), testScope(), ReviewThreadsOptions{Limit: 1, Cursor: "previous"})
			requireAPIError(t, err, code)
			require.Empty(t, result.Threads)
			require.Empty(t, result.NextCursor)
			require.NotContains(t, err.Error(), "private-provider-detail")
			require.NotContains(t, err.Error(), "installation-token")
		})
	}
}

func TestReviewThreadsAcceptChangedGlobalIDEncoding(t *testing.T) {
	body := testReviewThreadsResponse([]any{testReviewThread()}, false, nil)
	data := testutil.RequireType[map[string]any](t, body["data"])
	node := testutil.RequireType[map[string]any](t, data["node"])
	// preparePull returns PR_7, while GraphQL can represent the same node using
	// a newer encoding. Its database ID, type and PR number still match.
	node["id"] = "PR_new_encoding"
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	result, err := client.ListReviewThreads(t.Context(), testScope(), ReviewThreadsOptions{})
	require.NoError(t, err)
	require.Len(t, result.Threads, 1)
}

func TestReviewThreadsValidateBeforeQuery(t *testing.T) {
	client, _ := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input made a request") })
	for _, options := range []ReviewThreadsOptions{
		{Limit: -1}, {Limit: 101}, {Cursor: strings.Repeat("x", 4097)}, {Cursor: "\xff"},
	} {
		_, err := client.ListReviewThreads(t.Context(), testScope(), options)
		require.Error(t, err)
	}
	_, err := client.ListReviewThreads(t.Context(), Scope{}, ReviewThreadsOptions{})
	require.Error(t, err)
	client, _ = testClient(t, withToken(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, pullPath(testRepository(), 42), r.URL.Path)
		fmt.Fprint(w, `{"id":7,"number":42,"base":{"repo":{"id":789}}}`)
	}))
	_, err = client.ListReviewThreads(t.Context(), testScope(), ReviewThreadsOptions{})
	requireAPIError(t, err, InvalidResponse)
}

func TestReviewThreadReadsRetryAndFenceEachRequest(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			var queries atomic.Int32
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/graphql", r.URL.Path)
				if queries.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(testReviewThreadsResponse([]any{}, false, nil))
			}))
			denied := errors.New("credential revoked")
			client.beforeRequest = func(context.Context) error {
				if revoke && queries.Load() != 0 {
					return denied
				}
				return nil
			}
			result, err := client.ListReviewThreads(t.Context(), testScope(), ReviewThreadsOptions{})
			if revoke {
				require.ErrorIs(t, err, denied)
				require.EqualValues(t, 1, queries.Load())
			} else {
				require.NoError(t, err)
				require.NotNil(t, result.Threads)
				require.Empty(t, result.Threads)
				require.EqualValues(t, 2, queries.Load())
			}
		})
	}
}

func TestReviewThreadGraphQLErrorsAndHTTPFailuresAreReads(t *testing.T) {
	for _, scenario := range []string{
		"primary rate limit", "secondary rate limit", "unauthorized", "server error", "transport", "oversized",
	} {
		t.Run(scenario, func(t *testing.T) {
			var queries atomic.Int32
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				queries.Add(1)
				switch scenario {
				case "primary rate limit":
					w.Header().Set("X-Ratelimit-Remaining", "0")
					fmt.Fprint(w, `{"data":null,"errors":[{"message":"private-provider-detail"}]}`)
				case "secondary rate limit":
					w.Header().Set("Retry-After", "23")
					fmt.Fprint(w, `{"errors":[{"message":"private-provider-detail"}]}`)
				case "unauthorized":
					w.WriteHeader(http.StatusUnauthorized)
				case "server error":
					w.WriteHeader(http.StatusInternalServerError)
				case "transport":
					hijacker, ok := w.(http.Hijacker)
					if !assert.True(t, ok, "test server must support connection hijacking") {
						return
					}
					conn, _, err := hijacker.Hijack()
					if assert.NoError(t, err) {
						_ = conn.Close()
					}
				case "oversized":
					_, _ = io.WriteString(w, strings.Repeat("x", ResponseMaxBytes+1))
				}
			}))
			_, err := client.ListReviewThreads(t.Context(), testScope(), ReviewThreadsOptions{})
			code, attempts := InvalidResponse, int32(1)
			switch scenario {
			case "primary rate limit", "secondary rate limit":
				code = RateLimited
			case "unauthorized":
				code = PermanentFailure
				require.Empty(t, client.tokens[0].token)
			case "server error", "transport":
				code, attempts = TransientFailure, 3
			}
			apiErr := requireAPIError(t, err, code)
			require.Equal(t, attempts, queries.Load())
			require.NotContains(t, err.Error(), "private-provider-detail")
			if scenario == "secondary rate limit" {
				require.EqualValues(t, 23, apiErr.RetryAfter.Seconds())
			}
		})
	}
}
