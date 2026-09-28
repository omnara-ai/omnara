package arker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	arkersdk "github.com/ArkerHQ/arker-sdk/go"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	provisioningTimeout = time.Minute
	daemonPollInterval  = 500 * time.Millisecond
	wakeTimeout         = 15 * time.Second

	bootPath = "/tmp/omnara-boot.sh"

	daemonCommand = "exec /bin/sh " + bootPath

	daemonSessionIdx = 1
	// Waking must not queue behind a user run on session 0, nor interrupt the
	// daemon on session 1.
	wakeSessionIdx = 2
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var errNotThisMachine = errors.New("arker vm does not belong to this machine")

//nolint:misspell // "cancelled" is the wire value.
var terminalRunStates = map[string]bool{"completed": true, "failed": true, "cancelled": true}

// A run is only started once it reports `running`; `pending` is queued behind
// an earlier run on the session.
const runStateRunning = "running"

// The boot script installs omnarad before exec'ing it, which outlasts the
// settle window. Derived from provisioningTimeout so the inner wait cannot
// outlive the deadline the manager already applies to ProvisionMachine, which
// would surface as a cancellation instead of the state the daemon was stuck in.
// Variable so tests do not pay it.
//
// A CEILING, not the budget: it is measured from when polling starts, while the
// manager's deadline has been running since Fork. Everything before the first
// poll -- the fork, canceling a stale daemon, writing the boot script -- eats
// into the same minute, so on a slow call this ceiling alone would still let the
// outer deadline fire first and report a bare cancellation. waitForDaemon takes
// the smaller of this and what the context actually has left, so the caller is
// told the run state the daemon was stuck in rather than that time ran out.
const defaultDaemonStartTimeout = provisioningTimeout - 5*time.Second

// Reserved out of the remaining context so the wait ends, and reports, just
// before the manager gives up on it.
const daemonDeadlineMargin = 2 * time.Second

var daemonStartTimeout = defaultDaemonStartTimeout

const defaultDaemonSettleWindow = 3 * time.Second

var daemonSettleWindow = defaultDaemonSettleWindow

var (
	_ providers.Provider        = (*provider)(nil)
	_ providers.MachineWaker    = (*provider)(nil)
	_ providers.RuntimeProvider = (*provider)(nil)
)

type provider struct {
	apiKey         string
	baseURL        string
	controlBaseURL string
	omnaraAPIURL   string
}

func newProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (*provider, error) {
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(runtimeConfig.ProviderAuthToken) == "" {
		return nil, errors.New("arker provider auth token is required")
	}
	return &provider{
		apiKey:         runtimeConfig.ProviderAuthToken,
		baseURL:        config.BaseURL,
		controlBaseURL: config.ControlBaseURL,
		omnaraAPIURL:   runtimeConfig.OmnaraAPIURL,
	}, nil
}

func (p *provider) client(options providerOptions) (*arkersdk.Client, error) {
	return p.clientFor(arkersdk.Options{
		BaseURL:  p.baseURL,
		Provider: options.Provider,
		Region:   options.Region,
	})
}

func (p *provider) clientFor(opts arkersdk.Options) (*arkersdk.Client, error) {
	if opts.BaseURL == "" && opts.Provider == "" {
		return nil, errors.New(
			"arker machine has no placement: set base_url in the provider config, or provider and region in provider_options",
		)
	}
	opts.APIKey = p.apiKey
	opts.ControlBaseURL = p.controlBaseURL
	return arkersdk.New(opts)
}

func (*provider) ProvisioningTimeout() time.Duration {
	return provisioningTimeout
}

func (*provider) PrepareProvisioning(
	_ context.Context,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	if _, err := providerOptionsFromProvisioning(machineProvisioning); err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	return executionstore.MachineResourceFacts{
		CPU:      machineProvisioning.CPU,
		MemoryMB: machineProvisioning.MemoryMB,
	}, nil
}

