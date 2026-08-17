package kernel

import (
	"context"
	"fmt"
	"sort"

	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (e AgentExecutor) compactionRunner(
	resolver model.Resolver,
	builder modelcontext.Builder,
) compaction.Runner {
	return compaction.Runner{
		Store:           compaction.NewStore(e.Store.Execution()),
		Resolver:        resolver,
		ContextBuilder:  builder,
		Now:             e.Now,
		ModelRetryDelay: e.ModelRetryDelay,
	}
}

func (e AgentExecutor) resumeCompactionContext(
	ctx context.Context,
	input ModelWorkExecution,
	builder modelcontext.Builder,
	resolver model.Resolver,
	contextRow executionstore.ModelCallContextRecord,
) error {
	if contextRow.SourceEventSequenceEnd == nil {
		return fmt.Errorf("active compaction context has no source range: %w", storeerr.ErrStateTransitionConflict)
	}
	sourceStart := int64(1)
	checkpoint, found, err := e.Store.Execution().GetLatestApplicableContextCheckpoint(
		ctx,
		contextRow.ProjectID,
		contextRow.AgentID,
		contextRow.InputEventSequence,
	)
	if err != nil {
		return err
	}
	if found {
		sourceStart = checkpoint.SummarizedThroughEventSequence + 1
	}
	parent, found, err := e.Store.Execution().GetNormalModelCallContextForFrontier(
		ctx,
		contextRow.ProjectID,
		contextRow.AgentID,
		contextRow.InputEventSequence,
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("active compaction context has no parent normal call: %w", storeerr.ErrStateTransitionConflict)
	}
	_, err = e.compactionRunner(resolver, builder).Run(
		ctx,
		compaction.RunInput{
			Plan: compaction.Plan{
				ProjectID:          contextRow.ProjectID,
				AgentID:            contextRow.AgentID,
				InputEventSequence: contextRow.InputEventSequence,
				EventSequenceStart: sourceStart,
				EventSequenceEnd:   *contextRow.SourceEventSequenceEnd,
			},
			TurnID:                   input.TurnID,
			OpeningInputIDs:          input.InputIDs,
			OpeningEventSequence:     input.OpeningEventSequence,
			RuntimeLockID:            input.RuntimeLockID,
			ParentModelCallContextID: parent.ID,
		},
	)
	return err
}

func (e AgentExecutor) planCompactionForContext(
	ctx context.Context,
	contextRow executionstore.ModelCallContextRecord,
	client model.Client,
	input ModelWorkExecution,
) (compaction.Plan, bool, error) {
	frontier := contextRow.InputEventSequence
	checkpoint, hasCheckpoint, err := e.Store.Execution().GetLatestApplicableContextCheckpoint(
		ctx,
		contextRow.ProjectID,
		contextRow.AgentID,
		frontier,
	)
	if err != nil {
		return compaction.Plan{}, false, err
	}
	summarizedThrough := int64(0)
	if hasCheckpoint {
		summarizedThrough = checkpoint.SummarizedThroughEventSequence
	}
	if frontier <= summarizedThrough+1 {
		return compaction.Plan{}, false, nil
	}
	groups, err := e.Store.Execution().ListCompactionAtomicGroups(
		ctx,
		contextRow.ProjectID,
		contextRow.AgentID,
		summarizedThrough,
		frontier,
	)
	if err != nil {
		return compaction.Plan{}, false, err
	}
	atomicGroups := make([]compaction.AtomicGroup, 0, len(groups))
	for _, group := range groups {
		atomicGroups = append(atomicGroups, compaction.AtomicGroup{
			Kind:          group.Kind,
			StartSequence: group.StartSequence,
			EndSequence:   group.EndSequence,
		})
	}
	events, err := loadContextEventsForCompaction(
		ctx,
		e.Store.Execution(),
		contextRow.ProjectID,
		contextRow.AgentID,
		summarizedThrough,
		frontier,
	)
	if err != nil {
		return compaction.Plan{}, false, err
	}
	if len(events) < 2 {
		return compaction.Plan{}, false, nil
	}
	capabilities := model.CapabilitiesForClient(client)
	requestPolicy := model.RequestPolicyFromCapabilities(capabilities)
	recentTailTargetTokens := compaction.RecentTailTargetTokens(
		model.UsableInputTokensForRequest(capabilities, requestPolicy),
	)
	retainFrom := retainFromForRecentEvents(events, recentTailTargetTokens)
	maxRetainFrom := frontier + 1
	if modelCallOpeningRequiresVerbatimRetention(
		events,
		summarizedThrough,
		input.OpeningEventSequence,
	) {
		if retainFrom > input.OpeningEventSequence {
			retainFrom = input.OpeningEventSequence
		}
		maxRetainFrom = input.OpeningEventSequence
	}
	boundaryInput := compaction.RetainBoundaryInput{
		SourceEventSequenceStart:  summarizedThrough + 1,
		DesiredRetainFromSequence: retainFrom,
		DesiredRetainTokens:       recentTailTargetTokens,
		MaximumRetainFromSequence: maxRetainFrom,
		Events:                    events,
		AtomicGroups:              atomicGroups,
	}
	retainFrom, ok, err := compaction.SelectRetainFromEventSequence(boundaryInput)
	if err != nil || !ok {
		return compaction.Plan{}, false, err
	}
	boundaryInput.DesiredRetainFromSequence = retainFrom
	projectionSummary := "[Earlier conversation compacted.]"
	if hasCheckpoint {
		projectionSummary = checkpoint.Summary + "\n\n[Additional closed history compacted.]"
	}
	retainFrom, ok, err = e.clampRetainFromToModelBudget(
		ctx,
		contextRow,
		client,
		input,
		boundaryInput,
		projectionSummary,
	)
	if err != nil || !ok {
		return compaction.Plan{}, false, err
	}
	plan, ok, err := compaction.PlanCheckpoint(compaction.PlanInput{
		ProjectID:                      contextRow.ProjectID,
		AgentID:                        contextRow.AgentID,
		InputEventSequence:             frontier,
		SummarizedThroughEventSequence: summarizedThrough,
		RetainFromEventSequence:        retainFrom,
		AtomicGroups:                   atomicGroups,
	})
	return plan, ok, err
}

func (e AgentExecutor) clampRetainFromToModelBudget(
	ctx context.Context,
	contextRow executionstore.ModelCallContextRecord,
	client model.Client,
	input ModelWorkExecution,
	boundaryInput compaction.RetainBoundaryInput,
	projectionSummary string,
) (int64, bool, error) {
	candidates, err := compaction.RetainFromEventSequenceCandidates(boundaryInput)
	if err != nil {
		return 0, false, err
	}
	snapshot, err := e.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		contextRow.ProjectID,
		contextRow.AgentID,
		contextRow.InputEventSequence,
	)
	if err != nil {
		return 0, false, err
	}
	return firstFittingRetainFrom(
		candidates,
		boundaryInput.DesiredRetainFromSequence,
		func(retainFrom int64) (bool, error) {
			bundle, buildErr := e.contextBuilder().Build(ctx, modelcontext.BuildInput{
				ProjectID:           contextRow.ProjectID,
				AgentID:             contextRow.AgentID,
				TurnID:              input.TurnID,
				OpeningInputIDs:     input.InputIDs,
				Now:                 input.Now,
				AgentConfigSnapshot: &snapshot,
				MediaProjector:      model.MediaProjectorForClient(client),
				CheckpointOverride: &modelcontext.CheckpointRef{
					SummarizedThroughEventSequence: retainFrom - 1,
					Summary:                        projectionSummary,
				},
			})
			if buildErr != nil {
				return false, buildErr
			}
			prepared, prepareErr := model.PrepareForSend(
				ctx,
				client,
				model.PrepareForSendInput{
					Context: bundle,
					Policy: model.RequestPolicyFromCapabilities(
						model.CapabilitiesForClient(client),
					),
					ErrorSource: modelErrorSourceForClient(client),
				},
			)
			if prepareErr != nil {
				return false, prepareErr
			}
			return prepared.InputBudget.Fits(), nil
		},
	)
}

