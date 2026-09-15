package tools

import (
	"context"
	"errors"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

var ErrNoActiveAgentMachineBinding = errors.New("no_active_agent_machine_binding")
var ErrMachineSelectionRequired = errors.New("machine_selection_required")
var ErrMachineIDUnavailable = errors.New("machine_id_unavailable")

func (e Executor) ResolveMachineExecutionTarget(
	ctx context.Context,
	turn Turn,
	machineID storage.ID,
) (executionstore.AgentMachineBindingRecord, error) {
	if e.Store == nil {
		return executionstore.AgentMachineBindingRecord{}, errors.New("tool executor store is required")
	}
	bindings, err := e.Store.Execution().ListExecutableAgentMachineBindings(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return executionstore.AgentMachineBindingRecord{}, err
	}
	return selectMachineExecutionTarget(bindings, machineID)
}

func resolveMachineExecutionTargetForToolCall(
	ctx context.Context,
	reader *executionstore.ToolCallReader,
	machineID storage.ID,
) (executionstore.AgentMachineBindingRecord, error) {
	bindings, err := reader.ListExecutableAgentMachineBindings(ctx)
	if err != nil {
		return executionstore.AgentMachineBindingRecord{}, err
	}
	return selectMachineExecutionTarget(bindings, machineID)
}

func selectMachineExecutionTarget(
	bindings []executionstore.AgentMachineBindingRecord,
	machineID storage.ID,
) (executionstore.AgentMachineBindingRecord, error) {
	if machineID != storage.NilID {
		for _, binding := range bindings {
			if binding.MachineID == machineID {
				return binding, nil
			}
		}
		return executionstore.AgentMachineBindingRecord{}, ErrMachineIDUnavailable
	}
	switch len(bindings) {
	case 0:
		return executionstore.AgentMachineBindingRecord{}, ErrNoActiveAgentMachineBinding
	case 1:
		return bindings[0], nil
	default:
		return executionstore.AgentMachineBindingRecord{}, ErrMachineSelectionRequired
	}
}
