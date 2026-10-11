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
	unit, h, err := beginExecution(ctx, s, output.ProjectID, output.AgentID)
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
		err = unit.Savepoint(ctx, func(_ *agentexecution.Unit) error {
			work, err := h.Advance(ctx, output.RuntimeLockID, input.AllowModelWork, input.PrepareModel)
			if err != nil {
				return err
			}
			if err = applyAdmissionDestination(ctx, unit, output.ProjectID, output.AgentID, work.Admission); err != nil {
				return err
			}
			shaped, err := shapeOwnedWork(ctx, unit, output.ProjectID, output.AgentID, work)
			if err != nil {
				return err
			}
			result = OwnedAgentWorkTransition{Work: shaped, Continued: !work.Released}
			return nil
		})
	}
	if errors.Is(err, agentexecution.ErrLockPlan) {
		return OwnedAgentWorkTransition{}, err
	}
	if !advance || !accepted.Created || err != nil {
		result = OwnedAgentWorkTransition{Fallback: err}
		if err = h.Release(ctx, output.RuntimeLockID); err != nil {
			return result, err
		}
	}
	if err = unit.Commit(ctx, "accept model output and advance"); err != nil {
		if !advance || errors.Is(err, agentexecution.ErrLockPlan) {
			return result, err
		}
		_ = unit.Rollback(ctx)
		fallback, acceptErr := s.acceptModelOutput(ctx, input, false)
		fallback.Fallback = errors.Join(result.Fallback, err, fallback.Fallback)
		return fallback, acceptErr
	}
	return result, nil
}
