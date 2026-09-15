package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Subscription interface {
	// Unsubscribe waits for in-flight handlers; a handler must not call it synchronously.
	Unsubscribe() error
}

type DaemonWakeupSubscriber interface {
	SubscribeDaemonReplicaWakeups(
		ctx context.Context,
		replicaID uuid.UUID,
		handler func(context.Context, WakeupMessage),
	) (Subscription, error)
	SubscribeDaemonReplicaInbox(
		ctx context.Context,
		replicaID uuid.UUID,
		handler func(context.Context, []byte),
	) (Subscription, error)
}

type AgentEventWakeupSubscriber interface {
	SubscribeAgentEventWakeups(
		ctx context.Context,
		agentID uuid.UUID,
		handler func(context.Context),
	) (Subscription, error)
}

type AgentStreamDeltaSubscriber interface {
	SubscribeAgentStreamDeltas(
		ctx context.Context,
		agentID uuid.UUID,
		handler func(context.Context, json.RawMessage),
	) (Subscription, error)
}

type AgentUpdateSubscriber interface {
	SubscribeAgentUpdates(
		ctx context.Context,
		agentID uuid.UUID,
		handler func(context.Context, AgentUpdate),
	) (Subscription, error)
}

type WorkerControlSubscriber interface {
	SubscribeWorkerControl(
		ctx context.Context,
		workerProcessID uuid.UUID,
		handler func(context.Context, WorkerControl),
	) (Subscription, error)
}

type DaemonWakeupPublisher interface {
	PublishDaemonReplicaWakeup(ctx context.Context, replicaID uuid.UUID, msg WakeupMessage) error
}

type AgentEventWakeupPublisher interface {
	PublishAgentEventWakeup(ctx context.Context, agentID uuid.UUID) error
}

type AgentUpdatePublisher interface {
	PublishAgentUpdate(ctx context.Context, destinationID uuid.UUID, update AgentUpdate) error
}

// AgentAncestryReader reads immutable parent edges after commit, scoped to the
// owner's project. Results exclude the owner and are nearest first, at most eight.
type AgentAncestryReader interface {
	ListAgentAncestors(ctx context.Context, projectID, agentID uuid.UUID) ([]uuid.UUID, error)
}

type WorkerControlPublisher interface {
	PublishWorkerControl(ctx context.Context, workerProcessID uuid.UUID, msg WorkerControl) error
}

type AgentStreamDeltaPublisher interface {
	PublishAgentStreamDelta(ctx context.Context, agentID uuid.UUID, payload json.RawMessage) error
}

type PostCommitIntent interface {
	postCommitIntent()
}

type DaemonWorkCommitted struct {
	MachineID uuid.UUID
}

func (DaemonWorkCommitted) postCommitIntent() {}

type DaemonRuntimeEndedCommitted struct {
	RuntimeID uuid.UUID
	MachineID uuid.UUID
	Cause     DaemonRuntimeEndCause
}

func (DaemonRuntimeEndedCommitted) postCommitIntent() {}

type DaemonRuntimeEndCause string

const (
	DaemonRuntimeEndReconnect             DaemonRuntimeEndCause = "reconnect"
	DaemonRuntimeEndAuthorizationRevoked  DaemonRuntimeEndCause = "authorization_revoked"
	DaemonRuntimeEndMachineDecommissioned DaemonRuntimeEndCause = "machine_decommissioned"
)

type DaemonProcessTerminationCommitted struct {
	MachineID  uuid.UUID
	ProcessIDs []uuid.UUID
}

func (DaemonProcessTerminationCommitted) postCommitIntent() {}

type AgentEventCommitted struct {
	AgentID uuid.UUID
}

func (AgentEventCommitted) postCommitIntent() {}

type ToolCallUpdatedCommitted struct {
	ProjectID  uuid.UUID `json:"-"`
	AgentID    uuid.UUID `json:"agent_id"`
	ToolCallID uuid.UUID `json:"tool_call_id"`
	ToolType   string    `json:"-"`
	State      string    `json:"state"`
}

func (ToolCallUpdatedCommitted) postCommitIntent() {}

