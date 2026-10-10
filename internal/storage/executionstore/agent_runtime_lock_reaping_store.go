package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/errutil"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const maximumReportedAgentRuntimeLockReapErrors = 10

func (s *Store) ReapExpiredAgentRuntimeLocks(ctx context.Context, batchSize int32) (int64, error) {
	if batchSize <= 0 {
		return 0, errors.New("runtime lock reap batch size must be positive")
	}
	candidates, err := s.q.ListExpiredAgentRuntimeLockCandidates(
		ctx,
		dbsqlc.ListExpiredAgentRuntimeLockCandidatesParams{BatchSize: batchSize},
	)
	if err != nil {
		return 0, fmt.Errorf("list expired agent runtime locks: %w", err)
	}
	var total int64
	var reapErrs []error
	var suppressedErrors int
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		reaped, err := s.reapExpiredAgentRuntimeLock(
			ctx,
			candidate.ProjectID,
			candidate.AgentID,
			candidate.ID,
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && errutil.OnlyMatches(err, ctxErr) {
				break
			}
			err = fmt.Errorf(
				"reap runtime lock %s for agent %s in project %s: %w",
				candidate.ID,
				candidate.AgentID,
				candidate.ProjectID,
				err,
			)
			if len(reapErrs) < maximumReportedAgentRuntimeLockReapErrors {
				reapErrs = append(reapErrs, err)
			} else {
				suppressedErrors++
			}
			continue
		}
		if reaped {
			total++
		}
	}
	if suppressedErrors > 0 {
		reapErrs = append(reapErrs, fmt.Errorf(
			"%d additional runtime lock reap errors omitted",
			suppressedErrors,
		))
	}
	if len(reapErrs) > 0 {
		return total, errors.Join(reapErrs...)
	}
	return total, ctx.Err()
}

func (s *Store) reapExpiredAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, runtimeLockID uuid.UUID,
) (bool, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin reap expired agent runtime lock: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()

	reaped, err := reapExpiredAgentRuntimeLockTx(ctx, unit, projectID, agentID, runtimeLockID)
	if err != nil || !reaped {
		return false, err
	}
	if err := unit.Commit(ctx, "reap expired agent runtime lock"); err != nil {
		return false, err
	}
	return true, nil
}

func reapExpiredAgentRuntimeLockTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID, runtimeLockID uuid.UUID,
) (bool, error) {
	plan, err := unit.PlanAgentFamily(ctx, projectID, agentID, agentexecution.LifecycleAuthority{})
	var locked bool
	if err == nil {
		locked, err = unit.TryLockAgents(ctx, plan)
	}
	if errors.Is(err, storeerr.ErrNotFound) {
		return false, nil
	}
	if err != nil || !locked {
		return false, err
	}
	h, err := unit.Handle(projectID, agentID)
	if err != nil {
		return false, err
	}
	return h.Reap(ctx, runtimeLockID)
}
