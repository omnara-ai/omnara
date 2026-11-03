//go:build integration

package agentexecution

import (
	"context"
	"fmt"
)

func (h *Handle) ReconstructExecution(ctx context.Context) (ExecutionSnapshot, error) {
	route, err := h.Route()
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	valid, err := validateFullExecutionLineage(ctx, h.unit.DB(), route.AgentID)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	if !valid {
		return ExecutionSnapshot{}, fmt.Errorf("%w: agent %s immutable lineage", ErrInvalidState, route.AgentID)
	}
	return reconstructExecution(ctx, h.unit.DB(), route)
}

func (h *Handle) Repair(ctx context.Context) (ExecutionSnapshot, error) {
	snapshot, err := h.ReconstructExecution(ctx)
	if err != nil {
		h.unit.aborted = true
		return ExecutionSnapshot{}, err
	}
	h.mutation = &executionMutation{head: snapshot.Head, loaded: &snapshot, dirty: true, insertHead: true}
	return snapshot, nil
}
