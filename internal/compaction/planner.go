package compaction

import (
	"encoding/json"
	"errors"
	"fmt"

	agentevents "github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const maxRecentTailTargetTokens = 8_000

func RecentTailTargetTokens(usableInputTokens int) int {
	if usableInputTokens <= 0 {
		return 0
	}
	recentTailTargetTokens := usableInputTokens / 4
	if recentTailTargetTokens > maxRecentTailTargetTokens {
		return maxRecentTailTargetTokens
	}
	return recentTailTargetTokens
}

type AtomicGroup struct {
	Kind          string
	StartSequence int64
	EndSequence   int64
}

type PlanInput struct {
	ProjectID                      storage.ID
	AgentID                        storage.ID
	InputEventSequence             int64
	SummarizedThroughEventSequence int64
	RetainFromEventSequence        int64
	AtomicGroups                   []AtomicGroup
}

type Plan struct {
	ProjectID          storage.ID
	AgentID            storage.ID
	InputEventSequence int64
	EventSequenceStart int64
	EventSequenceEnd   int64
}

type RetainBoundaryInput struct {
	SourceEventSequenceStart  int64
	DesiredRetainFromSequence int64
	DesiredRetainTokens       int
	MaximumRetainFromSequence int64
	Events                    []executionstore.CompactionSourceEventRecord
	AtomicGroups              []AtomicGroup
}

func SelectRetainFromEventSequence(input RetainBoundaryInput) (int64, bool, error) {
	if err := validateRetainBoundaryInput(input); err != nil {
		return 0, false, err
	}

	safeEnds := safeCompactionSourceEnds(input.Events, input.AtomicGroups)
	if retainFrom := wholeTurnRetainFrom(input, safeEnds); retainFrom > 0 {
		return retainFrom, true, nil
	}
	if retainFrom := wholeTurnBoundaryRetainFrom(input, safeEnds); retainFrom > 0 {
		return retainFrom, true, nil
	}
	desiredSourceEnd := input.DesiredRetainFromSequence - 1
	latestBefore := int64(0)
	firstAfter := int64(0)
	for _, end := range safeEnds {
		if end < input.SourceEventSequenceStart || end >= input.MaximumRetainFromSequence {
			continue
		}
		if end <= desiredSourceEnd {
			latestBefore = end
			continue
		}
		firstAfter = end
		break
	}
	if latestBefore > 0 && retainedTailFitsTarget(input, latestBefore+1) {
		return latestBefore + 1, true, nil
	}
	if firstAfter > 0 {
		return firstAfter + 1, true, nil
	}
	if latestBefore > 0 {
		return latestBefore + 1, true, nil
	}
	return 0, false, nil
}

func wholeTurnBoundaryRetainFrom(input RetainBoundaryInput, safeEnds []int64) int64 {
	if input.DesiredRetainTokens <= 0 || len(input.Events) == 0 {
		return 0
	}
	desiredSourceEnd := input.DesiredRetainFromSequence - 1
	eligibleEnds := make([]int64, 0, len(safeEnds))
	latestSequence := input.Events[len(input.Events)-1].Sequence
	for _, end := range completeTurnSourceEnds(input.Events, input.Events, safeEnds) {
		if end < input.SourceEventSequenceStart || end >= input.MaximumRetainFromSequence ||
			end >= latestSequence {
			continue
		}
		eligibleEnds = append(eligibleEnds, end)
	}
	latestBefore, firstAfter := boundariesAroundTarget(
		eligibleEnds,
		func(end int64) bool { return end <= desiredSourceEnd },
	)
	if latestBefore > 0 && retainedTailFitsTarget(input, latestBefore+1) {
		return latestBefore + 1
	}
	if firstAfter > 0 {
		return firstAfter + 1
	}
	return 0
}

func completeTurnSourceEnds(
	events, witnessEvents []executionstore.CompactionSourceEventRecord,
	candidates []int64,
) []int64 {
	latestSequenceByTurn := make(map[storage.ID]int64)
	for _, event := range witnessEvents {
		if event.Kind != string(agentevents.KindContextCheckpoint) && event.TurnID != storage.NilID {
			latestSequenceByTurn[event.TurnID] = event.Sequence
		}
	}
	eventBySequence := make(map[int64]executionstore.CompactionSourceEventRecord, len(events))
	for _, event := range events {
		eventBySequence[event.Sequence] = event
	}
	ends := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		event, ok := eventBySequence[candidate]
		if ok && event.Kind == string(agentevents.KindModelOutput) &&
			event.TurnID != storage.NilID && latestSequenceByTurn[event.TurnID] == candidate {
			ends = append(ends, candidate)
		}
	}
	return ends
}

