package executionstore

import (
	"context"
	"errors"

	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

type AcceptModelOutputAndAdvanceInput struct {
	Output           RecordModelOutputAndCompleteContextInput
	ToolCallBindings []ToolCallBindingInput
	AllowModelWork   bool
	PrepareModel     bool
}

type OwnedAgentWorkTransition struct {
	Work      ClaimedAgentWork
	Continued bool
	Fallback  error
}

func (s *Store) AcceptModelOutputAndAdvance(
	ctx context.Context,
	input AcceptModelOutputAndAdvanceInput,
) (OwnedAgentWorkTransition, error) {
	return agentexecution.Retry(ctx,
		"accept model output",
		func(ctx context.Context) (OwnedAgentWorkTransition, error) {
			return s.acceptModelOutput(ctx, input, true)
		})
}

func (s *Store) acceptModelOutput(ctx context.Context,
	input AcceptModelOutputAndAdvanceInput,
	advance bool) (OwnedAgentWorkTransition,
	error) {
	output := input.Output
	unit, h, err := beginExecution(ctx, s, output.ProjectID, output.AgentID,
		agentexecution.ParentEffectKind(output.ProviderResponse) != "")
	if err != nil {
		return OwnedAgentWorkTransition{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	accepted, err := h.AcceptOutput(ctx, executionOutput(output, input.ToolCallBindings))
	if err != nil {
		return OwnedAgentWorkTransition{}, err
	}
	result := OwnedAgentWorkTransition{}
	if advance && accepted.Created {
		var work agentexecution.OwnedWork
		work, err = h.Advance(ctx, output.RuntimeLockID, input.AllowModelWork, input.PrepareModel)
		if err == nil {
			err = applyAdmissionDestination(ctx, unit, output.ProjectID, output.AgentID, work.Admission)
		}
		if err == nil {
			result.Work, err = shapeOwnedWork(ctx, unit, output.ProjectID, output.AgentID, work)
			result.Continued = !work.Released
		}
	} else {
		err = h.Release(ctx, output.RuntimeLockID)
	}
	if err == nil {
		err = unit.Commit(ctx, "accept model output and advance")
	}
	if err == nil || !advance || errors.Is(err, agentexecution.ErrLockPlan) {
		return result, err
	}
	_ = unit.Rollback(ctx)
	fallback, acceptErr := s.acceptModelOutput(ctx, input, false)
	fallback.Fallback = errors.Join(err, fallback.Fallback)
	return fallback, acceptErr
}
