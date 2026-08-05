//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func TestAgentInputAdmissionUsesItsCausalEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "input_admission_causal_event")
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"first"}]`),
			IdempotencyKey: "input-admission-causal-event",
		},
	)
	if err != nil {
		t.Fatalf("create input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found || len(admitted.Inputs) != 1 || len(admitted.Events) != 1 || admitted.Inputs[0].ID != input.ID {
		t.Fatalf("admitted turn = %+v found=%v, want input %s", admitted, found, input.ID)
	}
	admittedInput := admitted.Inputs[0]
	admittedEvent := admitted.Events[0]
	if admittedInput.AdmittedAt == nil || !admittedInput.AdmittedAt.Equal(admittedEvent.At) {
		t.Fatalf("input admitted_at = %v, want event created_at %s", admittedInput.AdmittedAt, admittedEvent.At)
	}
	if admittedInput.ResolvedAt == nil || !admittedInput.ResolvedAt.Equal(admittedEvent.At) {
		t.Fatalf("input resolved_at = %v, want event created_at %s", admittedInput.ResolvedAt, admittedEvent.At)
	}

	unrelatedInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"second"}]`),
			IdempotencyKey: "input-admission-unrelated-event",
		},
	)
	if err != nil {
		t.Fatalf("create unrelated input: %v", err)
	}
	_, err = fixture.Store.q.AdmitAgentInput(ctx, dbsqlc.AdmitAgentInputParams{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              unrelatedInput.ID,
		AdmittedEventID: admittedEvent.ID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("admit input with unrelated event error = %v, want %v", err, pgx.ErrNoRows)
	}
	var state string
	var admittedEventID *ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT state, admitted_event_id
FROM agent_inputs
WHERE project_id = $1
  AND agent_id = $2
  AND id = $3
`, testProjectID, fixture.AgentID, unrelatedInput.ID).Scan(&state, &admittedEventID); err != nil {
		t.Fatalf("load unrelated input after rejected admission: %v", err)
	}
	if state != "received" || admittedEventID != nil {
		t.Fatalf("unrelated input state=%q admitted_event_id=%v, want received with no event", state, admittedEventID)
	}
}
