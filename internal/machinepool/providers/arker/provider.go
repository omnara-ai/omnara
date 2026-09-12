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

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
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
)

// Machine env names are only checked for "=" and NUL upstream, and they are
// rendered into a script here.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Not a provider outage but a fact about one machine: the id resolves to a vm
// that is not this machine's.
var errNotThisMachine = errors.New("arker vm does not belong to this machine")

// Wire values from Arker's RunState enum, so the spelling is theirs: changing
// it would stop the state ever matching.
//
//nolint:misspell // "cancelled" is the wire value.
var terminalRunStates = map[string]bool{"completed": true, "failed": true, "cancelled": true}

// How long a boot must stay non-terminal to be treated as started. A variable
// so unit tests do not pay it.
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

// The allocation name alone, so every attempt for a machine presents the same
// key and converges on the vm an earlier attempt built.
func provisionKey(allocationName string) string {
	return allocationName
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
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
	if vm.Info == nil {
		return result, fmt.Errorf("arker fork returned vm %s without its record", vm.ID)
	}
	if vm.Info.Name != name {
		// Reported with its id rather than deleted: the key is this machine's
		// name, so deleting the vm it points at would spend the key for good and
		// the machine could never provision again. The id is what lets cleanup
		// remove the vm instead.
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

// The machine token goes in a file the boot script deletes, not in the command
// or the session env: both of those are readable back from the API.
func startDaemon(ctx context.Context, vm *arkersdk.VM, env map[string]string) error {
	// The fork replays onto the same vm on a retry, so adopt a boot already
	// under way instead of starting a second omnarad.
	running, err := daemonIsRunning(ctx, vm)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	script, err := bootScript(env)
	if err != nil {
		return err
	}
	if err := vm.WriteFile(ctx, bootPath, []byte(script)); err != nil {
		return fmt.Errorf("write omnara boot script to arker vm %s: %w", vm.ID, classifyError(err))
	}
	// A session index is find-or-create, so a retry reuses the daemon's session
	// instead of leaving another behind. Not index 0, where plain runs land and
	// would interrupt the daemon.
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

// Both live states: a run queued behind another on its session is "pending",
// which is not terminal, and missing it would start a second daemon.
func daemonIsRunning(ctx context.Context, vm *arkersdk.VM) (bool, error) {
	for _, state := range []string{"running", "pending"} {
		runs, err := vm.ListRuns(ctx, arkersdk.ListRunsOptions{State: state})
		if err != nil {
			return false, fmt.Errorf("list runs on arker vm %s: %w", vm.ID, classifyError(err))
		}
		for _, run := range runs.Runs {
			if run.Command == daemonCommand {
				return true, nil
			}
		}
	}
	return false, nil
}

// The script deletes itself first: a shell keeps reading a file it has already
// opened, so the credentials are gone before anything that can fail runs.
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

// The boot script ends in `exec omnarad`, so a live daemon keeps its run
// non-terminal for the life of the machine: any terminal state means omnarad is
// gone, whatever it exited with, and `completed` with status 0 is the usual
// shape of a boot that died before the exec.
//
// Staying non-terminal for the window is a liveness sample, not a readiness
// proof. It catches a boot that dies on startup; a daemon that dies later is
// for the machine's own connection tracking to notice.
func waitForDaemon(ctx context.Context, vm *arkersdk.VM, runID string) error {
	deadline := time.Now().Add(daemonSettleWindow)
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
		if !time.Now().Before(deadline) {
			return nil
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

// Arker allows duplicate names and resolves one arbitrarily, so an unknown id
// is absence rather than a lookup that could adopt the wrong machine.
func (p *provider) InspectMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	if strings.TrimSpace(providerResourceID) == "" {
		return "", false, nil
	}
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	client, err := p.clientForMachine(machineProvisioning)
	if err != nil {
		return "", false, err
	}
	vm, found, err := client.GetVM(ctx, providerResourceID)
	if err != nil {
		return "", false, classifyError(err)
	}
	if !found {
		return "", false, nil
	}
	if vm.Info == nil || vm.Info.Name != expectedName {
		return "", false, fmt.Errorf(
			"arker vm %s does not belong to machine %q: %w",
			providerResourceID,
			expectedName,
			errNotThisMachine,
		)
	}
	return vm.ID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
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
	// By the id this provider recorded, without the ownership check InspectMachine
	// applies to adoption: an id only ever comes from this provider's own fork,
	// vm ids are unique and never reused, and a 404 is org-scoped. Refusing a vm
	// whose name is unexpected would leave it billable with nothing able to
	// remove it.
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
		Command: "true",
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
