//go:build integration

package agentexecution

import "context"

func BeginTestUnit(ctx context.Context, cell *AgentCell) (*Unit, error) {
	return Retry(ctx, "test unit", cell.Begin)
}
