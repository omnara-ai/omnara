package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestRunnerShrinksOversizedSourceBeforeProviderSend(t *testing.T) {
	events := make([]executionstore.CompactionSourceEventRecord, 0, 8)
	for sequence := int64(1); sequence <= 8; sequence++ {
		events = append(events, textCompactionEvent(sequence, strings.Repeat("large event payload ", 180)))
	}
	store := &fakeStore{events: events}
	client := &summaryModel{caps: model.Capabilities{
		ContextWindowTokens: 8_000,
		MaxOutputTokens:     1_024,
	}}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 8, 8)))
	if err != nil {
		t.Fatalf("run oversized source: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) == 0 {
		t.Fatalf("oversized result=%+v replacements=%+v", result, store.replacements)
	}
	if store.replacements[0].NextSourceEventSequenceEnd >= 8 ||
		store.replacements[0].APIFormat != client.APIFormat() ||
		store.replacements[0].APIVariant != client.ModelAPIVariant() ||
		store.replacements[0].ProviderRequestID != "" ||
		store.replacements[0].ProviderResponseID != "" {
		t.Fatalf("preflight replacement = %+v", store.replacements[0])
	}
	if len(client.requests) != 1 {
		t.Fatalf("provider requests = %d, want only the fitting replacement", len(client.requests))
	}
}

func TestRunnerStopsBeforeSendWhenSmallestSourceExceedsBudget(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "only closed semantic unit"),
	}}
	client := &summaryModel{
		caps: model.Capabilities{
			ContextWindowTokens: 8_000,
			MaxOutputTokens:     1_024,
		},
		preparedEstimates: []int{7_000},
	}

	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 1, 1)))
	if err != nil {
		t.Fatalf("run irreducibly oversized source: %v", err)
	}
	if result.State != RunTerminal || len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorKind != model.ErrorKindContextWindow ||
		store.terminalFailures[0].ErrorCode != "compaction_source_irreducible" ||
		!strings.Contains(store.terminalFailures[0].ErrorMessage, "no closed source prefix fits") ||
		len(client.preparedBundles) != 1 || len(client.requests) != 0 ||
		len(store.replacements) != 0 || len(store.retryFailures) != 0 ||
		len(store.publishInputs) != 0 {
		t.Fatalf(
			"irreducible result=%+v terminal=%+v prepares=%d requests=%d replacements=%+v retries=%+v publishes=%+v",
			result,
			store.terminalFailures,
			len(client.preparedBundles),
			len(client.requests),
			store.replacements,
			store.retryFailures,
			store.publishInputs,
		)
	}
}

func TestRunnerRecordsBoundaryAdjustmentSeparatelyFromBudgetOverflow(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("closed semantic event ", 20)),
		mustCompactionEvent(
			2,
			string(events.KindContextCheckpoint),
			"",
			json.RawMessage(`[]`),
		),
	}}
	result, err := testRunner(store, &summaryModel{}).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run boundary adjustment: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 ||
		store.replacements[0].NextSourceEventSequenceEnd != 1 ||
		store.replacements[0].ErrorKind != model.ErrorKindInvalidRequest ||
		store.replacements[0].ErrorCode != "compaction_source_boundary_adjustment" {
		t.Fatalf("boundary adjustment result=%+v replacements=%+v", result, store.replacements)
	}
}

func TestRunnerReplansWhenPublishFindsUnsafeBoundary(t *testing.T) {
	store := &fakeStore{
		events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("first closed event ", 20)),
			textCompactionEvent(2, strings.Repeat("second closed event ", 20)),
		},
		publishErrs: []error{executionstore.ErrCheckpointBoundaryUnsafe},
	}
	client := &summaryModel{results: []summaryResult{
		{response: completeSummaryResponse("Concise first summary.")},
		{response: completeSummaryResponse("Concise replacement summary.")},
	}}

	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 2, 2)),
	)
	if err != nil {
		t.Fatalf("run publish-time boundary adjustment: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 ||
		store.replacements[0].ErrorCode != "compaction_source_boundary_adjustment" ||
		store.replacements[0].NextSourceEventSequenceEnd != 1 ||
		store.replacements[0].APIFormat != client.APIFormat() ||
		store.replacements[0].APIVariant != client.ModelAPIVariant() ||
		len(client.requests) != 2 {
		t.Fatalf(
			"publish-time adjustment result=%+v replacements=%+v requests=%d",
			result,
			store.replacements,
			len(client.requests),
		)
	}
}

func TestRunnerShrinksSourceWhenSerializedProviderRequestExceedsBudget(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("first closed unit ", 40)),
		textCompactionEvent(2, strings.Repeat("second closed unit ", 40)),
	}}
	client := &summaryModel{
		caps: model.Capabilities{
			ContextWindowTokens: 8_000,
			MaxOutputTokens:     1_024,
		},
		preparedEstimates: []int{500, 7_000, 500},
	}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run serialized oversized source: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 ||
		store.replacements[0].ErrorCode != "compaction_source_over_budget" ||
		store.replacements[0].NextSourceEventSequenceEnd != 1 ||
		store.replacements[0].APIFormat != client.APIFormat() ||
		store.replacements[0].APIVariant != client.ModelAPIVariant() ||
		store.replacements[0].ProviderRequestID != "" ||
		store.replacements[0].ProviderResponseID != "" {
		t.Fatalf("serialized oversized result=%+v replacements=%+v", result, store.replacements)
	}
	compactionPrepares := 0
	checkpointPrepares := 0
	for _, bundle := range client.preparedBundles {
		if bundle.ContextCheckpoint == nil {
			compactionPrepares++
		} else {
			checkpointPrepares++
		}
	}
	if compactionPrepares != 3 || checkpointPrepares != 1 || len(client.requests) != 1 {
		t.Fatalf(
			"compaction/checkpoint/provider calls = %d/%d/%d, want 3/1/1",
			compactionPrepares,
			checkpointPrepares,
			len(client.requests),
		)
	}
}

