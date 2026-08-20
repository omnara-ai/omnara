package executionstore

import (
	"encoding/json"
	"time"

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
)

const ProcessToolMachineUnreachableGrace = 30 * time.Second

type ProcessActionKind = processaction.Kind

const (
	ProcessActionKindWrite     = processaction.KindWrite
	ProcessActionKindRead      = processaction.KindRead
	ProcessActionKindInterrupt = processaction.KindInterrupt
	ProcessActionKindTerminate = processaction.KindTerminate
)

type CreateProcessInput struct {
	AgentMachineBindingID ID
	IOMode                processcmd.IOMode
	Command               string
	ShellSelector         processcmd.ShellSelector
	Cwd                   string
	TimeoutSeconds        int
	InitialWaitMS         int
}

type CreateProcessActionInput struct {
	ProcessID  ID
	ActionKind ProcessActionKind
	Payload    json.RawMessage
}

type DaemonWorkInput struct {
	Authority DaemonRuntimeAuthority
	Limit     int32
}

type AcceptDaemonProcessInput struct {
	Authority DaemonRuntimeAuthority
	ProcessID ID
}

type AcceptDaemonProcessActionInput struct {
	Authority DaemonRuntimeAuthority
	ProcessID ID
	ID        ID
}

type CompleteDaemonProcessActionInput struct {
	Authority          DaemonRuntimeAuthority
	ProjectID          ID
	AgentID            ID
	ProcessID          ID
	ID                 ID
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
	ProjectID       ID
	AgentID         ID
	ID              ID
	Result          json.RawMessage
	SourceStartedAt time.Time
}

type DaemonProcessReportApplication struct {
	Process             ProcessRecord
	ToolResultCommitted bool
}

type CompleteProcessInput struct {
	ProjectID          ID
	AgentID            ID
	ID                 ID
	RuntimeLockID      ID
	State              ProcessState
	ExitCode           *int
	ExitSignal         string
	StateReasonCode    string
	StateReasonMessage string
	SourceEndedAt      time.Time
}

type CompleteDaemonProcessInput struct {
	Authority          DaemonRuntimeAuthority
	ProjectID          ID
	AgentID            ID
	ID                 ID
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
	ID                    ID                       `json:"id"`
	OrgID                 ID                       `json:"org_id"`
	ProjectID             ID                       `json:"project_id"`
	AgentID               ID                       `json:"agent_id"`
	ToolCallID            ID                       `json:"tool_call_id,omitempty"`
	RuntimeLockID         ID                       `json:"runtime_lock_id"`
	AgentMachineBindingID ID                       `json:"agent_machine_binding_id"`
	MachineID             ID                       `json:"machine_id"`
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
	ID                 ID                 `json:"id"`
	OrgID              ID                 `json:"org_id"`
	ProjectID          ID                 `json:"project_id"`
	AgentID            ID                 `json:"agent_id"`
	ProcessID          ID                 `json:"process_id"`
	ToolCallID         ID                 `json:"tool_call_id,omitempty"`
	RuntimeLockID      ID                 `json:"runtime_lock_id"`
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
	ID              ID                       `json:"id"`
	State           ProcessState             `json:"state"`
	MachineID       ID                       `json:"machine_id"`
	IOMode          processcmd.IOMode        `json:"io_mode"`
	Command         string                   `json:"command,omitempty"`
	ShellSelector   processcmd.ShellSelector `json:"shell_selector,omitempty"`
	Cwd             string                   `json:"cwd"`
	SourceStartedAt *time.Time               `json:"source_started_at,omitempty"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
	ToolCallID      ID                       `json:"tool_call_id,omitempty"`
}

func isProcessTerminal(state ProcessState) bool {
	switch state {
	case ProcessStateExited, ProcessStateFailed, ProcessStateKilled, ProcessStateUnknown:
		return true
	default:
		return false
	}
}
