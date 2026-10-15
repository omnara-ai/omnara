package agentexecution

import (
	"bytes"

	"github.com/google/uuid"
)

type AgentRoute struct {
	ProjectID   uuid.UUID
	AgentID     uuid.UUID
	RootAgentID uuid.UUID
}

func compareRoutes(a, b AgentRoute) int {
	if a.ProjectID != b.ProjectID {
		return bytes.Compare(a.ProjectID[:], b.ProjectID[:])
	}
	return bytes.Compare(a.AgentID[:], b.AgentID[:])
}
