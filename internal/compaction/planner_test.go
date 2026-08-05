package compaction

import (
	"strings"
	"testing"

	agentevents "github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestRecentTailTargetTokens(t *testing.T) {
	for _, test := range []struct {
		name        string
		usableInput int
		want        int
	}{
		{name: "no input budget", usableInput: 0, want: 0},
		{name: "negative input budget", usableInput: -1, want: 0},
		{name: "quarter of input budget", usableInput: 20_000, want: 5_000},
		{name: "large input budget capped", usableInput: 128_000, want: 8_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := RecentTailTargetTokens(test.usableInput); got != test.want {
				t.Fatalf("recent tail target = %d, want %d", got, test.want)
			}
		})
	}
}

func TestPlanCheckpointCreatesClosedRangeBeforeKeptTail(t *testing.T) {
	plan, ok, err := PlanCheckpoint(
		PlanInput{
			ProjectID:                      testProjectID,
			AgentID:                        testAgentID,
			InputEventSequence:             100,
			SummarizedThroughEventSequence: 20,
			RetainFromEventSequence:        81,
		},
	)
	if err != nil {
		t.Fatalf("plan checkpoint: %v", err)
	}
	if !ok {
		t.Fatal("expected checkpoint plan")
	}
	if plan.EventSequenceStart != 21 || plan.EventSequenceEnd != 80 {
		t.Fatalf("unexpected range: %+v", plan)
	}
}

func TestPlanCheckpointRejectsSplitAtomicGroup(t *testing.T) {
	_, _, err := PlanCheckpoint(
		PlanInput{
			ProjectID:               testProjectID,
			AgentID:                 testAgentID,
			InputEventSequence:      100,
			RetainFromEventSequence: 50,
			AtomicGroups:            []AtomicGroup{{Kind: "tool_batch", StartSequence: 45, EndSequence: 55}},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "splits atomic") {
		t.Fatalf("expected split atomic group error, got %v", err)
	}
}

func TestPlanCheckpointRejectsSplitParallelAtomicGroups(t *testing.T) {
	_, _, err := PlanCheckpoint(
		PlanInput{
			ProjectID:               testProjectID,
			AgentID:                 testAgentID,
			InputEventSequence:      100,
			RetainFromEventSequence: 53,
			AtomicGroups: []AtomicGroup{
				{Kind: "tool_call_result", StartSequence: 45, EndSequence: 52},
				{Kind: "tool_call_result", StartSequence: 45, EndSequence: 55},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "splits atomic") {
		t.Fatalf("expected split parallel atomic group error, got %v", err)
	}
}

func TestPlanCheckpointAllowsWholeAtomicGroup(t *testing.T) {
	_, ok, err := PlanCheckpoint(
		PlanInput{
			ProjectID:               testProjectID,
			AgentID:                 testAgentID,
			InputEventSequence:      100,
			RetainFromEventSequence: 70,
			AtomicGroups:            []AtomicGroup{{Kind: "tool_batch", StartSequence: 45, EndSequence: 55}},
		},
	)
	if err != nil {
		t.Fatalf("expected whole atomic group to be allowed: %v", err)
	}
	if !ok {
		t.Fatal("expected checkpoint plan")
	}
}

func TestPlanCheckpointNoopsWithoutClosedRange(t *testing.T) {
	_, ok, err := PlanCheckpoint(
		PlanInput{
			ProjectID:                      testProjectID,
			AgentID:                        testAgentID,
			InputEventSequence:             10,
			SummarizedThroughEventSequence: 9,
			RetainFromEventSequence:        10,
		},
	)
	if err != nil {
		t.Fatalf("noop plan should not fail: %v", err)
	}
	if ok {
		t.Fatal("expected no checkpoint plan")
	}
}

func TestSelectRetainBoundaryKeepsWholeAtomicGroupInTail(t *testing.T) {
	events := make([]executionstore.CompactionSourceEventRecord, 0, 10)
	for sequence := int64(1); sequence <= 10; sequence++ {
		events = append(events, textCompactionEvent(sequence, "visible event"))
	}
	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 10,
		MaximumRetainFromSequence: 11,
		Events:                    events,
		AtomicGroups: []AtomicGroup{
			{Kind: "tool_call_result", StartSequence: 7, EndSequence: 10},
			{Kind: "tool_call_result", StartSequence: 4, EndSequence: 7},
		},
	})
	if err != nil || !ok {
		t.Fatalf("select retain boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 4 {
		t.Fatalf("retain from = %d, want group-expanded boundary 4", retainFrom)
	}
}

func TestRetainBoundaryCandidatesExcludeAtomicGroupInteriors(t *testing.T) {
	events := make([]executionstore.CompactionSourceEventRecord, 0, 6)
	for sequence := int64(1); sequence <= 6; sequence++ {
		events = append(events, textCompactionEvent(sequence, "visible event"))
	}
	candidates, err := RetainFromEventSequenceCandidates(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 2,
		MaximumRetainFromSequence: 7,
		Events:                    events,
		AtomicGroups: []AtomicGroup{{
			Kind: "tool_call_result", StartSequence: 2, EndSequence: 4,
		}},
	})
	if err != nil {
		t.Fatalf("list retain boundary candidates: %v", err)
	}
	want := []int64{2, 5, 6, 7}
	if len(candidates) != len(want) {
		t.Fatalf("retain candidates = %v, want %v", candidates, want)
	}
	for index := range want {
		if candidates[index] != want[index] {
			t.Fatalf("retain candidates = %v, want %v", candidates, want)
		}
	}
}

func TestSelectRetainBoundaryCompactsWholeFirstAtomicGroup(t *testing.T) {
	events := make([]executionstore.CompactionSourceEventRecord, 0, 5)
	for sequence := int64(10); sequence <= 14; sequence++ {
		events = append(events, textCompactionEvent(sequence, "visible event"))
	}
	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  10,
		DesiredRetainFromSequence: 12,
		MaximumRetainFromSequence: 20,
		Events:                    events,
		AtomicGroups: []AtomicGroup{
			{Kind: "tool_call_result", StartSequence: 10, EndSequence: 12},
			{Kind: "tool_call_result", StartSequence: 10, EndSequence: 14},
		},
	})
	if err != nil || !ok {
		t.Fatalf("select retain boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 15 {
		t.Fatalf("retain from = %d, want boundary after complete parallel group at 15", retainFrom)
	}
}

