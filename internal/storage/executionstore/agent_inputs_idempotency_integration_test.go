//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCreateAgentContentInputIdempotencyReplayAndConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 18, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "input-idempotency@example.com", "Input Idempotency")

	create := executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"same"}]`),
		IdempotencyKey: "idem-agent-input-replay",
	}
	first, _, _, err := store.Execution().CreateAgentContentInput(ctx, create)
	if err != nil {
		t.Fatalf("create agent content input: %v", err)
	}
	replayed, _, _, err := store.Execution().CreateAgentContentInput(ctx, create)
	if err != nil {
		t.Fatalf("replay agent input: %v", err)
	}
	if replayed.ID != first.ID || replayed.State != first.State || replayed.DeliveryMode != first.DeliveryMode {
		t.Fatalf("idempotent replay changed input: first=%+v replayed=%+v", first, replayed)
	}

	create.ContentBlocks = json.RawMessage(`[{"type":"text","text":"different"}]`)
	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, create)
	if !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting agent input idempotency error = %v, want ErrIdempotencyConflict", err)
	}

	otherUser := mustCreateProjectOperatorUser(t, ctx, store, "input-producer-swap@example.com", "Producer Swap")
	otherActorID := mustEnsureOmnaraActor(t, ctx, store, testOrgID, testProjectID, otherUser.ID)
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_inputs SET actor_id = $3 WHERE project_id = $1 AND id = $2`,
		testProjectID,
		first.ID,
		otherActorID,
	); !isPgCode(err, "25006") {
		t.Fatalf("mutate producer actor error = %v, want SQLSTATE 25006", err)
	}
}

func TestCreateAgentContentInputIdempotencyIgnoresTransientInteractionCancellation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 18, 10, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(
		t,
		ctx,
		store,
		"input-cancel-idempotency@example.com",
		"Input Cancel Idempotency",
	)
	for _, initial := range []bool{false, true} {
		label := "without-cancel"
		if initial {
			label = "with-cancel"
		}
		create := executionstore.CreateAgentContentInputInput{
			ProjectID:              testProjectID,
			AgentID:                agentID,
			Actor:                  mustOmnaraActorParams(t, user.ID),
			ContentBlocks:          json.RawMessage(`[{"type":"text","text":"same command"}]`),
			DeliveryMode:           executionstore.DeliveryModeSteering,
			IdempotencyKey:         "idem-agent-input-" + label,
			CancelOpenInteractions: initial,
		}
		first, _, created, err := store.Execution().CreateAgentContentInput(ctx, create)
		if err != nil || !created {
			t.Fatalf("create %s input: created=%v err=%v", label, created, err)
		}
		replayed, _, created, err := store.Execution().CreateAgentContentInput(ctx, create)
		if err != nil || created || replayed.ID != first.ID {
			t.Fatalf("replay %s input = %+v created=%v err=%v", label, replayed, created, err)
		}
		create.CancelOpenInteractions = !initial
		replayed, _, created, err = store.Execution().CreateAgentContentInput(ctx, create)
		if err != nil || created || replayed.ID != first.ID {
			t.Fatalf(
				"replay %s input with opposite transient option = %+v created=%v err=%v",
				label,
				replayed,
				created,
				err,
			)
		}
	}
}

func TestCreateAgentContentInputRejectsArchivedAgentButReplaysExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 18, 15, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "input-archived@example.com", "Input Archived")

	create := executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"before archive"}]`),
		IdempotencyKey: "idem-agent-input-before-archive",
	}
	first, _, _, err := store.Execution().CreateAgentContentInput(ctx, create)
	if err != nil {
		t.Fatalf("create pre-archive agent input: %v", err)
	}
	if _, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, agentID, userPrincipal(user.ID)); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	replayed, _, created, err := store.Execution().CreateAgentContentInput(ctx, create)
	if err != nil {
		t.Fatalf("replay archived agent input: %v", err)
	}
	if created || replayed.ID != first.ID || replayed.State != "canceled" || replayed.CanceledAt == nil {
		t.Fatalf(
			"archived replay = id %s state %s canceled_at %v created %v, want id %s canceled with timestamp and created false",
			replayed.ID,
			replayed.State,
			replayed.CanceledAt,
			created,
			first.ID,
		)
	}

	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"after archive"}]`),
		IdempotencyKey: "idem-agent-input-after-archive",
	})
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("archived agent input error = %v, want ErrStateTransitionConflict", err)
	}
	var afterArchiveInputs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND input_idempotency_key = $3`, testProjectID, agentID, "idem-agent-input-after-archive").
		Scan(&afterArchiveInputs); err != nil {
		t.Fatalf("count post-archive inputs: %v", err)
	}
	if afterArchiveInputs != 0 {
		t.Fatalf("post-archive inputs = %d, want 0", afterArchiveInputs)
	}
}

func TestCreateAgentContentInputConcurrentIdempotencyReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 18, 30, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "input-concurrent@example.com", "Input Concurrent")

	create := executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"concurrent same key"}]`),
		IdempotencyKey: "idem-agent-input-concurrent-replay",
	}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	ids := make(chan ID, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			input, _, _, err := store.Execution().CreateAgentContentInput(ctx, create)
			if err != nil {
				errs <- err
				return
			}
			ids <- input.ID
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatalf("concurrent create agent content input: %v", err)
	}
	var first ID
	for id := range ids {
		if first == NilID {
			first = id
			continue
		}
		if id != first {
			t.Fatalf("concurrent idempotency returned different input ids: first=%s got=%s", first, id)
		}
	}
	if first == NilID {
		t.Fatal("no concurrent create returned an input id")
	}
	var inputs, blocks int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND input_idempotency_key = $3
