//go:build integration

package tools

import (
	"encoding/json"
	"regexp"
	"sync"
	"testing"

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
	put("b.md", "other\n")
	put("paged.md", "1\n2\n3\n")
	const a, b, paged = "/memory/engineering/a.md", "/memory/engineering/b.md", "/memory/engineering/paged.md"
	steps := []struct{ id, name, input string }{
		{"first_read", toolcatalog.ToolNameReadFile, `{"path":"` + a + `"}`},
		{"unchanged_read", toolcatalog.ToolNameReadFile, `{"path":"` + a + `"}`},
		{"stale_write", toolcatalog.ToolNameWriteFile, `{"path":"` + a + `","content":"stale"}`},
		{"changed_read", toolcatalog.ToolNameReadFile, `{"path":"` + a + `"}`},
		{"fresh_write", toolcatalog.ToolNameWriteFile, `{"path":"` + a + `","content":"two"}`},
		{"second_write", toolcatalog.ToolNameWriteFile, `{"path":"` + a + `","content":" more","append":true}`},
		{"unread_write", toolcatalog.ToolNameWriteFile, `{"path":"` + b + `","content":"x"}`},
		{"forced_write", toolcatalog.ToolNameWriteFile, `{"path":"` + b + `","content":"x","force":true}`},
		{"create", toolcatalog.ToolNameWriteFile, `{"path":"/memory/engineering/new.md","content":"x"}`},
		{"paged_read", toolcatalog.ToolNameReadFile, `{"path":"` + paged + `","offset_line":2,"limit_lines":1}`},
		{"paged_write", toolcatalog.ToolNameWriteFile, `{"path":"` + paged + `","content":"4\n"}`},
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
	for _, name := range []string{toolcatalog.ToolNameReadFile, toolcatalog.ToolNameWriteFile} {
		turn.Tools[name] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)}
	}
	executor := Executor{Store: fixture.Store}
	run := func(id string) (string, map[string]any) {
		t.Helper()
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, calls[id])
		require.NoError(t, err)
		var parts []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Value map[string]any `json:"value"`
		}
		require.NoError(t, json.Unmarshal(result.ContentParts, &parts))
		note := ""
		if parts[0].Type == "text" {
			note, parts = parts[0].Text, parts[1:]
		}
		require.Len(t, parts, 1)
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
	fails("stale_write", quote(a)+" was updated at "+at+" after your last read at "+last+
		"; read it again before writing")
	note, value := run("changed_read")
	require.Regexp(t, "^note: "+quote(a)+" was updated at "+at+" since your last read at "+last+"$", note)
	require.Equal(t, "changed\n", value["content"])
	succeeds("fresh_write")
	succeeds("second_write")
	fails("unread_write", quote(b)+" exists but you have not read it; read it before writing")
	succeeds("forced_write")
	succeeds("create")
	succeeds("paged_read")
	succeeds("paged_write")
	_, content, err := memories.Read(ctx, scope, store.ID, "a.md")
	require.NoError(t, err)
	require.Equal(t, "two more", string(content))

	direct := asyncToolContext{Executor: executor, Turn: turn}
	write := func(agent asyncToolContext, content string) error {
		agent.Call = model.ToolCall{
			Name: toolcatalog.ToolNameWriteFile, Input: json.RawMessage(`{"path":"` + a + `","content":"` + content + `"}`),
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
	require.Regexp(t, "^"+quote(a)+" was updated at ", failures[0].Error())

	child, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: toolsTestProjectID, AgentConfigID: fixture.AgentConfig.ID,
		LaunchedBy: toolsTestUserPrincipal(fixture.User.ID),
		Subagent:   &executionstore.SubagentLaunch{ParentAgentID: fixture.Agent.ID, Key: "fork", MaxDepth: 4},
	})
	require.NoError(t, err)
	subagent := direct
	subagent.Turn.AgentID = child.Agent.ID
	require.EqualError(t, write(subagent, "C"), a+" exists but you have not read it; read it before writing")
	subagent.Call = model.ToolCall{Name: toolcatalog.ToolNameReadFile, Input: json.RawMessage(`{"path":"` + a + `"}`)}
	result, err := runReadFileAsync(ctx, subagent)
	require.NoError(t, err)
	require.NotContains(t, string(asyncCompletionContent(t, result)), "note:")
}
