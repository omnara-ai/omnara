package machinedaemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

var errDaemonRuntimeUnregistered = errors.New("daemon runtime unregistered")
var errDaemonInstanceSuperseded = errors.New("daemon instance superseded")
var errDaemonBindingMismatch = errors.New("daemon binding mismatch")
var errDaemonAuthorizationRevoked = errors.New("daemon authorization revoked")
var errMachineDecommissioned = errors.New("machine decommissioned")
var errDaemonRuntimeMissingHeartbeatDirective = errors.New(
	"daemon runtime response missing next heartbeat directive",
)
var ErrDaemonUpdate = errors.New("daemon update")

const maximumRetryDelay = time.Duration(math.MaxInt64)

func New(cfg Config, httpClient *http.Client, log *slog.Logger) Client {
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = time.Second
	}
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	securedHTTPClient := outboundhttp.CloneWithoutRedirects(httpClient)
	if log == nil {
		log = slog.Default()
	}
	return Client{
		cfg:            cfg,
		instanceID:     uuid.New(),
		http:           securedHTTPClient,
		log:            log,
		runnerLauncher: subprocessRunnerLauncher{},
		processes:      make(map[string]*processRuntime),
		wakeSignals:    make(chan struct{}, 1),
	}
}

func (c *Client) Run(ctx context.Context) error {
	platform, err := newSleepPlatform(c.cfg.SleepPlatform)
	if err != nil {
		return err
	}
	c.sleepPlatform = platform
	if err := c.sleepPlatform.preventSleep(); err != nil {
		return err
	}
	if c.sleepEnabled() {
		if _, err := c.startWakeListener(ctx); err != nil {
			return err
		}
	}
	_, err = c.bootstrapWithRetry(ctx)
	if IsAuthenticationRejected(err) ||
		errors.Is(err, errDaemonBindingMismatch) {
		c.log.Warn("daemon bootstrap rejected; exiting", "error", err)
		if IsAuthenticationRejected(err) {
			c.shutdownAfterAuthorityLoss(ctx, "authorization_revoked")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := c.stateStore(ctx); err != nil {
		return err
	}
	defer func() {
		_ = c.closeState()
	}()

	consecutiveDisconnects := 0
	for {
		runtime, startup, err := c.registerWithRetry(ctx)
		if errors.Is(err, errDaemonInstanceSuperseded) ||
			IsAuthenticationRejected(err) {
			c.log.Warn(
				"daemon runtime registration rejected; exiting",
				"error",
				err,
			)
			if IsAuthenticationRejected(err) {
				c.shutdownAfterAuthorityLoss(
					ctx,
					"authorization_revoked",
				)
			}
			return nil
		}
		if err != nil {
			return err
		}

		heartbeatAcknowledged, err := c.runRegisteredRuntime(
			ctx,
			runtime,
			startup,
		)
		if errors.Is(err, errDaemonAsleep) {
			consecutiveDisconnects = 0
			if err := c.sleepUntilWake(ctx); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, errDaemonSleepVetoed) {
			consecutiveDisconnects = 0
			continue
		}
		shutdownReason := ""
		switch {
		case errors.Is(err, errMachineDecommissioned):
			shutdownReason = "machine_deleted"
		case errors.Is(err, errDaemonAuthorizationRevoked):
			shutdownReason = "authorization_revoked"
		}
		if shutdownReason != "" {
			c.shutdownAfterAuthorityLoss(ctx, shutdownReason)
			return nil
		}
		if !errors.Is(err, errDaemonRuntimeUnregistered) {
			return err
		}
		if heartbeatAcknowledged {
			consecutiveDisconnects = 0
		}
		consecutiveDisconnects++
		retryDelay := retryDelayForDaemonError(
			c.cfg.RetryInterval,
			30*time.Second,
			consecutiveDisconnects,
			err,
		)
		c.log.Warn(
			"daemon connection lost; reconciling before reconnect",
			"daemon_runtime_id",
			runtime.ID,
			"retry_after",
			retryDelay,
		)
		if err := sleepContext(ctx, retryDelay); err != nil {
			return err
		}
	}
}

func (c *Client) runRegisteredRuntime(
	ctx context.Context,
	runtime DaemonRuntime,
	startup localStartupState,
) (bool, error) {
	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)

	transport := newDaemonSocketTransport(c, runtime, startup)
	c.setTransport(transport)
	heartbeatAcknowledged, err := transport.Run(runCtx)
	c.clearTransport(transport)
	if errors.Is(err, errDaemonIdleSleep) {
		c.drainWakeSignals()
		sleepErr := c.Sleep(runCtx, runtime)
		switch {
		case sleepErr == nil:
			return heartbeatAcknowledged, errDaemonAsleep
		case errors.Is(sleepErr, errDaemonRuntimeUnregistered):
			return heartbeatAcknowledged, errDaemonRuntimeUnregistered
		case errors.Is(sleepErr, errDaemonSleepNotWakeCapable):
			c.sleepDisabled.Store(true)
			c.log.Warn(
				"daemon sleep rejected: machine has no sandbox url; sleep disabled for this session",
				"daemon_runtime_id",
				runtime.ID,
			)
		case errors.Is(sleepErr, errDaemonSleepVetoed):
			c.log.Info(
				"daemon sleep vetoed by pending work; reconnecting",
				"daemon_runtime_id",
				runtime.ID,
			)
		default:
			c.log.Warn(
				"daemon sleep request failed; reconnecting",
				"daemon_runtime_id",
				runtime.ID,
				"error",
				sleepErr,
			)
		}
		return heartbeatAcknowledged, errors.Join(errDaemonRuntimeUnregistered, sleepErr)
	}

	if errors.Is(err, errMachineDecommissioned) ||
		errors.Is(err, errDaemonAuthorizationRevoked) {
		return heartbeatAcknowledged, err
	}
	if ctx.Err() == nil {
		if errors.Is(err, errDaemonRuntimeUnregistered) {
			return heartbeatAcknowledged, err
		}
		return heartbeatAcknowledged, errors.Join(
			errDaemonRuntimeUnregistered,
			err,
		)
	}
	endTimeout := 2 * time.Second
	if errors.Is(context.Cause(ctx), ErrDaemonUpdate) {
		endTimeout = 10 * time.Second
	}
	endCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		endTimeout,
	)
	defer cancel()
	if endErr := c.End(endCtx, c.currentActiveRuntime()); endErr != nil {
		c.log.Warn(
			"daemon runtime end failed",
			"daemon_runtime_id",
			runtime.ID,
			"error",
			endErr,
		)
	}
	return heartbeatAcknowledged, err
}

