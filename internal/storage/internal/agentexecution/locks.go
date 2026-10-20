package agentexecution

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type RuntimeAuthority struct {
	AgentID       uuid.UUID
	RuntimeLockID uuid.UUID
}

type MutationPlan struct {
	agents    []AgentRoute
	authority *RuntimeAuthority
	unit      *Unit
}

func (u *Unit) PlanAgents(
	ctx context.Context, refs []lifecyclelock.AgentRef, authority *RuntimeAuthority,
) (MutationPlan, error) {
	if err := u.active(); err != nil {
		return MutationPlan{}, err
	}
	plan := MutationPlan{authority: authority, unit: u}
	q := executiondb.New()
	for _, ref := range refs {
		if h := u.handles[ref.AgentID]; h != nil && h.route.ProjectID == ref.ProjectID {
			plan.agents = append(plan.agents, h.route)
			continue
		}
		root, err := q.LoadExecutionScope(ctx, u.DB(), executiondb.LoadExecutionScopeParams{
			ProjectID: ref.ProjectID, ID: ref.AgentID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return MutationPlan{}, storeerr.ErrNotFound
		}
		if err != nil {
			return MutationPlan{}, err
		}
		plan.agents = append(plan.agents, AgentRoute{
			ProjectID: ref.ProjectID, AgentID: ref.AgentID, RootAgentID: root,
		})
	}
	return plan, nil
}

func (u *Unit) LockAgents(ctx context.Context, plan MutationPlan) error {
	_, err := u.lockAgents(ctx, plan, false)
	return err
}

func (u *Unit) TryLockAgents(ctx context.Context, plan MutationPlan) (bool, error) {
	return u.lockAgents(ctx, plan, true)
}

func (u *Unit) lockAgents(ctx context.Context, plan MutationPlan, skipLocked bool) (bool, error) {
	if err := u.active(); err != nil {
		return false, err
	}
	if plan.unit != u {
		return false, fmt.Errorf("%w: plan belongs to a different unit", ErrLockPlan)
	}
	if len(u.handles) == 0 && len(u.expanded.agents) > 0 {
		expanded, err := u.PlanAgents(ctx, u.expanded.agents, nil)
		if err != nil {
			return false, err
		}
		plan.agents = append(plan.agents, expanded.agents...)
	}
	routes := slices.Clone(plan.agents)
	slices.SortFunc(routes, compareRoutes)
	routes = slices.Compact(routes)
	var missing []lifecyclelock.AgentRef
	for _, route := range routes {
		if u.handles[route.AgentID] == nil {
			missing = append(missing, lifecyclelock.AgentRef{ProjectID: route.ProjectID, AgentID: route.AgentID})
		}
	}
	for _, route := range routes {
		if route.ProjectID == uuid.Nil ||
			route.AgentID == uuid.Nil || route.RootAgentID == uuid.Nil {
			return false, fmt.Errorf("%w: invalid agent route", ErrLockPlan)
		}
		if h := u.handles[route.AgentID]; h != nil {
			if h.route != route {
				return false, fmt.Errorf("%w: route changed", ErrLockPlan)
			}
			continue
		}
		if u.runtimeLocked {
			return false, u.expandPlan(missing...)
		}
		_, excluded := u.exclusiveProjects[route.ProjectID]
		for _, held := range u.handles {
			if held.created || excluded {
				continue
			}
			if compareRoutes(route, held.route) < 0 {
				return false, u.expandPlan(missing...)
			}
		}
	}
	q := dbsqlc.New(u.DB())
	for _, route := range routes {
		if u.handles[route.AgentID] != nil {
			continue
		}
		var err error
		var identity dbsqlc.LockAgentInProjectRow
		if skipLocked {
			var row dbsqlc.TryLockAgentInProjectRow
			row, err = q.TryLockAgentInProject(ctx, dbsqlc.TryLockAgentInProjectParams{
				ProjectID: route.ProjectID, ID: route.AgentID,
			})
			identity = dbsqlc.LockAgentInProjectRow(row)
		} else {
			identity, err = q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
				ProjectID: route.ProjectID, ID: route.AgentID,
			})
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if skipLocked {
				return false, nil
			}
			return false, storeerr.ErrNotFound
		}
		if err != nil {
			return false, err
		}
		u.handles[route.AgentID] = &Handle{unit: u, route: route, identity: &identity}
	}
	if owned := plan.authority; owned != nil {
		h := u.handles[owned.AgentID]
		if h == nil || owned.RuntimeLockID == uuid.Nil {
			return false, storeerr.ErrRuntimeLockInactive
		}
		if h.lockedRuntimeID != owned.RuntimeLockID {
			if _, err := q.LockAgentRuntimeLockForOwnedMutation(ctx, dbsqlc.LockAgentRuntimeLockForOwnedMutationParams{
				ProjectID: h.route.ProjectID, AgentID: owned.AgentID, ID: owned.RuntimeLockID,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return false, storeerr.ErrRuntimeLockInactive
				}
				return false, err
			}
			h.lockedRuntimeID = owned.RuntimeLockID
		}
		u.runtimeLocked = true
	}
	return true, nil
}