type AgentChangeKind string

const (
	AgentChangeAgent        AgentChangeKind = "agent"
	AgentChangeInteractions AgentChangeKind = "interactions"
)

type AgentChangeCommitted struct {
	ProjectID     uuid.UUID         `json:"-"`
	AgentID       uuid.UUID         `json:"agent_id"`
	ParentAgentID *uuid.UUID        `json:"parent_agent_id"`
	Changes       []AgentChangeKind `json:"changes"`
}

func (AgentChangeCommitted) postCommitIntent() {}

// AgentUpdate carries exactly one payload. Its owner is independent of the
// subscription destination, which can be an ancestor of that owner.
type AgentUpdate struct {
	ToolCallUpdate *ToolCallUpdatedCommitted `json:"tool_call_update,omitempty"`
	Change         *AgentChangeCommitted     `json:"change,omitempty"`
}

func (u AgentUpdate) Validate() error {
	if (u.ToolCallUpdate == nil) == (u.Change == nil) {
		return errors.New("agent update requires exactly one payload")
	}
	if v := u.ToolCallUpdate; v != nil {
		if v.AgentID == uuid.Nil || v.ToolCallID == uuid.Nil || v.State == "" {
			return errors.New("agent, tool call, and state are required")
		}
		return nil
	}
	v := u.Change
	if v.AgentID == uuid.Nil || len(v.Changes) == 0 {
		return errors.New("agent and changes are required")
	}
	if v.ParentAgentID != nil && (*v.ParentAgentID == uuid.Nil || *v.ParentAgentID == v.AgentID) {
		return errors.New("invalid parent agent id")
	}
	for _, kind := range v.Changes {
		if kind != AgentChangeAgent && kind != AgentChangeInteractions {
			return fmt.Errorf("unknown agent change kind %q", kind)
		}
	}
	return nil
}

type agentNotificationKey struct {
	projectID uuid.UUID
	agentID   uuid.UUID
}

func mergeAgentChangeKinds(a, b []AgentChangeKind) []AgentChangeKind {
	var agent, interactions bool
	for _, kinds := range [][]AgentChangeKind{a, b} {
		for _, kind := range kinds {
			switch kind {
			case AgentChangeAgent:
				agent = true
			case AgentChangeInteractions:
				interactions = true
			}
		}
	}
	var kinds []AgentChangeKind
	if agent {
		kinds = append(kinds, AgentChangeAgent)
	}
	if interactions {
		kinds = append(kinds, AgentChangeInteractions)
	}
	return kinds
}

type WorkerControlCommitted struct {
	WorkerProcessID uuid.UUID
	Control         WorkerControl
}

func (WorkerControlCommitted) postCommitIntent() {}

type WorkerControlKind string

const WorkerControlKindCancel WorkerControlKind = "cancel"

type WorkerControl struct {
	Kind   WorkerControlKind    `json:"kind"`
	Cancel *WorkerControlCancel `json:"cancel,omitempty"`
}

type WorkerControlCancel struct {
	AgentID       uuid.UUID `json:"agent_id"`
	RuntimeLockID uuid.UUID `json:"runtime_lock_id"`
}

func NewWorkerControlCancel(agentID, runtimeLockID uuid.UUID) WorkerControl {
	cancel := WorkerControlCancel{AgentID: agentID, RuntimeLockID: runtimeLockID}
	return WorkerControl{Kind: WorkerControlKindCancel, Cancel: &cancel}
}

func (c WorkerControl) Validate() error {
	switch c.Kind {
	case WorkerControlKindCancel:
		if c.Cancel == nil {
			return errors.New("worker control cancel payload is required")
		}
		if c.Cancel.AgentID == uuid.Nil {
			return errors.New("worker control cancel agent id is required")
		}
		if c.Cancel.RuntimeLockID == uuid.Nil {
			return errors.New("worker control cancel runtime lock id is required")
		}
		return nil
	case "":
		return errors.New("worker control kind is required")
	default:
		return fmt.Errorf("unknown worker control kind %q", c.Kind)
	}
}