func provisionKey(allocationName string) string {
	return allocationName
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
) (providers.ProvisionMachineResult, error) {
	options, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	env, err := providers.BuildManagedMachineEnv(
		p.omnaraAPIURL,
		machineToken,
		options.StartupScript,
		machineEnv,
	)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	client, err := p.client(options)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}

	vm, err := client.Fork(ctx, arkersdk.ForkRequest{
		SourceVMName:   options.Source,
		Name:           name,
		Description:    "omnara pool machine",
		IdempotencyKey: provisionKey(name),
		Resources: &arkersdk.Resources{
			VCPU:      machineProvisioning.CPU,
			MemoryMiB: machineProvisioning.MemoryMB,
		},
	})
	if err != nil {
		return providers.ProvisionMachineResult{}, classifyError(err)
	}
	if vm == nil || strings.TrimSpace(vm.ID) == "" {
		return providers.ProvisionMachineResult{}, errors.New("arker fork returned no vm id")
	}
	result := providers.ProvisionMachineResult{
		ProviderResourceID: vm.ID,
		SandboxURL:         vm.BaseURL(),
	}
	// WakeMachineInput carries only the id and the stored sandbox URL -- not the
	// machine's provisioning -- so a machine recorded without an endpoint can
	// never be woken: there is nothing left to rebuild a client from. Fail the
	// provision here instead, while still returning the id so cleanup can delete
	// the VM that Arker did create.
	if strings.TrimSpace(result.SandboxURL) == "" {
		return result, fmt.Errorf("arker fork returned vm %s without a sandbox url", vm.ID)
	}
	if vm.Info == nil {
		return result, fmt.Errorf("arker fork returned vm %s without its record", vm.ID)
	}
	if vm.Info.Name != name {
		return result, fmt.Errorf(
			"arker vm %s is named %q and does not belong to machine %q",
			vm.ID,
			vm.Info.Name,
			name,
		)
	}
	if err := startDaemon(ctx, vm, env); err != nil {
		return result, err
	}
	return result, nil
}

// ProvisionMachine is required to be idempotent, and the manager retries it.
// Starting a second daemon unconditionally is what broke that: session 1 runs
// one command at a time, so the retry's run sat `pending` behind a first daemon
// that was already healthy, and the provision failed on a VM with nothing wrong
// with it.
//
// Cancel rather than adopt. Adopting an in-flight boot leaves the guest running
// the token the FIRST attempt wrote, while the manager has since issued a new
// one, so the pool can mark a machine provisioned whose omnarad never
// authenticates. Canceling and restarting costs one boot and guarantees the
// daemon that survives is the one holding current credentials -- and there is
// still exactly one at the end, which is what idempotent has to mean here.
func cancelStaleDaemons(ctx context.Context, vm *arkersdk.VM) error {
	listed, err := vm.ListRuns(ctx, arkersdk.ListRunsOptions{Limit: 100})
	if err != nil {
		return fmt.Errorf("list runs on arker vm %s: %w", vm.ID, classifyError(err))
	}
	if listed == nil {
		return nil
	}
	for _, run := range listed.Runs {
		if run.Command != daemonCommand || terminalRunStates[run.State] {
			continue
		}
		if _, err := vm.CancelRun(ctx, run.RunID); err != nil && !arkersdk.IsNotFound(err) {
			return fmt.Errorf(
				"cancel stale omnara daemon %s on arker vm %s: %w",
				run.RunID,
				vm.ID,
				classifyError(err),
			)
		}
	}
	return nil
}

func startDaemon(ctx context.Context, vm *arkersdk.VM, env map[string]string) error {
	script, err := bootScript(env)
	if err != nil {
		return err
	}
	if err := cancelStaleDaemons(ctx, vm); err != nil {
		return err
	}
	if err := vm.WriteFile(ctx, bootPath, []byte(script)); err != nil {
		return fmt.Errorf("write omnara boot script to arker vm %s: %w", vm.ID, classifyError(err))
	}
	started, err := vm.Run(ctx, arkersdk.RunRequest{
		Command:          daemonCommand,
		SessionIdx:       arkersdk.Ptr(daemonSessionIdx),
		TimeToBackground: arkersdk.Ptr(0),
	})
	if err != nil {
		return fmt.Errorf("start omnara daemon on arker vm %s: %w", vm.ID, classifyError(err))
	}
	if started.RunID == "" {
		return fmt.Errorf("arker vm %s returned no run id for the omnara daemon", vm.ID)
	}
	return waitForDaemon(ctx, vm, started.RunID)
}

func bootScript(env map[string]string) (string, error) {
	var out strings.Builder
	out.WriteString("rm -f \"$0\"\n")
	for _, name := range slices.Sorted(maps.Keys(env)) {
		if !envName.MatchString(name) {
			return "", fmt.Errorf("machine env name %q is not a shell identifier", name)
		}
		out.WriteString("export ")
		out.WriteString(name)
		out.WriteString("='")
		out.WriteString(strings.ReplaceAll(env[name], "'", `'\''`))
		out.WriteString("'\n")
	}
	out.WriteString("\n")
	out.WriteString(providers.ManagedBootScript())
	return out.String(), nil
}