func TestSelectRetainBoundaryPrefersWholeTurnOpening(t *testing.T) {
	firstTurnID := testIDN(301)
	secondTurnID := testIDN(302)
	events := []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "first turn opening"),
		textCompactionEvent(2, "first turn answer"),
		textCompactionEvent(3, "second turn opening"),
		textCompactionEvent(4, "second turn answer"),
		textCompactionEvent(5, "newer raw tail"),
	}
	for index := range events[:2] {
		events[index].TurnID = firstTurnID
	}
	for index := 2; index < len(events); index++ {
		events[index].TurnID = secondTurnID
	}
	events[0].IsOpeningEvent = true
	events[2].IsOpeningEvent = true

	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 4,
		DesiredRetainTokens:       100,
		MaximumRetainFromSequence: 6,
		Events:                    events,
	})
	if err != nil || !ok {
		t.Fatalf("select whole-turn retain boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 3 {
		t.Fatalf("retain from = %d, want second turn opening at 3", retainFrom)
	}
}

func TestSelectRetainBoundaryCompactsCompleteTurnBeforeInternalStep(t *testing.T) {
	closedTurnID := testIDN(307)
	currentTurnID := testIDN(308)
	opening := textCompactionEvent(1, "closed turn opening")
	opening.Kind = string(agentevents.KindAgentInput)
	opening.IsOpeningEvent = true
	opening.TurnID = closedTurnID
	answer := textCompactionEvent(2, "closed turn answer")
	answer.TurnID = closedTurnID
	current := textCompactionEvent(3, "current unanswered opening")
	current.Kind = string(agentevents.KindAgentInput)
	current.IsOpeningEvent = true
	current.TurnID = currentTurnID

	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 1,
		DesiredRetainTokens:       100,
		MaximumRetainFromSequence: 3,
		Events:                    []executionstore.CompactionSourceEventRecord{opening, answer, current},
	})
	if err != nil || !ok {
		t.Fatalf("select complete-turn boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 3 {
		t.Fatalf("retain from = %d, want boundary after complete closed turn at 3", retainFrom)
	}
}