func TestRunnerKeepsLaterAnswerAsBoundaryWitnessWithoutSummarizingToolGroup(t *testing.T) {
	turnID := testIDN(810)
	oldOutput := textCompactionEvent(1, strings.Repeat("older closed output ", 40))
	oldOutput.TurnID = testIDN(811)
	answeredInput := textCompactionEvent(2, strings.Repeat("answered opening input ", 40))
	answeredInput.Kind = string(events.KindAgentInput)
	answeredInput.IsOpeningEvent = true
	answeredInput.TurnID = turnID
	toolOutput := textCompactionEvent(3, "TOOL_GROUP_MUST_STAY_RAW")
	toolOutput.TurnID = turnID
	toolResult := textCompactionEvent(4, "TOOL_RESULT_MUST_STAY_RAW")
	toolResult.Kind = string(events.KindToolResult)
	toolResult.TurnID = turnID
	store := &fakeStore{
		events: []executionstore.CompactionSourceEventRecord{
			oldOutput,
			answeredInput,
			toolOutput,
			toolResult,
		},
		atomicGroups: []executionstore.CompactionAtomicGroupRecord{{
			Kind: "tool_call_result", StartSequence: 3, EndSequence: 4,
		}},
	}
	client := &summaryModel{results: []summaryResult{{
		response: completeSummaryResponse("Concise checkpoint for the answered request."),
	}}}

	result, err := testRunner(store, client).Run(
		context.Background(),
		runInput(testPlan(1, 2, 4)),
	)
	if err != nil {
		t.Fatalf("run compaction with retained boundary witness: %v", err)
	}
	if result.State != RunCompleted || result.Checkpoint == nil ||
		result.Checkpoint.SummarizedThroughEventSequence != 2 || len(client.requests) != 1 {
		t.Fatalf("boundary-witness result=%+v requests=%d", result, len(client.requests))
	}
	prompt := string(client.requests[0].ProviderRequest)
	if !strings.Contains(prompt, "answered opening input") ||
		strings.Contains(prompt, "TOOL_GROUP_MUST_STAY_RAW") ||
		strings.Contains(prompt, "TOOL_RESULT_MUST_STAY_RAW") {
		t.Fatalf("sent compaction source leaked retained tool group: %s", prompt)
	}
}

func TestSafeCompactionSourceEndsDoNotSplitAtomicGroups(t *testing.T) {
	events := []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "one"), textCompactionEvent(2, "two"),
		textCompactionEvent(3, "three"), textCompactionEvent(4, "four"),
	}
	ends := safeCompactionSourceEnds(events, []AtomicGroup{{
		Kind: "tool_call_result", StartSequence: 2, EndSequence: 3,
	}})
	want := []int64{1, 3, 4}
	if len(ends) != len(want) {
		t.Fatalf("safe ends = %v, want %v", ends, want)
	}
	for index := range want {
		if ends[index] != want[index] {
			t.Fatalf("safe ends = %v, want %v", ends, want)
		}
	}
}

