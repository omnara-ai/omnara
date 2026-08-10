package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	Blaxel   = "blaxel"
	Daytona  = "daytona"
	Unikraft = "unikraft"
)

type RuntimeConfig struct {
	PublicURL         string
	ProviderAuthToken string
}

type ProvisionMachineResult struct {
	ProviderResourceID string
	SandboxURL         string
}

type WakeMachineInput struct {
	ProviderResourceID string
	SandboxURL         string
}

type MachineWaker interface {
	WakeMachine(context.Context, WakeMachineInput) error
}

type Provider interface {
	ProvisioningTimeout() time.Duration
	// PrepareProvisioning must be retry-safe and must not mutate external resources.
	PrepareProvisioning(
		context.Context,
		executionstore.MachineProvisioningConfig,
	) (executionstore.MachineResourceFacts, error)
	// ProvisionMachine must be idempotent by installation and machine identity.
	// Calling it is the external side-effect boundary and must be recorded durably first.
	// Retries for the same machine must converge on one canonical live provider
	// resource, usually by using a deterministic allocation name. A provider must
	// not create more than one live sandbox for one Omnara machine. The result must
	// include any trusted resource id observed before a later readiness error.
	// machineEnv is the machine's resolved environment and is applied to the
	// provider resource at creation; retries that adopt an existing resource
	// keep the environment it was created with.
	ProvisionMachine(
		ctx context.Context,
		installationID, machineID storage.ID,
		machineProvisioning executionstore.MachineProvisioningConfig,
		machineToken string,
		machineEnv map[string]string,
	) (ProvisionMachineResult, error)
	InspectMachine(
		ctx context.Context,
		installationID, machineID storage.ID,
		machineProvisioning executionstore.MachineProvisioningConfig,
		providerResourceID string,
	) (string, bool, error)
	DeleteMachine(
		ctx context.Context,
		installationID, machineID storage.ID,
		machineProvisioning executionstore.MachineProvisioningConfig,
		providerResourceID string,
	) error
}

type Definition interface {
	NewProvider(json.RawMessage, RuntimeConfig) (Provider, error)
	ResolveMachineProviderOptions(
		defaultOptions map[string]json.RawMessage,
		projectOptions map[string]json.RawMessage,
		agentOptions map[string]json.RawMessage,
	) map[string]json.RawMessage
	ValidatePool(
		executionstore.MachinePoolProviderPolicy,
	) error
	ValidateMachineProvisioning(
		executionstore.MachinePoolProviderPolicy,
		executionstore.MachineProvisioningConfig,
	) error
	BuildMachineProvisioningIntent(
		executionstore.MachinePoolProviderPolicy,
		executionstore.MachineProvisioningConfig,
	) (executionstore.MachineProvisioningConfig, error)
}

func MergeOptions(
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	var merged map[string]json.RawMessage
	for _, overlay := range []map[string]json.RawMessage{
		defaultOptions,
		projectOptions,
		agentOptions,
	} {
		if overlay != nil && merged == nil {
			merged = map[string]json.RawMessage{}
		}
		for key, value := range overlay {
			merged[key] = append(json.RawMessage(nil), value...)
		}
	}
	return merged
}

func MachineAllocationName(installationID, machineID storage.ID) (string, error) {
	if installationID == storage.NilID || machineID == storage.NilID {
		return "", errors.New("installation and machine ids are required")
	}
	allocationID := uuid.NewSHA1(installationID, machineID[:])
	publicMachineID, err := publicid.Encode(publicid.KindMachine, allocationID)
	if err != nil {
		return "", err
	}
	suffix := strings.TrimPrefix(publicMachineID, "mch_")
	if suffix == publicMachineID {
		return "", fmt.Errorf("public machine id %q does not have expected mch_ prefix", publicMachineID)
	}
	return "omnara-mch-" + suffix, nil
}