func (c *Client) setActiveRuntime(runtime DaemonRuntime) {
	c.runtimeMu.Lock()
	c.activeRuntime = runtime
	c.runtimeMu.Unlock()
}

func (c *Client) currentActiveRuntime() DaemonRuntime {
	c.runtimeMu.RLock()
	defer c.runtimeMu.RUnlock()
	return c.activeRuntime
}

func (c *Client) bootstrapWithRetry(
	ctx context.Context,
) (daemonBootstrap, error) {
	consecutiveFailures := 0
	for {
		bootstrap, err := c.Bootstrap(ctx)
		if err == nil {
			return bootstrap, nil
		}
		if ctx.Err() != nil {
			return daemonBootstrap{}, ctx.Err()
		}
		if isTerminalDaemonRequestError(err) ||
			errors.Is(err, errDaemonBindingMismatch) {
			return daemonBootstrap{}, err
		}
		consecutiveFailures++
		backoff := retryDelayForDaemonError(
			c.cfg.RetryInterval,
			30*time.Second,
			consecutiveFailures,
			err,
		)
		c.log.Warn(
			"daemon bootstrap failed; retrying",
			"daemon_instance_id",
			c.instanceID,
			"error",
			err,
			"retry_after",
			backoff,
		)
		if err := sleepContext(ctx, backoff); err != nil {
			return daemonBootstrap{}, err
		}
	}
}

func (c *Client) registerWithRetry(
	ctx context.Context,
) (DaemonRuntime, localStartupState, error) {
	consecutiveFailures := 0
	for {
		startup, err := c.scanLocalProcessesForRegistration(ctx)
		if err != nil {
			return DaemonRuntime{}, localStartupState{}, err
		}
		runtime, reconciliation, err := c.Register(ctx, startup.Claims)
		if err == nil {
			applyErr := c.applyRegistrationReconciliation(
				ctx,
				&startup,
				reconciliation,
			)
			if applyErr == nil {
				c.setActiveRuntime(runtime)
				return runtime, startup, nil
			}
			err = fmt.Errorf(
				"apply daemon runtime reconciliation: %w",
				applyErr,
			)
		}
		startup.releaseResources()
		if ctx.Err() != nil {
			return DaemonRuntime{}, localStartupState{}, ctx.Err()
		}
		if errors.Is(err, errDaemonInstanceSuperseded) ||
			isTerminalDaemonRequestError(err) {
			return DaemonRuntime{}, localStartupState{}, err
		}
		consecutiveFailures++
		backoff := retryDelayForDaemonError(
			c.cfg.RetryInterval,
			30*time.Second,
			consecutiveFailures,
			err,
		)
		c.log.Warn(
			"daemon runtime registration failed; retrying",
			"daemon_instance_id",
			c.instanceID,
			"error",
			err,
			"retry_after",
			backoff,
		)
		if err := sleepContext(ctx, backoff); err != nil {
			return DaemonRuntime{}, localStartupState{}, err
		}
	}
}

