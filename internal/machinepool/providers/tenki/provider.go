package tenki

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout = 30 * time.Second
	baseImageDiskSizeGB = 20
	installationLabel   = "omnara-installation"
	machineLabel        = "omnara-machine"
	managedTag          = "omnara-managed"
	maxRuntimeEnvBytes  = 128_000
	runtimeEnvOverhead  = 16
)

var runtimeEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type provider struct {
	api             apiClient
	omnaraAPIURL    string
	createAttempted bool
}

func (*provider) ProvisioningTimeout() time.Duration { return provisioningTimeout }

func (*provider) PrepareProvisioning(
	_ context.Context,
	config executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	if _, err := providerOptionsFromProvisioning(config); err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	return executionstore.MachineResourceFacts{CPU: config.CPU, MemoryMB: config.MemoryMB}, nil
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	config executionstore.MachineProvisioningConfig,
	token string,
	machineEnv map[string]string,
	firstAttempt bool,
) (providers.ProvisionMachineResult, error) {
	var result providers.ProvisionMachineResult
	options, err := providerOptionsFromProvisioning(config)
	if err != nil {
		return result, err
	}
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return result, err
	}
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		return result, err
	}
	env, err := p.runtimeEnv(options.StartupScript, token, machineEnv)
	if err != nil {
		return result, err
	}
	var current session
	if firstAttempt && !p.createAttempted {
		p.createAttempted = true
		current, err = p.api.Create(ctx, createRequest{
			OwnerID:       "self",
			OwnerType:     "SERVICE",
			Name:          name,
			Metadata:      metadata,
			Tags:          []string{managedTag},
			AllowOutbound: true,
			CPUCores:      *config.CPU,
			MemoryMB:      *config.MemoryMB,
			DiskSizeGB:    options.diskSizeGB(),
			RegistryRef:   options.Image,
			Sticky:        true,
			Runtime: bootRuntime{
				Env:           env,
				RunAt:         "TEMPLATE_RUNTIME_RUN_AT_BOOT",
				Start:         startCommand{Argv: providers.ManagedDaemonLauncherArgs()},
				RestartPolicy: "TEMPLATE_RESTART_POLICY_NEVER",
			},
		})
		if err != nil {
			p.createAttempted = !isTooManyRequests(err)
			if p.createAttempted && rejectedBeforeCreate(err) {
				return result, fmt.Errorf("create tenki session: %w: %w", err, providers.ErrPermanent)
			}
			return result, fmt.Errorf("create tenki session: %w", err)
		}
	} else {
		var found bool
		current, found, err = p.findOwned(ctx, installationID, machineID)
		if err != nil {
			return result, err
		}
		if !found {
			return result, errors.New(
				"tenki create outcome is unknown; waiting to discover the existing session instead of creating a duplicate",
			)
		}
	}
	if err := validateOwnedSession(current, "", installationID, machineID); err != nil {
		return result, err
	}
	result.ProviderResourceID = current.ID
	if !current.Sticky {
		return result, errors.New("tenki session is not sticky")
	}
	return result, nil
}

func (p *provider) ValidateMachineConfig(
	config executionstore.MachineProvisioningConfig,
	machineEnv map[string]string,
) error {
	options, err := providerOptionsFromProvisioning(config)
	if err != nil {
		return err
	}
	env, err := p.runtimeEnv(options.StartupScript, "", machineEnv)
	if err != nil {
		return err
	}
	size := 0
	for key, value := range env {
		size += len(key) + len(value) + runtimeEnvOverhead
	}
	if size > maxRuntimeEnvBytes {
		return fmt.Errorf("tenki env and startup_script must total at most about %d bytes", maxRuntimeEnvBytes)
	}
	return nil
}

func (p *provider) runtimeEnv(startupScript, token string, machineEnv map[string]string) (map[string]string, error) {
	env, err := providers.BuildManagedMachineEnv(p.omnaraAPIURL, token, startupScript, machineEnv)
	if err != nil {
		return nil, err
	}
	maps.DeleteFunc(env, func(name, _ string) bool { return !runtimeEnvNamePattern.MatchString(name) })
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	return env, nil
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
	return map[string]string{installationLabel: installationOwner, machineLabel: machineOwner}, nil
}

func validateOwnedSession(current session, resourceID string, installationID, machineID uuid.UUID) error {
	switch {
	case current.ID == "":
		return errors.New("tenki session is missing its id")
	case resourceID != "" && current.ID != resourceID:
		return fmt.Errorf("tenki session %q was returned for resource id %q", current.ID, resourceID)
	case !ownedBy(current, installationID, machineID):
		return fmt.Errorf("tenki session %q does not have the expected ownership metadata", current.ID)
	}
	return nil
}

func ownedBy(current session, installationID, machineID uuid.UUID) bool {
	metadata, err := ownershipMetadata(installationID, machineID)
	return err == nil &&
		current.Metadata[installationLabel] == metadata[installationLabel] &&
		current.Metadata[machineLabel] == metadata[machineLabel]
}

func (p *provider) findOwned(
	ctx context.Context,
	installationID, machineID uuid.UUID,
) (session, bool, error) {
	if installationID == uuid.Nil || machineID == uuid.Nil {
		return session{}, false, errors.New("installation and machine ids are required")
	}
	sessions, err := p.api.List(ctx)
	if err != nil {
		return session{}, false, err
	}
	var found session
	for _, current := range sessions {
		if !ownedBy(current, installationID, machineID) || current.State == sessionStateTerminated {
			continue
		}
		if current.ID == "" {
			return session{}, false, errors.New("owned tenki session is missing its id")
		}
		if found.ID != "" {
			return session{}, false, errors.New("multiple tenki sessions have the same Omnara ownership")
		}
		found = current
	}
	return found, found.ID != "", nil
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	resourceID string,
) (string, bool, error) {
	var current session
	var found bool
	var err error
	if resourceID == "" {
		current, found, err = p.findOwned(ctx, installationID, machineID)
	} else {
		current, found, err = p.api.Get(ctx, resourceID)
	}
	if err != nil || !found {
		return "", false, err
	}
	if err := validateOwnedSession(current, resourceID, installationID, machineID); err != nil {
		return "", false, err
	}
	return current.ID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	resourceID string,
) error {
	if resourceID == "" {
		return errors.New("provider resource id is required")
	}
	current, found, err := p.api.Get(ctx, resourceID)
	if err != nil {
		return err
	}
	if found {
		if err := validateOwnedSession(current, resourceID, installationID, machineID); err != nil {
			return err
		}
		if current.State == sessionStateTerminated || current.State == sessionStateTerminating {
			return nil
		}
	}
	return p.api.Delete(ctx, resourceID)
}
