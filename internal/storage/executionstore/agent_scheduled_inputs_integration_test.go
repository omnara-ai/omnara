//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type scheduledInputFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	store   *Store
	userID  uuid.UUID
	agentID uuid.UUID
}

func newScheduledInputFixture(t *testing.T, email string) scheduledInputFixture {
	t.Helper()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectOperatorUser(t, ctx, store, email, "Scheduled Inputs")
	return scheduledInputFixture{
		t: t, pool: pool, store: store, userID: user.ID, agentID: mustCreateAgent(t, ctx, store),
	}
}

func (f scheduledInputFixture) create(
	label string,
	mode executionstore.AgentInputDeliveryMode,
	scheduledFor *time.Time,
) (executionstore.AgentInputRecord, error) {
	f.t.Helper()
	input, _, _, err := f.store.Execution().CreateAgentContentInput(f.t.Context(),
		executionstore.CreateAgentContentInputInput{
			ProjectID:          testProjectID,
			AgentID:            f.agentID,
			Actor:              mustOmnaraActorParams(f.t, f.userID),
			ContentBlocks:      json.RawMessage(`[{"type":"text","text":"` + label + `"}]`),
			DeliveryMode:       mode,
			IdempotencyKey:     "idem-scheduled-" + label,
			SourceScheduledFor: scheduledFor,
		})
	return input, err
}

func (f scheduledInputFixture) mustCreate(
	label string,
	mode executionstore.AgentInputDeliveryMode,
	scheduledFor *time.Time,
) executionstore.AgentInputRecord {
	f.t.Helper()
	input, err := f.create(label, mode, scheduledFor)
	if err != nil {
		f.t.Fatalf("create %s input: %v", label, err)
	}
	return input
}

func (f scheduledInputFixture) state(inputID uuid.UUID) string {
	f.t.Helper()
	var state string
	err := f.pool.QueryRow(f.t.Context(), `SELECT state FROM agent_inputs WHERE id = $1`, inputID).Scan(&state)
	if err != nil {
		f.t.Fatalf("load input state: %v", err)
	}
	return state
}

func (f scheduledInputFixture) wakeups() int {
	f.t.Helper()
	var count int
	if err := f.pool.QueryRow(f.t.Context(),
		`SELECT count(*)::integer FROM agent_wakeups WHERE agent_id = $1`, f.agentID).Scan(&count); err != nil {
		f.t.Fatalf("count wakeups: %v", err)
	}
	return count
}

func (f scheduledInputFixture) list(
	limit int,
	after executionstore.AgentInputQueueCursor,
) executionstore.ListQueuedBacklogInputsResult {
	f.t.Helper()
	page, err := f.store.Execution().ListQueuedBacklogInputs(f.t.Context(), executionstore.ListQueuedBacklogInputsInput{
		ProjectID: testProjectID, AgentID: f.agentID, Limit: limit, After: after,
	})
	if err != nil {
		f.t.Fatalf("list backlog: %v", err)
	}
	return page
}

