//go:build integration

package tools

import (
	"encoding/json"
	"regexp"
	"sync"
	"testing"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestMemoryReadTracking(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "memory-tracking",
		toolFixtureOptions{withMemory: true, memoryReadWrite: true, withSubagents: true})
	memories := fixture.Store.Memories()
	scope := memorystore.Scope{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Principal: toolsTestUserPrincipal(fixture.User.ID),
	}
	store, err := memories.Resolve(ctx, toolsTestProjectID, "engineering")
	require.NoError(t, err)
	put := func(file, content string) {
		t.Helper()
		var expected *string
		if digest, _, err := memories.Read(ctx, scope, store.ID, file); err == nil {
			expected = &digest
		}
		_, err := memories.Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: store.ID, Path: file, Content: []byte(content), ExpectedDigest: expected,
		})
		require.NoError(t, err)
	}
	put("a.md", "one\n")
	put("paged.md", "1\n2\n3\n")
	put("gone.md", "old\n")
	const a, paged, gone = "/memory/engineering/a.md", "/memory/engineering/paged.md", "/memory/engineering/gone.md"
	steps := []struct{ id, name, input string }{
		{"first_read", toolcatalog.ToolNameStorageRead, `{"path":"` + a + `"}`},
		{"unchanged_read", toolcatalog.ToolNameStorageRead, `{"path":"` + a + `"}`},
		{"stale_write", toolcatalog.ToolNameStorageWrite, `{"path":"` + a + `","content":"stale"}`},
		{"stale_append", toolcatalog.ToolNameStorageWrite, `{"path":"` + a + `","content":"stale","append":true}`},
		{"changed_read", toolcatalog.ToolNameStorageRead, `{"path":"` + a + `"}`},
		{"fresh_write", toolcatalog.ToolNameStorageWrite, `{"path":"` + a + `","content":"two"}`},
		{"second_write", toolcatalog.ToolNameStorageWrite, `{"path":"` + a + `","content":" more","append":true}`},
		{"create", toolcatalog.ToolNameStorageWrite, `{"path":"/memory/engineering/new.md","content":"x"}`},
		{"paged_read", toolcatalog.ToolNameStorageRead, `{"path":"` + paged + `","offset_line":2,"limit_lines":1}`},
		{"paged_write", toolcatalog.ToolNameStorageWrite, `{"path":"` + paged + `","content":"4\n"}`},
		{"gone_read", toolcatalog.ToolNameStorageRead, `{"path":"` + gone + `"}`},
		{"gone_write", toolcatalog.ToolNameStorageWrite, `{"path":"` + gone + `","content":"new"}`},
		{"download", toolcatalog.ToolNameStorageDownload, `{"path":"` + a + `","destination":"a.md"}`},
		{"after_download_write", toolcatalog.ToolNameStorageWrite, `{"path":"` + a + `","content":"edited"}`},
	}
	calls := map[string]model.ToolCall{}
	batch := make([]model.ToolCall, 0, len(steps))
	for _, step := range steps {
		call := model.ToolCall{ID: step.id, Name: step.name, Input: json.RawMessage(step.input)}
		calls[step.id] = call
		batch = append(batch, call)
	}
	fixture.recordToolCalls(t, ctx, batch, fixture.Now)
	turn := fixture.turn()
	for _, name := range []string{toolcatalog.ToolNameStorageRead, toolcatalog.ToolNameStorageWrite} {
		turn.Tools[name] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)}
	}
	executor := Executor{Store: fixture.Store}
	run := func(id string) (string, map[string]any) {
		t.Helper()
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, calls[id])
		require.NoError(t, err)
		var parts []struct {
			Type    string         `json:"type"`
			Text    string         `json:"text"`
			Value   map[string]any `json:"value"`
			Exclude bool           `json:"exclude_from_model_context"`
		}
		require.NoError(t, json.Unmarshal(result.ContentParts, &parts))
		note := ""
		if parts[0].Type == "text" {
			note, parts = parts[0].Text, parts[1:]
		}
		if len(parts) == 2 {
			require.True(t, parts[1].Exclude, "digest must stay out of model context")
			require.NotEmpty(t, parts[1].Value["digest"])
			require.NotContains(t, parts[0].Value, "digest")
		} else {
			require.Len(t, parts, 1)
			require.Contains(t, parts[0].Value, "error")
		}
		return note, parts[0].Value
	}
	succeeds := func(id string) {
		t.Helper()
		note, value := run(id)
		require.Empty(t, note, id)
		require.NotContains(t, value, "error", id)
	}
	fails := func(id, pattern string) {
		t.Helper()
		_, value := run(id)
		require.Regexp(t, "^"+pattern+"$", value["error"], id)
	}
	const at = `\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UTC`
	const last = `(\d{4}-\d{2}-\d{2} )?\d{2}:\d{2}:\d{2} UTC`
	quote := regexp.QuoteMeta

	succeeds("first_read")
	succeeds("unchanged_read")
	put("a.md", "changed\n")
	stale := quote(a) + " was updated at " + at + " after your last read at " + last + "; read it again before writing"
	fails("stale_write", stale)
	fails("stale_append", stale)
	note, value := run("changed_read")
	require.Regexp(t, "^note: "+quote(a)+" was updated at "+at+" since your last read at "+last+"$", note)
	require.Equal(t, "changed\n", value["content"])
	succeeds("fresh_write")
	succeeds("second_write")
	succeeds("create")
	succeeds("paged_read")
	succeeds("paged_write")
	succeeds("gone_read")
	goneDigest, _, err := memories.Read(ctx, scope, store.ID, "gone.md")
	require.NoError(t, err)
	require.NoError(t, memories.DeleteFile(ctx, scope, store.ID, "gone.md", goneDigest))
	succeeds("gone_write")
	for file, want := range map[string]string{"a.md": "two more", "gone.md": "new"} {
		_, content, err := memories.Read(ctx, scope, store.ID, file)
		require.NoError(t, err)
		require.Equal(t, want, string(content), file)
	}

	// A memory download counts as reading the file it copied.
	put("a.md", "downloaded\n")
	visible, err := structuredToolResultPart(map[string]any{"destination": "a.md"})
	require.NoError(t, err)
	hidden, err := fileDigestToolResultPart(blobstore.ContentDigest([]byte("downloaded\n")))
	require.NoError(t, err)
	require.NoError(t, executor.completeAsyncToolResult(ctx, turn, fixture.toolCallID(t, ctx, "download"),
		newToolResultContent(visible, hidden), executionstore.ToolResultOutcomeSucceeded))
	succeeds("after_download_write")

	direct := asyncToolContext{Executor: executor, Turn: turn}
	write := func(agent asyncToolContext, content string) error {
		agent.Call = model.ToolCall{
			Name: toolcatalog.ToolNameStorageWrite, Input: json.RawMessage(`{"path":"` + a + `","content":"` + content + `"}`),
		}
		_, err := runWriteFileAsync(ctx, agent)
		return err
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, content := range []string{"A", "B"} {
		wg.Go(func() { results <- write(direct, content) })
	}
	wg.Wait()
	close(results)
	var failures []error
	for err := range results {
		if err != nil {
			failures = append(failures, err)
		}
	}
	require.Len(t, failures, 1)
	require.ErrorIs(t, failures[0], storeerr.ErrConflict)
	// The loser fails its read check if the winner committed first, or the store's compare-and-swap
	// if both passed the check before either wrote.
	require.Regexp(t, "^"+quote(a)+" (was updated at |changed while writing; )", failures[0].Error())

	child, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: toolsTestProjectID, AgentConfigID: fixture.AgentConfig.ID,
		LaunchedBy: toolsTestUserPrincipal(fixture.User.ID),
		Subagent:   &executionstore.SubagentLaunch{ParentAgentID: fixture.Agent.ID, Key: "fork", MaxDepth: 4},
	})
	require.NoError(t, err)
	subagent := direct
	subagent.Turn.AgentID = child.Agent.ID
	require.EqualError(t, write(subagent, "C"), a+" exists but you have not read it; read it before writing")
	subagent.Call = model.ToolCall{Name: toolcatalog.ToolNameStorageRead, Input: json.RawMessage(`{"path":"` + a + `"}`)}
	result, err := runReadFileAsync(ctx, subagent)
	require.NoError(t, err)
	require.NotContains(t, string(asyncCompletionContent(t, result)), "note:")
}