func TestSelectRetainBoundaryCompactsOversizedClosedTurnAsAWhole(t *testing.T) {
	closedTurnID := testIDN(309)
	currentTurnID := testIDN(310)
	opening := textCompactionEvent(1, strings.Repeat("closed turn opening ", 100))
	opening.Kind = string(agentevents.KindAgentInput)
	opening.IsOpeningEvent = true
	opening.TurnID = closedTurnID
	answer := textCompactionEvent(2, "closed turn answer")
	answer.TurnID = closedTurnID
	current := textCompactionEvent(3, "current unanswered opening")
	current.Kind = string(agentevents.KindAgentInput)
	current.IsOpeningEvent = true
	current.TurnID = currentTurnID

	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 1,
		DesiredRetainTokens:       10,
		MaximumRetainFromSequence: 3,
		Events:                    []executionstore.CompactionSourceEventRecord{opening, answer, current},
	})
	if err != nil || !ok {
		t.Fatalf("select oversized whole-turn boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 3 {
		t.Fatalf("retain from = %d, want boundary after oversized closed turn at 3", retainFrom)
	}
}

func TestSelectRetainBoundarySplitsTurnThatExceedsTailBudget(t *testing.T) {
	turnID := testIDN(303)
	events := []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "older complete event"),
		textCompactionEvent(2, strings.Repeat("oversized turn opening ", 20)),
		textCompactionEvent(3, strings.Repeat("oversized turn continuation ", 20)),
		textCompactionEvent(4, "newest continuation"),
	}
	events[0].TurnID = testIDN(304)
	for index := 1; index < len(events); index++ {
		events[index].TurnID = turnID
	}
	events[1].IsOpeningEvent = true

	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 3,
		DesiredRetainTokens:       10,
		MaximumRetainFromSequence: 5,
		Events:                    events,
	})
	if err != nil || !ok {
		t.Fatalf("select long-turn retain boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 4 {
		t.Fatalf("retain from = %d, want settled mid-turn fallback at 4", retainFrom)
	}
}

func TestSelectRetainBoundaryCountsProtectedOpeningTail(t *testing.T) {
	events := []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "older output"),
		textCompactionEvent(2, "older output"),
		textCompactionEvent(3, "older output"),
		textCompactionEvent(4, "candidate tail"),
		textCompactionEvent(5, strings.Repeat("protected opening tail ", 20)),
		textCompactionEvent(6, strings.Repeat("newer protected tail ", 20)),
	}
	events[4].Kind = string(agentevents.KindAgentInput)
	events[4].IsOpeningEvent = true

	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 4,
		DesiredRetainTokens:       20,
		MaximumRetainFromSequence: 5,
		Events:                    events,
	})
	if err != nil || !ok {
		t.Fatalf("select protected tail boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 5 {
		t.Fatalf("retain from = %d, want protected opening boundary 5", retainFrom)
	}
}

func TestSafeCompactionSourceEndsAllowAnsweredInputsAndRequireSettledModelStep(t *testing.T) {
	input := textCompactionEvent(1, "opening input")
	input.Kind = string(agentevents.KindAgentInput)
	input.IsOpeningEvent = true
	output := textCompactionEvent(2, "answer")
	toolOutput := textCompactionEvent(3, "tool call")
	result := textCompactionEvent(4, "tool result")
	result.Kind = string(agentevents.KindToolResult)
	unanswered := textCompactionEvent(5, "current opening input")
	unanswered.Kind = string(agentevents.KindAgentInput)
	unanswered.IsOpeningEvent = true

	ends := safeCompactionSourceEnds(
		[]executionstore.CompactionSourceEventRecord{input, output, toolOutput, result, unanswered},
		[]AtomicGroup{{Kind: "tool_call_result", StartSequence: 3, EndSequence: 4}},
	)
	want := []int64{1, 2, 4}
	if len(ends) != len(want) {
		t.Fatalf("safe ends = %v, want %v", ends, want)
	}
	for index := range want {
		if ends[index] != want[index] {
			t.Fatalf("safe ends = %v, want %v", ends, want)
		}
	}
}

