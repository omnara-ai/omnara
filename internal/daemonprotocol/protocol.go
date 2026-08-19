package daemonprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
)

type MessageType string

const (
	MessageHeartbeat        MessageType = "heartbeat"
	MessageHeartbeatAck     MessageType = "heartbeat_ack"
	MessageProcessOffer     MessageType = "process_offer"
	MessageProcessAccept    MessageType = "process_accept"
	MessageProcessAcceptAck MessageType = "process_accept_ack"
	MessageProcessTerminate MessageType = "process_terminate"
	MessageActionOffer      MessageType = "action_offer"
	MessageActionAccept     MessageType = "action_accept"
	MessageActionAcceptAck  MessageType = "action_accept_ack"
	MessageReport           MessageType = "report"
	MessageReportAck        MessageType = "report_ack"
	MessageError            MessageType = "error"
	MessageRuntimeEnded     MessageType = "runtime_ended"
	MessageSkillOffer       MessageType = "skill_offer"
	MessageSkillReport      MessageType = "skill_report"
)

type AckStatus string

const (
	MaxMessageBytes = 1048576

	AckStatusCommitted       AckStatus = "committed"
	AckStatusCleanupOnly     AckStatus = "cleanup_only"
	AckStatusPermanentReject AckStatus = "permanent_reject"
	AckStatusTransientError  AckStatus = "transient_error"

	ErrorCodeInvalidRuntime          = "invalid_runtime"
	ErrorCodeValidationFailed        = "validation_failed"
	ErrorCodeIdempotencyConflict     = "idempotency_conflict"
	ErrorCodeProcessOfferUnavailable = "process_offer_unavailable"
	ErrorCodeActionOfferUnavailable  = "action_offer_unavailable"
	ErrorCodeProcessActionBlocked    = "process_action_blocked"
	ErrorCodeStorageUnavailable      = "storage_unavailable"
	ErrorCodePresenceReplaced        = "presence_replaced"
	ErrorCodeMachineDecommissioned   = "machine_decommissioned"
	ErrorCodeAuthorizationRevoked    = "authorization_revoked"
	ErrorCodeInternal                = "internal_error"
)

// Message is the in-process normalized form for daemon websocket messages. The
// wire contract is the typed envelope implemented by MarshalJSON and
// UnmarshalJSON, not the struct field layout below.
//
//nolint:recvcheck // MarshalJSON must work for both Message values and pointers; UnmarshalJSON must mutate a pointer.
type Message struct {
	Type                 MessageType
	ProcessID            string
	ProcessActionID      string
	ReportID             string
	ProcessOffer         *ProcessOffer
	ActionOffer          *ActionOffer
	ActionGrant          *ActionGrant
	SkillOffer           *SkillOffer
	SkillReport          *SkillReport
	Event                *ReportedEvent
	DaemonInstanceID     uuid.UUID
	ObservedPlatform     json.RawMessage
	NextHeartbeatAfterMS int
	AckStatus            AckStatus
	ErrorCode            string
	Error                string
}

type envelope struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type heartbeatPayload struct {
	DaemonInstanceID uuid.UUID       `json:"daemon_instance_id"`
	ObservedPlatform json.RawMessage `json:"observed_platform,omitempty"`
}

type heartbeatAckPayload struct {
	NextHeartbeatAfterMS int `json:"next_heartbeat_after_ms"`
}

type processPayload struct {
	ProcessID string `json:"process_id"`
}

type actionPayload struct {
	ProcessID       string `json:"process_id"`
	ProcessActionID string `json:"process_action_id"`
}

type reportPayload struct {
	ReportID string         `json:"report_id"`
	Event    *ReportedEvent `json:"event"`
}

