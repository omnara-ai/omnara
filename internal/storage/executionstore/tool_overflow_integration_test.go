//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type overflowBlobs struct {
	mu       sync.Mutex
	content  map[string][]byte
	puts     int
	fail     bool
	beforeIO func()
}

func (b *overflowBlobs) PutBlob(_ context.Context, key string, content []byte) (blobstore.Metadata, error) {
	if b.beforeIO != nil {
		b.beforeIO()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return blobstore.Metadata{}, errors.New("injected blob failure")
	}
	b.puts++
	b.content[key] = append([]byte(nil), content...)
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *overflowBlobs) GetBlob(_ context.Context, key string) ([]byte, blobstore.Metadata, error) {
	if b.beforeIO != nil {
		b.beforeIO()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	content, ok := b.content[key]
	if !ok {
		return nil, blobstore.Metadata{}, blobstore.ErrNotFound
	}
	return content, blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *overflowBlobs) DeleteBlob(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.content, key)
	return nil
}

func TestToolOverflowMissingToolCall(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	agentID, callID := uuid.New(), uuid.New()
	if _, err := store.Execution().GetToolCall(
		ctx, testProjectID, agentID, callID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing tool call: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := executionstore.IntegrationGetToolCallTx(
		ctx, tx, testProjectID, agentID, callID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing tool call in transaction: %v", err)
	}
}

func TestToolOverflowConcurrentCompletions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	const count = 12
	started := make(chan struct{}, count)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	blobs := &overflowBlobs{content: map[string][]byte{}, beforeIO: func() {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}}
	store := newIntegrationStore(pool, storage.WithBlobStore(blobs))
	user := mustCreateProjectOperatorUser(t, ctx, store, "overflow-concurrent@example.com", "Overflow")
	fixture := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, "overflow-concurrent", time.Now())
	items := make([]processToolCallBatchItem, count)
	for i := range items {
		items[i] = processToolCallBatchItem{
			TestName: fmt.Sprintf("overflow-%d", i), ToolName: "mcp__docs__search",
			ToolType: toolcatalog.ToolTypeMCP, Allowed: true,
		}
	}
	ids := createToolCallBatchForProcessTest(t, ctx, fixture, "overflow-concurrent", items)
	for _, id := range ids {
		claimToolCallForTest(t, ctx, store, fixture.AgentID, id, fixture.Lock.ID, true)
	}
	parts, err := json.Marshal([]map[string]any{{"type": "text", "text": strings.Repeat("x", 64*1024)}})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, count)
	var workers sync.WaitGroup
	defer func() {
		unblock()
		workers.Wait()
	}()
	for _, id := range ids {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ID: id, RuntimeLockID: fixture.Lock.ID,
				Outcome: executionstore.ToolResultOutcomeSucceeded, ResultContentParts: parts,
			})
			results <- err
		}()
	}
	for range count {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("uploads did not all start concurrently")
		}
	}
	if got := pool.Stat().AcquiredConns(); got != 0 {
		t.Fatalf("uploads retained %d database connections", got)
	}
	probe, stopProbe := context.WithTimeout(ctx, time.Second)
	defer stopProbe()
	if _, err := store.Execution().RenewAgentRuntimeLock(
		probe, testProjectID, fixture.AgentID, fixture.Lock.ID, 90*time.Second,
	); err != nil {
		t.Fatalf("uploads blocked lease renewal: %v", err)
	}
	unblock()
	for range count {
		if err := <-results; err != nil {
			t.Fatalf("complete concurrent tool result: %v", err)
		}
	}
	if blobs.puts != count {
		t.Fatalf("got %d uploads, want %d", blobs.puts, count)
	}
}

