package lifecyclelock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Canonical order: scope gates, account principals, agent profiles, agent
// sources, pools and grants, machines, existing agents, environment keys, then
// child state. A transaction may enter later in the order, but it must never
// acquire an earlier class afterward. IDs in the same class are locked in
// stable UUID order.

type PoolRef struct {
	OrgID  uuid.UUID
	PoolID uuid.UUID
}

type MachineRef struct {
	OrgID     uuid.UUID
	MachineID uuid.UUID
}

type AgentRef struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
}

func OrganizationShared(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) error {
	return organizationShared(ctx, dbsqlc.New(tx), orgID)
}

func organizationShared(ctx context.Context, q *dbsqlc.Queries, orgID uuid.UUID) error {
	if err := q.LockOrganizationLifecycleShared(
		ctx,
		dbsqlc.LockOrganizationLifecycleSharedParams{OrgID: orgID},
	); err != nil {
		return fmt.Errorf("lock organization lifecycle shared: %w", err)
	}
	return nil
}

func EnterActiveOrganization(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) error {
	q := dbsqlc.New(tx)
	return enterActiveOrganization(ctx, q, orgID)
}

func enterActiveOrganization(ctx context.Context, q *dbsqlc.Queries, orgID uuid.UUID) error {
	if err := organizationShared(ctx, q, orgID); err != nil {
		return err
	}
	// Revalidation is separate so READ COMMITTED sees changes committed while the lock waited.
	active, err := q.OrgExistsActive(ctx, dbsqlc.OrgExistsActiveParams{ID: orgID})
	if err != nil {
		return fmt.Errorf("revalidate active organization lifecycle: %w", err)
	}
	if !active {
		return storeerr.ErrNotFound
	}
	return nil
}

func OrganizationExclusive(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) error {
	q := dbsqlc.New(tx)
	if err := q.LockOrganizationLifecycleExclusive(
		ctx,
		dbsqlc.LockOrganizationLifecycleExclusiveParams{OrgID: orgID},
	); err != nil {
		return fmt.Errorf("lock organization lifecycle exclusive: %w", err)
	}
	return nil
}

func ProjectShared(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) error {
	return projectShared(ctx, dbsqlc.New(tx), projectID)
}

func projectShared(ctx context.Context, q *dbsqlc.Queries, projectID uuid.UUID) error {
	if err := q.LockProjectLifecycleShared(
		ctx,
		dbsqlc.LockProjectLifecycleSharedParams{ProjectID: projectID},
	); err != nil {
		return fmt.Errorf("lock project lifecycle shared: %w", err)
	}
	return nil
}

func ProjectsShared(ctx context.Context, tx pgx.Tx, projectIDs []uuid.UUID) error {
	q := dbsqlc.New(tx)
	for _, projectID := range orderedIDs(projectIDs) {
		if err := projectShared(ctx, q, projectID); err != nil {
			return err
		}
	}
	return nil
}

func EnterActiveProject(
	ctx context.Context,
	tx pgx.Tx,
	orgID, projectID uuid.UUID,
) error {
	return EnterActiveProjects(ctx, tx, orgID, []uuid.UUID{projectID})
}

func EnterActiveProjects(
	ctx context.Context,
	tx pgx.Tx,
	orgID uuid.UUID,
	projectIDs []uuid.UUID,
) error {
	q := dbsqlc.New(tx)
	if len(projectIDs) == 0 {
		return enterActiveOrganization(ctx, q, orgID)
	}
	if err := organizationShared(ctx, q, orgID); err != nil {
		return err
	}
	ordered := orderedIDs(projectIDs)
	for _, projectID := range ordered {
		if err := projectShared(ctx, q, projectID); err != nil {
			return err
		}
	}
	for _, projectID := range ordered {
		if _, err := q.GetActiveProjectForLifecycle(
			ctx,
			dbsqlc.GetActiveProjectForLifecycleParams{OrgID: orgID, ProjectID: projectID},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return storeerr.ErrNotFound
			}
			return fmt.Errorf("revalidate active project lifecycle: %w", err)
		}
	}
	return nil
}

func ProjectsExclusive(ctx context.Context, tx pgx.Tx, projectIDs []uuid.UUID) error {
	q := dbsqlc.New(tx)
	for _, projectID := range orderedIDs(projectIDs) {
		if err := q.LockProjectLifecycleExclusive(
			ctx,
			dbsqlc.LockProjectLifecycleExclusiveParams{ProjectID: projectID},
		); err != nil {
			return fmt.Errorf("lock project lifecycle exclusive: %w", err)
		}
	}
	return nil
}