`, testProjectID, agentID, create.IdempotencyKey).
		Scan(&inputs); err != nil {
		t.Fatalf("count agent inputs: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM content_blocks block
JOIN agent_inputs input
  ON input.agent_id = block.agent_id
 AND input.id = block.owner_agent_input_id
WHERE input.project_id = $1 AND block.agent_id = $2 AND block.owner_agent_input_id = $3
`, testProjectID, agentID, first).
		Scan(&blocks); err != nil {
		t.Fatalf("count content blocks: %v", err)
	}
	if inputs != 1 || blocks != 1 {
		t.Fatalf("concurrent idempotency wrote inputs=%d blocks=%d, want 1/1", inputs, blocks)
	}
}

func TestAgentWriteActorParamsMaterializeActorsInTx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 20, 30, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "producer-arms@example.com", "Producer Arms")

	omnaraParams := mustOmnaraActorParams(t, user.ID)
	byUser, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          omnaraParams,
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"by user"}]`),
		IdempotencyKey: "idem-producer-arm-user",
	})
	if err != nil {
		t.Fatalf("create content input by omnara params: %v", err)
	}
	omnaraActors, err := store.Execution().ListActors(ctx, executionstore.ListActorsInput{
		ProjectID:        testProjectID,
		Provider:         omnaraParams.Provider,
		ProviderTenantID: omnaraParams.ProviderTenantID,
		ProviderUserID:   omnaraParams.ProviderUserID,
	})
	if err != nil || len(omnaraActors) != 1 {
		t.Fatalf("omnara actors after omnara params input = %+v err=%v, want one actor", omnaraActors, err)
	}
	if byUser.ActorID != omnaraActors[0].ID {
		t.Fatalf("omnara params producer actor = %v, want %v", byUser.ActorID, omnaraActors[0].ID)
	}
	if omnaraActors[0].DisplayName != "Producer Arms" {
		t.Fatalf("omnara actor display name = %q, want user display name", omnaraActors[0].DisplayName)
	}

	byExternal, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID,
		AgentID:   agentID,
		Actor: &executionstore.ActorParams{
			Provider:         executionstore.ActorProviderExternal,
			ProviderTenantID: "producer-arm-tenant",
			ProviderUserID:   "producer-arm-external",
		},
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"by external"}]`),
		IdempotencyKey: "idem-producer-arm-external",
	})
	if err != nil {
		t.Fatalf("create content input by external params: %v", err)
	}
	externals, err := store.Execution().ListActors(ctx, executionstore.ListActorsInput{
		ProjectID:      testProjectID,
		Provider:       executionstore.ActorProviderExternal,
		ProviderUserID: "producer-arm-external",
	})
	if err != nil || len(externals) != 1 {
		t.Fatalf("external params actors = %+v err=%v, want one actor", externals, err)
	}
	if byExternal.ActorID != externals[0].ID {
		t.Fatalf("external params producer actor = %v, want %v", byExternal.ActorID, externals[0].ID)
	}

	cancelResult, err := store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: testProjectID,
		AgentID:   agentID,
		Actor: &executionstore.ActorParams{
			Provider:         executionstore.ActorProviderExternal,
			ProviderTenantID: "producer-arm-tenant",
			ProviderUserID:   "producer-arm-cancel",
		},
	})
	if err != nil {
		t.Fatalf("cancel agent by external params: %v", err)
	}
	cancelActors, err := store.Execution().ListActors(ctx, executionstore.ListActorsInput{
		ProjectID:      testProjectID,
		Provider:       executionstore.ActorProviderExternal,
		ProviderUserID: "producer-arm-cancel",
	})
	if err != nil || len(cancelActors) != 1 {
		t.Fatalf("cancel external params actors = %+v err=%v, want one actor", cancelActors, err)
	}
	if cancelResult.ActorID != cancelActors[0].ID {
		t.Fatalf("cancel producer actor = %v, want %v", cancelResult.ActorID, cancelActors[0].ID)
	}
}

