//go:build integration

package tools

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/omnara-ai/omnara/internal/webaccess/telemlineage"
)

func TestWebSearchLineageWalksSpawnChain(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithOptions(
		t, ctx, "web-search-lineage", toolFixtureOptions{withSubagents: true},
	)
	spawnCall := fixture.recordToolCall(
		t, ctx, "call_lineage_spawn", "spawn_agent",
		`{"agent":"fork","task":"Research the question.","name":"researcher"}`,
		fixture.Now.Add(20*time.Second),
	)
	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"spawn_agent": {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)},
	}
	if _, err := (Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}).Dispatch(ctx, turn, spawnCall); err != nil {
		t.Fatalf("dispatch spawn_agent: %v", err)
	}
	subagents, err := fixture.Store.Execution().ListSubagents(ctx, toolsTestProjectID, fixture.Agent.ID)
	if err != nil || len(subagents) != 1 {
		t.Fatalf("list subagents = %+v, %v; want one", subagents, err)
	}

	log := slog.New(slog.DiscardHandler)
	parent := loadWebSearchLineage(ctx, fixture.Store, toolsTestProjectID, telemlineage.Call{
		AgentID:            fixture.Agent.ID,
		ModelCallContextID: fixture.ModelCallContextID,
		ToolCallID:         fixture.toolCallID(t, ctx, spawnCall.ID),
	}, log)
	if parent.Goal != "send an integration reply" {
		t.Fatalf("parent goal = %q, want the text of its first message", parent.Goal)
	}
	if parent.Call.CreatedAt.IsZero() || len(parent.Ancestors) != 0 {
		t.Fatalf("parent lineage = %+v, want a created time and no ancestors", parent)
	}

	// The child's own tool call is unknown, so its created time is missing,
	// but its goal and its link to the parent's spawn call still load.
	child := loadWebSearchLineage(ctx, fixture.Store, toolsTestProjectID, telemlineage.Call{
		AgentID:    subagents[0].AgentID,
		ToolCallID: uuid.New(),
	}, log)
	if !child.Call.CreatedAt.IsZero() {
		t.Fatalf("child call = %+v, want no created time", child.Call)
	}
	if child.Goal != "Research the question." {
		t.Fatalf("child goal = %q, want the spawn task", child.Goal)
	}
	if len(child.Ancestors) != 1 || child.Ancestors[0] != parent.Call {
		t.Fatalf("child ancestors = %+v, want the parent's spawn call %+v", child.Ancestors, parent.Call)
	}
}
