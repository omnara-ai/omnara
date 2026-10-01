package freestyle

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout     = 2 * time.Minute
	sleepIdleTimeoutSeconds = 60
	wakeDomainSuffix        = ".style.dev"
	installationMarkerKey   = "omnara-installation"
	machineMarkerKey        = "omnara-machine"
	daemonInstallTimeoutMS  = int((providers.HTTPClientTimeout - 2*time.Second) / time.Millisecond)
	autoDeleteNever         = -1
	startPollDelay          = 250 * time.Millisecond
)

const bootstrapKeepAwakeScript = `(while read -r c </proc/$$/comm && [ "$c" != omnarad ]; do ` +
	`curl -fsSI -m 10 -o /dev/null "$OMNARA_INSTALLER_URL" || :; sleep 20; done) >/dev/null 2>&1 &` + "\n"

//go:embed daemon_install.sh
var daemonInstallScript string

type provider struct {
	api           apiClient
	omnaraAPIURL  string
	wakeTransport http.RoundTripper
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

func (*provider) ValidateMachineConfig(executionstore.MachineProvisioningConfig, map[string]string) error {
	return nil
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
	_ bool,
) (providers.ProvisionMachineResult, error) {
	options, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	target, found, err := p.api.GetVM(ctx, name)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if !found {
		metadata, metadataErr := ownershipMetadata(installationID, machineID)
		if metadataErr != nil {
			return providers.ProvisionMachineResult{}, metadataErr
		}
		create := createVMRequest{
			SnapshotID:        options.Snapshot,
			Slug:              name,
			DisplayName:       name,
			AutoDeleteSeconds: autoDeleteNever,
			AutomaticRestart:  true,
			Metadata:          metadata,
			Firewall: firewallSpec{Rules: []firewallRule{{
				Action:      "allow",
				Source:      firewallEndpoint{},
				Destination: firewallEndpoint{Public: true},
			}}},
		}
		if options.SleepAfterMS > 0 {
			create.IdleTimeoutSeconds = sleepIdleTimeoutSeconds
			create.TLS = &tlsSpec{Rules: []tlsRule{{
				Action:      "allow",
				Domain:      name + wakeDomainSuffix,
				Protocol:    "http",
				Source:      tlsEndpoint{Public: true},
				Destination: tlsEndpoint{Port: daemonprotocol.WakeListenerPort},
			}}}
		}
		target, err = p.api.CreateVM(ctx, create)
		if err != nil {
			existing, existingFound, inspectErr := p.api.GetVM(ctx, name)
			if inspectErr == nil && existingFound {
				target = existing
				err = nil
			}
		}
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
	}

	if err := validateOwnedVM(target, installationID, machineID, name); err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if options.SleepAfterMS > 0 {
		result.SandboxURL = "https://" + name + wakeDomainSuffix + "/"
	}

	target, err = p.ensureRunning(ctx, target)
	if err != nil {
		return result, err
	}
	if err := p.ensureResources(ctx, target, machineProvisioning); err != nil {
		return result, err
	}
	if err := p.ensureDaemon(ctx, target.ID, options, machineToken, machineEnv); err != nil {
		return result, err
	}
	return result, nil
}

func (p *provider) ensureRunning(ctx context.Context, target vm) (vm, error) {
	startRequested := false
	for {
		switch normalizeVMState(target.State) {
		case vmStateRunning:
			return target, nil
		case vmStatePaused, vmStateStopped:
			if !startRequested {
				started, err := p.api.StartVM(ctx, target.ID)
				if err != nil {
					return vm{}, err
				}
				target = started
				startRequested = true
				continue
			}
		case vmStateStarting, vmStatePausing:
		case "":
			return vm{}, errors.New("freestyle VM state is required")
		default:
			return vm{}, fmt.Errorf("freestyle VM %q has unsupported state %q", target.ID, target.State)
		}
		select {
		case <-ctx.Done():
			return vm{}, fmt.Errorf("wait for freestyle VM %q to run: %w", target.ID, ctx.Err())
		case <-time.After(startPollDelay):
		}
		refreshed, found, err := p.api.GetVM(ctx, target.ID)
		if err != nil {
			return vm{}, err
		}
		if !found {
			return vm{}, fmt.Errorf("freestyle VM %q disappeared while starting", target.ID)
		}
		target = refreshed
	}
}

func (p *provider) ensureResources(
	ctx context.Context,
	target vm,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	wantCPU := *machineProvisioning.CPU
	wantMemoryMB := *machineProvisioning.MemoryMB
	if target.Resources.CPU > wantCPU || target.Resources.MemoryMB > wantMemoryMB {
		return fmt.Errorf(
			"freestyle snapshot resources cpu=%d memory_mb=%d exceed configured cpu=%d memory_mb=%d: %w",
			target.Resources.CPU,
			target.Resources.MemoryMB,
			wantCPU,
			wantMemoryMB,
			providers.ErrPermanent,
		)
	}
	if target.Resources.CPU == wantCPU && target.Resources.MemoryMB == wantMemoryMB {
		return nil
	}
	resize := resizeVMRequest{}
	if target.Resources.CPU < wantCPU {
		resize.CPU = wantCPU
	}
	if target.Resources.MemoryMB < wantMemoryMB {
		resize.MemoryMB = wantMemoryMB
	}
	return p.api.ResizeVM(ctx, target.ID, resize)
}

func (p *provider) ensureDaemon(
	ctx context.Context,
	resourceID string,
	options providerOptions,
	machineToken string,
	machineEnv map[string]string,
) error {
	env, err := providers.BuildManagedMachineEnv(
		p.omnaraAPIURL,
		machineToken,
		options.StartupScript,
		machineEnv,
	)
	if err != nil {
		return err
	}
	launcher := providers.ManagedDaemonLauncherArgs()[2]
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		env[daemonprotocol.WakeListenAddrEnvVar] = ":" + strconv.Itoa(daemonprotocol.WakeListenerPort)
		launcher = bootstrapKeepAwakeScript + launcher
	}
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	response, err := p.api.ExecVM(ctx, resourceID, execVMRequest{
		Command:   daemonInstallScript,
		Stdin:     base64.StdEncoding.EncodeToString([]byte(managedDaemonStartScript(env, launcher))),
		LinuxUser: "root",
		TimeoutMS: daemonInstallTimeoutMS,
	})
	if err != nil {
		return err
	}
	if response.StatusCode == nil {
		return errors.New("freestyle daemon install command timed out")
	}
	if *response.StatusCode != 0 {
		return fmt.Errorf("freestyle daemon install command exited with status %d", *response.StatusCode)
	}
	return nil
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	lookup := providerResourceID
	if lookup == "" {
		lookup = expectedName
	}
	target, found, err := p.api.GetVM(ctx, lookup)
	if err != nil || !found {
		return "", false, err
	}
	if err := validateOwnedVM(target, installationID, machineID, expectedName); err != nil {
		return "", false, err
	}
	return target.ID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	if providerResourceID == "" {
		return errors.New("provider resource id is required")
	}
	resourceID, found, err := p.InspectMachine(
		ctx,
		installationID,
		machineID,
		machineProvisioning,
		providerResourceID,
	)
	if err == nil && !found {
		resourceID, found, err = p.InspectMachine(
			ctx,
			installationID,
			machineID,
			machineProvisioning,
			"",
		)
	}
	if err != nil || !found {
		return err
	}
	return p.api.DeleteVM(ctx, resourceID)
}

