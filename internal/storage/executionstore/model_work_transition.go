package executionstore

import (
	"context"
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
}

func (s *Store) AcceptModelOutputAndAdvance(
	ctx context.Context,
	input AcceptModelOutputAndAdvanceInput,
) (OwnedAgentWorkTransition, error) {
	output := input.Output
	unit, h, err := beginExecution(ctx, s, output.ProjectID, output.AgentID)
	if err != nil {
		return OwnedAgentWorkTransition{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	accepted, work, err := h.AcceptAndAdvance(
		ctx,
		executionOutput(output, input.ToolCallBindings),
		input.AllowModelWork,
		input.PrepareModel,
	)
	if err != nil {
		return OwnedAgentWorkTransition{}, err
	}
	result := OwnedAgentWorkTransition{}
	if accepted.Created {
		if err := applyAdmissionDestination(ctx,
			unit,
			input.Output.ProjectID,
			input.Output.AgentID,
			work.Admission); err != nil {
			return result, err
		}
		result.Work, err = shapeOwnedWork(ctx, unit, output.ProjectID, output.AgentID, work)
		if err != nil {
			return result, err
		}
		result.Continued = !work.Released
	}
	if err = unit.Commit(ctx, "accept model output and advance"); err != nil {
		return OwnedAgentWorkTransition{}, err
	}
	return result, nil
}
