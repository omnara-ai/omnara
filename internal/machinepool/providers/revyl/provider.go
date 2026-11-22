package revyl

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout    = 5 * time.Minute
	daemonInstallTimeoutMS = 60_000
	maxFileWriteBytes      = 64 * 1024
	uploadFilePrefix       = ".omnara-revyl-upload-"

	// Revyl computer states follow https://docs.revyl.ai/computers.
	computerStateStarting  computerState = "starting"
	computerStateActive    computerState = "active"
	computerStateClosing   computerState = "closing"
	computerStateStopped   computerState = "stopped"
	computerStateFailed    computerState = "failed"
	computerStateOwnerLost computerState = "owner_lost"

	releaseConfirmed = "confirmed"
)

var (
	computerNamespace    = uuid.MustParse("6f6d6e61-7261-5265-b679-6c636f6d7075")
	computerPollInterval = 2 * time.Second
)

//go:embed daemon_install.sh
var daemonInstallScript string

type computerState string

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
	return executionstore.MachineResourceFacts{}, nil
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
	id, err := machineComputerID(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	startScript, err := p.managedDaemonStartScript(options, machineToken, machineEnv)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	target, err := p.api.CreateComputer(ctx, createComputerRequest{
		ID:             id,
		OS:             computerOSMacOS,
		TimeoutSeconds: int(options.Lifetime / time.Second),
	})
	if err != nil {
		return providers.ProvisionMachineResult{}, classifyCreateError(err)
	}
	if err := validateOwnedComputer(target, id); err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: id.String()}
	if err := p.waitUntilActive(ctx, target); err != nil {
		return result, err
	}
	if err := p.ensureDaemon(ctx, id, startScript); err != nil {
		return result, err
	}
	return result, nil
}

func (p *provider) waitUntilActive(ctx context.Context, target computer) error {
	for {
		switch target.State {
		case computerStateActive:
			return nil
		case computerStateStarting:
		case computerStateClosing, computerStateStopped, computerStateFailed, computerStateOwnerLost:
			reason := "none"
			if target.FailureReason != nil {
				reason = *target.FailureReason
			}
			return fmt.Errorf(
				"revyl computer %s is %s (failure reason %s) and cannot be restarted: %w",
				target.ID,
				target.State,
				reason,
				providers.ErrPermanent,
			)
		default:
			return fmt.Errorf("revyl computer %s has unsupported state %q", target.ID, target.State)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for revyl computer %s to start: %w", target.ID, ctx.Err())
		case <-time.After(computerPollInterval):
		}
		refreshed, found, err := p.api.GetComputer(ctx, target.ID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("revyl computer %s disappeared while starting", target.ID)
		}
		if err := validateOwnedComputer(refreshed, target.ID); err != nil {
			return err
		}
		target = refreshed
	}
}

func (p *provider) managedDaemonStartScript(
	options providerOptions,
	machineToken string,
	machineEnv map[string]string,
) ([]byte, error) {
	env, err := providers.BuildManagedMachineEnv(
		p.omnaraAPIURL,
		machineToken,
		options.StartupScript,
		machineEnv,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
	}
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	for _, key := range slices.Sorted(maps.Keys(env)) {
		script.WriteString("export ")
		script.WriteString(key)
		script.WriteByte('=')
		script.WriteString(shellQuote(env[key]))
		script.WriteByte('\n')
	}
	script.WriteString("exec /bin/sh -c ")
	script.WriteString(shellQuote(providers.ManagedDaemonLauncherArgs()[2]))
	script.WriteByte('\n')
	return []byte(script.String()), nil
}

func (p *provider) ensureDaemon(ctx context.Context, id uuid.UUID, script []byte) error {
	upload := uploadFilePrefix + uuid.NewString()
	parts := slices.Collect(slices.Chunk(script, maxFileWriteBytes))
	for index, part := range parts {
		if err := p.api.WriteFile(ctx, id, fmt.Sprintf("%s-%03d", upload, index), part); err != nil {
			return err
		}
	}
	result, err := p.api.Exec(ctx, id, execRequest{
		Argv:      []string{"/bin/sh", "-c", daemonInstallScript, "sh", upload, strconv.Itoa(len(parts))},
		TimeoutMS: daemonInstallTimeoutMS,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"revyl daemon install command exited with status %d: %s",
			result.ExitCode,
			strings.TrimSpace(string(result.Stderr)),
		)
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
	id, err := ownedComputerID(installationID, machineID, providerResourceID)
	if err != nil {
		return "", false, err
	}
	target, found, err := p.api.GetComputer(ctx, id)
	if err != nil || !found {
		return "", false, err
	}
	if err := validateOwnedComputer(target, id); err != nil {
		return "", false, err
	}
	if computerReleased(target) {
		return "", false, nil
	}
	return id.String(), true, nil
}

func (p *provider) CheckConnection(ctx context.Context, _ executionstore.MachineProvisioningConfig) error {
	return p.api.ListComputers(ctx, 1)
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	if providerResourceID == "" {
		return errors.New("provider resource id is required")
	}
	id, err := ownedComputerID(installationID, machineID, providerResourceID)
	if err != nil {
		return err
	}
	_, _, err = p.api.StopComputer(ctx, id)
	return err
}

func machineComputerID(installationID, machineID uuid.UUID) (uuid.UUID, error) {
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.NewSHA1(computerNamespace, []byte(name)), nil
}

func ownedComputerID(installationID, machineID uuid.UUID, providerResourceID string) (uuid.UUID, error) {
	id, err := machineComputerID(installationID, machineID)
	if err != nil {
		return uuid.Nil, err
	}
	if providerResourceID != "" && providerResourceID != id.String() {
		return uuid.Nil, fmt.Errorf("revyl computer %q is not owned by this machine", providerResourceID)
	}
	return id, nil
}

func ownsComputer(target computer, id uuid.UUID) bool {
	return target.ID == id && target.OS == computerOSMacOS
}

func validateOwnedComputer(target computer, id uuid.UUID) error {
	if !ownsComputer(target, id) {
		return fmt.Errorf("revyl computer %s does not match the expected macOS computer", id)
	}
	return nil
}

func computerReleased(target computer) bool {
	switch target.State {
	case computerStateStopped, computerStateFailed, computerStateOwnerLost:
		return target.Release == releaseConfirmed
	case computerStateStarting, computerStateActive, computerStateClosing:
		return false
	default:
		return false
	}
}

func classifyCreateError(err error) error {
	apiErr, ok := apiErrorOf(err)
	if !ok {
		return fmt.Errorf("create revyl computer: %w", err)
	}
	switch {
	case apiErr.StatusCode == http.StatusConflict && apiErr.Code != "run_conflict",
		apiErr.StatusCode == http.StatusRequestTimeout,
		apiErr.StatusCode == http.StatusTooManyRequests,
		apiErr.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("create revyl computer: %w", err)
	case apiErr.StatusCode >= http.StatusBadRequest:
		return fmt.Errorf("create revyl computer: %w: %w", err, providers.ErrPermanent)
	default:
		return fmt.Errorf("create revyl computer: %w", err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