func TestToolOverflowCompletionAndReplay(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := &overflowBlobs{content: map[string][]byte{}}
	store := newIntegrationStore(pool, storage.WithBlobStore(blobs))
	blobs.beforeIO = func() {
		if got := pool.Stat().AcquiredConns(); got != 0 {
			t.Fatalf("blob I/O retained %d database connections", got)
		}
	}
	user := mustCreateProjectOperatorUser(t, ctx, store, "overflow@example.com", "Overflow")
	fixture := newProcessDaemonFixtureInStore(
		t, ctx, store, user.ID, "overflow", time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC),
	)
	callID := createToolCallForProcessTest(t, ctx, fixture, "overflow_call", "web_fetch")
	text := strings.Repeat("line é\n", 15000)
	parts, err := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	if err != nil {
		t.Fatal(err)
	}
	input := executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 callID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: parts,
	}
	blobs.fail = true
	if _, err := store.Execution().CompleteToolCall(ctx, input); err == nil {
		t.Fatal("expected blob failure")
	}
	blobs.fail = false
	invalidParts, err := json.Marshal([]map[string]any{
		{"type": "text", "text": text},
		{"type": "media_ref", "artifact_id": uuid.NewString()},
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidInput := input
	invalidInput.ResultContentParts = invalidParts
	if _, err := store.Execution().CompleteToolCall(ctx, invalidInput); err == nil {
		t.Fatal("expected attachment admission failure")
	}
	if len(blobs.content) != 1 {
		t.Fatal("failed completion should retain its independently committed artifact")
	}
	completed, err := store.Execution().CompleteToolCall(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []struct {
		Value struct {
			Path string `json:"path"`
		} `json:"value"`
	}
	if err := json.Unmarshal(completed.ResultContentParts, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) == 0 {
		t.Fatal("missing overflow marker")
	}
	id, err := publicid.Decode(publicid.KindArtifact, strings.TrimPrefix(blocks[0].Value.Path, "/artifacts/"))
	if err != nil {
		t.Fatal(err)
	}
	content, _, err := store.Artifacts().GetArtifactBlob(ctx, testProjectID, fixture.AgentID, id)
	if err != nil || string(content) != text {
		t.Fatalf("retained content mismatch: %v", err)
	}
	if _, err := store.Execution().CompleteToolCall(ctx, input); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if blobs.puts != 2 {
		t.Fatalf("duplicate uploads: %d", blobs.puts)
	}
	input.ResultContentParts, err = json.Marshal([]map[string]any{{"type": "text", "text": text + "changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Execution().CompleteToolCall(ctx, input); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay: %v", err)
	}
	if _, _, err := store.Artifacts().GetArtifactBlob(ctx, testProjectID, callID, id); err == nil {
		t.Fatal("cross-agent artifact access succeeded")
	}
	for _, kind := range []string{toolcatalog.ToolTypeMCP, toolcatalog.ToolTypeCustom} {
		name := "read_process"
		if kind == toolcatalog.ToolTypeMCP {
			name = "mcp__docs__search"
		}
		id := createTypedToolCallForProcessTest(t, ctx, fixture, "overflow_"+kind, name, kind, true)
		var completedParts json.RawMessage
		if kind == toolcatalog.ToolTypeMCP {
			claimToolCallForTest(t, ctx, store, fixture.AgentID, id, fixture.Lock.ID, true)
			completion := executionstore.CompleteRuntimeToolCallInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ID:                 id,
				RuntimeLockID:      fixture.Lock.ID,
				Outcome:            executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: parts,
			}
			result, err := store.Execution().CompleteRuntimeToolCall(ctx, completion)
			if err != nil {
				t.Fatal(err)
			}
			completedParts = result.ResultContentParts
			if _, err := store.Execution().CompleteRuntimeToolCall(ctx, completion); err != nil {
				t.Fatal(err)
			}
		} else {
			result, err := store.Execution().CompleteCustomToolCall(ctx, executionstore.CompleteCustomToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ID:            id,
				Outcome:       executionstore.ToolResultOutcomeSucceeded,
				ContentBlocks: parts,
			})
			if err != nil {
				t.Fatal(err)
			}
			completedParts = result.ContentBlocks
		}
		if !strings.Contains(string(completedParts), "/artifacts/") || len(completedParts) > 50*1024 {
			t.Fatalf("%s result was not offloaded", kind)
		}
	}
}

func TestToolOverflowExcludesProcessReadReplay(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := &overflowBlobs{content: map[string][]byte{}}
	store := newIntegrationStore(pool, storage.WithBlobStore(blobs))
	user := mustCreateProjectOperatorUser(t, ctx, store, "overflow-read@example.com", "Overflow")
	fixture := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, "overflow_read", time.Now())
	process := startRunningProcessForReadTest(t, ctx, fixture, "overflow_read", nil)
	callID := createToolCallForProcessActionTest(t, ctx, fixture, "overflow_read_action")
	action, err := createProcessActionForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: callID, RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID: process.ID, ActionKind: executionstore.ProcessActionKindRead, Payload: json.RawMessage(`{"max_bytes":65536}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := store.Execution().AcceptDaemonProcessAction(ctx, executionstore.AcceptDaemonProcessActionInput{
		Authority: fixture.authority(), ProcessID: process.ID, ID: action.ID,
	})
	if err != nil || !found {
		t.Fatalf("accept read: found=%v err=%v", found, err)
	}
	blobs.beforeIO = func() {
		if got := pool.Stat().AcquiredConns(); got != 0 {
			t.Fatalf("blob I/O retained %d database connections", got)
		}
	}
	output := strings.Repeat("x", 60000)
	raw, err := json.Marshal(map[string]any{
		"process_id": publicResourceID(publicid.KindProcess, process.ID), "output": output,
		"cursor": 0, "next_cursor": len(output), "truncated": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := executionstore.CompleteDaemonProcessActionInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ProcessID: process.ID,
		ID: action.ID, Authority: fixture.authority(), Result: raw,
	}
	blobs.fail = true
	for range 2 {
		result, err := store.Execution().ApplyDaemonProcessAction(ctx, input)
		if err != nil || !result.ToolResultCommitted {
			t.Fatalf("read completion: %+v, %v", result, err)
		}
		assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 60000)
	}
	if blobs.puts != 0 {
		t.Fatalf("uploads=%d, want 0", blobs.puts)
	}
	call, err := store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, callID)
	if err != nil || !strings.Contains(string(call.ResultContentParts), output) {
		t.Fatalf("process output was not retained inline: %v", err)
	}
	input.Result = json.RawMessage(strings.Replace(string(raw), output, "y"+output[1:], 1))
	result, err := store.Execution().ApplyDaemonProcessAction(ctx, input)
	if err != nil || result.ToolResultCommitted {
		t.Fatalf("changed read replay accepted: %+v, %v", result, err)
	}
}

func TestQuestionResponseSizeLimit(t *testing.T) {
	const limit = 50 * 1024
	for _, test := range []struct {
		name       string
		size       int
		prompt     string
		text       string
		legacy     bool
		wantFailed bool
	}{
		{name: "below limit", size: limit - 1},
		{name: "at limit", size: limit},
		{name: "above limit", size: limit + 1, wantFailed: true},
		{name: "escaped answer", text: strings.Repeat("<", 10000), wantFailed: true},
		{name: "large prompt", prompt: strings.Repeat("p", limit), wantFailed: true},
		{name: "legacy success", size: limit + 1, legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			blobs := &overflowBlobs{beforeIO: func() { t.Fatal("question completion must not use blob storage") }}
			store := newIntegrationStore(pool, storage.WithBlobStore(blobs))
			user := mustCreateProjectOperatorUser(t, ctx, store, "bounded-question@example.com", "Question")
			fixture := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, "bounded_question", time.Now())
			callID := createToolCallForProcessTest(t, ctx, fixture, "bounded_question_call", "ask_question")
			form := questionInteractionFormForTest(t)
			form.Questions[0].Options[0].AllowsText = true
			if test.prompt != "" {
				form.Questions[0].Prompt = test.prompt
			}
			result, err := store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: callID, RuntimeLockID: fixture.Lock.ID,
			}, func(_ *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.CreateQuestionForToolCall(executionstore.CreateQuestionInteractionInput{Form: form}), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			interaction, ok := result.CommandResult.(executionstore.AgentInteractionRecord)
			if !ok {
				t.Fatalf("question command returned %T", result.CommandResult)
			}
			resultParts := func(answer string) json.RawMessage {
				return mustTestRawJSON(t, []map[string]any{{"type": "structured_data", "value": map[string]any{
					"answers": []map[string]any{{
						"question_index": 0, "question": form.Questions[0].Prompt, "text": answer,
						"selected_options": []map[string]any{{"option_index": 0, "label": "Yes"}},
					}},
				}}})
			}
			answer := "answer"
			if test.size != 0 {
				answer = strings.Repeat("x", test.size-len(resultParts("")))
			} else if test.text != "" {
				answer = test.text
			}
			expectedParts := resultParts(answer)
			input := executionstore.ResolveAgentInteractionInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ID: interaction.ID,
				Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
					OptionIndices: []int{0}, Text: answer,
				}}},
				Actor: mustOmnaraActorParams(t, fixture.UserID),
			}
			if test.legacy {
				q := dbsqlc.New(pool)
				actorID := fixture.omnaraActorID(t, ctx)
				scope, key := "agent_interaction_response", interaction.ID.String()
				response, err := q.InsertInteractionResponseAgentInput(ctx, dbsqlc.InsertInteractionResponseAgentInputParams{
					ProjectID: testProjectID, AgentID: fixture.AgentID, TargetInteractionID: interaction.ID,
					ActorID:          &actorID,
					IdempotencyScope: &scope, InputIdempotencyKey: &key,
					Metadata: json.RawMessage(`{}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := q.ResolveAgentInteraction(ctx, dbsqlc.ResolveAgentInteractionParams{
					ProjectID: testProjectID, AgentID: fixture.AgentID, ID: interaction.ID,
					Resolution:        mustTestRawJSON(t, input.Resolution),
					ResolvedByInputID: &response.ID,
				}); err != nil {
					t.Fatal(err)
				}
				var blocks []struct{ Value json.RawMessage }
				if err := json.Unmarshal(expectedParts, &blocks); err != nil {
					t.Fatal(err)
				}
				forceToolCallResultForTest(t, ctx, store, testProjectID, fixture.AgentID, callID,
					executionstore.ToolResultOutcomeSucceeded, blocks[0].Value)
			}
			for range 2 {
				if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
					t.Fatal(err)
				}
				resolved, err := store.Execution().ResolveAgentInteraction(ctx, input)
				if err != nil || resolved.State != executionstore.AgentInteractionStateResolved {
					t.Fatalf("question resolution: state=%s, error=%v", resolved.State, err)
				}
				assertJSONRawEqual(t, resolved.Resolution, string(mustTestRawJSON(t, input.Resolution)))
				if got := countAgentWakeups(t, ctx, store, fixture.AgentID); got != 1 {
					t.Fatalf("wakeups=%d, want 1", got)
				}
				call, err := store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, callID)
				if err != nil || call.State != executionstore.ToolCallStateCompleted {
					t.Fatalf("question completion: state=%s, error=%v", call.State, err)
				}
				if test.wantFailed {
					if call.Outcome != executionstore.ToolResultOutcomeFailed || len(call.ResultContentParts) > limit {
						t.Fatalf("outcome=%s, bytes=%d", call.Outcome, len(call.ResultContentParts))
					}
					if !strings.Contains(string(call.ResultContentParts), "request a shorter answer") {
						t.Fatalf("missing retry instructions: %s", call.ResultContentParts)
					}
				} else {
					if call.Outcome != executionstore.ToolResultOutcomeSucceeded {
						t.Fatalf("outcome=%s, want succeeded", call.Outcome)
					}
					assertJSONRawEqual(t, call.ResultContentParts, string(expectedParts))
				}
			}
			input.Resolution.Answers[0].Text += "changed"
			_, err = store.Execution().ResolveAgentInteraction(ctx, input)
			if !errors.Is(err, storeerr.ErrIdempotencyConflict) {
				t.Fatalf("conflicting replay: %v", err)
			}
		})
	}
}

