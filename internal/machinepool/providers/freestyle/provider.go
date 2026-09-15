package freestyle

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout       = 2 * time.Minute
	providerMarkerKey         = "omnara-provider"
	providerMarkerValue       = "freestyle"
	installationMarkerKey     = "omnara-installation"
	machineMarkerKey          = "omnara-machine"
	daemonServiceName         = "omnara-daemon.service"
	daemonStartScriptPath     = "/etc/omnara/managed-daemon.sh"
	daemonServiceUnitPath     = "/etc/systemd/system/" + daemonServiceName
	daemonInstallTimeoutMS    = 30_000
	autoDeleteNever           = -1
	freestyleRuntimePollDelay = 250 * time.Millisecond
)

type provider struct {
	api          apiClient
	omnaraAPIURL string
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
	target, found, err := p.api.GetVM(ctx, name)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if !found {
		target, err = p.api.CreateVM(ctx, createVMRequest{
			SnapshotID:         options.Snapshot,
			Slug:               name,
			DisplayName:        name,
			IdleTimeoutSeconds: options.IdleTimeoutSeconds,
			AutoDeleteSeconds:  autoDeleteNever,
			AutomaticRestart:   true,
			Metadata:           ownershipMetadata(installationID, machineID),
			Firewall: firewallSpec{Rules: []firewallRule{{
				Action:      "allow",
				Source:      firewallEndpoint{},
				Destination: firewallEndpoint{Public: true},
			}}},
		})
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

	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if err := validateOwnedVM(target, installationID, machineID, name); err != nil {
		return result, err
	}
	if !vmUsesSnapshot(target, options.Snapshot) {
		return result, fmt.Errorf(
			"freestyle VM %q was not created from configured snapshot %q",
			target.ID,
			options.Snapshot,
		)
	}

	target, err = p.ensureRunning(ctx, target, installationID, machineID, name)
	if err != nil {
		return result, err
	}
	target, err = p.ensureResources(ctx, target, machineProvisioning, installationID, machineID, name)
	if err != nil {
		return result, err
	}
	if err := p.ensureDaemon(ctx, target.ID, options.StartupScript, machineToken, machineEnv); err != nil {
		return result, err
	}
	return result, nil
}

func (p *provider) ensureRunning(
	ctx context.Context,
	target vm,
	installationID, machineID uuid.UUID,
	expectedName string,
) (vm, error) {
	startRequested := false
	for {
		if err := validateOwnedVM(target, installationID, machineID, expectedName); err != nil {
			return vm{}, err
		}
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
		case <-time.After(freestyleRuntimePollDelay):
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
	installationID, machineID uuid.UUID,
	expectedName string,
) (vm, error) {
	wantCPU := *machineProvisioning.CPU
	wantMemoryMB := *machineProvisioning.MemoryMB
	if target.Resources.CPU > wantCPU || target.Resources.MemoryMB > wantMemoryMB {
		return vm{}, fmt.Errorf(
			"freestyle snapshot resources cpu=%d memory_mb=%d exceed configured cpu=%d memory_mb=%d",
			target.Resources.CPU,
			target.Resources.MemoryMB,
			wantCPU,
			wantMemoryMB,
		)
	}
	if target.Resources.CPU == wantCPU && target.Resources.MemoryMB == wantMemoryMB {
		return target, nil
	}
	resize := resizeVMRequest{}
	if target.Resources.CPU < wantCPU {
		resize.CPU = wantCPU
	}
	if target.Resources.MemoryMB < wantMemoryMB {
		resize.MemoryMB = wantMemoryMB
	}
	resized, err := p.api.ResizeVM(ctx, target.ID, resize)
	if err != nil {
		return vm{}, err
	}
	if err := validateOwnedVM(resized, installationID, machineID, expectedName); err != nil {
		return vm{}, err
	}
	if resized.Resources.CPU != wantCPU || resized.Resources.MemoryMB != wantMemoryMB {
		return vm{}, fmt.Errorf(
			"freestyle VM %q resize returned cpu=%d memory_mb=%d, want cpu=%d memory_mb=%d",
			resized.ID,
			resized.Resources.CPU,
			resized.Resources.MemoryMB,
			wantCPU,
			wantMemoryMB,
		)
	}
	return resized, nil
}

func (p *provider) ensureDaemon(
	ctx context.Context,
	resourceID, startupScript, machineToken string,
	machineEnv map[string]string,
) error {
	env, err := providers.BuildManagedMachineEnv(
		p.omnaraAPIURL,
		machineToken,
		startupScript,
		machineEnv,
	)
	if err != nil {
		return err
	}
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	response, err := p.api.ExecVM(ctx, resourceID, execVMRequest{
		Command:   managedDaemonInstallCommand(env),
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
	if input.ProviderResourceID == "" {
		return errors.New("provider resource id is required")
	}
	target, found, err := p.api.GetVM(ctx, input.ProviderResourceID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("freestyle VM %q was not found", input.ProviderResourceID)
	}
	if target.Metadata[providerMarkerKey] != providerMarkerValue ||
		!strings.HasPrefix(target.Slug, "omnara-mch-") {
		return fmt.Errorf("freestyle VM %q does not have Omnara ownership metadata", target.ID)
	}
	switch normalizeVMState(target.State) {
	case vmStateRunning, vmStateStarting:
		return nil
	case vmStatePaused, vmStateStopped:
		_, err := p.api.StartVM(ctx, target.ID)
		return err
	default:
		return fmt.Errorf("freestyle VM %q cannot be woken from state %q", target.ID, target.State)
	}
}

func ownershipMetadata(installationID, machineID uuid.UUID) map[string]string {
	return map[string]string{
		providerMarkerKey:     providerMarkerValue,
		installationMarkerKey: installationID.String(),
		machineMarkerKey:      machineID.String(),
	}
}

func validateOwnedVM(
	target vm,
	installationID, machineID uuid.UUID,
	expectedName string,
) error {
	if target.ID == "" {
		return errors.New("freestyle VM is missing its id")
	}
	if target.Slug != expectedName ||
		target.Metadata[providerMarkerKey] != providerMarkerValue ||
		target.Metadata[installationMarkerKey] != installationID.String() ||
		target.Metadata[machineMarkerKey] != machineID.String() {
		return fmt.Errorf("freestyle VM %q does not have the expected ownership metadata", target.ID)
	}
	return nil
}

func vmUsesSnapshot(target vm, snapshot string) bool {
	return target.SnapshotID == snapshot || target.SourceSnapshotSlugAtCreate == snapshot
}

func managedDaemonInstallCommand(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var startScript strings.Builder
	startScript.WriteString("#!/bin/sh\nexec env")
	for _, key := range keys {
		startScript.WriteByte(' ')
		startScript.WriteString(shellQuote(key + "=" + env[key]))
	}
	launcher := providers.ManagedDaemonLauncherArgs()
	startScript.WriteString(" /bin/sh -c ")
	startScript.WriteString(shellQuote(launcher[2]))
	startScript.WriteByte('\n')

	unit := `[Unit]
Description=Omnara machine daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + daemonStartScriptPath + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`
	startPayload := base64.StdEncoding.EncodeToString([]byte(startScript.String()))
	unitPayload := base64.StdEncoding.EncodeToString([]byte(unit))
	return "set -eu;" +
		"if systemctl is-active --quiet " + daemonServiceName + ";then exit 0;fi;" +
		"d=$(mktemp -d);trap 'rm -rf \"$d\"' EXIT HUP INT TERM;" +
		"printf '%s' '" + startPayload + "'|base64 -d >\"$d/start\";" +
		"printf '%s' '" + unitPayload + "'|base64 -d >\"$d/unit\";" +
		"install -d -m 700 /etc/omnara;" +
		"install -m 700 \"$d/start\" " + daemonStartScriptPath + ";" +
		"install -m 644 \"$d/unit\" " + daemonServiceUnitPath + ";" +
		"systemctl daemon-reload;" +
		"systemctl enable " + daemonServiceName + ";" +
		"systemctl reset-failed " + daemonServiceName + " 2>/dev/null||:;" +
		"systemctl start " + daemonServiceName + ";" +
		"systemctl is-active --quiet " + daemonServiceName
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