func orderedIDs(ids []uuid.UUID) []uuid.UUID {
	ordered := append([]uuid.UUID(nil), ids...)
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare(ordered[i][:], ordered[j][:]) < 0
	})
	if len(ordered) < 2 {
		return ordered
	}
	deduped := ordered[:1]
	for _, id := range ordered[1:] {
		if id != deduped[len(deduped)-1] {
			deduped = append(deduped, id)
		}
	}
	return deduped
}

func AgentSources(ctx context.Context, tx pgx.Tx, agentIDs []uuid.UUID) error {
	q := dbsqlc.New(tx)
	for _, agentID := range orderedIDs(agentIDs) {
		if err := q.LockAgentMachineSources(
			ctx,
			dbsqlc.LockAgentMachineSourcesParams{AgentID: agentID},
		); err != nil {
			return fmt.Errorf("lock agent machine sources for lifecycle: %w", err)
		}
	}
	return nil
}

func Pools(ctx context.Context, tx pgx.Tx, refs []PoolRef) error {
	q := dbsqlc.New(tx)
	ordered := append([]PoolRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].OrgID != ordered[j].OrgID {
			return bytes.Compare(ordered[i].OrgID[:], ordered[j].OrgID[:]) < 0
		}
		return bytes.Compare(ordered[i].PoolID[:], ordered[j].PoolID[:]) < 0
	})
	var previous PoolRef
	for i, ref := range ordered {
		if i > 0 && ref == previous {
			continue
		}
		if _, err := q.LockMachinePoolForLifecycle(
			ctx,
			dbsqlc.LockMachinePoolForLifecycleParams{OrgID: ref.OrgID, ID: ref.PoolID},
		); err != nil {
			return rowLockError("lock machine pool for lifecycle", err)
		}
		previous = ref
	}
	return nil
}

func PoolGrants(ctx context.Context, tx pgx.Tx, grantIDs []uuid.UUID) error {
	q := dbsqlc.New(tx)
	for _, grantID := range orderedIDs(grantIDs) {
		if _, err := q.LockProjectMachinePoolGrantForLifecycle(
			ctx,
			dbsqlc.LockProjectMachinePoolGrantForLifecycleParams{
				ID: grantID,
			},
		); err != nil {
			return rowLockError("lock project machine pool grant for lifecycle", err)
		}
	}
	return nil
}

func Machines(ctx context.Context, tx pgx.Tx, refs []MachineRef) error {
	q := dbsqlc.New(tx)
	ordered := append([]MachineRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].OrgID != ordered[j].OrgID {
			return bytes.Compare(ordered[i].OrgID[:], ordered[j].OrgID[:]) < 0
		}
		return bytes.Compare(ordered[i].MachineID[:], ordered[j].MachineID[:]) < 0
	})
	var previous MachineRef
	for i, ref := range ordered {
		if i > 0 && ref == previous {
			continue
		}
		if _, err := q.LockMachineForLifecycle(
			ctx,
			dbsqlc.LockMachineForLifecycleParams{OrgID: ref.OrgID, ID: ref.MachineID},
		); err != nil {
			return rowLockError("lock machine for lifecycle", err)
		}
		previous = ref
	}
	return nil
}

func Agents(ctx context.Context, tx pgx.Tx, refs []AgentRef) error {
	q := dbsqlc.New(tx)
	for _, ref := range orderedAgentRefs(refs) {
		if _, err := q.LockAgentInProject(
			ctx,
			dbsqlc.LockAgentInProjectParams{ProjectID: ref.ProjectID, ID: ref.AgentID},
		); err != nil {
			return rowLockError("lock agent for lifecycle", err)
		}
	}
	return nil
}

func rowLockError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func orderedAgentRefs(refs []AgentRef) []AgentRef {
	ordered := append([]AgentRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ProjectID != ordered[j].ProjectID {
			return bytes.Compare(ordered[i].ProjectID[:], ordered[j].ProjectID[:]) < 0
		}
		return bytes.Compare(ordered[i].AgentID[:], ordered[j].AgentID[:]) < 0
	})
	if len(ordered) < 2 {
		return ordered
	}
	deduped := ordered[:1]
	for _, ref := range ordered[1:] {
		if ref != deduped[len(deduped)-1] {
			deduped = append(deduped, ref)
		}
	}
	return deduped
}
