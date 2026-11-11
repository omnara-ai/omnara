package maritime

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestMaritimeProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("MARITIME_API_KEY"))
	if apiKey == "" {
		t.Skip("a Maritime API key is required")
	}
	templateName := strings.TrimSpace(os.Getenv("OMNARA_MARITIME_TEST_TEMPLATE"))
	config := mustRawJSON(t, map[string]any{"allowed_templates": []string{"*"}})
	cpu := 2
	memoryMB := 4096
	provisional := executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: testOptions(t, templateName, ""),
	}
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	omnaraPublicAPIURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_API_URL"))
	if omnaraPublicAPIURL == "" {
		omnaraPublicAPIURL = omnaraPublicURL + "/api/v1"
	}
	machineProvider, err := (Definition{}).NewProvider(
		config,
		providers.RuntimeConfig{OmnaraAPIURL: omnaraPublicAPIURL, ProviderAuthToken: apiKey},
	)
	if err != nil {
		t.Fatalf("new live maritime provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Maritime provider has an unexpected implementation")
	}
	concreteProvider.api = liveTestAPI{apiClient: concreteProvider.api}
	facts, err := machineProvider.PrepareProvisioning(context.Background(), provisional)
	if err != nil {
		t.Fatalf("prepare live maritime provisioning: %v", err)
	}
	provisioning := provisional
	provisioning.CPU = facts.CPU
	provisioning.MemoryMB = facts.MemoryMB
	installationID := uuid.New()
	machineID := uuid.New()
	cleanupResourceID := ""
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		id := cleanupResourceID
		for attempt := 0; id == "" && attempt < 10; attempt++ {
			if attempt > 0 {
				select {
				case <-cleanupCtx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
			foundID, found, err := machineProvider.InspectMachine(cleanupCtx, installationID, machineID, provisioning, "")
			if err != nil {
				t.Errorf("find live maritime sandbox for cleanup: %v", err)
				return
			}
			if found {
				id = foundID
			}
		}
		if id == "" {
			return
		}
		if err := machineProvider.DeleteMachine(cleanupCtx, installationID, machineID, provisioning, id); err != nil {
			t.Errorf("delete live maritime sandbox: %v", err)
		}
	})
	provisionCtx, provisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	result, err := machineProvider.ProvisionMachine(
		provisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, true,
	)
	provisionCancel()
	cleanupResourceID = result.ProviderResourceID
	if err != nil {
		t.Fatalf("provision live maritime sandbox: %v", err)
	}
	reprovisionCtx, reprovisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	reprovisioned, err := machineProvider.ProvisionMachine(
		reprovisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, false,
	)
	reprovisionCancel()
	if err != nil {
		t.Fatalf("reprovision live maritime sandbox: %v", err)
	}
	if reprovisioned.ProviderResourceID != result.ProviderResourceID {
		t.Fatalf("reprovisioned resource id = %q, want %q", reprovisioned.ProviderResourceID, result.ProviderResourceID)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	_, found, err := machineProvider.InspectMachine(
		inspectCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	)
	if err != nil || !found {
		t.Fatalf("inspect live maritime sandbox = found %v error %v", found, err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("maritime provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  result.ProviderResourceID,
		MachineProvisioning: provisioning,
	}
	observationCtx, observationCancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer observationCancel()
	providercontract.WaitForPresentRuntimeObservation(
		t, observationCtx, runtimeTarget,
		func() (providers.RuntimeObservation, error) {
			return observer.ObserveRuntimeState(observationCtx, runtimeTarget)
		},
	)
	providercontract.WaitForPresentRuntimeObservation(
		t, observationCtx, runtimeTarget,
		func() (providers.RuntimeObservation, error) {
			observations, err := observer.ObserveRuntimeStates(observationCtx, []providers.RuntimeTarget{runtimeTarget})
			if err != nil {
				return providers.RuntimeObservation{}, err
			}
			if len(observations) != 1 {
				return providers.RuntimeObservation{}, fmt.Errorf(
					"bulk live Maritime runtime observations = %d, want 1",
					len(observations),
				)
			}
			return observations[0], nil
		},
	)
	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = uuid.NewString()
	missingCtx, missingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer missingCancel()
	missingObservation, err := observer.ObserveRuntimeState(missingCtx, missingTarget)
	if err != nil {
		t.Fatalf("observe missing live maritime sandbox: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, missingTarget, missingObservation, providers.RuntimeStateTerminated)
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), time.Minute)
	defer deleteCancel()
	if err := machineProvider.DeleteMachine(
		deleteCtx, uuid.New(), machineID, provisioning, result.ProviderResourceID,
	); err == nil {
		t.Fatal("delete live maritime sandbox with another installation succeeded")
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete live maritime sandbox: %v", err)
	}
	for {
		observation, err := observer.ObserveRuntimeState(deleteCtx, runtimeTarget)
		if err == nil && observation.State == providers.RuntimeStateTerminated {
			break
		}
		select {
		case <-deleteCtx.Done():
			t.Fatalf("deleted live maritime sandbox never terminated: %+v %v", observation, err)
		case <-time.After(time.Second):
		}
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete terminated live maritime sandbox again: %v", err)
	}
}

type liveTestAPI struct {
	apiClient
}

const liveStandInDaemon = "exec /bin/sh -c 'while :; do sleep 60; done' omnarad-live-stand-in"

func (a liveTestAPI) Run(ctx context.Context, target sandbox, request runRequest) (command, error) {
	launcher := daemonLauncherRequest(nil)
	if len(request.Cmd) == len(launcher.Cmd) && request.Cmd[2] == launcher.Cmd[2] && request.Cmd[4] == launcher.Cmd[4] {
		cmd := slices.Clone(request.Cmd)
		cmd[4] = liveStandInDaemon
		request.Cmd = cmd
	}
	return a.apiClient.Run(ctx, target, request)
}

func (a liveTestAPI) CreateSandbox(ctx context.Context, request createSandboxRequest) (sandbox, error) {
	if request.Metadata == nil {
		request.Metadata = map[string]string{}
	}
	request.Metadata[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return a.apiClient.CreateSandbox(ctx, request)
}
