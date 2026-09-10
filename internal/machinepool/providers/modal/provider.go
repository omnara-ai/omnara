package modal

import (
	"context"
	"errors"
	"fmt"
	"time"

	modalsdk "github.com/modal-labs/modal-client/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout = 2 * time.Minute
	sandboxTimeout      = 24 * time.Hour
	machineTag          = "omnara-machine"
	installationTag     = "omnara-installation"
)

type provider struct {
	app          string
	environment  string
	credential   providerCredential
	omnaraAPIURL string
}

type sandbox struct {
	ID      string
	Tags    map[string]string
	Running bool
}

func (p *provider) newClient() (*modalsdk.Client, error) {
	return modalsdk.NewClientWithOptions(&modalsdk.ClientParams{
		TokenID:     p.credential.TokenID,
		TokenSecret: p.credential.TokenSecret,
		Environment: p.environment,
	})
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
	client, err := p.newClient()
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	defer client.Close()
	if existing, found, err := p.sandboxByName(ctx, client, name); err != nil {
		return providers.ProvisionMachineResult{}, err
	} else if found {
		return provisionResult(existing, installationID, machineID)
	}
	installationOwner, machineOwner, err := sandboxOwnershipTagValues(installationID, machineID)
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
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	app, err := client.Apps.FromName(ctx, p.app, &modalsdk.AppFromNameParams{
		Environment:     p.environment,
		CreateIfMissing: true,
	})
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	var regions []string
	if options.Region != "" {
		regions = []string{options.Region}
	}
	cpu := float64(*machineProvisioning.CPU) / 2
	tags := map[string]string{installationTag: installationOwner, machineTag: machineOwner}
	created, err := client.Sandboxes.Create(
		ctx,
		app,
		client.Images.FromRegistry(options.Image, nil),
		&modalsdk.SandboxCreateParams{
			CPU:            cpu,
			CPULimit:       cpu,
			MemoryMiB:      *machineProvisioning.MemoryMB,
			MemoryLimitMiB: *machineProvisioning.MemoryMB,
			Timeout:        sandboxTimeout,
			Command:        providers.ManagedDaemonLauncherArgs(),
			Env:            env,
			Regions:        regions,
			Name:           name,
			Tags:           tags,
		},
	)
	if err != nil {
		if existing, found, inspectErr := p.sandboxByName(ctx, client, name); inspectErr == nil && found {
			return provisionResult(existing, installationID, machineID)
		}
		return providers.ProvisionMachineResult{}, err
	}
	return provisionResult(sandbox{ID: created.SandboxID, Tags: tags, Running: true}, installationID, machineID)
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	client, err := p.newClient()
	if err != nil {
		return "", false, err
	}
	defer client.Close()
	return p.inspectMachine(ctx, client, installationID, machineID, providerResourceID)
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	if providerResourceID == "" {
		return errors.New("provider resource id is required")
	}
	client, err := p.newClient()
	if err != nil {
		return err
	}
	defer client.Close()
	resourceID, found, err := p.inspectMachine(ctx, client, installationID, machineID, providerResourceID)
	if err == nil && !found {
		resourceID, found, err = p.inspectMachine(ctx, client, installationID, machineID, "")
	}
	if err != nil || !found {
		return err
	}
	target, err := client.Sandboxes.FromID(ctx, resourceID, nil)
	if err != nil {
		return err
	}
	if _, err := target.Terminate(ctx, nil); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (p *provider) inspectMachine(
	ctx context.Context,
	client *modalsdk.Client,
	installationID storage.ID,
	machineID storage.ID,
	providerResourceID string,
) (string, bool, error) {
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	var target sandbox
	var found bool
	if providerResourceID != "" {
		target, found, err = sandboxByID(ctx, client, providerResourceID)
	} else {
		target, found, err = p.sandboxByName(ctx, client, expectedName)
	}
	if err != nil || !found {
		return "", false, err
	}
	if !target.Running {
		return "", false, nil
	}
	result, err := provisionResult(target, installationID, machineID)
	if err != nil {
		return "", false, err
	}
	return result.ProviderResourceID, true, nil
}

func (p *provider) sandboxByName(
	ctx context.Context,
	client *modalsdk.Client,
	name string,
) (sandbox, bool, error) {
	target, err := client.Sandboxes.FromName(
		ctx,
		p.app,
		name,
		&modalsdk.SandboxFromNameParams{Environment: p.environment},
	)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	return describeSandbox(ctx, target)
}

func sandboxByID(ctx context.Context, client *modalsdk.Client, id string) (sandbox, bool, error) {
	target, err := client.Sandboxes.FromID(ctx, id, nil)
	if err != nil {
		return sandbox{}, false, err
	}
	return describeSandbox(ctx, target)
}

func describeSandbox(ctx context.Context, target *modalsdk.Sandbox) (sandbox, bool, error) {
	tags, err := target.GetTags(ctx, nil)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	exitCode, err := target.Poll(ctx, nil)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	return sandbox{ID: target.SandboxID, Tags: tags, Running: exitCode == nil}, true, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var notFound modalsdk.NotFoundError
	return errors.As(err, &notFound) || status.Code(err) == codes.NotFound
}

func provisionResult(target sandbox, installationID, machineID storage.ID) (providers.ProvisionMachineResult, error) {
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if target.ID == "" {
		return result, errors.New("modal sandbox is missing its id")
	}
	if !sandboxOwnedBy(target, installationID, machineID) {
		return providers.ProvisionMachineResult{}, fmt.Errorf(
			"modal sandbox %q does not have the expected ownership tags", target.ID,
		)
	}
	if !target.Running {
		return providers.ProvisionMachineResult{}, fmt.Errorf(
			"modal sandbox %q has terminated; provisioning must be retried: %w",
			target.ID,
			providers.ErrResourceReplaced,
		)
	}
	return result, nil
}

func sandboxOwnedBy(target sandbox, installationID, machineID storage.ID) bool {
	installationOwner, machineOwner, err := sandboxOwnershipTagValues(installationID, machineID)
	if err != nil {
		return false
	}
	return target.Tags[installationTag] == installationOwner && target.Tags[machineTag] == machineOwner
}

func sandboxOwnershipTagValues(installationID, machineID storage.ID) (string, string, error) {
	installationOwner, err := publicid.Encode(publicid.KindInstallation, installationID)
	if err != nil {
		return "", "", err
	}
	machineOwner, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		return "", "", err
	}
	return installationOwner, machineOwner, nil
}