func (u *Unit) LockAgentRefs(ctx context.Context, refs []lifecyclelock.AgentRef, authority *RuntimeAuthority) error {
	plan, err := u.PlanAgents(ctx, refs, authority)
	if err != nil {
		return err
	}
	return u.LockAgents(ctx, plan)
}

func (u *Unit) LockProjectDeletion(ctx context.Context, orgID, projectID uuid.UUID) error {
	if len(u.handles) != 0 {
		return ErrLockPlan
	}
	if err := lifecyclelock.OrganizationShared(ctx, u.DB(), orgID); err != nil {
		return err
	}
	if err := lifecyclelock.ProjectExclusive(ctx, u.DB(), projectID); err != nil {
		return err
	}
	u.exclusiveProjects[projectID] = struct{}{}
	return nil
}

func (u *Unit) LockOrganizationDeletion(ctx context.Context, orgID uuid.UUID) error {
	if len(u.handles) != 0 {
		return ErrLockPlan
	}
	if err := lifecyclelock.OrganizationExclusive(ctx, u.DB(), orgID); err != nil {
		return err
	}
	ids, err := dbsqlc.New(u.DB()).ListActiveProjectIDsForOrganization(ctx,
		dbsqlc.ListActiveProjectIDsForOrganizationParams{OrgID: orgID})
	if err != nil {
		return err
	}
	for _, id := range ids {
		u.exclusiveProjects[id] = struct{}{}
	}
	return nil
}

func (u *Unit) ClaimAgent(ctx context.Context) (AgentRoute, error) {
	if len(u.handles) != 0 {
		return AgentRoute{}, ErrLockPlan
	}
	if u.expanded.claimed != uuid.Nil {
		if err := u.LockAgentRefs(ctx, u.expanded.agents, nil); err != nil {
			return AgentRoute{}, err
		}
		if _, err := dbsqlc.New(u.DB()).LockClaimableAgentWakeup(ctx,
			dbsqlc.LockClaimableAgentWakeupParams{AgentID: u.expanded.claimed}); err != nil {
			return AgentRoute{}, err
		}
		u.claimedAgent = u.expanded.claimed
		return u.handles[u.claimedAgent].route, nil
	}
	wakeup, err := dbsqlc.New(u.DB()).ClaimNextAgentWakeup(ctx)
	if err != nil {
		return AgentRoute{}, err
	}
	route := AgentRoute{ProjectID: wakeup.ProjectID,
		AgentID: wakeup.AgentID, RootAgentID: wakeup.RootAgentID}
	if u.handles[route.AgentID] == nil {
		u.handles[route.AgentID] = &Handle{unit: u, route: route, identity: &dbsqlc.LockAgentInProjectRow{ID: route.AgentID, OrgID: wakeup.OrgID, State: wakeup.State}}
	}
	u.claimedAgent = route.AgentID
	return route, nil
}

func (u *Unit) CreatedAgent(projectID, agentID, parentID uuid.UUID) error {
	if err := u.active(); err != nil {
		return err
	}
	if projectID == uuid.Nil || agentID == uuid.Nil || u.handles[agentID] != nil {
		return ErrLockPlan
	}
	rootID := agentID
	if parentID != uuid.Nil {
		parent := u.handles[parentID]
		if parent == nil || parent.route.ProjectID != projectID {
			return ErrLockPlan
		}
		rootID = parent.route.RootAgentID
	}
	route := AgentRoute{ProjectID: projectID, AgentID: agentID, RootAgentID: rootID}
	u.handles[agentID] = &Handle{unit: u, route: route, created: true}
	return nil
}

func (u *Unit) PlanAgentFamily(
	ctx context.Context, projectID, agentID uuid.UUID, authority *RuntimeAuthority,
) (MutationPlan, error) {
	if err := u.active(); err != nil {
		return MutationPlan{}, err
	}
	row, err := executiondb.New().LoadExecutionFamily(ctx, u.DB(), executiondb.LoadExecutionFamilyParams{
		ProjectID: projectID, ID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MutationPlan{}, storeerr.ErrNotFound
	}
	if err != nil {
		return MutationPlan{}, err
	}
	routes := []AgentRoute{{ProjectID: projectID, AgentID: agentID, RootAgentID: row.RootAgentID}}
	if row.ParentAgentID != nil {
		routes = append(routes, AgentRoute{ProjectID: projectID,
			AgentID: *row.ParentAgentID, RootAgentID: row.RootAgentID})
	}
	return MutationPlan{unit: u, agents: routes, authority: authority}, nil
}

func (u *Unit) PlanSubagentLaunch(ctx context.Context, projectID, parentID uuid.UUID) (MutationPlan, error) {
	plan, err := u.PlanAgents(ctx, []lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: parentID}}, nil)
	if err != nil {
		return MutationPlan{}, err
	}
	root := plan.agents[0].RootAgentID
	plan.agents = append(plan.agents, AgentRoute{ProjectID: projectID, AgentID: root, RootAgentID: root})
	return plan, nil
}
