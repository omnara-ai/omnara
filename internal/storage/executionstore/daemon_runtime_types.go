package executionstore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

type DaemonRuntimeState string

const (
	DaemonRuntimeStateActive DaemonRuntimeState = "active"
	DaemonRuntimeStateEnded  DaemonRuntimeState = "ended"
)

type DaemonRuntimeRecord struct {
	ID                 uuid.UUID          `json:"id"`
	OrgID              uuid.UUID          `json:"org_id"`
	MachineID          uuid.UUID          `json:"machine_id"`
	DaemonTokenID      uuid.UUID          `json:"daemon_token_id"`
	DaemonInstanceID   uuid.UUID          `json:"daemon_instance_id"`
	DaemonVersion      string             `json:"daemon_version"`
	State              DaemonRuntimeState `json:"state"`
	StateReasonCode    string             `json:"state_reason_code,omitempty"`
	StateReasonMessage string             `json:"state_reason_message,omitempty"`
	Capacity           json.RawMessage    `json:"capacity"`
	Metadata           json.RawMessage    `json:"metadata"`
	CreatedAt          time.Time          `json:"created_at"`
	LastSeenAt         time.Time          `json:"last_seen_at"`
	LeaseExpiresAt     time.Time          `json:"lease_expires_at"`
	EndedAt            *time.Time         `json:"ended_at,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

type RegisterDaemonRuntimeInput struct {
	OrgID            uuid.UUID
	MachineID        uuid.UUID
	DaemonTokenID    uuid.UUID
	DaemonInstanceID uuid.UUID
	DaemonVersion    string
	Capacity         json.RawMessage
	ObservedPlatform json.RawMessage
	ProcessClaims    []ProcessReconciliationClaim
	LeaseTimeout     time.Duration
}

type ProcessReconciliationClaim struct {
	ProcessID             uuid.UUID
	SupervisorInstanceID  string
	Phase                 daemonprotocol.ProcessPhase
	SupervisorLive        bool
	ExecutionCommitted    bool
	ActionAdmissionClosed bool
	ResolvedActionSeq     int64
	Actions               []ProcessActionReconciliationClaim
}

type ProcessActionReconciliationClaim struct {
	ProcessActionID uuid.UUID
	Seq             int64
	ActionKind      ProcessActionKind
	Position        daemonprotocol.ActionPosition
}

type DaemonRuntimeRegistrationRecord struct {
	Runtime        DaemonRuntimeRecord
	Reconciliation DaemonRuntimeReconciliation
}

type DaemonRuntimeReconciliation struct {
	Processes []ProcessReconciliationDirective
}

type DaemonRuntimeAuthority struct {
	OrgID           uuid.UUID
	MachineID       uuid.UUID
	DaemonRuntimeID uuid.UUID
	DaemonTokenID   uuid.UUID
}

func validateDaemonRuntimeAuthority(authority DaemonRuntimeAuthority) error {
	if authority.OrgID == uuid.Nil || authority.MachineID == uuid.Nil || authority.DaemonRuntimeID == uuid.Nil ||
		authority.DaemonTokenID == uuid.Nil {
		return errors.New("org, machine, daemon runtime, and daemon token are required")
	}
	return nil
}

type ProcessReconciliationDirective struct {
	ProcessID            uuid.UUID
	SupervisorInstanceID string
	Disposition          daemonprotocol.ProcessDisposition
	Actions              []ProcessActionReconciliationDirective
}

type ProcessActionReconciliationDirective struct {
	ProcessActionID uuid.UUID
	Seq             int64
	ActionKind      ProcessActionKind
	Payload         json.RawMessage
	Disposition     daemonprotocol.ActionDisposition
}

type DaemonRuntimeLeaseInput struct {
	Authority        DaemonRuntimeAuthority
	DaemonInstanceID uuid.UUID
	Capacity         json.RawMessage
	ObservedPlatform json.RawMessage
	LeaseTimeout     time.Duration
}