func waitForDaemon(ctx context.Context, vm *arkersdk.VM, runID string) error {
	started := time.Now()
	deadline := started.Add(daemonSettleWindow)
	hardDeadline := started.Add(daemonStartTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		if reserved := ctxDeadline.Add(-daemonDeadlineMargin); reserved.Before(hardDeadline) {
			hardDeadline = reserved
		}
	}
	// Never before the settle window. Success is only concluded once the daemon
	// has held `running` through it, so a hard deadline inside that window would
	// fail a boot that is doing nothing wrong -- reporting a healthy daemon as
	// stuck, which is the opposite of what bounding the wait was for. If the
	// context really cannot afford the settle, ctx.Done() ends the wait and says
	// so honestly rather than blaming the daemon.
	if hardDeadline.Before(deadline) {
		hardDeadline = deadline
	}
	for {
		record, err := vm.GetRun(ctx, runID)
		if err != nil {
			return fmt.Errorf("check omnara daemon on arker vm %s: %w", vm.ID, classifyError(err))
		}
		if terminalRunStates[record.State] {
			return fmt.Errorf(
				"omnara daemon on arker vm %s is %s instead of running (exit %s)",
				vm.ID,
				record.State,
				exitText(record.ExitCode),
			)
		}
		if record.State == runStateRunning && !time.Now().Before(deadline) {
			return nil
		}
		if !time.Now().Before(hardDeadline) {
			// The elapsed time, not the ceiling: the context may have cut
			// the wait short, and reporting a budget that was never spent
			// sends whoever reads this looking for the wrong thing.
			return fmt.Errorf(
				"omnara daemon on arker vm %s is still %s after %s",
				vm.ID,
				record.State,
				time.Since(started).Round(time.Second),
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(daemonPollInterval):
		}
	}
}

func exitText(code *int) string {
	if code == nil {
		return "none"
	}
	return strconv.Itoa(*code)
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	// GET /v1/vms/{id} resolves a name as well as an id, so a machine whose id
	// was never recorded is still reachable by the name it was allocated. The
	// ownership check below rejects anything the name resolved to unexpectedly.
	lookup := strings.TrimSpace(providerResourceID)
	if lookup == "" {
		lookup = expectedName
	}
	client, err := p.clientForMachine(machineProvisioning)
	if err != nil {
		return "", false, err
	}
	vm, found, err := client.GetVM(ctx, lookup)
	if err != nil {
		return "", false, classifyError(err)
	}
	if !found {
		return "", false, nil
	}
	if vm.Info == nil || vm.Info.Name != expectedName {
		return "", false, fmt.Errorf(
			"arker vm %s does not belong to machine %q: %w",
			lookup,
			expectedName,
			errNotThisMachine,
		)
	}
	// Info.VMID, not vm.ID: Refresh does not rewrite the handle, so vm.ID is
	// still the lookup — a name when the id was missing.
	if vm.Info.VMID == "" {
		return "", false, fmt.Errorf("arker vm %s returned no id", lookup)
	}
	return vm.Info.VMID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	if strings.TrimSpace(providerResourceID) == "" {
		return errors.New("provider resource id is required")
	}
	client, err := p.clientForMachine(machineProvisioning)
	if err != nil {
		return err
	}
	if err := client.VM(providerResourceID).Delete(ctx); err != nil &&
		!arkersdk.IsNotFound(err) {
		return classifyError(err)
	}
	return nil
}

func (p *provider) WakeMachine(ctx context.Context, input providers.WakeMachineInput) error {
	if strings.TrimSpace(input.ProviderResourceID) == "" {
		return errors.New("provider resource id is required")
	}
	baseURL := strings.TrimSpace(input.SandboxURL)
	if baseURL == "" {
		baseURL = p.baseURL
	}
	client, err := p.clientFor(arkersdk.Options{BaseURL: baseURL})
	if err != nil {
		return err
	}
	wakeCtx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	if _, err := client.VM(input.ProviderResourceID).Run(wakeCtx, arkersdk.RunRequest{
		Command:          "true",
		SessionIdx:       arkersdk.Ptr(wakeSessionIdx),
		TimeToBackground: arkersdk.Ptr(0),
	}); err != nil {
		return classifyError(err)
	}
	return nil
}

func (p *provider) clientForMachine(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (*arkersdk.Client, error) {
	options, err := parseProviderOptions(machineProvisioning.ProviderOptions)
	if err != nil {
		return nil, err
	}
	return p.client(options)
}

func classifyError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *arkersdk.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode != http.StatusTooManyRequests &&
			apiErr.StatusCode < http.StatusInternalServerError {
			return err
		}
		err = fmt.Errorf("%w: %w", storeerr.ErrMachineProviderUnavailable, err)
		if apiErr.RetryAfter == nil {
			return err
		}
		header := http.Header{}
		header.Set("Retry-After", strconv.Itoa(int(math.Ceil(*apiErr.RetryAfter))))
		return providers.WithRetryAfter(err, header)
	}
	var unknown *arkersdk.UnknownOutcomeError
	if errors.As(err, &unknown) {
		return fmt.Errorf("%w: %w", storeerr.ErrMachineProviderUnavailable, err)
	}
	return err
}
