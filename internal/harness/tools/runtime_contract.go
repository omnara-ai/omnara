package tools

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/agentconfig"
)

func (e Executor) runtimeContractForTurn(
	ctx context.Context,
	turn Turn,
) (agentconfig.RuntimeContract, error) {
	contextRow, found, err := e.Store.Execution().GetModelCallContext(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.ModelCallContextID,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, err
	}
	if !found {
		return agentconfig.RuntimeContract{}, fmt.Errorf(
			"model call context %s not found",
			turn.ModelCallContextID,
		)
	}
	config, found, err := e.Store.Execution().GetAgentConfig(
		ctx,
		turn.ProjectID,
		contextRow.AgentConfigID,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, err
	}
	if !found {
		return agentconfig.RuntimeContract{}, fmt.Errorf(
			"agent config %s not found",
			contextRow.AgentConfigID,
		)
	}
	return agentconfig.RuntimeContractFromCompiled(
		config.CompiledDefinition,
		config.CompilerVersion,
		config.EffectiveDefinitionHash,
	)
}
