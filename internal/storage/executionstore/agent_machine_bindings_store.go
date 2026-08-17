package executionstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ID               ID                       `json:"id"`
	OrgID            ID                       `json:"org_id"`
	ProjectID        ID                       `json:"project_id"`
	AgentID          ID                       `json:"agent_id"`
	CreateToolCallID ID                       `json:"create_tool_call_id,omitempty"`
	DeleteToolCallID ID                       `json:"delete_tool_call_id,omitempty"`
	MachineID        ID                       `json:"machine_id"`
	MachineRef       string                   `json:"machine_ref"`
	BindingKind      AgentMachineBindingKind  `json:"binding_kind"`
	State            AgentMachineBindingState `json:"state"`
	Description      string                   `json:"description,omitempty"`
	Cwd              string                   `json:"cwd,omitempty"`
	EnvOverlay       json.RawMessage          `json:"env_overlay"`
	SecretEnvOverlay json.RawMessage          `json:"secret_env_overlay"`
	Metadata         json.RawMessage          `json:"metadata"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
}

type insertAgentMachineBindingInput struct {
	ProjectID             ID
	AgentID               ID
	CreateToolCallID      ID
	ProjectMachineGrantID ID
	MachineRef            string
	BindingKind           AgentMachineBindingKind
	Description           string
	Cwd                   string
	EnvOverlay            json.RawMessage
	SecretEnvOverlay      json.RawMessage
	Metadata              json.RawMessage
}

const shortRefAlphabet = "abcdefghijklmnpqrstvwxyz23456789"

func newMachineRef() (string, error) {
	var buf [6]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return "", fmt.Errorf("generate machine ref: %w", err)
	}
	out := make([]byte, 0, 11)
	out = append(out, "mchr-"...)
	for _, value := range buf {
		out = append(out, shortRefAlphabet[int(value)%len(shortRefAlphabet)])
	}
	return string(out), nil
}

func newMachineRefs(count int) ([]string, error) {
	refs := make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	for len(refs) < count {
		ref, err := newMachineRef()
		if err != nil {
			return nil, err
		}
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs, nil
}