func boundariesAroundTarget(candidates []int64, beforeTarget func(int64) bool) (int64, int64) {
	before := int64(0)
	for _, candidate := range candidates {
		if beforeTarget(candidate) {
			before = candidate
			continue
		}
		return before, candidate
	}
	return before, 0
}

func retainedTailFitsTarget(input RetainBoundaryInput, retainFrom int64) bool {
	if input.DesiredRetainTokens <= 0 {
		return true
	}
	tokens := 0
	for _, event := range input.Events {
		if event.Sequence < retainFrom {
			continue
		}
		tokens += EstimateSourceEventTokens(event)
		if tokens > input.DesiredRetainTokens {
			return false
		}
	}
	return true
}

func RetainFromEventSequenceCandidates(input RetainBoundaryInput) ([]int64, error) {
	if err := validateRetainBoundaryInput(input); err != nil {
		return nil, err
	}
	candidates := make([]int64, 0, len(input.Events))
	for _, end := range safeCompactionSourceEnds(input.Events, input.AtomicGroups) {
		if end < input.SourceEventSequenceStart || end >= input.MaximumRetainFromSequence {
			continue
		}
		candidates = append(candidates, end+1)
	}
	return candidates, nil
}

func validateRetainBoundaryInput(input RetainBoundaryInput) error {
	if input.SourceEventSequenceStart <= 0 || input.DesiredRetainFromSequence <= 0 ||
		input.MaximumRetainFromSequence <= 0 {
		return errors.New("source, desired retain, and maximum retain sequences are required")
	}
	if input.DesiredRetainFromSequence > input.MaximumRetainFromSequence {
		return errors.New("desired retain sequence exceeds maximum retain sequence")
	}
	previousSequence := int64(0)
	for _, event := range input.Events {
		if event.Sequence <= previousSequence {
			return errors.New("compaction source events must be strictly ordered")
		}
		previousSequence = event.Sequence
	}
	for _, group := range input.AtomicGroups {
		if group.StartSequence <= 0 || group.EndSequence < group.StartSequence {
			return fmt.Errorf("invalid atomic group %q", group.Kind)
		}
	}
	return nil
}

func wholeTurnRetainFrom(input RetainBoundaryInput, safeEnds []int64) int64 {
	if input.DesiredRetainTokens <= 0 {
		return 0
	}
	desiredTurnID := storage.NilID
	for _, event := range input.Events {
		if event.Sequence == input.DesiredRetainFromSequence {
			desiredTurnID = event.TurnID
			break
		}
	}
	if desiredTurnID == storage.NilID {
		return 0
	}
	openingSequence := int64(0)
	turnTokens := 0
	for _, event := range input.Events {
		if event.TurnID != desiredTurnID {
			continue
		}
		turnTokens += EstimateSourceEventTokens(event)
		if event.IsOpeningEvent && (openingSequence == 0 || event.Sequence < openingSequence) {
			openingSequence = event.Sequence
		}
	}
	if openingSequence <= input.SourceEventSequenceStart ||
		openingSequence > input.DesiredRetainFromSequence ||
		openingSequence > input.MaximumRetainFromSequence ||
		turnTokens > input.DesiredRetainTokens {
		return 0
	}
	wantedSourceEnd := openingSequence - 1
	for _, safeEnd := range safeEnds {
		if safeEnd == wantedSourceEnd {
			return openingSequence
		}
	}
	return 0
}

func EstimateSourceEventTokens(event executionstore.CompactionSourceEventRecord) int {
	return len(event.ContentParts)/4 + 1
}