func TestSelectRetainBoundaryKeepsCompleteAnsweredToolGroupRaw(t *testing.T) {
	turnID := testIDN(305)
	oldOutput := textCompactionEvent(1, "older closed output")
	oldOutput.TurnID = testIDN(306)
	answeredInput := textCompactionEvent(2, strings.Repeat("answered opening input ", 200))
	answeredInput.Kind = string(agentevents.KindAgentInput)
	answeredInput.IsOpeningEvent = true
	answeredInput.TurnID = turnID
	toolOutput := textCompactionEvent(3, "tool call")
	toolOutput.TurnID = turnID
	toolResult := textCompactionEvent(4, "tool result")
	toolResult.Kind = string(agentevents.KindToolResult)
	toolResult.TurnID = turnID

	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: 3,
		DesiredRetainTokens:       200,
		MaximumRetainFromSequence: 5,
		Events: []executionstore.CompactionSourceEventRecord{
			oldOutput,
			answeredInput,
			toolOutput,
			toolResult,
		},
		AtomicGroups: []AtomicGroup{{
			Kind: "tool_call_result", StartSequence: 3, EndSequence: 4,
		}},
	})
	if err != nil || !ok {
		t.Fatalf("select answered tool-group boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 3 {
		t.Fatalf("retain from = %d, want answered input summarized through 2 and tool group 3..4 raw", retainFrom)
	}
}

func TestSelectRetainBoundarySkipsCheckpointOnlyPrefix(t *testing.T) {
	checkpoint := mustCompactionEvent(
		10,
		string(agentevents.KindContextCheckpoint),
		"published",
		nil,
	)
	events := []executionstore.CompactionSourceEventRecord{checkpoint}
	for sequence := int64(11); sequence <= 14; sequence++ {
		events = append(events, textCompactionEvent(sequence, "visible event"))
	}
	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  10,
		DesiredRetainFromSequence: 13,
		MaximumRetainFromSequence: 20,
		Events:                    events,
		AtomicGroups: []AtomicGroup{{
			Kind: "tool_call_result", StartSequence: 11, EndSequence: 14,
		}},
	})
	if err != nil || !ok {
		t.Fatalf("select retain boundary: retain=%d ok=%t err=%v", retainFrom, ok, err)
	}
	if retainFrom != 15 {
		t.Fatalf("retain from = %d, want model-visible source through atomic group", retainFrom)
	}
}

func TestSelectRetainBoundaryPreservesUnansweredOpening(t *testing.T) {
	events := make([]executionstore.CompactionSourceEventRecord, 0, 3)
	for sequence := int64(10); sequence <= 12; sequence++ {
		events = append(events, textCompactionEvent(sequence, "visible event"))
	}
	retainFrom, ok, err := SelectRetainFromEventSequence(RetainBoundaryInput{
		SourceEventSequenceStart:  10,
		DesiredRetainFromSequence: 12,
		MaximumRetainFromSequence: 12,
		Events:                    events,
		AtomicGroups: []AtomicGroup{{
			Kind: "tool_call_result", StartSequence: 10, EndSequence: 12,
		}},
	})
	if err != nil {
		t.Fatalf("select protected retain boundary: %v", err)
	}
	if ok || retainFrom != 0 {
		t.Fatalf("protected opening boundary = %d ok=%t, want no safe compactable prefix", retainFrom, ok)
	}
}

func TestOpeningEventsRequireModelOutputBeforeCompactingOpening(t *testing.T) {
	events := []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "opening input"),
		textCompactionEvent(2, "tool result"),
		textCompactionEvent(3, "later steering input"),
		textCompactionEvent(4, "checkpoint"),
	}
	events[0].Kind = string(agentevents.KindAgentInput)
	events[1].Kind = string(agentevents.KindToolResult)
	events[2].Kind = string(agentevents.KindAgentInput)
	events[3].Kind = string(agentevents.KindContextCheckpoint)
	if !OpeningEventsRequireVerbatimRetention(events, 1) {
		t.Fatal("non-model events must not make an unanswered opening compactable")
	}
	output := textCompactionEvent(5, "model consumed the opening")
	output.Kind = string(agentevents.KindModelOutput)
	events = append(events, output)
	if OpeningEventsRequireVerbatimRetention(events, 1) {
		t.Fatal("a later model output should make the opening compactable")
	}
}