func TestPreferredWholeTurnSourceEndAvoidsSplittingAnsweredTurn(t *testing.T) {
	firstTurnID := testIDN(812)
	secondTurnID := testIDN(813)
	records := []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("first opening ", 20)),
		textCompactionEvent(2, "first answer"),
		textCompactionEvent(3, strings.Repeat("second opening ", 20)),
		textCompactionEvent(4, "second answer"),
	}
	records[0].Kind = string(events.KindAgentInput)
	records[0].TurnID = firstTurnID
	records[1].TurnID = firstTurnID
	records[2].Kind = string(events.KindAgentInput)
	records[2].TurnID = secondTurnID
	records[3].TurnID = secondTurnID
	prefixBytes := []int{100, 110, 210, 220}

	completeTurnEnds := completeTurnSourceEnds(records, records, []int64{1, 2, 3})
	before, after := boundariesAroundTarget(completeTurnEnds, func(end int64) bool {
		return prefixBytes[end-1] <= 100
	})
	got := before
	if got == 0 {
		got = after
	}
	if got != 2 {
		t.Fatalf("preferred reduced source end = %d, want complete first turn at 2", got)
	}
}

func TestSafeCompactionSourceEndsRequireModelVisibleSemantics(t *testing.T) {
	events := []executionstore.CompactionSourceEventRecord{
		mustCompactionEvent(1, string(events.KindAgentInput), "config_change", json.RawMessage(`[]`)),
		textCompactionEvent(2, "visible input"),
		mustCompactionEvent(3, string(events.KindContextCheckpoint), "", json.RawMessage(`[]`)),
		mustCompactionEvent(4, string(events.KindToolResult), "completed", json.RawMessage(`[]`)),
		textCompactionEvent(5, "visible output"),
	}
	ends := safeCompactionSourceEnds(events, nil)
	want := []int64{2, 5}
	if len(ends) != len(want) {
		t.Fatalf("semantic safe ends = %v, want %v", ends, want)
	}
	for index := range want {
		if ends[index] != want[index] {
			t.Fatalf("semantic safe ends = %v, want %v", ends, want)
		}
	}
	rendered, err := renderEventSource(events)
	if err != nil {
		t.Fatalf("render semantic source: %v", err)
	}
	if strings.Contains(rendered, "Event 1") || strings.Contains(rendered, "Event 3") ||
		strings.Contains(rendered, "Event 4") || !strings.Contains(rendered, "Event 2") ||
		!strings.Contains(rendered, "Event 5") || strings.Contains(rendered, "execution_error=true") {
		t.Fatalf("rendered semantic source = %q", rendered)
	}
}

func TestRenderEventSourceReducesProjectionWithoutMutatingCanonicalOutput(t *testing.T) {
	original := json.RawMessage(`[{"type":"structured_data","value":{"stdout":"` + strings.Repeat("x", 40_960) +
		`TAIL"}}]`)
	event := mustCompactionEvent(1, "tool_result", "completed", append(json.RawMessage(nil), original...))
	event.ToolName = "run_command"
	event.ProviderCallID = "call_1"
	rendered, err := renderEventSource([]executionstore.CompactionSourceEventRecord{event})
	if err != nil {
		t.Fatalf("render event source: %v", err)
	}
	if string(event.ContentParts) != string(original) {
		t.Fatal("canonical tool output was mutated")
	}
	if !strings.Contains(rendered, "omitted") || !strings.Contains(rendered, "TAIL") ||
		!strings.Contains(rendered, "Tool: run_command call_id=call_1") {
		t.Fatalf("reduced tool projection = %q", rendered)
	}
}

func TestRenderContentPartsLabelsMediaRefsAsArtifacts(t *testing.T) {
	got := renderContentParts(json.RawMessage(`[{"type":"media_ref","artifact_id":"art_media"}]`))
	if !strings.Contains(got, "Artifact: ") || !strings.Contains(got, "art_media") {
		t.Fatalf("rendered content parts = %q", got)
	}
	if strings.Contains(got, "Content: ") {
		t.Fatalf("media_ref should not use generic content label: %q", got)
	}
}

func TestRenderEventSourceIncludesToolOutcome(t *testing.T) {
	event := mustCompactionEvent(1, "tool_result", "", nil)
	event.ToolName = "run_command"
	event.ProviderCallID = "call_1"
	event.ToolOutcome = string(executionstore.ToolResultOutcomeCanceled)
	rendered, err := renderEventSource([]executionstore.CompactionSourceEventRecord{event})
	if err != nil {
		t.Fatalf("render event source: %v", err)
	}
	if !strings.Contains(rendered, "Tool: run_command call_id=call_1 outcome=canceled") {
		t.Fatalf("rendered source = %q", rendered)
	}
}

func TestRunnerDurablyRetriesOpenSourceRange(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{textCompactionEvent(1, "one")}}
	result, err := testRunner(store, &summaryModel{}).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil || result.State != RunRetryScheduled || len(store.retryFailures) != 1 ||
		store.retryFailures[0].ErrorKind != model.ErrorKindTransient ||
		store.retryFailures[0].ErrorCode != "load_compaction_source_failed" ||
		!strings.Contains(string(store.retryFailures[0].ErrorDetails), "not closed") {
		t.Fatalf("open range result=%+v failures=%+v err=%v", result, store.retryFailures, err)
	}
}
