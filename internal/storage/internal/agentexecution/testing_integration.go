//go:build integration

package agentexecution

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

func BeginTestUnit(ctx context.Context, cell *AgentCell) (*Unit, error) {
	return ReplanTest(ctx, cell.Begin)
}

func ReplanTest[T any](ctx context.Context, run func(context.Context) (T, error)) (T, error) {
	if _, nested := ctx.Value(lockPlanKey{}).(retryPlan); nested {
		return run(ctx)
	}
	return replan(ctx, run)
}

func LockTestAgent(
	ctx context.Context, unit *Unit, projectID, agentID uuid.UUID, authority *RuntimeAuthority,
) (*Handle, error) {
	refs := []lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: agentID}}
	if err := unit.LockAgentRefs(ctx, refs, authority); err != nil {
		return nil, err
	}
	return unit.Handle(projectID, agentID)
}

func CapacityExemptForTest(input ReceiveContentInput) ReceiveContentInput {
	input.capacityExempt = true
	return input
}
