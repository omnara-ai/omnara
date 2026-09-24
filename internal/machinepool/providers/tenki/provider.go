package tenki

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout = 3 * time.Minute
	installationLabel   = "omnara-installation"
	machineLabel        = "omnara-machine"
	managedTag          = "omnara-managed"
)

type sandbox struct {
	ID         string
	State      string
	Metadata   map[string]string
	CPU        int
	MemoryMB   int
	DiskSizeGB int32
	Sticky     bool
}

type createRequest struct {
	Name     string
	Metadata map[string]string
	CPU      int
	MemoryMB int
	Options  providerOptions
}

type apiClient interface {
	Create(context.Context, createRequest) (sandbox, error)
	List(context.Context) ([]sandbox, error)
	Get(context.Context, string) (sandbox, bool, error)
	WaitReady(context.Context, string) (sandbox, error)
	Bootstrap(context.Context, string, map[string]string) error
	Delete(context.Context, string) error
}

type provider struct {
	api                apiClient
	omnaraAPIURL       string
	creationMu         sync.Mutex
	creationAuthorized bool
	creationConsumed   bool
	installationID     uuid.UUID
	machineID          uuid.UUID
}

var _ providers.CreationGuardedProvider = (*provider)(nil)

func (p *provider) AuthorizeCreation(installationID, machineID uuid.UUID) {
	p.creationMu.Lock()
	defer p.creationMu.Unlock()
	if !p.creationAuthorized && !p.creationConsumed {
		p.creationAuthorized = true
		p.installationID, p.machineID = installationID, machineID
	}
}

func (p *provider) consumeCreation(installationID, machineID uuid.UUID) bool {
	p.creationMu.Lock()
	defer p.creationMu.Unlock()
	if !p.creationAuthorized || p.creationConsumed || p.installationID != installationID ||
		p.machineID != machineID {
		return false
	}
	p.creationConsumed = true
	return true
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
	env, err := providers.BuildManagedMachineEnv(p.omnaraAPIURL, token, options.StartupScript, machineEnv)
	if err != nil {
		return result, err
	}
	current, found, err := p.findOwned(ctx, installationID, machineID)
	if err != nil {
		return result, err
	}
	if !found {
		if !p.consumeCreation(installationID, machineID) {
			return result, errors.New(
				"tenki create outcome is unknown; waiting to discover the existing session instead of creating a duplicate",
			)
		}
		current, err = p.api.Create(
			ctx,
			createRequest{
				Name:     name,
				Metadata: map[string]string{installationLabel: installationID.String(), machineLabel: machineID.String()},
				CPU:      *config.CPU,
				MemoryMB: *config.MemoryMB,
				Options:  options,
			},
		)
		if current.ID != "" && ownedBy(current, installationID, machineID) {
			result.ProviderResourceID = current.ID
		}
		if err != nil {
			return result, fmt.Errorf("create tenki session: %w", err)
		}
	}
	if !ownedBy(current, installationID, machineID) || current.ID == "" {
		return result, errors.New("tenki session has unexpected ownership or no resource id")
	}
	result.ProviderResourceID = current.ID
	if current.State != "RUNNING" {
		current, err = p.api.WaitReady(ctx, current.ID)
		if err != nil {
			return result, fmt.Errorf("wait for tenki session: %w", err)
		}
	}
	if current.ID != result.ProviderResourceID || !ownedBy(current, installationID, machineID) {
		return result, errors.New("tenki session identity changed while waiting for readiness")
	}
	if current.State != "RUNNING" || !current.Sticky || current.CPU != *config.CPU ||
		current.MemoryMB != *config.MemoryMB ||
		current.DiskSizeGB != options.DiskSizeGB {
		return result, errors.New("tenki session does not match the requested state, resources, or sticky lifetime")
	}
	if err := p.api.Bootstrap(ctx, current.ID, env); err != nil {
		return result, fmt.Errorf("bootstrap tenki daemon: %w", err)
	}
	return result, nil
}

func ownedBy(current sandbox, installationID, machineID uuid.UUID) bool {
	return installationID != uuid.Nil && machineID != uuid.Nil &&
		current.Metadata[installationLabel] == installationID.String() &&
		current.Metadata[machineLabel] == machineID.String()
}

func (p *provider) findOwned(
	ctx context.Context,
	installationID, machineID uuid.UUID,
) (sandbox, bool, error) {
	if installationID == uuid.Nil || machineID == uuid.Nil {
		return sandbox{}, false, errors.New("installation and machine ids are required")
	}
	sessions, err := p.api.List(ctx)
	if err != nil {
		return sandbox{}, false, err
	}
	var found sandbox
	for _, current := range sessions {
		if !ownedBy(current, installationID, machineID) || current.State == "TERMINATED" {
			continue
		}
		if current.ID == "" {
			return sandbox{}, false, errors.New("owned tenki session is missing its id")
		}
		if found.ID != "" {
			return sandbox{}, false, errors.New("multiple tenki sessions have the same Omnara ownership")
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
	var current sandbox
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
	if !ownedBy(current, installationID, machineID) || current.ID == "" ||
		(resourceID != "" && current.ID != resourceID) {
		return "", false, errors.New("tenki session has unexpected ownership or identity")
	}
	return current.ID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	config executionstore.MachineProvisioningConfig,
	resourceID string,
) error {
	if resourceID == "" {
		return errors.New("tenki deletion requires an observed resource id")
	}
	id, found, err := p.InspectMachine(ctx, installationID, machineID, config, resourceID)
	if err != nil || !found {
		return err
	}
	return p.api.Delete(ctx, id)
}