func TestScheduledAgentInputIsHeldUntilReleased(t *testing.T) {
	t.Parallel()
	f := newScheduledInputFixture(t, "scheduled-release@example.com")
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	past := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)

	later := f.mustCreate("later", executionstore.DeliveryModeQueued, &future)
	if later.State != executionstore.AgentInputStateScheduled {
		t.Fatalf("scheduled input state = %q, want scheduled", later.State)
	}
	if later.ScheduledFor == nil || !later.ScheduledFor.Equal(future) {
		t.Fatalf("scheduled_for = %v, want %v", later.ScheduledFor, future)
	}
	if got := f.wakeups(); got != 0 {
		t.Fatalf("wakeups after scheduling = %d, want 0", got)
	}
	replay, err := f.create("later", executionstore.DeliveryModeQueued, &future)
	if err != nil || replay.ID != later.ID || replay.ScheduledFor == nil || !replay.ScheduledFor.Equal(future) {
		t.Fatalf("idempotent replay = %+v, %v; want input %s", replay, err, later.ID)
	}

	due := f.mustCreate("due", executionstore.DeliveryModeQueued, &past)
	result, err := f.store.Execution().ReleaseDueScheduledAgentInputs(f.t.Context(), 100)
	if err != nil {
		t.Fatalf("release due inputs: %v", err)
	}
	if result.Due != 1 || result.Released != 1 {
		t.Fatalf("release result = %+v, want one due and released", result)
	}
	if got := f.state(due.ID); got != "received" {
		t.Fatalf("due input state = %q, want received", got)
	}
	if got := f.state(later.ID); got != "scheduled" {
		t.Fatalf("future input state = %q, want scheduled", got)
	}
	if got := f.wakeups(); got != 1 {
		t.Fatalf("wakeups after release = %d, want 1", got)
	}
	again, err := f.store.Execution().ReleaseDueScheduledAgentInputs(f.t.Context(), 100)
	if err != nil || again.Due != 0 || again.Released != 0 {
		t.Fatalf("second release = %+v, %v; want nothing due", again, err)
	}

	page := f.list(10, executionstore.AgentInputQueueCursor{})
	if len(page.Inputs) != 2 || page.Inputs[0].ID != due.ID || page.Inputs[1].ID != later.ID {
		t.Fatalf("backlog = %+v, want released input then scheduled input", page.Inputs)
	}
	if page.Inputs[0].ScheduledFor == nil || !page.Inputs[0].ScheduledFor.Equal(past) {
		t.Fatalf("released scheduled_for = %v, want %v", page.Inputs[0].ScheduledFor, past)
	}
}

func TestScheduledAgentInputsListAfterWaitingInputsByDueTime(t *testing.T) {
	t.Parallel()
	f := newScheduledInputFixture(t, "scheduled-list@example.com")
	soon := time.Now().Add(time.Hour)
	later := time.Now().Add(2 * time.Hour)

	scheduledLater := f.mustCreate("scheduled-later", executionstore.DeliveryModeSteering, &later)
	scheduledSoon := f.mustCreate("scheduled-soon", executionstore.DeliveryModeQueued, &soon)
	queued := f.mustCreate("queued", executionstore.DeliveryModeQueued, nil)
	steering := f.mustCreate("steering", executionstore.DeliveryModeSteering, nil)
	if rank := loadAgentInputRank(t, f.t.Context(), f.pool, queued.ID); rank != backlogRankStride {
		t.Fatalf("queued rank = %d, want %d: scheduled inputs must not occupy the backlog", rank, backlogRankStride)
	}

	want := []uuid.UUID{steering.ID, queued.ID, scheduledSoon.ID, scheduledLater.ID}
	var got []uuid.UUID
	var cursor executionstore.AgentInputQueueCursor
	for range want {
		page := f.list(1, cursor)
		if len(page.Inputs) != 1 {
			t.Fatalf("page length = %d, want 1 (after %v)", len(page.Inputs), got)
		}
		last := page.Inputs[0]
		got = append(got, last.ID)
		cursor = executionstore.AgentInputQueueCursor{
			Set: true, DeliveryMode: last.DeliveryMode, InputRank: last.InputRank, QueuedAt: last.QueuedAt, ID: last.ID,
		}
		if last.State == executionstore.AgentInputStateScheduled {
			cursor.ScheduledFor = last.ScheduledFor
		}
		if page.HasMore != (len(got) < len(want)) {
			t.Fatalf("HasMore = %v after %d of %d inputs", page.HasMore, len(got), len(want))
		}
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paginated order = %v, want %v", got, want)
		}
	}
}

