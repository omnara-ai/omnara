package storeerr

import (
	"errors"

	"github.com/jackc/pgx/v5"
)

const ManagedWorkAdmissionDeniedCode = "managed_work_admission_denied"

var (
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
	ErrInvalidSecretRequest          = errors.New("invalid secret request")
	ErrInvalidActorRequest           = errors.New("invalid actor request")
	ErrInvalidModelProviderConfig    = errors.New("invalid model provider config")
	ErrConflict                      = errors.New("resource conflict")
	ErrIdempotencyConflict           = errors.New("idempotency key used for different resource")
	ErrAuthConnectorImmutable        = errors.New("auth connector kind and issuer are immutable for slug")
	ErrAuthConnectorIdentityConflict = errors.New("auth connector issuer already exists")
	ErrInvalidDeviceAuthFlow         = errors.New("invalid device auth flow")
	ErrDaemonRuntimeUnregistered     = errors.New("daemon runtime is no longer registered for this machine")
	ErrDaemonInstanceSuperseded      = errors.New("daemon instance has been superseded")
	ErrProcessExecutionNotGranted    = errors.New("process execution was not granted")
	ErrMachineSleepPendingWork       = errors.New("machine has pending daemon work")
	ErrMachineNotWakeCapable         = errors.New("machine is not wake capable")
	ErrMachineWakeUnresolved         = errors.New("machine wake outcome is unresolved")
	ErrProcessActionReportBlocked    = errors.New("process action report is blocked by earlier non-terminal action")
	ErrStateTransitionConflict       = errors.New("state transition conflict")
	ErrMachineProviderUnavailable    = errors.New("machine provider unavailable")
	ErrPermanentEnvironment          = errors.New("permanent environment resolution error")
	ErrModelGrantUnavailable         = errors.New("model grant unavailable")
	ErrUnauthorized                  = errors.New("unauthorized")
	ErrNotFound                      = errors.New("not found")
	ErrMCPOAuthFlowConsumed          = errors.New("mcp oauth flow already consumed")
	ErrIntegrationOAuthFlowConsumed  = errors.New("integration oauth flow already consumed")
)

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
