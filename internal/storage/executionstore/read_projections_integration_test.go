//go:build integration

package executionstore_test

import (
	"context"
	"testing"
)

func TestSemanticReadProjectionsDeriveOwningScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "semantic_read_projection_scope")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"semantic_read_projection_scope",
		"read_process",
	)
	interaction := createQuestionInteractionForTest(t, ctx, fixture, toolCallID)

	var eventID, eventOrgID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT projection.id, projection.org_id
FROM agent_event_read_projection projection
WHERE projection.agent_id = $1
ORDER BY projection.sequence, projection.id
LIMIT 1
`, fixture.AgentID).Scan(&eventID, &eventOrgID); err != nil {
		t.Fatalf("read event projection scope: %v", err)
	}
	if eventOrgID != fixture.OrgID {
		t.Fatalf("event projection org scope = %s, want %s", eventOrgID, fixture.OrgID)
	}

	projections := []struct {
		name  string
		query string
		id    ID
	}{
		{
			name: "event",
			query: `SELECT project_id FROM agent_event_read_projection
WHERE agent_id = $1 AND id = $2`,
			id: eventID,
		},
		{
			name: "tool call",
			query: `SELECT project_id FROM tool_call_read_projection
WHERE agent_id = $1 AND id = $2`,
			id: toolCallID,
		},
		{
			name: "interaction",
			query: `SELECT project_id FROM agent_interaction_read_projection
WHERE agent_id = $1 AND id = $2`,
			id: interaction.ID,
		},
	}
	for _, projection := range projections {
		t.Run(projection.name, func(t *testing.T) {
			var projectID ID
			if err := fixture.Store.pool.QueryRow(
				ctx,
				projection.query,
				fixture.AgentID,
				projection.id,
			).Scan(&projectID); err != nil {
				t.Fatalf("read derived project scope: %v", err)
			}
			if projectID != testProjectID {
				t.Fatalf("derived project scope = %s, want %s", projectID, testProjectID)
			}
		})
	}
}
