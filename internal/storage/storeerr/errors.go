package storeerr

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
)

const (
	AgentBacklogFullCode             = "agent_backlog_full"
	ManagedWorkAdmissionDeniedCode   = "managed_work_admission_denied"
	InsufficientOmnaraCreditsMessage = "Insufficient Omnara credits."
	BudgetReachedCode                = "budget_reached"
)

func BudgetReachedMessage(scope, interval string) string {
	period := interval
	switch interval {
	case "day":
		period = "daily"
	case "week":
		period = "weekly"
	case "month":
		period = "monthly"
	}
	switch scope {
	case "project":
		return fmt.Sprintf("This project's %s budget has been reached. "+
			"An org admin can raise it on the Usage page.", period)
	case "profile":
		return fmt.Sprintf("The top-level agent's profile has reached its %s budget. "+
			"An org admin can raise it on that profile's Usage tab.", period)
	case "agent":
		return fmt.Sprintf("The top-level agent's %s budget has been reached. "+
			"An org admin can raise it on that agent's page.", period)
	}
	return fmt.Sprintf("This organization's %s budget has been reached. "+
		"An org admin can raise it on the Usage page.", period)
}

var (
	ErrCheckpointBoundaryUnsafe = errors.New("context checkpoint boundary is unsafe")
	ErrCommitOutcomeUnknown     = errors.New("transaction commit outcome is unknown")
	AgentBacklogFullMessage     = fmt.Sprintf(
		"This agent already has %d pending messages. "+
			"Wait for messages to be processed or cancel a pending message, then try again.",
		executionpolicy.MaxPendingAgentContentInputs)
	ErrAgentBacklogFull              = errors.New(AgentBacklogFullMessage)
	ErrInvalidRequest                = errors.New("invalid request")
	ErrNoClaimableAgentWakeup        = errors.New("no claimable agent wakeup")
	ErrAgentNotAdvanceable           = errors.New("agent is not advanceable")
	ErrAgentLaunchFailed             = errors.New("launch agent for integration target")
	ErrRuntimeLockInactive           = errors.New("runtime lock is not active")
	ErrToolCallInProgress            = errors.New("tool call is already in progress")
	ErrInvalidToolCallDisposition    = errors.New("invalid tool call transaction disposition")
	ErrNoOnlineDaemonRuntime         = errors.New("machine is offline")
	ErrAgentProcessLimitReached      = errors.New("agent process limit reached")
	ErrManagedWorkAdmissionDenied    = errors.New("new managed work is not allowed")
	ErrMachineNotReachable           = errors.New("machine is not reachable")
	ErrProcessTerminal               = errors.New("process is terminal")
	ErrProcessTerminating            = errors.New("process is terminating")
	ErrProcessAlreadyStopped         = errors.New("process is already stopped")
	ErrProcessStateUnknown           = errors.New("process state is unknown")
	ErrInvalidSkillName              = errors.New("invalid skill name")
	ErrInvalidSecretName             = errors.New("invalid secret name")
	ErrSecretNameExists              = errors.New("secret name already exists")
	ErrInvalidSecretRequest          = errors.New("invalid secret request")
	ErrInvalidActorRequest           = errors.New("invalid actor request")
	ErrInvalidModelProviderConfig    = errors.New("invalid model provider config")
	ErrConflict                      = errors.New("resource conflict")
	ErrFileReadOnly                  = errors.New("file access is read-only")
	ErrExpectedDigestRequired        = errors.New("expected_digest is required")
	ErrNotAFile                      = errors.New("path is a directory; list its contents and choose a file")
	ErrPathNotFound                  = errors.New("no file or directory exists at this path")
	ErrIdempotencyConflict           = errors.New("idempotency key used for different resource")
	ErrAuthConnectorImmutable        = errors.New("auth connector kind and issuer are immutable for slug")
	ErrAuthConnectorIdentityConflict = errors.New("auth connector issuer already exists")
	ErrInvalidDeviceAuthFlow         = errors.New("invalid device auth flow")
	ErrDaemonRuntimeUnregistered     = errors.New("daemon runtime is no longer registered for this machine")
	ErrDaemonInstanceSuperseded      = errors.New("daemon instance has been superseded")
	ErrProcessExecutionNotGranted    = errors.New("process execution was not granted")
	ErrInvalidProcessOutputMatch     = errors.New("invalid process output match")
	ErrMachineSleepPendingWork       = errors.New("machine has pending daemon work")
	ErrMachineNotWakeCapable         = errors.New("machine is not wake capable")
	ErrMachineWakeUnresolved         = errors.New("machine wake outcome is unresolved")
	ErrProcessActionReportBlocked    = errors.New(
		"process action report is blocked by earlier non-terminal action",
	)
	ErrStateTransitionConflict      = errors.New("state transition conflict")
	ErrToolCallCompleted            = fmt.Errorf("tool call already completed: %w", ErrStateTransitionConflict)
	ErrMachineProviderUnavailable   = errors.New("machine provider unavailable")
	ErrPermanentEnvironment         = errors.New("permanent environment resolution error")
	ErrModelGrantUnavailable        = errors.New("model grant unavailable")
	ErrUnauthorized                 = errors.New("unauthorized")
	ErrOAuthScopeExceedsGrant       = errors.New("requested scope exceeds the granted scope")
	ErrNotFound                     = errors.New("not found")
	ErrMCPOAuthFlowConsumed         = errors.New("mcp oauth flow already consumed")
	ErrIntegrationOAuthFlowConsumed = errors.New("integration oauth flow already consumed")
)

type FileContentConflictError struct {
	CurrentDigest string
}

func (err *FileContentConflictError) Error() string { return ErrConflict.Error() }

func (err *FileContentConflictError) Unwrap() error { return ErrConflict }

func FileErrorCode(err error) string {
	var conflict *FileContentConflictError
	switch {
	case errors.Is(err, ErrFileReadOnly):
		return "file_read_only"
	case errors.Is(err, ErrExpectedDigestRequired):
		return "expected_digest_required"
	case errors.Is(err, ErrNotAFile):
		return "not_a_file"
	case errors.Is(err, ErrPathNotFound):
		return "path_not_found"
	case errors.As(err, &conflict):
		return "file_content_conflict"
	default:
		return ""
	}
}

type taggedError struct {
	sentinel error
	err      error
}

func (err taggedError) Error() string {
	return err.err.Error()
}

func (err taggedError) Unwrap() []error {
	return []error{err.sentinel, err.err}
}

func Tag(sentinel, err error) error {
	if err == nil {
		return nil
	}
	return taggedError{sentinel: sentinel, err: err}
}

func InvalidRequest(err error) error {
	return Tag(ErrInvalidRequest, err)
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}