func TestToolOverflowRechecksArchiveDuringUpload(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := &overflowBlobs{content: map[string][]byte{}}
	store := newIntegrationStore(pool, storage.WithBlobStore(blobs))
	user := mustCreateProjectOperatorUser(t, ctx, store, "overflow-archive@example.com", "Overflow")
	fixture := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, "overflow_archive", time.Now())
	callID := createToolCallForProcessTest(t, ctx, fixture, "overflow_archive_call", "web_fetch")
	blobs.beforeIO = func() {
		if got := pool.Stat().AcquiredConns(); got != 0 {
			t.Fatalf("blob I/O retained %d database connections", got)
		}
		if _, _, err := store.Execution().ArchiveAgent(
			ctx, testProjectID, fixture.AgentID, userPrincipal(user.ID),
		); err != nil {
			t.Fatal(err)
		}
	}
	parts, err := json.Marshal([]map[string]any{{"type": "text", "text": strings.Repeat("result", 10000)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ID: callID,
		RuntimeLockID: fixture.Lock.ID, Outcome: executionstore.ToolResultOutcomeSucceeded, ResultContentParts: parts,
	})
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("archived completion: %v", err)
	}
	call, err := store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, callID)
	if err != nil || call.Outcome != executionstore.ToolResultOutcomeCanceled {
		t.Fatalf("archive outcome overwritten: %+v, %v", call, err)
	}
	if len(blobs.content) != 0 {
		t.Fatal("rejected artifact upload was not cleaned up")
	}
}