type WakeupType string

const (
	WakeupTypeDaemonWork             WakeupType = "daemon_work"
	WakeupTypeDaemonRuntimeEnded     WakeupType = "daemon_runtime_ended"
	WakeupTypeDaemonProcessTerminate WakeupType = "daemon_process_terminate"
)

type WakeupMessage struct {
	Type            WakeupType            `json:"type"`
	MachineID       uuid.UUID             `json:"machine_id"`
	RuntimeID       *uuid.UUID            `json:"runtime_id,omitempty"`
	RuntimeEndCause DaemonRuntimeEndCause `json:"runtime_end_cause,omitempty"`
	ProcessIDs      []uuid.UUID           `json:"process_ids,omitempty"`
}

func (m WakeupMessage) Validate() error {
	if m.MachineID == uuid.Nil {
		return errors.New("daemon wakeup machine id is required")
	}
	switch m.Type {
	case WakeupTypeDaemonWork:
		if m.RuntimeID != nil || m.RuntimeEndCause != "" ||
			len(m.ProcessIDs) != 0 {
			return errors.New("daemon work wakeup has unexpected payload")
		}
	case WakeupTypeDaemonRuntimeEnded:
		if m.RuntimeID == nil || *m.RuntimeID == uuid.Nil {
			return errors.New("daemon runtime-ended wakeup runtime id is required")
		}
		switch m.RuntimeEndCause {
		case DaemonRuntimeEndReconnect,
			DaemonRuntimeEndAuthorizationRevoked,
			DaemonRuntimeEndMachineDecommissioned:
		default:
			return errors.New(
				"daemon runtime-ended wakeup has invalid cause",
			)
		}
		if len(m.ProcessIDs) != 0 {
			return errors.New("daemon runtime-ended wakeup has unexpected process ids")
		}
	case WakeupTypeDaemonProcessTerminate:
		if m.RuntimeID != nil || m.RuntimeEndCause != "" {
			return errors.New("daemon process-terminate wakeup has unexpected runtime id")
		}
		if len(m.ProcessIDs) == 0 {
			return errors.New("daemon process-terminate wakeup process ids are required")
		}
		for _, processID := range m.ProcessIDs {
			if processID == uuid.Nil {
				return errors.New("daemon process-terminate wakeup process id is required")
			}
		}
	case "":
		return errors.New("daemon wakeup type is required")
	default:
		return fmt.Errorf("unknown daemon wakeup type %q", m.Type)
	}
	return nil
}

type PostCommitPublisher interface {
	PublishPostCommit(context.Context, PostCommitIntent)
}

type Recorder interface {
	RecordNotification(intent, result, reason string)
}

type TxNotifications struct {
	daemonWorkByMachine         map[uuid.UUID]struct{}
	processTerminationByMachine map[uuid.UUID]map[uuid.UUID]struct{}
	runtimeEndedByID            map[uuid.UUID]DaemonRuntimeEndedCommitted
	agentEventByID              map[uuid.UUID]struct{}
	agentChanges                map[agentNotificationKey]AgentChangeCommitted
	toolCallUpdates             []ToolCallUpdatedCommitted
	workerControls              []WorkerControlCommitted
}

func NewTxNotifications() *TxNotifications {
	return &TxNotifications{
		daemonWorkByMachine:         map[uuid.UUID]struct{}{},
		processTerminationByMachine: map[uuid.UUID]map[uuid.UUID]struct{}{},
		runtimeEndedByID:            map[uuid.UUID]DaemonRuntimeEndedCommitted{},
		agentEventByID:              map[uuid.UUID]struct{}{},
		agentChanges:                map[agentNotificationKey]AgentChangeCommitted{},
	}
}

func (n *TxNotifications) AddDaemonWork(machineID uuid.UUID) {
	if n == nil || machineID == uuid.Nil {
		return
	}
	n.daemonWorkByMachine[machineID] = struct{}{}
}