func (c *Client) Register(
	ctx context.Context,
	claims []ProcessReconciliationClaim,
) (DaemonRuntime, DaemonRuntimeReconciliation, error) {
	if _, err := c.Bootstrap(ctx); err != nil {
		return DaemonRuntime{}, DaemonRuntimeReconciliation{}, err
	}
	if claims == nil {
		claims = []ProcessReconciliationClaim{}
	}
	var response registerResponse
	err := c.postJSON(
		ctx,
		daemonAPIPath+"/runtimes",
		map[string]any{
			"daemon_instance_id": c.instanceID,
			"daemon_version":     c.cfg.DaemonVersion,
			"observed_platform":  daemonObservedPlatform(),
			"processes":          claims,
		},
		&response,
	)
	var statusErr httpStatusError
	if errors.As(err, &statusErr) &&
		statusErr.StatusCode == http.StatusGone {
		err = errDaemonInstanceSuperseded
	}
	if err == nil && response.Runtime.NextHeartbeatAfterMS <= 0 {
		err = errDaemonRuntimeMissingHeartbeatDirective
	}
	return response.Runtime, response.Reconciliation, err
}

func daemonObservedPlatform() map[string]any {
	return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH}
}

func (c *Client) Bootstrap(ctx context.Context) (daemonBootstrap, error) {
	if c.bootstrap.MachineID != "" {
		return c.bootstrap, nil
	}
	var response daemonBootstrap
	if err := c.postJSON(
		ctx,
		daemonAPIPath+"/bootstrap",
		nil,
		&response,
	); err != nil {
		return daemonBootstrap{}, err
	}
	if response.InstallationID == "" || response.MachineID == "" {
		return daemonBootstrap{}, errors.New(
			"daemon bootstrap response is missing required identity",
		)
	}
	if c.cfg.ExpectedInstallationID != "" &&
		response.InstallationID != c.cfg.ExpectedInstallationID {
		return daemonBootstrap{}, fmt.Errorf(
			"%w: authenticated installation %q does not match configured installation %q",
			errDaemonBindingMismatch,
			response.InstallationID,
			c.cfg.ExpectedInstallationID,
		)
	}
	if c.cfg.ExpectedMachineID != "" &&
		response.MachineID != c.cfg.ExpectedMachineID {
		return daemonBootstrap{}, fmt.Errorf(
			"%w: authenticated machine %q does not match configured machine %q",
			errDaemonBindingMismatch,
			response.MachineID,
			c.cfg.ExpectedMachineID,
		)
	}
	c.bootstrap = response
	return response, nil
}

func (c *Client) ValidateBootstrap(
	ctx context.Context,
) (BootstrapIdentity, error) {
	bootstrap, err := c.Bootstrap(ctx)
	if err != nil {
		return BootstrapIdentity{}, err
	}
	return BootstrapIdentity(bootstrap), nil
}

func (c *Client) End(
	ctx context.Context,
	runtime DaemonRuntime,
) error {
	if runtime.ID == "" {
		return nil
	}
	return c.postJSON(
		ctx,
		daemonAPIPath+"/runtimes/"+runtime.ID+"/end",
		nil,
		nil,
	)
}

func (c *Client) stateStore(
	ctx context.Context,
) (*statedb.Store, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.state != nil {
		return c.state, nil
	}
	if c.cfg.OmnaraHome == "" {
		return nil, errors.New("OMNARA_HOME is required for daemon state")
	}
	machine, err := c.machineStore()
	if err != nil {
		return nil, err
	}
	binding, err := c.localMachineBinding()
	if err != nil {
		return nil, err
	}
	store, err := statedb.Open(
		ctx,
		machine.StateDBPath(),
		binding.InstallationID,
		binding.MachineID,
	)
	if err != nil {
		return nil, fmt.Errorf("open machine state database: %w", err)
	}
	c.state = store
	return store, nil
}

func (c *Client) closeState() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.state != nil {
		err := c.state.Close()
		c.state = nil
		if err != nil {
			c.log.Warn("close machine state database failed", "error", err)
			return err
		}
	}
	return nil
}

func retryBackoffDelay(
	initial, maximum time.Duration,
	failures int,
) time.Duration {
	if initial <= 0 {
		initial = time.Second
	}
	if maximum < initial {
		maximum = initial
	}
	delay := initial
	for range max(failures-1, 0) {
		if delay >= maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		delay = maximum
	}
	if delay <= time.Nanosecond {
		return delay
	}
	minimum := delay / 2
	return minimum + time.Duration(rand.Int64N(int64(delay-minimum)))
}

func retryDelayForDaemonError(
	initial, maximum time.Duration,
	failures int,
	err error,
) time.Duration {
	delay := retryBackoffDelay(initial, maximum, failures)
	serverDelay, ok := retryAfterDelay(err, time.Now())
	if !ok {
		return delay
	}
	if serverDelay > maximumRetryDelay-delay {
		return maximumRetryDelay
	}
	return serverDelay + delay
}
