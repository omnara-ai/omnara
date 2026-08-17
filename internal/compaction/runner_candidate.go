package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentevents "github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func compactionSourceAdjustmentError(
	events []executionstore.CompactionSourceEventRecord,
	witnessEvents []executionstore.CompactionSourceEventRecord,
	groups []executionstore.CompactionAtomicGroupRecord,
	plannedEnd int64,
) error {
	safeEnds := safeCompactionSourceEndsWithWitness(
		events,
		witnessEvents,
		atomicGroupsFromRecords(groups),
	)
	if len(safeEnds) == 0 || safeEnds[len(safeEnds)-1] < plannedEnd {
		return model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "compaction",
			Code:    "compaction_source_boundary_adjustment",
			Message: "The planned compaction source does not end at a safe model-visible boundary.",
		}
	}
	return model.ProviderError{
		Kind:    model.ErrorKindContextWindow,
		Source:  "compaction",
		Code:    "compaction_source_over_budget",
		Message: "The planned compaction source exceeds the configured input budget.",
	}
}

func (r Runner) loadClosedEventRange(
	ctx context.Context,
	plan Plan,
) ([]executionstore.CompactionSourceEventRecord, error) {
	after := plan.EventSequenceStart - 1
	out := make([]executionstore.CompactionSourceEventRecord, 0, plan.EventSequenceEnd-plan.EventSequenceStart+1)
	for after < plan.EventSequenceEnd {
		page, err := r.Store.ListCompactionSourceEvents(ctx, plan.ProjectID, plan.AgentID, after, 500)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, event := range page {
			if event.Sequence > plan.EventSequenceEnd {
				after = plan.EventSequenceEnd
				break
			}
			out = append(out, event)
			after = event.Sequence
		}
		if len(page) < 500 {
			break
		}
	}
	want := plan.EventSequenceEnd - plan.EventSequenceStart + 1
	if int64(len(out)) != want {
		return nil, fmt.Errorf("compaction source range %d..%d is not closed", plan.EventSequenceStart, plan.EventSequenceEnd)
	}
	for index, event := range out {
		wantSequence := plan.EventSequenceStart + int64(index)
		if event.Sequence != wantSequence {
			return nil, fmt.Errorf("compaction source range missing event sequence %d", wantSequence)
		}
	}
	return out, nil
}

func (r Runner) openingEventsRequireVerbatimRetention(
	ctx context.Context,
	input RunInput,
) (bool, error) {
	after := input.OpeningEventSequence
	for after < input.Plan.InputEventSequence {
		page, err := r.Store.ListCompactionSourceEvents(
			ctx,
			input.Plan.ProjectID,
			input.Plan.AgentID,
			after,
			500,
		)
		if err != nil {
			return false, err
		}
		if len(page) == 0 {
			break
		}
		for _, event := range page {
			if event.Sequence > input.Plan.InputEventSequence {
				return true, nil
			}
			after = event.Sequence
			if event.Kind == string(agentevents.KindModelOutput) {
				return false, nil
			}
		}
		if len(page) < 500 {
			break
		}
	}
	return true, nil
}

func (r Runner) hasRemainingSemanticSource(
	ctx context.Context,
	input RunInput,
	protectOpening bool,
) (bool, error) {
	plan := input.Plan
	after := plan.EventSequenceEnd
	for after < plan.InputEventSequence {
		page, err := r.Store.ListCompactionSourceEvents(
			ctx,
			plan.ProjectID,
			plan.AgentID,
			after,
			500,
		)
		if err != nil {
			return false, err
		}
		if len(page) == 0 {
			break
		}
		for _, event := range page {
			if event.Sequence > plan.InputEventSequence {
				return false, nil
			}
			after = event.Sequence
			if protectOpening && event.Sequence >= input.OpeningEventSequence {
				continue
			}
			if compactionEventHasModelSemantics(event) {
				return true, nil
			}
		}
		if len(page) < 500 {
			break
		}
	}
	return false, nil
}

type preparedCompactionRequest struct {
	prepared        model.PreparedRequest
	sourceEnd       int64
	sourceText      string
	adjustmentCause error
}