func (n *TxNotifications) AddDaemonRuntimeEnded(
	runtimeID uuid.UUID,
	machineID uuid.UUID,
	cause DaemonRuntimeEndCause,
) {
	if n == nil || runtimeID == uuid.Nil || machineID == uuid.Nil ||
		cause == "" {
		return
	}
	n.runtimeEndedByID[runtimeID] = DaemonRuntimeEndedCommitted{
		RuntimeID: runtimeID,
		MachineID: machineID,
		Cause:     cause,
	}
}

func (n *TxNotifications) AddDaemonProcessTermination(machineID, processID uuid.UUID) {
	if n == nil || machineID == uuid.Nil || processID == uuid.Nil {
		return
	}
	processIDs := n.processTerminationByMachine[machineID]
	if processIDs == nil {
		processIDs = map[uuid.UUID]struct{}{}
		n.processTerminationByMachine[machineID] = processIDs
	}
	processIDs[processID] = struct{}{}
}

func (n *TxNotifications) AddAgentEvent(agentID uuid.UUID) {
	if n == nil || agentID == uuid.Nil {
		return
	}
	n.agentEventByID[agentID] = struct{}{}
}

func (n *TxNotifications) AddAgentChange(projectID, agentID uuid.UUID, kind AgentChangeKind) {
	if n == nil || projectID == uuid.Nil || agentID == uuid.Nil ||
		(kind != AgentChangeAgent && kind != AgentChangeInteractions) {
		return
	}
	key := agentNotificationKey{projectID: projectID, agentID: agentID}
	change := n.agentChanges[key]
	change.ProjectID = projectID
	change.AgentID = agentID
	change.Changes = mergeAgentChangeKinds(change.Changes, []AgentChangeKind{kind})
	n.agentChanges[key] = change
}

func (n *TxNotifications) AddToolCallUpdate(projectID, agentID, toolCallID uuid.UUID, toolType, state string) {
	if n == nil || projectID == uuid.Nil || agentID == uuid.Nil || toolCallID == uuid.Nil ||
		toolType == "" || state == "" {
		return
	}
	n.toolCallUpdates = append(n.toolCallUpdates, ToolCallUpdatedCommitted{
		ProjectID:  projectID,
		AgentID:    agentID,
		ToolCallID: toolCallID,
		ToolType:   toolType,
		State:      state,
	})
}

func (n *TxNotifications) AddWorkerControl(workerProcessID uuid.UUID, control WorkerControl) {
	if n == nil || workerProcessID == uuid.Nil || control.Validate() != nil {
		return
	}
	n.workerControls = append(n.workerControls, WorkerControlCommitted{
		WorkerProcessID: workerProcessID,
		Control:         control,
	})
}

func (n *TxNotifications) AddWorkerControlCancel(workerProcessID, agentID, runtimeLockID uuid.UUID) {
	n.AddWorkerControl(workerProcessID, NewWorkerControlCancel(agentID, runtimeLockID))
}

func (n *TxNotifications) Flush(ctx context.Context, publisher PostCommitPublisher) {
	if n == nil || publisher == nil {
		return
	}
	for machineID := range n.daemonWorkByMachine {
		publisher.PublishPostCommit(ctx, DaemonWorkCommitted{MachineID: machineID})
	}
	for machineID, processIDSet := range n.processTerminationByMachine {
		processIDs := make([]uuid.UUID, 0, len(processIDSet))
		for processID := range processIDSet {
			processIDs = append(processIDs, processID)
		}
		publisher.PublishPostCommit(ctx, DaemonProcessTerminationCommitted{
			MachineID:  machineID,
			ProcessIDs: processIDs,
		})
	}
	for _, intent := range n.runtimeEndedByID {
		publisher.PublishPostCommit(ctx, intent)
	}
	for agentID := range n.agentEventByID {
		publisher.PublishPostCommit(ctx, AgentEventCommitted{AgentID: agentID})
	}
	for _, intent := range n.toolCallUpdates {
		publisher.PublishPostCommit(ctx, intent)
	}
	for _, intent := range n.agentChanges {
		publisher.PublishPostCommit(ctx, intent)
	}
	for _, intent := range n.workerControls {
		publisher.PublishPostCommit(ctx, intent)
	}
}
