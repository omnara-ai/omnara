//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

func TestWebFetchOverflowRetrieval(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "fetch-overflow", false,
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)))
	var page strings.Builder
	for index := range 1000 {
		fmt.Fprintf(&page, "TARGET %04d %s\n", index, strings.Repeat("é", 30))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(page.String()))
	}))
	defer server.Close()
	input, err := json.Marshal(map[string]string{"url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	call := startOverflowTestCall(t, &fixture, "web_fetch", string(input))
	call.Executor.WebFetcher = webaccess.NewFetcher(webaccess.FetcherOptions{AllowLoopback: true})
	if err := call.Executor.executeAsyncTool(ctx, call, toolHandler{Async: runWebFetch}); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, call.ToolCallID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != executionstore.ToolResultOutcomeSucceeded ||
		len(result.ResultContentParts) > executionstore.ToolResultInlineBudgetBytes {
		t.Fatalf("fetch result: %+v", result)
	}
	var parts []struct {
		Value struct {
			Path       string `json:"path"`
			StatusCode int    `json:"status_code"`
		} `json:"value"`
	}
	if err := json.Unmarshal(result.ResultContentParts, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Value.Path == "" || parts[2].Value.StatusCode != http.StatusOK {
		t.Fatalf("missing path or inline metadata: %s", result.ResultContentParts)
	}
	path := parts[0].Value.Path
	for _, test := range []struct {
		name, input string
		run         asyncToolHandler
		offsetLine  int
	}{
		{"search_files", `{"path":"` + path + `","pattern":"^TARGET","max_matches":20}`, runSearchFilesAsync, 1},
		{"search_files", `{"path":"` + path + `","pattern":"^TARGET","offset_line":21,"max_matches":20}`,
			runSearchFilesAsync, 21},
		{"read_file", `{"path":"` + path + `","offset_line":2,"limit_lines":1}`, runReadFileAsync, 0},
	} {
		call.Call = model.ToolCall{Name: test.name, Input: json.RawMessage(test.input)}
		dispatch, err := test.run(ctx, call)
		if err != nil {
			t.Fatal(err)
		}
		raw := asyncCompletionContent(t, dispatch)
		var content []struct {
			Value struct {
				MatchCount     int    `json:"match_count"`
				Truncated      bool   `json:"truncated"`
				Content        string `json:"content"`
				NextOffsetLine int    `json:"next_offset_line"`
				Matches        []struct {
					MatchLine int `json:"match_line"`
				} `json:"matches"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &content); err != nil {
			t.Fatal(err)
		}
		if test.name == "search_files" && (content[0].Value.MatchCount != 20 || !content[0].Value.Truncated ||
			content[0].Value.NextOffsetLine != test.offsetLine+20 || len(content[0].Value.Matches) != 20 ||
			content[0].Value.Matches[0].MatchLine != test.offsetLine) {
			t.Fatalf("search did not find separate original lines: %s", raw)
		}
		if test.name == "read_file" && content[0].Value.Content != "TARGET 0001 "+strings.Repeat("é", 30)+"\n" {
			t.Fatalf("read did not preserve original lines: %s", raw)
		}
	}
}

type failingOverflowBlobStore struct {
	blobstore.Store
	cancel    context.CancelFunc
	beforePut func()
	puts      int
}

func (s *failingOverflowBlobStore) PutBlob(_ context.Context, _ string, _ []byte) (blobstore.Metadata, error) {
	s.puts++
	if s.beforePut != nil {
		s.beforePut()
	}
	if s.cancel != nil {
		s.cancel()
	}
	return blobstore.Metadata{}, errors.New("private blob backend failure")
}

func TestAsyncToolOverflowPersistenceFailure(t *testing.T) {
	for _, outcome := range []executionstore.ToolResultOutcome{
		executionstore.ToolResultOutcomeSucceeded, executionstore.ToolResultOutcomeFailed,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := t.Context()
			completionCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			blobs := &failingOverflowBlobStore{cancel: cancel}
			fixture := newIntegrationToolFixtureWithMCP(t, ctx, "overflow-failure", false, storage.WithBlobStore(blobs))
			call := startOverflowTestCall(t, &fixture, "web_fetch", `{"url":"https://example.com"}`)
			content := newToolResultContent(textToolResultPart(strings.Repeat("x", 60000)))
			var err error
			if outcome == executionstore.ToolResultOutcomeSucceeded {
				err = call.Executor.completeAsyncToolResult(
					completionCtx, call.Turn, call.ToolCallID, content, outcome,
				)
			} else {
				err = call.Executor.completeAsyncToolFailure(
					completionCtx, call.Turn, call.ToolCallID, content, errors.New("tool failed"),
				)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, call.ToolCallID)
			if err != nil {
				t.Fatal(err)
			}
			expected := "The tool ran successfully, but its result could not be stored."
			if outcome == executionstore.ToolResultOutcomeFailed {
				expected = "The tool failed, but its error result could not be stored."
			}
			if result.Outcome != executionstore.ToolResultOutcomeFailed ||
				!strings.Contains(string(result.ResultContentParts), expected) ||
				strings.Contains(string(result.ResultContentParts), "private blob") || blobs.puts != 1 {
				t.Fatalf("unexpected fallback: puts=%d result=%s", blobs.puts, result.ResultContentParts)
			}
			if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
				ctx, toolsTestProjectID, fixture.Agent.ID, fixture.Lock.ID,
			); err != nil {
				t.Fatal(err)
			}
			after, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, call.ToolCallID)
			if err != nil || string(after.ResultContentParts) != string(result.ResultContentParts) {
				t.Fatalf("runtime cleanup changed the fallback: %v", err)
			}
		})
	}
}

func TestAsyncToolOverflowMissingCall(t *testing.T) {
	ctx := t.Context()
	blobs := &failingOverflowBlobStore{}
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "missing-call", false, storage.WithBlobStore(blobs))
	missingID := fixture.Agent.ID
	_, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, missingID)
	if !errors.Is(err, storeerr.ErrNotFound) || retryableAsyncToolPersistenceError(ctx, err) {
		t.Fatalf("missing tool call should not be retried: %v", err)
	}
	executor := Executor{Store: fixture.Store}
	content := newToolResultContent(textToolResultPart(strings.Repeat("x", 60000)))
	err = executor.completeAsyncToolResult(
		ctx, fixture.turn(), missingID, content, executionstore.ToolResultOutcomeSucceeded,
	)
	if !errors.Is(err, storeerr.ErrNotFound) || blobs.puts != 0 {
		t.Fatalf("missing tool completion: puts=%d error=%v", blobs.puts, err)
	}
}

func TestAsyncToolInvalidResult(t *testing.T) {
	for _, test := range []struct {
		name    string
		part    toolResultPart
		outcome executionstore.ToolResultOutcome
	}{
		{"nul", textToolResultPart("private\x00output"), executionstore.ToolResultOutcomeSucceeded},
		{"oversized", textToolResultPart(strings.Repeat("x", toolcatalog.MaxReadableArtifactBytes+1)),
			executionstore.ToolResultOutcomeFailed},
		{"invalid artifact ID", toolResultMediaPart{}, executionstore.ToolResultOutcomeSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			blobs := &failingOverflowBlobStore{}
			fixture := newIntegrationToolFixtureWithMCP(t, ctx, "invalid-result", false, storage.WithBlobStore(blobs))
			call := startOverflowTestCall(t, &fixture, "web_fetch", `{"url":"https://example.com"}`)
			content := newToolResultContent(test.part)
			if err := call.Executor.completeAsyncToolResult(ctx, call.Turn, call.ToolCallID, content, test.outcome); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, call.ToolCallID)
			if err != nil {
				t.Fatal(err)
			}
			var parts []struct {
				Value struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"value"`
			}
			if err := json.Unmarshal(result.ResultContentParts, &parts); err != nil {
				t.Fatal(err)
			}
			if result.State != executionstore.ToolCallStateCompleted ||
				result.Outcome != executionstore.ToolResultOutcomeFailed ||
				len(result.ResultContentParts) > executionstore.ToolResultInlineBudgetBytes || len(parts) != 1 ||
				parts[0].Value.Code != "tool_result_invalid" ||
				parts[0].Value.Message != "The tool returned an invalid result." ||
				blobs.puts != 0 {
				t.Fatalf("unexpected invalid-result fallback: puts=%d result=%+v", blobs.puts, result)
			}
		})
	}
}

func TestAsyncToolOverflowFailureRechecksOwnership(t *testing.T) {
	ctx := t.Context()
	completionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	blobs := &failingOverflowBlobStore{cancel: cancel}
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "overflow-owner", false, storage.WithBlobStore(blobs))
	call := startOverflowTestCall(t, &fixture, "web_fetch", `{"url":"https://example.com"}`)
	blobs.beforePut = func() {
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx, toolsTestProjectID, fixture.Agent.ID, fixture.Lock.ID,
		); err != nil {
			t.Fatal(err)
		}
	}
	content := newToolResultContent(textToolResultPart(strings.Repeat("x", 60000)))
	if err := call.Executor.completeAsyncToolResult(
		completionCtx, call.Turn, call.ToolCallID, content, executionstore.ToolResultOutcomeSucceeded,
	); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("completion after ownership loss: %v", err)
	}
	result, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, call.ToolCallID)
	if err != nil || strings.Contains(string(result.ResultContentParts), "tool_result_persistence_failed") {
		t.Fatalf("fallback replaced runtime recovery: %v", err)
	}
}

func startOverflowTestCall(t *testing.T, fixture *integrationToolFixture, name, input string) asyncToolContext {
	t.Helper()
	ctx := t.Context()
	call := fixture.recordToolCall(t, ctx, "overflow_call", name, input, fixture.Now.Add(time.Second))
	id := fixture.toolCallID(t, ctx, call.ID)
	_, err := fixture.Store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
		ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, ToolCallID: id, RuntimeLockID: fixture.Lock.ID,
	}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
		return executionstore.StartToolCallAsync(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return asyncToolContext{Executor: Executor{Store: fixture.Store}, Turn: fixture.turn(), Call: call, ToolCallID: id}
}