func largestFittingCompactionRequest(
	ctx context.Context,
	input RunInput,
	priorSummary string,
	events []executionstore.CompactionSourceEventRecord,
	witnessEvents []executionstore.CompactionSourceEventRecord,
	groups []executionstore.CompactionAtomicGroupRecord,
	client model.Client,
	policy model.RequestPolicy,
	errorSource string,
) (preparedCompactionRequest, error) {
	candidates := safeCompactionSourceEndsWithWitness(
		events,
		witnessEvents,
		atomicGroupsFromRecords(groups),
	)
	if len(candidates) == 0 {
		return preparedCompactionRequest{}, nil
	}
	low, high := 0, len(candidates)-1
	best := -1
	var bestRequest preparedCompactionRequest
	localOverBudget := false
	for low <= high {
		mid := low + (high-low)/2
		end := candidates[mid]
		count := int(end-input.Plan.EventSequenceStart) + 1
		if count <= 0 || count > len(events) {
			return preparedCompactionRequest{}, errors.New("compaction source candidates do not match loaded events")
		}
		sourceText, err := renderEventSource(events[:count])
		if err != nil {
			return preparedCompactionRequest{}, err
		}
		bundle, err := compactionRequestBundle(input, priorSummary, sourceText)
		if err != nil {
			return preparedCompactionRequest{}, err
		}
		prepared, err := model.PrepareForSend(
			ctx,
			client,
			model.PrepareForSendInput{
				Context:     bundle,
				Policy:      policy,
				ErrorSource: errorSource,
			},
		)
		if err != nil {
			return preparedCompactionRequest{}, err
		}
		if prepared.InputBudget.OverBudget() {
			localOverBudget = true
			high = mid - 1
			continue
		}
		best = mid
		bestRequest = preparedCompactionRequest{
			prepared:   prepared,
			sourceEnd:  end,
			sourceText: sourceText,
		}
		low = mid + 1
	}
	if best < 0 {
		return preparedCompactionRequest{}, nil
	}
	if localOverBudget {
		bestRequest.adjustmentCause = compactionSourceAdjustmentError(
			events,
			witnessEvents,
			groups,
			input.Plan.EventSequenceEnd,
		)
	}
	return bestRequest, nil
}

func compactionRequestBundle(
	input RunInput,
	priorSummary, sourceText string,
) (modelcontext.Bundle, error) {
	bundle, err := compactionBundle(input, priorSummary, sourceText)
	if err != nil {
		return modelcontext.Bundle{}, err
	}
	if err := (modelcontext.ProjectionNormalizer{}).Normalize(bundle); err != nil {
		return modelcontext.Bundle{}, fmt.Errorf("normalize compaction model context: %w", err)
	}
	return bundle, nil
}

func (r Runner) nextSmallerSourceEnd(ctx context.Context, plan Plan) (int64, error) {
	window, err := r.loadCompactionBoundaryWindow(ctx, plan)
	if err != nil {
		return 0, err
	}
	events := window.sourceEvents
	candidates := safeCompactionSourceEndsWithWitness(
		events,
		window.witnessEvents,
		atomicGroupsFromRecords(window.atomicGroups),
	)
	if len(candidates) == 0 {
		return 0, nil
	}
	if candidates[len(candidates)-1] == plan.EventSequenceEnd {
		candidates = candidates[:len(candidates)-1]
		if len(candidates) == 0 {
			return 0, nil
		}
	}
	prefixBytes := make([]int, len(events))
	totalBytes := 0
	for index := range events {
		rendered, err := renderEventSource(events[index : index+1])
		if err != nil {
			return 0, err
		}
		totalBytes += len(rendered)
		prefixBytes[index] = totalBytes
	}
	targetBytes := totalBytes / 2
	completeTurnEnds := completeTurnSourceEnds(events, window.witnessEvents, candidates)
	before, after := boundariesAroundTarget(completeTurnEnds, func(end int64) bool {
		count := int(end-plan.EventSequenceStart) + 1
		return prefixBytes[count-1] <= targetBytes
	})
	if before > 0 {
		return before, nil
	}
	if after > 0 {
		return after, nil
	}
	chosen := candidates[0]
	for _, candidate := range candidates {
		count := int(candidate-plan.EventSequenceStart) + 1
		if prefixBytes[count-1] > targetBytes {
			break
		}
		chosen = candidate
	}
	return chosen, nil
}

type compactionBoundaryWindow struct {
	sourceEvents  []executionstore.CompactionSourceEventRecord
	witnessEvents []executionstore.CompactionSourceEventRecord
	atomicGroups  []executionstore.CompactionAtomicGroupRecord
}

func (r Runner) loadCompactionBoundaryWindow(
	ctx context.Context,
	plan Plan,
) (compactionBoundaryWindow, error) {
	sourceEvents, err := r.loadClosedEventRange(ctx, plan)
	if err != nil {
		return compactionBoundaryWindow{}, err
	}
	witnessEvents := sourceEvents
	if plan.EventSequenceEnd < plan.InputEventSequence &&
		compactionSourceNeedsLaterModelWitness(sourceEvents) {
		witnessPlan := plan
		witnessPlan.EventSequenceEnd = plan.InputEventSequence
		witnessEvents, err = r.loadClosedEventRange(ctx, witnessPlan)
		if err != nil {
			return compactionBoundaryWindow{}, err
		}
	}
	atomicGroups, err := r.Store.ListCompactionAtomicGroups(
		ctx,
		plan.ProjectID,
		plan.AgentID,
		plan.EventSequenceStart-1,
		plan.EventSequenceEnd,
	)
	if err != nil {
		return compactionBoundaryWindow{}, err
	}
	return compactionBoundaryWindow{
		sourceEvents:  sourceEvents,
		witnessEvents: witnessEvents,
		atomicGroups:  atomicGroups,
	}, nil
}