func TestToolOverflowStructuredReplayPreservesJSONEquality(t *testing.T) {
	for _, name := range []string{
		"structured", "multiple large blocks", "text with metadata", "hidden text block", "runtime",
		"inline boundary", "runtime inline boundary",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			blobs := &overflowBlobs{content: map[string][]byte{}}
			store := newIntegrationStore(pool, storage.WithBlobStore(blobs))
			user := mustCreateProjectOperatorUser(t, ctx, store, "overflow-json@example.com", "Overflow")
			fixture := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, "overflow_json", time.Now())
			callID := createToolCallForProcessTest(t, ctx, fixture, "overflow_json_call", "web_fetch")
			runtime := name == "runtime" || name == "runtime inline boundary"
			if runtime {
				claimToolCallForTest(t, ctx, store, fixture.AgentID, callID, fixture.Lock.ID, true)
			}
			original := []map[string]any{{
				"type": "structured_data", "value": map[string]any{"number": 1, "text": strings.Repeat("result", 10000)},
			}}
			if name == "multiple large blocks" {
				original = append(original, map[string]any{"type": "text", "text": strings.Repeat("TARGET é\n", 8000)})
			}
			if name == "text with metadata" || name == "hidden text block" {
				original = []map[string]any{
					{"type": "text", "text": strings.Repeat("TARGET é\n", 8000)},
					{"type": "structured_data", "value": map[string]any{"number": 1}},
				}
			}
			if name == "hidden text block" {
				original[0]["metadata"] = map[string]string{"source": "crm", "omnara_hidden": "true"}
			}
			parts, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			numbers := []string{"1", "1.0"}
			if name == "inline boundary" || name == "runtime inline boundary" {
				const empty = `[{"type":"structured_data","value":{"number":1,"text":""}}]`
				parts = []byte(strings.Replace(empty, `"text":""`, `"text":"`+
					strings.Repeat("x", executionstore.ToolResultInlineBudgetBytes-len(empty))+`"`, 1))
				numbers = []string{"1.0", "1"}
			}
			ioCalls := 0
			blobs.beforeIO = func() {
				ioCalls++
				if got := pool.Stat().AcquiredConns(); got != 0 {
					t.Fatalf("blob I/O retained %d database connections", got)
				}
			}
			input := executionstore.CompleteToolCallInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ID: callID, RuntimeLockID: fixture.Lock.ID,
				Outcome: executionstore.ToolResultOutcomeSucceeded, ResultContentParts: parts,
			}
			for _, number := range numbers {
				input.ResultContentParts = json.RawMessage(strings.Replace(string(parts), `"number":1`, `"number":`+number, 1))
				if runtime {
					_, err = store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
						ProjectID: input.ProjectID, AgentID: input.AgentID, ID: input.ID, RuntimeLockID: input.RuntimeLockID,
						Outcome: input.Outcome, ResultContentParts: input.ResultContentParts,
					})
				} else {
					_, err = store.Execution().CompleteToolCall(ctx, input)
				}
				if err != nil {
					t.Fatalf("completion with number=%s: %v", number, err)
				}
			}
			if name == "hidden text block" {
				var hiddenBlocks int
				if err := pool.QueryRow(ctx, `
					SELECT count(*) FROM content_blocks block
					JOIN tool_call_results result ON result.agent_id = block.agent_id
					  AND result.id = block.owner_tool_call_result_id
					WHERE result.agent_id = $1 AND result.tool_call_id = $2
					  AND block.metadata->>'omnara_hidden' = 'true'
				`, fixture.AgentID, callID).Scan(&hiddenBlocks); err != nil {
					t.Fatal(err)
				}
				if hiddenBlocks != 2 {
					t.Fatalf("stored hidden blocks=%d, want preview and attachment", hiddenBlocks)
				}
			}
			if ioCalls != 2 {
				t.Fatalf("blob I/O calls=%d, want one upload and one replay read", ioCalls)
			}
			if blobs.puts != 1 {
				t.Fatalf("uploads=%d, want 1", blobs.puts)
			}
		})
	}
}

