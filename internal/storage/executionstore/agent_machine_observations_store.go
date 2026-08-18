package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentMachineObservationRecord struct {
	MachineRef             string                   `json:"machine_ref"`
	SourceKind             MachineSourceKind        `json:"source_kind"`
	BindingKind            AgentMachineBindingKind  `json:"binding_kind"`
	BindingState           AgentMachineBindingState `json:"binding_state"`
	DisplayName            string                   `json:"display_name"`
	MachinePoolName        string                   `json:"machine_pool_name,omitempty"`
	LifecycleState         MachineLifecycleState    `json:"lifecycle_state"`
	ConnectionState        MachineConnectionState   `json:"connection_state"`
	ConnectionStateReason  string                   `json:"connection_state_reason,omitempty"`
	Description            string                   `json:"description"`
	Cwd                    string                   `json:"cwd"`
	Executable             bool                     `json:"executable"`
	ProjectGrantMissing    bool                     `json:"project_grant_missing,omitempty"`
	LifecycleReasonCode    string                   `json:"lifecycle_reason_code"`
	LifecycleReasonMessage string                   `json:"lifecycle_reason_message"`
	FailureReport          json.RawMessage          `json:"failure_report,omitempty"`
	BindingCreatedAt       time.Time                `json:"binding_created_at"`
	BindingUpdatedAt       time.Time                `json:"binding_updated_at"`
}

func (s *Store) ListAgentMachineObservations(
	ctx context.Context,
	projectID, agentID ID,
) ([]AgentMachineObservationRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	return selectAgentMachineObservations(ctx, s.q, projectID, agentID, nil, false)
}

func (r *ToolCallReader) ListAgentMachineObservations(
	ctx context.Context,
) ([]AgentMachineObservationRecord, error) {
	return selectAgentMachineObservations(
		ctx,
		r.transaction.q,
		r.transaction.input.ProjectID,
		r.transaction.input.AgentID,
		nil,
		false,
	)
}

func (r *ToolCallReader) GetAgentMachineObservationByRef(
	ctx context.Context,
	machineRef string,
) (AgentMachineObservationRecord, error) {
	if machineRef == "" {
		return AgentMachineObservationRecord{}, errors.New("machine ref is required")
	}
	return getAgentMachineObservationByRef(
		ctx,
		r.transaction.q,
		r.transaction.input.ProjectID,
		r.transaction.input.AgentID,
		machineRef,
	)
}

func getAgentMachineObservationByRef(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
	machineRef string,
) (AgentMachineObservationRecord, error) {
	records, err := selectAgentMachineObservations(
		ctx,
		q,
		projectID,
		agentID,
		&machineRef,
		true,
	)
	if err != nil {
		return AgentMachineObservationRecord{}, err
	}
	if len(records) == 0 {
		return AgentMachineObservationRecord{}, storeerr.ErrNotFound
	}
	return records[0], nil
}

func selectAgentMachineObservations(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
	machineRef *string,
	includeReleasedPool bool,
) ([]AgentMachineObservationRecord, error) {
	rows, err := q.SelectAgentMachineObservations(
		ctx,
		dbsqlc.SelectAgentMachineObservationsParams{
			ProjectID:           projectID,
			AgentID:             agentID,
			MachineRef:          machineRef,
			IncludeReleasedPool: includeReleasedPool,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("select agent machine observations: %w", err)
	}
	records := make([]AgentMachineObservationRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, AgentMachineObservationRecord{
			MachineRef:             row.MachineRef,
			SourceKind:             MachineSourceKind(row.SourceKind),
			BindingKind:            AgentMachineBindingKind(row.BindingKind),
			BindingState:           AgentMachineBindingState(row.BindingState),
			DisplayName:            row.DisplayName,
			MachinePoolName:        row.MachinePoolName,
			LifecycleState:         MachineLifecycleState(row.LifecycleState),
			ConnectionState:        MachineConnectionState(row.ConnectionState),
			ConnectionStateReason:  row.ConnectionStateReason,
			Description:            row.Description,
			Cwd:                    row.EffectiveCwd,
			Executable:             row.Executable,
			ProjectGrantMissing:    row.ProjectGrantMissing,
			LifecycleReasonCode:    row.LifecycleReasonCode,
			LifecycleReasonMessage: row.LifecycleReasonMessage,
			FailureReport:          rawMessageFromSQLCPtr(row.FailureReport),
			BindingCreatedAt:       row.CreatedAt,
			BindingUpdatedAt:       row.UpdatedAt,
		})
	}
	return records, nil
}