type reportAckPayload struct {
	AckStatus AckStatus `json:"ack_status"`
	ReportID  string    `json:"report_id"`
	ErrorCode string    `json:"error_code,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type errorPayload struct {
	ErrorCode       string `json:"error_code,omitempty"`
	Error           string `json:"error"`
	ProcessID       string `json:"process_id,omitempty"`
	ProcessActionID string `json:"process_action_id,omitempty"`
}

type runtimeEndedPayload struct {
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	if m.Type == "" {
		return nil, errors.New("daemon protocol message type is required")
	}
	payload, err := m.payloadForType()
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{Type: m.Type, Payload: payload})
}

func (m Message) payloadForType() (json.RawMessage, error) {
	var payload any
	switch m.Type {
	case MessageHeartbeat:
		payload = heartbeatPayload{
			DaemonInstanceID: m.DaemonInstanceID,
			ObservedPlatform: m.ObservedPlatform,
		}
	case MessageHeartbeatAck:
		payload = heartbeatAckPayload{NextHeartbeatAfterMS: m.NextHeartbeatAfterMS}
	case MessageProcessOffer:
		if m.ProcessOffer == nil {
			return nil, errors.New("process_offer payload is required")
		}
		payload = m.ProcessOffer
	case MessageProcessAccept, MessageProcessAcceptAck, MessageProcessTerminate:
		payload = processPayload{ProcessID: m.ProcessID}
	case MessageActionOffer:
		if m.ActionOffer == nil {
			return nil, errors.New("action_offer payload is required")
		}
		payload = m.ActionOffer
	case MessageActionAccept:
		payload = actionPayload{
			ProcessID:       m.ProcessID,
			ProcessActionID: m.ProcessActionID,
		}
	case MessageActionAcceptAck:
		if m.ActionGrant == nil {
			return nil, errors.New("action_accept_ack payload is required")
		}
		payload = m.ActionGrant
	case MessageReport:
		payload = reportPayload{ReportID: m.ReportID, Event: m.Event}
	case MessageReportAck:
		payload = reportAckPayload{
			AckStatus: m.AckStatus,
			ReportID:  m.ReportID,
			ErrorCode: m.ErrorCode,
			Error:     m.Error,
		}
	case MessageError:
		payload = errorPayload{
			ErrorCode:       m.ErrorCode,
			Error:           m.Error,
			ProcessID:       m.ProcessID,
			ProcessActionID: m.ProcessActionID,
		}
	case MessageRuntimeEnded:
		payload = runtimeEndedPayload{ErrorCode: m.ErrorCode, Error: m.Error}
	case MessageSkillOffer:
		if m.SkillOffer == nil {
			return nil, errors.New("skill_offer payload is required")
		}
		payload = m.SkillOffer
	case MessageSkillReport:
		if m.SkillReport == nil {
			return nil, errors.New("skill_report payload is required")
		}
		payload = m.SkillReport
	default:
		return nil, fmt.Errorf("unsupported daemon protocol message type %q", m.Type)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var env envelope
	if err := decodeEnvelope(data, &env); err != nil {
		return err
	}
	if env.Type == "" {
		return errors.New("daemon protocol message type is required")
	}
	if len(env.Payload) == 0 {
		return fmt.Errorf("daemon protocol payload is required for %s", env.Type)
	}
	trimmedPayload := bytes.TrimSpace(env.Payload)
	if len(trimmedPayload) == 0 || bytes.Equal(trimmedPayload, []byte("null")) || trimmedPayload[0] != '{' {
		return fmt.Errorf("daemon protocol payload for %s must be a JSON object", env.Type)
	}
	msg, err := messageFromEnvelope(env)
	if err != nil {
		return err
	}
	*m = msg
	return nil
}

func messageFromEnvelope(env envelope) (Message, error) {
	msg := Message{Type: env.Type}
	switch env.Type {
	case MessageHeartbeat:
		var payload heartbeatPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.DaemonInstanceID = payload.DaemonInstanceID
		msg.ObservedPlatform = payload.ObservedPlatform
	case MessageHeartbeatAck:
		var payload heartbeatAckPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.NextHeartbeatAfterMS = payload.NextHeartbeatAfterMS
	case MessageProcessOffer:
		var payload ProcessOffer
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ProcessID = payload.ProcessID
		msg.ProcessOffer = &payload
	case MessageProcessAccept, MessageProcessAcceptAck, MessageProcessTerminate:
		var payload processPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ProcessID = payload.ProcessID
	case MessageActionOffer:
		var payload ActionOffer
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ProcessID = payload.ProcessID
		msg.ProcessActionID = payload.ProcessActionID
		msg.ActionOffer = &payload
	case MessageActionAccept:
		var payload actionPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ProcessID = payload.ProcessID
		msg.ProcessActionID = payload.ProcessActionID
	case MessageActionAcceptAck:
		var payload ActionGrant
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ProcessID = payload.ProcessID
		msg.ProcessActionID = payload.ProcessActionID
		msg.ActionGrant = &payload
	case MessageReport:
		var payload reportPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ReportID = payload.ReportID
		msg.Event = payload.Event
	case MessageReportAck:
		var payload reportAckPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.AckStatus = payload.AckStatus
		msg.ReportID = payload.ReportID
		msg.ErrorCode = payload.ErrorCode
		msg.Error = payload.Error
	case MessageError:
		var payload errorPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ErrorCode = payload.ErrorCode
		msg.Error = payload.Error
		msg.ProcessID = payload.ProcessID
		msg.ProcessActionID = payload.ProcessActionID
	case MessageRuntimeEnded:
		var payload runtimeEndedPayload
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.ErrorCode = payload.ErrorCode
		msg.Error = payload.Error
	case MessageSkillOffer:
		var payload SkillOffer
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.SkillOffer = &payload
	case MessageSkillReport:
		var payload SkillReport
		if err := decodeEnvelope(env.Payload, &payload); err != nil {
			return Message{}, err
		}
		msg.SkillReport = &payload
	default:
		return msg, nil
	}
	return msg, nil
}

func decodeEnvelope(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("daemon protocol payload contains multiple JSON values")
	}
	return nil
}

type ProcessOffer struct {
	ProcessID        string                   `json:"process_id"`
	PreparationError string                   `json:"preparation_error,omitempty"`
	IOMode           processcmd.IOMode        `json:"io_mode"`
	Command          string                   `json:"command"`
	ShellSelector    processcmd.ShellSelector `json:"shell_selector"`
	Cwd              string                   `json:"cwd"`
	Env              map[string]string        `json:"env,omitempty"`
	WaitMs           int                      `json:"wait_ms,omitempty"`
	TimeoutSeconds   int                      `json:"timeout_seconds"`
}

type ActionOffer struct {
	ProcessID       string            `json:"process_id"`
	ProcessActionID string            `json:"process_action_id"`
	ActionKind      ProcessActionKind `json:"action_kind"`
	Seq             int64             `json:"seq"`
	Payload         json.RawMessage   `json:"payload"`
}

type ActionGrant struct {
	ProcessID           string       `json:"process_id"`
	ProcessActionID     string       `json:"process_action_id"`
	ProcessState        ProcessState `json:"process_state"`
	DefaultOutputCursor int64        `json:"default_output_cursor"`
}

type SkillState string

const (
	SkillStateReady       SkillState = "ready"
	SkillStateFailed      SkillState = "failed"
	SkillStateTimeout     SkillState = "timeout"
	SkillStateUnreachable SkillState = "unreachable"
)

type SkillOffer struct {
	RequestID string `json:"request_id"`
	SkillID   string `json:"skill_id"`
	// RevisionID is the public id of the skill revision to download. The
	// daemon fetches it from the configured API using its daemon token and
	// this offer's machine-, skill-, and revision-bound download capability.
	RevisionID        string `json:"revision_id"`
	DownloadToken     string `json:"download_token"`
	DownloadExpiresAt int64  `json:"download_expires_at"`
	// Digest is the expected "sha256:<hex>" content digest of the archive
	// revision. The daemon MUST verify the downloaded bytes against this
	// digest before extracting; the WS channel that carries this offer is
	// the authoritative integrity anchor.
	Digest string `json:"digest"`
}

type SkillReport struct {
	RequestID string     `json:"request_id"`
	SkillID   string     `json:"skill_id"`
	State     SkillState `json:"state"`
	ErrorCode string     `json:"error_code,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type ReportedEvent struct {
	Type               ReportedEventType `json:"type"`
	ProcessID          string            `json:"process_id"`
	ProcessActionID    string            `json:"process_action_id,omitempty"`
	State              ProcessState      `json:"state,omitempty"`
	ExitCode           *int              `json:"exit_code,omitempty"`
	ExitSignal         string            `json:"exit_signal,omitempty"`
	StateReasonCode    string            `json:"state_reason_code,omitempty"`
	StateReasonMessage string            `json:"state_reason_message,omitempty"`
	Result             json.RawMessage   `json:"result,omitempty"`
	StartedAt          time.Time         `json:"started_at,omitempty"`
	EndedAt            time.Time         `json:"ended_at,omitempty"`
	ObservedAt         time.Time         `json:"observed_at,omitempty"`
}

