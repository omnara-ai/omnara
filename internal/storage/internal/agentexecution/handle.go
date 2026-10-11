package agentexecution

import (
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

type Handle struct {
	unit            *Unit
	route           AgentRoute
	valid           bool
	created         bool
	mutation        *executionMutation
	identity        *dbsqlc.LockAgentInProjectRow
	lockedRuntimeID uuid.UUID
}

func (u *Unit) Agent(route AgentRoute) (*Handle, error) {
	if err := u.active(); err != nil {
		return nil, err
	}
	h, found := u.handles[route.AgentID]
	if !found || h.route != route {
		return nil, u.expandPlan(lifecyclelock.AgentRef{ProjectID: route.ProjectID, AgentID: route.AgentID})
	}
	return h, nil
}

func (h *Handle) Route() (AgentRoute, error) {
	if !h.valid || h.unit.handles[h.route.AgentID] != h {
		return AgentRoute{}, ErrUnitClosed
	}
	if err := h.unit.active(); err != nil {
		return AgentRoute{}, err
	}
	return h.route, nil
}

func (u *Unit) Handle(projectID, agentID uuid.UUID) (*Handle, error) {
	if err := u.active(); err != nil {
		return nil, err
	}
	h := u.handles[agentID]
	if h == nil || h.route.ProjectID != projectID {
		return nil, u.expandPlan(lifecyclelock.AgentRef{ProjectID: projectID, AgentID: agentID})
	}
	return h, nil
}
