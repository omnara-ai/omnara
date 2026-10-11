package agentexecution

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type LockPlanError struct {
	Agents  []lifecyclelock.AgentRef
	claimed uuid.UUID
}

func (e *LockPlanError) Error() string {
	return fmt.Sprintf("%s: required agents %v", ErrLockPlan, e.Agents)
}
func (e *LockPlanError) Unwrap() error { return ErrLockPlan }

type lockPlanKey struct{}
type retryPlan struct {
	agents  []lifecyclelock.AgentRef
	claimed uuid.UUID
}

func Retry[T any](ctx context.Context, operation string, run func(context.Context) (T, error)) (T, error) {
	if _, nested := ctx.Value(lockPlanKey{}).(retryPlan); nested {
		return run(ctx)
	}
	return storeutil.RetryTransaction(ctx, operation, func() (T, error) {
		plan := retryPlan{}
		for attempt := 0; ; attempt++ {
			result, err := run(context.WithValue(ctx, lockPlanKey{}, plan))
			var missing *LockPlanError
			if !errors.As(err, &missing) || attempt == 7 || ctx.Err() != nil {
				return result, err
			}
			previous := len(plan.agents)
			plan.agents = mergeAgentRefs(plan.agents, missing.Agents)
			if len(plan.agents) == previous {
				return result, err
			}
			if missing.claimed != uuid.Nil {
				plan.claimed = missing.claimed
			}
		}
	})
}

func (u *Unit) expandPlan(refs ...lifecyclelock.AgentRef) error {
	var held []lifecyclelock.AgentRef
	for _, h := range u.handles {
		if !h.created {
			held = append(held, lifecyclelock.AgentRef{ProjectID: h.route.ProjectID, AgentID: h.route.AgentID})
		}
	}
	agents := mergeAgentRefs(held, refs)
	u.aborted = true
	return &LockPlanError{Agents: agents, claimed: u.claimedAgent}
}

func mergeAgentRefs(a, b []lifecyclelock.AgentRef) []lifecyclelock.AgentRef {
	seen := make(map[lifecyclelock.AgentRef]struct{}, len(a)+len(b))
	for _, refs := range [][]lifecyclelock.AgentRef{a, b} {
		for _, ref := range refs {
			seen[ref] = struct{}{}
		}
	}
	result := make([]lifecyclelock.AgentRef, 0, len(seen))
	for ref := range seen {
		result = append(result, ref)
	}
	slices.SortFunc(result, func(a, b lifecyclelock.AgentRef) int {
		return compareRoutes(AgentRoute{ProjectID: a.ProjectID,
			AgentID: a.AgentID},
			AgentRoute{ProjectID: b.ProjectID,
				AgentID: b.AgentID})
	})
	return result
}
