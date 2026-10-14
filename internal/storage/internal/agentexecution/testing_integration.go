//go:build integration

package agentexecution

import "context"

func BeginTestUnit(ctx context.Context, cell *AgentCell) (*Unit, error) {
	return ReplanTest(ctx, cell.Begin)
}

func ReplanTest[T any](ctx context.Context, run func(context.Context) (T, error)) (T, error) {
	if _, nested := ctx.Value(lockPlanKey{}).(retryPlan); nested {
		return run(ctx)
	}
	return replan(ctx, run)
}