func (p *provider) WakeMachine(ctx context.Context, input providers.WakeMachineInput) error {
	if input.SandboxURL == "" {
		return errors.New("freestyle sandbox url is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, input.SandboxURL, nil)
	if err != nil {
		return fmt.Errorf("build freestyle wake request: %w", err)
	}
	client := providers.NewHTTPClient()
	if p.wakeTransport != nil {
		client.Transport = p.wakeTransport
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("wake freestyle machine: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("wake freestyle machine: unexpected HTTP status %d", response.StatusCode)
	}
	return nil
}

func ownershipMetadata(installationID, machineID uuid.UUID) (map[string]string, error) {
	installationOwner, err := publicid.Encode(publicid.KindInstallation, installationID)
	if err != nil {
		return nil, err
	}
	machineOwner, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		installationMarkerKey: installationOwner,
		machineMarkerKey:      machineOwner,
	}, nil
}

func validateOwnedVM(
	target vm,
	installationID, machineID uuid.UUID,
	expectedName string,
) error {
	if target.ID == "" {
		return errors.New("freestyle VM is missing its id")
	}
	expected, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		return err
	}
	if target.Slug != expectedName {
		return fmt.Errorf("freestyle VM %q does not have the expected ownership metadata", target.ID)
	}
	for key, value := range expected {
		if target.Metadata[key] != value {
			return fmt.Errorf("freestyle VM %q does not have the expected ownership metadata", target.ID)
		}
	}
	return nil
}

func managedDaemonStartScript(env map[string]string, launcher string) string {
	var startScript strings.Builder
	startScript.WriteString("#!/bin/sh\nexec env")
	for _, key := range slices.Sorted(maps.Keys(env)) {
		startScript.WriteByte(' ')
		startScript.WriteString(shellQuote(key + "=" + env[key]))
	}
	startScript.WriteString(" /bin/sh -c ")
	startScript.WriteString(shellQuote(launcher))
	startScript.WriteByte('\n')
	return startScript.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

const (
	vmStateStarting = "starting"
	vmStateRunning  = "running"
	vmStatePausing  = "pausing"
	vmStatePaused   = "paused"
	vmStateStopped  = "stopped"
)

func normalizeVMState(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

var _ providers.MachineWaker = (*provider)(nil)