func PlanCheckpoint(input PlanInput) (Plan, bool, error) {
	if input.ProjectID == storage.NilID || input.AgentID == storage.NilID {
		return Plan{}, false, errors.New("project and agent are required")
	}
	if input.InputEventSequence <= 0 {
		return Plan{}, false, errors.New("event watermark is required")
	}
	if input.RetainFromEventSequence <= 1 {
		return Plan{}, false, nil
	}
	start := input.SummarizedThroughEventSequence + 1
	end := input.RetainFromEventSequence - 1
	if end < start {
		return Plan{}, false, nil
	}
	if end > input.InputEventSequence {
		return Plan{}, false, errors.New("checkpoint range exceeds event watermark")
	}
	for _, group := range input.AtomicGroups {
		if group.StartSequence <= 0 || group.EndSequence < group.StartSequence {
			return Plan{}, false, fmt.Errorf("invalid atomic group %q", group.Kind)
		}
		if start <= group.StartSequence && end >= group.EndSequence {
			continue
		}
		if rangesOverlap(start, end, group.StartSequence, group.EndSequence) {
			return Plan{}, false, fmt.Errorf(
				"checkpoint range %d..%d splits atomic %s group %d..%d",
				start,
				end,
				group.Kind,
				group.StartSequence,
				group.EndSequence,
			)
		}
	}
	plan := Plan{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		InputEventSequence: input.InputEventSequence,
		EventSequenceStart: start,
		EventSequenceEnd:   end,
	}
	return plan, true, nil
}

func rangesOverlap(aStart, aEnd, bStart, bEnd int64) bool {
	return aStart <= bEnd && bStart <= aEnd
}

func safeCompactionSourceEnds(
	events []executionstore.CompactionSourceEventRecord,
	groups []AtomicGroup,
) []int64 {
	return safeCompactionSourceEndsWithWitness(events, events, groups)
}

func safeCompactionSourceEndsWithWitness(
	events []executionstore.CompactionSourceEventRecord,
	witnessEvents []executionstore.CompactionSourceEventRecord,
	groups []AtomicGroup,
) []int64 {
	latestModelOutputByTurn := make(map[storage.ID]int64)
	for _, event := range witnessEvents {
		if event.Kind == string(agentevents.KindModelOutput) {
			latestModelOutputByTurn[event.TurnID] = event.Sequence
		}
	}
	ends := make([]int64, 0, len(events))
	for _, event := range events {
		if !compactionEventHasModelSemantics(event) ||
			!compactionEventEndsSettledStep(event, latestModelOutputByTurn) {
			continue
		}
		safe := true
		for _, group := range groups {
			if group.StartSequence <= event.Sequence && event.Sequence < group.EndSequence {
				safe = false
				break
			}
		}
		if safe {
			ends = append(ends, event.Sequence)
		}
	}
	return ends
}

func compactionEventEndsSettledStep(
	event executionstore.CompactionSourceEventRecord,
	latestModelOutputByTurn map[storage.ID]int64,
) bool {
	switch agentevents.Kind(event.Kind) {
	case agentevents.KindModelOutput, agentevents.KindToolResult:
		return true
	case agentevents.KindAgentInput:
		return latestModelOutputByTurn[event.TurnID] > event.Sequence
	default:
		return false
	}
}

func compactionEventHasModelSemantics(event executionstore.CompactionSourceEventRecord) bool {
	if event.Kind == string(agentevents.KindContextCheckpoint) {
		return false
	}
	if event.ToolName != "" || event.ProviderCallID != "" || event.ToolOutcome != "" {
		return true
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(event.ContentParts, &parts); err != nil {
		// Keep malformed events in the compaction source instead of treating them as empty.
		return true
	}
	return len(parts) > 0
}

func atomicGroupsFromRecords(records []executionstore.CompactionAtomicGroupRecord) []AtomicGroup {
	groups := make([]AtomicGroup, 0, len(records))
	for _, record := range records {
		groups = append(groups, AtomicGroup{
			Kind:          record.Kind,
			StartSequence: record.StartSequence,
			EndSequence:   record.EndSequence,
		})
	}
	return groups
}

func OpeningEventsRequireVerbatimRetention(
	events []executionstore.CompactionSourceEventRecord,
	openingStart int64,
) bool {
	for _, event := range events {
		if event.Sequence > openingStart && event.Kind == string(agentevents.KindModelOutput) {
			return false
		}
	}
	return true
}