type ProcessActionKind = processaction.Kind

const (
	ProcessActionWrite     = processaction.KindWrite
	ProcessActionRead      = processaction.KindRead
	ProcessActionInterrupt = processaction.KindInterrupt
	ProcessActionTerminate = processaction.KindTerminate
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

func ValidProcessTerminalSourceTimes(
	state ProcessState,
	startedAt, endedAt time.Time,
) bool {
	if !startedAt.IsZero() && !endedAt.IsZero() && startedAt.After(endedAt) {
		return false
	}
	switch state {
	case ProcessStateExited, ProcessStateKilled:
		return !endedAt.IsZero()
	case ProcessStateFailed:
		return startedAt.IsZero() || !endedAt.IsZero()
	case ProcessStateUnknown:
		return true
	default:
		return false
	}
}

type ReportedEventType string

const (
	EventProcessStarted       ReportedEventType = "process_started"
	EventProcessFinished      ReportedEventType = "process_finished"
	EventProcessActionApplied ReportedEventType = "process_action_applied"
	EventProcessActionFailed  ReportedEventType = "process_action_failed"
	EventProcessActionUnknown ReportedEventType = "process_action_unknown"
)

const (
	ProcessReasonMachineStorageExhausted      = "machine_storage_exhausted"
	ProcessMessageMachineStorageExhausted     = "the machine ran out of writable storage"
	ProcessActionReasonAlreadyStopped         = "already_stopped"
	ProcessActionReasonOutcomeUnknown         = "action_outcome_unknown"
	ProcessActionReasonRunnerUnavailable      = "runner_unavailable"
	ProcessActionReasonInvalidPayload         = "invalid_payload"
	ProcessActionReasonOutputUnavailable      = "output_unavailable"
	ProcessActionReasonStdinWriteUnknown      = "stdin_write_unknown"
	ProcessActionReasonStdinCloseUnknown      = "stdin_close_unknown"
	ProcessActionReasonNotSupported           = "not_supported"
	ProcessActionReasonInterruptUnknown       = "interrupt_unknown"
	ProcessActionReasonTerminateUnknown       = "terminate_unknown"
	ProcessActionReasonUnsupportedAction      = "unsupported_action"
	ProcessActionReasonProcessNotRunning      = "process_not_running"
	ProcessActionReasonInvalidReadObservation = "invalid_read_observation"
	ProcessActionReasonReadInterrupted        = "read_interrupted"
)

type ProcessPhase string

const (
	ProcessPhasePreparing ProcessPhase = "preparing"
	ProcessPhasePrepared  ProcessPhase = "prepared"
	ProcessPhaseAccepted  ProcessPhase = "accepted"
	ProcessPhaseTerminal  ProcessPhase = "terminal"
)

type ActionPosition string

const (
	ActionPositionEffectCommitted ActionPosition = "effect_committed"
	ActionPositionTerminal        ActionPosition = "terminal"
)

type ProcessDisposition string

const (
	ProcessDispositionClosePreparation ProcessDisposition = "close_preparation"
	ProcessDispositionStart            ProcessDisposition = "start"
	ProcessDispositionRetain           ProcessDisposition = "retain"
	ProcessDispositionRelease          ProcessDisposition = "release"
)

type ActionDisposition string

const (
	ActionDispositionApply   ActionDisposition = "apply"
	ActionDispositionRetain  ActionDisposition = "retain"
	ActionDispositionSettle  ActionDisposition = "settle"
	ActionDispositionRelease ActionDisposition = "release"
)