func TestToolOverflowExcludesBoundedAndControlTools(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := &overflowBlobs{fail: true}
	store := newIntegrationStore(pool, storage.WithBlobStore(blobs))
	user := mustCreateProjectOperatorUser(t, ctx, store, "overflow-exclusions@example.com", "Overflow")
	fixture := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, "overflow_exclusions", time.Now())
	parts, err := json.Marshal([]map[string]any{{"type": "text", "text": strings.Repeat("result", 10000)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"read_file", "search_files", "run_command", "read_process", "list_processes", "write_process", "stop_process",
		"upload_file", "download_file",
		"create_machine", "delete_machine", "inspect_machine", "set_integration_target", "send_integration_message",
	} {
		for _, outcome := range []executionstore.ToolResultOutcome{
			executionstore.ToolResultOutcomeSucceeded, executionstore.ToolResultOutcomeFailed,
		} {
			t.Run(name+"/"+string(outcome), func(t *testing.T) {
				callID := createToolCallForProcessTest(t, ctx, fixture, name+"_"+string(outcome), name)
				result, err := store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, ID: callID, RuntimeLockID: fixture.Lock.ID,
					Outcome: outcome, ResultContentParts: parts,
				})
				if err != nil || result.State != executionstore.ToolCallStateCompleted {
					t.Fatalf("excluded completion: %+v, %v", result, err)
				}
				call, err := store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, callID)
				if err != nil {
					t.Fatal(err)
				}
				assertJSONRawEqual(t, call.ResultContentParts, string(parts))
			})
		}
	}
}
