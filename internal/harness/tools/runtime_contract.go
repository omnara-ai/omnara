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
	return agentConfigContract(ctx, e.Store, turn.ProjectID, contextRow.AgentConfigID)
}
