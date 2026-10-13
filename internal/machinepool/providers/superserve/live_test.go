package superserve

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

func TestSuperserveProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("SUPERSERVE_API_KEY"))
	if apiKey == "" {
		t.Skip("a Superserve API key is required")
	}
	templateName := strings.TrimSpace(os.Getenv("OMNARA_SUPERSERVE_TEST_TEMPLATE"))
	config := mustRawJSON(t, map[string]any{"allowed_templates": []string{"*"}})
	provisional := executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, templateName, "")}
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
		t.Fatalf("new live superserve provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Superserve provider has an unexpected implementation")
	}
	concreteProvider.api = liveTestAPI{apiClient: concreteProvider.api}
	facts, err := machineProvider.PrepareProvisioning(context.Background(), provisional)
	if err != nil {
		t.Fatalf("prepare live superserve provisioning: %v", err)
	}
	provisioning := provisional
	provisioning.CPU = facts.CPU
	provisioning.MemoryMB = facts.MemoryMB
	installationID := uuid.New()
	machineID := uuid.New()
	cleanupResourceID := ""
	t.Cleanup(func() {
		if cleanupResourceID == "" {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx, installationID, machineID, provisioning, cleanupResourceID,
		); err != nil {
			t.Errorf("delete live superserve sandbox: %v", err)
		}
	})
	provisionCtx, provisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	result, err := machineProvider.ProvisionMachine(
		provisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, true,
	)
	provisionCancel()
	cleanupResourceID = result.ProviderResourceID
	if err != nil {
		t.Fatalf("provision live superserve sandbox: %v", err)
	}
	reprovisionCtx, reprovisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	reprovisioned, err := machineProvider.ProvisionMachine(
		reprovisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, true,
	)
	reprovisionCancel()
	if err != nil {
		t.Fatalf("reprovision live superserve sandbox: %v", err)
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
		t.Fatalf("inspect live superserve sandbox = found %v error %v", found, err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("superserve provider does not implement runtime observation")
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
					"bulk live Superserve runtime observations = %d, want 1",
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
		t.Fatalf("observe missing live superserve sandbox: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, missingTarget, missingObservation, providers.RuntimeStateTerminated)
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), time.Minute)
	defer deleteCancel()
	if err := machineProvider.DeleteMachine(
		deleteCtx, uuid.New(), machineID, provisioning, result.ProviderResourceID,
	); err == nil {
		t.Fatal("delete live superserve sandbox with another installation succeeded")
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete live superserve sandbox: %v", err)
	}
	for {
		observation, err := observer.ObserveRuntimeState(deleteCtx, runtimeTarget)
		if err == nil && observation.State == providers.RuntimeStateTerminated {
			break
		}
		select {
		case <-deleteCtx.Done():
			t.Fatalf("deleted live superserve sandbox never terminated: %+v %v", observation, err)
		case <-time.After(time.Second):
		}
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete terminated live superserve sandbox again: %v", err)
	}
	cleanupResourceID = ""
}

type liveTestAPI struct {
	apiClient
}

// liveStandInDaemon replaces the managed daemon command in the launcher. The
// live test has no real machine token, so the daemon's bootstrap would exit as
// soon as Omnara rejects it, and whether that happens inside the launcher's
// liveness window is a race. A long-lived stand-in whose command line names
// omnarad keeps the launcher's detach, pid-file, and relaunch logic under test.
const liveStandInDaemon = "exec /bin/sh -c 'while :; do sleep 60; done' omnarad-live-stand-in"

func (a liveTestAPI) Exec(ctx context.Context, session execSession, request execRequest) (execResult, error) {
	launcher := daemonLauncherRequest(nil)
	if request.Command == launcher.Command && len(request.Args) == len(launcher.Args) &&
		request.Args[2] == launcher.Args[2] && request.Args[3] == launcher.Args[3] {
		args := slices.Clone(request.Args)
		args[3] = liveStandInDaemon
		request.Args = args
	}
	return a.apiClient.Exec(ctx, session, request)
}

func (a liveTestAPI) CreateSandbox(ctx context.Context, request createSandboxRequest) (sandbox, error) {
	if request.Metadata == nil {
		request.Metadata = map[string]string{}
	}
	request.Metadata[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return a.apiClient.CreateSandbox(ctx, request)
}