func TestCreateAgentContentInputConflictRollsBackExternalActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 21, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "actor-rollback@example.com", "Actor Rollback")

	create := executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"same"}]`),
		IdempotencyKey: "idem-actor-rollback",
	}
	if _, _, _, err := store.Execution().CreateAgentContentInput(ctx, create); err != nil {
		t.Fatalf("create content input: %v", err)
	}

	stolen := create
	stolen.Actor = &executionstore.ActorParams{
		Provider:         executionstore.ActorProviderExternal,
		ProviderTenantID: "rollback-tenant",
		ProviderUserID:   "rollback-external",
	}
	if _, _, _, err := store.Execution().CreateAgentContentInput(ctx, stolen); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("stolen idempotency key error = %v, want ErrIdempotencyConflict", err)
	}
	leaked, err := store.Execution().ListActors(ctx, executionstore.ListActorsInput{
		ProjectID:      testProjectID,
		Provider:       executionstore.ActorProviderExternal,
		ProviderUserID: "rollback-external",
	})
	if err != nil {
		t.Fatalf("list external actors after conflict: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("conflicting input leaked external actor: %+v", leaked)
	}
}

func TestCreateAgentContentInputReplayDoesNotRewriteActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 21, 30, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	originalName := "Replay Original"
	create := executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID,
		AgentID:   agentID,
		Actor: &executionstore.ActorParams{
			Provider:         executionstore.ActorProviderExternal,
			ProviderTenantID: "replay-tenant",
			ProviderUserID:   "replay-external",
			DisplayName:      &originalName,
			Metadata:         resourcemeta.Metadata{"seat": "original"},
		},
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"same"}]`),
		IdempotencyKey: "idem-actor-replay-no-rewrite",
	}
	first, _, _, err := store.Execution().CreateAgentContentInput(ctx, create)
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	actorBeforeReplay, err := store.Execution().GetActor(ctx, testProjectID, first.ActorID)
	if err != nil {
		t.Fatalf("get actor before replay: %v", err)
	}

	renamed := "Replay Renamed"
	replay := create
	replay.Actor = &executionstore.ActorParams{
		Provider:         executionstore.ActorProviderExternal,
		ProviderTenantID: "replay-tenant",
		ProviderUserID:   "replay-external",
		DisplayName:      &renamed,
		Metadata:         resourcemeta.Metadata{"seat": "changed"},
	}
	replayed, _, created, err := store.Execution().CreateAgentContentInput(ctx, replay)
	if err != nil {
		t.Fatalf("replay content input: %v", err)
	}
	if created || replayed.ID != first.ID {
		t.Fatalf("replay = id %s created %v, want id %s created false", replayed.ID, created, first.ID)
	}

	actor, err := store.Execution().GetActor(ctx, testProjectID, first.ActorID)
	if err != nil {
		t.Fatalf("get actor after replay: %v", err)
	}
	if actor.DisplayName != originalName {
		t.Fatalf("replay rewrote actor display name to %q, want %q", actor.DisplayName, originalName)
	}
	if !sameJSON(actor.Metadata, json.RawMessage(`{"seat":"original"}`)) {
		t.Fatalf("replay rewrote actor metadata to %s", actor.Metadata)
	}
	if !actor.UpdatedAt.Equal(actorBeforeReplay.UpdatedAt) {
		t.Fatalf(
			"replay touched actor updated_at = %v, want unchanged value %v",
			actor.UpdatedAt,
			actorBeforeReplay.UpdatedAt,
		)
	}
}

func TestCreateAgentContentInputAllowsUnattributedInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 20, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	input, _, created, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:     testProjectID,
		AgentID:       agentID,
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"unattributed"}]`),
	})
	if err != nil {
		t.Fatalf("create unattributed content input: %v", err)
	}
	if !created {
		t.Fatal("unattributed content input was not created")
	}
	if input.ActorID != NilID {
		t.Fatalf("unattributed content input producer actor = %v, want nil", input.ActorID)
	}
}