func compactionSourceNeedsLaterModelWitness(
	events []executionstore.CompactionSourceEventRecord,
) bool {
	latestModelOutputByTurn := make(map[storage.ID]int64)
	for _, event := range events {
		if event.Kind == string(agentevents.KindModelOutput) {
			latestModelOutputByTurn[event.TurnID] = event.Sequence
		}
	}
	for _, event := range events {
		if event.Kind == string(agentevents.KindAgentInput) &&
			compactionEventHasModelSemantics(event) &&
			latestModelOutputByTurn[event.TurnID] <= event.Sequence {
			return true
		}
	}
	return false
}

func compactionBundle(input RunInput, priorSummary, sourceText string) (modelcontext.Bundle, error) {
	prompt := SummaryPrompt(priorSummary, sourceText)
	content, err := marshalJSON([]map[string]string{{"type": "text", "text": prompt}})
	if err != nil {
		return modelcontext.Bundle{}, err
	}
	return modelcontext.Bundle{
		ProjectID:          input.Plan.ProjectID,
		AgentID:            input.Plan.AgentID,
		TurnID:             input.TurnID,
		OpeningInputIDs:    input.OpeningInputIDs,
		InputEventSequence: input.Plan.InputEventSequence,
		SystemPrompt: "You compact a closed Omnara event prefix into a cumulative continuation checkpoint. " +
			"Return only the requested concise summary and never call tools.",
		Messages: []modelcontext.Message{{
			ID:       fmt.Sprintf("compaction_source_%d_%d", input.Plan.EventSequenceStart, input.Plan.EventSequenceEnd),
			Role:     modelprotocol.RoleUser,
			Sequence: input.Plan.EventSequenceEnd,
			Content:  content,
		}},
	}, nil
}

func renderEventSource(events []executionstore.CompactionSourceEventRecord) (string, error) {
	const completedToolOutputProjectionBytes = 32_768
	reducer := modelcontext.ToolOutputReducer{PreviewBytes: completedToolOutputProjectionBytes}
	var builder strings.Builder
	for _, event := range events {
		if !compactionEventHasModelSemantics(event) {
			continue
		}
		label := event.Kind
		if event.InputKind != "" {
			label += "." + event.InputKind
		}
		fmt.Fprintf(&builder, "Event %d (%s) at %s\n", event.Sequence, label, event.CreatedAt.UTC().Format(time.RFC3339Nano))
		if event.ToolName != "" {
			fmt.Fprintf(&builder, "Tool: %s call_id=%s", event.ToolName, event.ProviderCallID)
			if event.ToolOutcome != "" {
				fmt.Fprintf(&builder, " outcome=%s", event.ToolOutcome)
			}
			builder.WriteString("\n")
		}
		parts := event.ContentParts
		if event.ToolName != "" {
			reduced, err := reducer.Reduce(parts)
			if err != nil {
				return "", fmt.Errorf("reduce tool output for event %d: %w", event.Sequence, err)
			}
			parts = reduced
		}
		if content := renderContentParts(parts); content != "" {
			builder.WriteString(content)
			if !strings.HasSuffix(content, "\n") {
				builder.WriteString("\n")
			}
		}
	}
	return builder.String(), nil
}

func renderContentParts(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return strings.TrimSpace(string(raw))
	}
	var builder strings.Builder
	for _, part := range parts {
		switch part["type"] {
		case "text", "error":
			if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
				label := "Text: "
				if part["type"] == "error" {
					label = "Error: "
				}
				builder.WriteString(label)
				builder.WriteString(strings.TrimSpace(text))
				builder.WriteString("\n")
			}
		case "reasoning":
			// Hidden reasoning is excluded from summaries.
		case "tool_call":
			builder.WriteString("Tool call: ")
			if name, ok := part["name"].(string); ok {
				builder.WriteString(name)
			}
			if toolInput, ok := part["input"]; ok {
				builder.WriteString(" input=")
				builder.WriteString(compactJSONValue(toolInput))
			}
			builder.WriteString("\n")
		case "media_ref":
			builder.WriteString("Artifact: ")
			builder.WriteString(compactJSONValue(part))
			builder.WriteString("\n")
		case "structured_data":
			if value, ok := part["value"]; ok {
				builder.WriteString("Structured data: ")
				builder.WriteString(compactJSONValue(value))
				builder.WriteString("\n")
			}
		default:
			builder.WriteString("Content: ")
			builder.WriteString(compactJSONValue(part))
			builder.WriteString("\n")
		}
	}
	return strings.TrimRight(builder.String(), "\n")
}

func compactJSONValue(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(body)
}

func estimateTokens(value string) int {
	if value == "" {
		return 0
	}
	return len(value)/4 + 1
}

func (r Runner) progressivePolicy() ProgressiveCheckpointPolicy {
	if r.ProgressivePolicy != nil {
		return r.ProgressivePolicy
	}
	return BoundedProgressiveCheckpointPolicy{}
}
