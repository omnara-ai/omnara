package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type AgentMachineBindingKind string

const (
	MachineBindingKindExplicit AgentMachineBindingKind = "explicit"
	MachineBindingKindPool     AgentMachineBindingKind = "pool"
)

type AgentMachineBindingState string

const (
	AgentMachineBindingStateAttached AgentMachineBindingState = "attached"
	AgentMachineBindingStateReleased AgentMachineBindingState = "released"
)

func (s *Store) ListExecutableAgentMachineBindings(
	ctx context.Context,
	projectID, agentID ID,
) ([]AgentMachineBindingRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	return listExecutableAgentMachineBindings(ctx, s.q, projectID, agentID)
}

func (s *Store) ListAgentMachineBindings(
	ctx context.Context,
	projectID, agentID ID,
) ([]AgentMachineBindingRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	rows, err := s.q.ListAgentMachineBindings(
		ctx,
		dbsqlc.ListAgentMachineBindingsParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent machine bindings: %w", err)
	}
	out := make([]AgentMachineBindingRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentMachineBindingRecordFromListSQLC(row))
	}
	return out, nil
}

func (r *ToolCallReader) ListExecutableAgentMachineBindings(
	ctx context.Context,
) ([]AgentMachineBindingRecord, error) {
	t := r.transaction
	return listExecutableAgentMachineBindings(
		ctx,
		t.q,
		t.input.ProjectID,
		t.input.AgentID,
	)
}

func listExecutableAgentMachineBindings(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
) ([]AgentMachineBindingRecord, error) {
	rows, err := q.ListExecutableAgentMachineBindings(
		ctx,
		dbsqlc.ListExecutableAgentMachineBindingsParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list executable agent machine bindings: %w", err)
	}
	out := make([]AgentMachineBindingRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, executableAgentMachineBindingRecordFromSQLC(row))
	}
	return out, nil
}

type AgentMachineBindingRecord struct {
	ID                     ID                       `json:"id"`
	OrgID                  ID                       `json:"org_id"`
	ProjectID              ID                       `json:"project_id"`
	AgentID                ID                       `json:"agent_id"`
	CreateToolCallID       ID                       `json:"create_tool_call_id,omitempty"`
	DeleteToolCallID       ID                       `json:"delete_tool_call_id,omitempty"`
	MachineID              ID                       `json:"machine_id"`
	BindingKind            AgentMachineBindingKind  `json:"binding_kind"`
	State                  AgentMachineBindingState `json:"state"`
	Description            string                   `json:"description,omitempty"`
	Cwd                    string                   `json:"cwd,omitempty"`
	EnvOverlay             json.RawMessage          `json:"env_overlay"`
	SecretEnvOverlay       json.RawMessage          `json:"secret_env_overlay"`
	DeleteAfterIdleMinutes *int                     `json:"delete_after_idle_minutes,omitempty"`
	Metadata               json.RawMessage          `json:"metadata"`
	CreatedAt              time.Time                `json:"created_at"`
	UpdatedAt              time.Time                `json:"updated_at"`
}

type insertAgentMachineBindingInput struct {
	ProjectID              ID
	AgentID                ID
	CreateToolCallID       ID
	ProjectMachineGrantID  ID
	BindingKind            AgentMachineBindingKind
	Description            string
	Cwd                    string
	EnvOverlay             json.RawMessage
	SecretEnvOverlay       json.RawMessage
	DeleteAfterIdleMinutes *int
	Metadata               json.RawMessage
}