func TestScheduledAgentInputCancelAndBacklogChanges(t *testing.T) {
	t.Parallel()
	f := newScheduledInputFixture(t, "scheduled-cancel@example.com")
	future := time.Now().Add(time.Hour)
	scheduled := f.mustCreate("scheduled", executionstore.DeliveryModeQueued, &future)
	queued := f.mustCreate("queued", executionstore.DeliveryModeQueued, nil)

	err := f.store.Execution().MoveQueuedBacklogInput(f.t.Context(), executionstore.MoveQueuedBacklogInputInput{
		ProjectID: testProjectID, AgentID: f.agentID, InputID: scheduled.ID,
		Position: executionstore.MoveQueuedBacklogInputBefore, AnchorInputID: queued.ID,
	})
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("move scheduled input error = %v, want state transition conflict", err)
	}
	err = f.store.Execution().PromoteQueuedInputToSteering(f.t.Context(), executionstore.PromoteQueuedInputToSteeringInput{
		ProjectID: testProjectID, AgentID: f.agentID, InputID: scheduled.ID,
	})
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("promote scheduled input error = %v, want state transition conflict", err)
	}
	_, err = f.pool.Exec(f.t.Context(),
		`UPDATE agent_inputs SET scheduled_for = scheduled_for + interval '1 hour' WHERE id = $1`, scheduled.ID)
	assertPgErrorMessage(t, err, "25006", "agent_input intent and identity are immutable")

	if err := f.store.Execution().CancelQueuedBacklogInput(f.t.Context(), executionstore.CancelQueuedBacklogInputInput{
		ProjectID: testProjectID, AgentID: f.agentID, InputID: scheduled.ID,
	}); err != nil {
		t.Fatalf("cancel scheduled input: %v", err)
	}
	if got := f.state(scheduled.ID); got != "canceled" {
		t.Fatalf("canceled scheduled input state = %q, want canceled", got)
	}
	page := f.list(10, executionstore.AgentInputQueueCursor{})
	if len(page.Inputs) != 1 || page.Inputs[0].ID != queued.ID {
		t.Fatalf("backlog after cancel = %+v, want only the queued input", page.Inputs)
	}

	_, err = f.create("steering-cancel", executionstore.DeliveryModeSteering, &future)
	if err != nil {
		t.Fatalf("create scheduled steering input: %v", err)
	}
	_, _, _, err = f.store.Execution().CreateAgentContentInput(f.t.Context(), executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: f.agentID, Actor: mustOmnaraActorParams(t, f.userID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"x"}]`), DeliveryMode: executionstore.DeliveryModeSteering,
		CancelOpenInteractions: true, SourceScheduledFor: &future,
	})
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("scheduled cancel_open_interactions error = %v, want invalid request", err)
	}
}

func TestScheduledAgentInputsCountTowardBacklogCapacityAndArchiveCancelsThem(t *testing.T) {
	t.Parallel()
	f := newScheduledInputFixture(t, "scheduled-capacity@example.com")
	future := time.Now().Add(time.Hour)
	var first executionstore.AgentInputRecord
	for i := range executionpolicy.MaxPendingAgentContentInputs {
		input := f.mustCreate("capacity-"+uuid.NewString(), executionstore.DeliveryModeQueued, &future)
		if i == 0 {
			first = input
		}
	}
	_, err := f.create("overflow", executionstore.DeliveryModeQueued, nil)
	if !errors.Is(err, storeerr.ErrAgentBacklogFull) {
		t.Fatalf("queued input over capacity error = %v, want backlog full", err)
	}
	_, err = f.create("overflow-scheduled", executionstore.DeliveryModeQueued, &future)
	if !errors.Is(err, storeerr.ErrAgentBacklogFull) {
		t.Fatalf("scheduled input over capacity error = %v, want backlog full", err)
	}

	_, _, err = f.store.Execution().ArchiveAgent(f.t.Context(), testProjectID, f.agentID, userPrincipal(f.userID))
	if err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if got := f.state(first.ID); got != "canceled" {
		t.Fatalf("scheduled input state after archive = %q, want canceled", got)
	}
	var remaining int
	if err := f.pool.QueryRow(f.t.Context(),
		`SELECT count(*)::integer FROM agent_inputs WHERE agent_id = $1 AND state = 'scheduled'`, f.agentID,
	).Scan(&remaining); err != nil {
		t.Fatalf("count scheduled inputs: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("scheduled inputs after archive = %d, want 0", remaining)
	}
}
