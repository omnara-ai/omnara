package tools

import (
	"context"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

var ErrNoActiveAgentMachineBinding = errors.New("no_active_agent_machine_binding")
var ErrMachineSelectionRequired = errors.New("machine_selection_required")
var ErrMachineRefUnavailable = errors.New("machine_ref_unavailable")

const (
	defaultMachineBindingWaitTimeout  = 30 * time.Second
	defaultMachineBindingPollInterval = 250 * time.Millisecond
)

func (e Executor) ResolveMachineExecutionTarget(
	ctx context.Context,
	turn Turn,
	machineRef string,
) (executionstore.AgentMachineBindingRecord, error) {
	if e.Store == nil {
		return executionstore.AgentMachineBindingRecord{}, errors.New("tool executor store is required")
	}
	bindings, err := e.Store.Execution().ListExecutableAgentMachineBindings(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return executionstore.AgentMachineBindingRecord{}, err
	}
	return selectMachineExecutionTarget(bindings, machineRef)
}

func (e Executor) waitForMachineExecutionTarget(
	ctx context.Context,
	turn Turn,
	machineRef string,
) (executionstore.AgentMachineBindingRecord, error) {
	if e.Store == nil {
		return executionstore.AgentMachineBindingRecord{}, errors.New("tool executor store is required")
	}
	timeout := e.MachineBindingWaitTimeout
	if timeout <= 0 {
		timeout = defaultMachineBindingWaitTimeout
	}
	interval := e.MachineBindingPollInterval
	if interval <= 0 {
		interval = defaultMachineBindingPollInterval
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		bindings, err := e.Store.Execution().ListExecutableAgentMachineBindings(
			waitCtx,
			turn.ProjectID,
			turn.AgentID,
		)
		if err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return executionstore.AgentMachineBindingRecord{}, ErrNoActiveAgentMachineBinding
			}
			return executionstore.AgentMachineBindingRecord{}, err
		}
		if len(bindings) > 0 {
			return selectMachineExecutionTarget(bindings, machineRef)
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return executionstore.AgentMachineBindingRecord{}, ctx.Err()
			}
			return executionstore.AgentMachineBindingRecord{}, ErrNoActiveAgentMachineBinding
		case <-timer.C:
		}
	}
}

func resolveMachineExecutionTargetForToolCall(
	ctx context.Context,
	reader *executionstore.ToolCallReader,
	machineRef string,
) (executionstore.AgentMachineBindingRecord, error) {
	bindings, err := reader.ListExecutableAgentMachineBindings(ctx)
	if err != nil {
		return executionstore.AgentMachineBindingRecord{}, err
	}
	return selectMachineExecutionTarget(bindings, machineRef)
}

func selectMachineExecutionTarget(
	bindings []executionstore.AgentMachineBindingRecord,
	machineRef string,
) (executionstore.AgentMachineBindingRecord, error) {
	if machineRef != "" {
		for _, binding := range bindings {
			if binding.MachineRef == machineRef {
				return binding, nil
			}
		}
		return executionstore.AgentMachineBindingRecord{}, ErrMachineRefUnavailable
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
