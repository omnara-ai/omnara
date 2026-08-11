package executionstore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

type DaemonRuntimeState string

const (
	DaemonRuntimeStateActive DaemonRuntimeState = "active"
	DaemonRuntimeStateEnded  DaemonRuntimeState = "ended"
)

type DaemonRuntimeRecord struct {
	ID                 ID                 `json:"id"`
	OrgID              ID                 `json:"org_id"`
	MachineID          ID                 `json:"machine_id"`
	DaemonTokenID      ID                 `json:"daemon_token_id"`
	DaemonInstanceID   ID                 `json:"daemon_instance_id"`
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
	OrgID            ID
	MachineID        ID
	DaemonTokenID    ID
	DaemonInstanceID ID
	DaemonVersion    string
	Capacity         json.RawMessage
	ObservedPlatform json.RawMessage
	ProcessClaims    []ProcessReconciliationClaim
	LeaseTimeout     time.Duration
}

type ProcessReconciliationClaim struct {
	ProcessID             ID
	SupervisorInstanceID  string
	Phase                 daemonprotocol.ProcessPhase
	SupervisorLive        bool
	ExecutionCommitted    bool
	ActionAdmissionClosed bool
	ResolvedActionSeq     int64
	Actions               []ProcessActionReconciliationClaim
}

type ProcessActionReconciliationClaim struct {
	ProcessActionID ID
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
	OrgID           ID
	MachineID       ID
	DaemonRuntimeID ID
	DaemonTokenID   ID
}

func validateDaemonRuntimeAuthority(authority DaemonRuntimeAuthority) error {
	if isNilID(authority.OrgID) || isNilID(authority.MachineID) || isNilID(authority.DaemonRuntimeID) ||
		isNilID(authority.DaemonTokenID) {
		return errors.New("org, machine, daemon runtime, and daemon token are required")
	}
	return nil
}

type ProcessReconciliationDirective struct {
	ProcessID            ID
	SupervisorInstanceID string
	Disposition          daemonprotocol.ProcessDisposition
	Actions              []ProcessActionReconciliationDirective
}

type ProcessActionReconciliationDirective struct {
	ProcessActionID ID
	Seq             int64
	ActionKind      ProcessActionKind
	Payload         json.RawMessage
	Disposition     daemonprotocol.ActionDisposition
}

type DaemonRuntimeLeaseInput struct {
	Authority        DaemonRuntimeAuthority
	DaemonInstanceID ID
	Capacity         json.RawMessage
	ObservedPlatform json.RawMessage
	LeaseTimeout     time.Duration
}
