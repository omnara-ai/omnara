package executionstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
)

type ProcessState string

const (
	ProcessStateQueued   ProcessState = "queued"
	ProcessStateStarting ProcessState = "starting"
	ProcessStateRunning  ProcessState = "running"
	ProcessStateExited   ProcessState = "exited"
	ProcessStateFailed   ProcessState = "failed"
	ProcessStateKilled   ProcessState = "killed"
	ProcessStateUnknown  ProcessState = "unknown"
)

type ProcessActionState string

const (
	ProcessActionStateQueued   ProcessActionState = "queued"
	ProcessActionStateAccepted ProcessActionState = "accepted"
	ProcessActionStateApplied  ProcessActionState = "applied"
	ProcessActionStateFailed   ProcessActionState = "failed"
	ProcessActionStateUnknown  ProcessActionState = "unknown"
)

const (
	ProcessToolReasonMachineUnreachable = "machine_unreachable"
	ProcessToolReasonQueueTimeout       = "process_queue_timeout"
)

const ProcessToolMachineUnreachableGrace = 30 * time.Second
const ProcessQueueTimeout = 5 * time.Minute

type ProcessActionKind = processaction.Kind

const (
	ProcessActionKindWrite     = processaction.KindWrite
	ProcessActionKindRead      = processaction.KindRead
	ProcessActionKindInterrupt = processaction.KindInterrupt
	ProcessActionKindTerminate = processaction.KindTerminate
)

type CreateProcessInput struct {
	AgentMachineBindingID uuid.UUID
	IOMode                processcmd.IOMode
	Command               string
	ShellSelector         processcmd.ShellSelector
	Cwd                   string
	TimeoutSeconds        int
	InitialWaitMS         int
}

type CreateProcessActionInput struct {
	ProcessID  uuid.UUID
	ActionKind ProcessActionKind
	Payload    json.RawMessage
}

type DaemonWorkInput struct {
	Authority DaemonRuntimeAuthority
	Limit     int32
}

type AcceptDaemonProcessInput struct {
	Authority DaemonRuntimeAuthority
	ProcessID uuid.UUID
}

type AcceptDaemonProcessActionInput struct {
	Authority DaemonRuntimeAuthority
	ProcessID uuid.UUID
	ID        uuid.UUID
}

type CompleteDaemonProcessActionInput struct {
	Authority          DaemonRuntimeAuthority
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	ProcessID          uuid.UUID
	ID                 uuid.UUID
	StateReasonCode    string
	StateReasonMessage string
	Result             json.RawMessage
}

type DaemonProcessActionReportApplication struct {
	Action              ProcessActionRecord
	ToolResultCommitted bool
}

type DaemonProcessActionGrant struct {
	Action              ProcessActionRecord
	ProcessState        ProcessState
	DefaultOutputCursor int64
}

type MarkProcessStartedInput struct {
	Authority       DaemonRuntimeAuthority
	ProjectID       uuid.UUID
	AgentID         uuid.UUID
	ID              uuid.UUID
	Result          json.RawMessage
	SourceStartedAt time.Time
}

type DaemonProcessReportApplication struct {
	Process             ProcessRecord
	ToolResultCommitted bool
}

type CompleteProcessInput struct {
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	ID                 uuid.UUID
	RuntimeLockID      uuid.UUID
	State              ProcessState
	ExitCode           *int
	ExitSignal         string
	StateReasonCode    string
	StateReasonMessage string
	SourceEndedAt      time.Time
}

type CompleteDaemonProcessInput struct {
	Authority          DaemonRuntimeAuthority
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	ID                 uuid.UUID
	State              ProcessState
	ExitCode           *int
	ExitSignal         string
	StateReasonCode    string
	StateReasonMessage string
	Result             json.RawMessage
	SourceStartedAt    time.Time
	SourceEndedAt      time.Time
	StorageExhausted   bool
}

type ProcessRecord struct {
	ID                    uuid.UUID                `json:"id"`
	OrgID                 uuid.UUID                `json:"org_id"`
	ProjectID             uuid.UUID                `json:"project_id"`
	AgentID               uuid.UUID                `json:"agent_id"`
	ToolCallID            uuid.UUID                `json:"tool_call_id,omitempty"`
	RuntimeLockID         uuid.UUID                `json:"runtime_lock_id"`
	AgentMachineBindingID uuid.UUID                `json:"agent_machine_binding_id"`
	MachineID             uuid.UUID                `json:"machine_id"`
	ExecutionGrantedAt    *time.Time               `json:"execution_granted_at,omitempty"`
	IOMode                processcmd.IOMode        `json:"io_mode"`
	Command               string                   `json:"command,omitempty"`
	ShellSelector         processcmd.ShellSelector `json:"shell_selector,omitempty"`
	Cwd                   string                   `json:"cwd"`
	TimeoutSeconds        int                      `json:"timeout_seconds"`
	InitialWaitMS         int                      `json:"initial_wait_ms"`
	DefaultOutputCursor   int64                    `json:"default_output_cursor"`
	State                 ProcessState             `json:"state"`
	StateReasonCode       string                   `json:"state_reason_code,omitempty"`
	StateReasonMessage    string                   `json:"state_reason_message,omitempty"`
	SourceStartedAt       *time.Time               `json:"source_started_at,omitempty"`
	SourceEndedAt         *time.Time               `json:"source_ended_at,omitempty"`
	StateChangedAt        time.Time                `json:"state_changed_at"`
	ExitCode              *int                     `json:"exit_code,omitempty"`
	ExitSignal            string                   `json:"exit_signal,omitempty"`
	CreatedAt             time.Time                `json:"created_at"`
	UpdatedAt             time.Time                `json:"updated_at"`
}

type ProcessActionRecord struct {
	ID                 uuid.UUID          `json:"id"`
	OrgID              uuid.UUID          `json:"org_id"`
	ProjectID          uuid.UUID          `json:"project_id"`
	AgentID            uuid.UUID          `json:"agent_id"`
	ProcessID          uuid.UUID          `json:"process_id"`
	ToolCallID         uuid.UUID          `json:"tool_call_id,omitempty"`
	RuntimeLockID      uuid.UUID          `json:"runtime_lock_id"`
	ActionKind         ProcessActionKind  `json:"action_kind"`
	Seq                int64              `json:"seq"`
	Payload            json.RawMessage    `json:"payload"`
	State              ProcessActionState `json:"state"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	StateReasonCode    string             `json:"state_reason_code,omitempty"`
	StateReasonMessage string             `json:"state_reason_message,omitempty"`
}

type ActiveProcessRecord struct {
	ID              uuid.UUID                `json:"id"`
	State           ProcessState             `json:"state"`
	MachineID       uuid.UUID                `json:"machine_id"`
	IOMode          processcmd.IOMode        `json:"io_mode"`
	Command         string                   `json:"command,omitempty"`
	ShellSelector   processcmd.ShellSelector `json:"shell_selector,omitempty"`
	Cwd             string                   `json:"cwd"`
	SourceStartedAt *time.Time               `json:"source_started_at,omitempty"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
	ToolCallID      uuid.UUID                `json:"tool_call_id,omitempty"`
}

func isProcessTerminal(state ProcessState) bool {
	switch state {
	case ProcessStateExited, ProcessStateFailed, ProcessStateKilled, ProcessStateUnknown:
		return true
	default:
		return false
	}
}