func firstFittingRetainFrom(
	candidates []int64,
	desired int64,
	fits func(int64) (bool, error),
) (int64, bool, error) {
	low := sort.Search(len(candidates), func(index int) bool { return candidates[index] >= desired })
	high := len(candidates) - 1
	best := -1
	for low <= high {
		mid := low + (high-low)/2
		ok, err := fits(candidates[mid])
		if err != nil {
			return 0, false, err
		}
		if ok {
			best = mid
			high = mid - 1
			continue
		}
		low = mid + 1
	}
	if best < 0 {
		return 0, false, nil
	}
	return candidates[best], true, nil
}

func modelCallOpeningRequiresVerbatimRetention(
	events []executionstore.CompactionSourceEventRecord,
	summarizedThrough, openingEventSequence int64,
) bool {
	if openingEventSequence <= summarizedThrough {
		return false
	}
	return compaction.OpeningEventsRequireVerbatimRetention(events, openingEventSequence)
}

func loadContextEventsForCompaction(
	ctx context.Context,
	store interface {
		ListCompactionSourceEvents(
			context.Context,
			storage.ID,
			storage.ID,
			int64,
			int32,
		) ([]executionstore.CompactionSourceEventRecord, error)
	},
	projectID, agentID storage.ID,
	afterSequence, watermark int64,
) ([]executionstore.CompactionSourceEventRecord, error) {
	var records []executionstore.CompactionSourceEventRecord
	after := afterSequence
	for after < watermark {
		page, err := store.ListCompactionSourceEvents(ctx, projectID, agentID, after, 500)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, event := range page {
			if event.Sequence > watermark {
				return records, nil
			}
			records = append(records, event)
			after = event.Sequence
		}
		if len(page) < 500 || after >= watermark {
			break
		}
	}
	return records, nil
}

func retainFromForRecentEvents(events []executionstore.CompactionSourceEventRecord, recentTailTargetTokens int) int64 {
	if len(events) == 0 {
		return 0
	}
	if recentTailTargetTokens <= 0 {
		return events[len(events)-1].Sequence
	}
	tokens := 0
	retainFrom := events[len(events)-1].Sequence
	for index := len(events) - 1; index >= 0; index-- {
		retainFrom = events[index].Sequence
		tokens += compaction.EstimateSourceEventTokens(events[index])
		if tokens >= recentTailTargetTokens {
			break
		}
	}
	return retainFrom
}
