package agent37

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestAgent37ProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("AGENT37_API_KEY"))
	templateName := strings.TrimSpace(os.Getenv("OMNARA_AGENT37_TEST_TEMPLATE"))
	if apiKey == "" {
		t.Skip("AGENT37_API_KEY is required")
	}
	config := mustRawJSON(t, map[string]any{"allowed_templates": []string{"*"}})
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
		t.Fatalf("new live agent37 provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Agent37 provider has an unexpected implementation")
	}
	concreteProvider.api = liveTestAPI{apiClient: concreteProvider.api}
	connectionCtx, connectionCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer connectionCancel()
	if err := machineProvider.CheckConnection(connectionCtx, executionstore.MachineProvisioningConfig{}); err != nil {
		t.Fatalf("check live agent37 connection: %v", err)
	}
	provisioning := testMachineProvisioning(t, templateName, 2, 4096)
	facts, err := machineProvider.PrepareProvisioning(context.Background(), provisioning)
	if err != nil {
		t.Fatalf("prepare live agent37 provisioning: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != 2 || facts.MemoryMB == nil || *facts.MemoryMB != 4096 {
		t.Fatalf("live agent37 resource facts = %+v, want the configured size", facts)
	}
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
				t.Errorf("find live agent37 instance for cleanup: %v", err)
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
			t.Errorf("delete live agent37 instance: %v", err)
		}
	})
	provisionCtx, provisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	result, err := machineProvider.ProvisionMachine(
		provisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, true,
	)
	provisionCancel()
	cleanupResourceID = result.ProviderResourceID
	if err != nil {
		t.Fatalf("provision live agent37 instance: %v", err)
	}
	reprovisionCtx, reprovisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	reprovisioned, err := machineProvider.ProvisionMachine(
		reprovisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, false,
	)
	reprovisionCancel()
	if err != nil {
		t.Fatalf("reprovision live agent37 instance: %v", err)
	}
	if reprovisioned.ProviderResourceID != result.ProviderResourceID {
		t.Fatalf("reprovisioned resource id = %q, want %q", reprovisioned.ProviderResourceID, result.ProviderResourceID)
	}
	daemonCtx, daemonCancel := context.WithTimeout(context.Background(), time.Minute)
	defer daemonCancel()
	daemon, err := concreteProvider.api.Exec(
		daemonCtx,
		result.ProviderResourceID,
		`p=$(cat "${HOME:-/tmp}/.omnara-agent37/pid") && kill -0 "$p" && tr '\0' ' ' <"/proc/$p/cmdline"`,
	)
	if err != nil || daemon.ExitCode != 0 || !strings.Contains(daemon.Stdout, "omnarad-live-stand-in") {
		t.Fatalf("live agent37 daemon check = %+v error %v, want the stand-in daemon running", daemon, err)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	_, found, err := machineProvider.InspectMachine(
		inspectCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	)
	if err != nil || !found {
		t.Fatalf("inspect live agent37 instance = found %v error %v", found, err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("agent37 provider does not implement runtime observation")
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
					"bulk live Agent37 runtime observations = %d, want 1",
					len(observations),
				)
			}
			return observations[0], nil
		},
	)
	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = "zzzzzzzzzz"
	missingCtx, missingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer missingCancel()
	missingObservation, err := observer.ObserveRuntimeState(missingCtx, missingTarget)
	if err != nil {
		t.Fatalf("observe missing live agent37 instance: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, missingTarget, missingObservation, providers.RuntimeStateTerminated)
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer deleteCancel()
	if err := machineProvider.DeleteMachine(
		deleteCtx, uuid.New(), machineID, provisioning, result.ProviderResourceID,
	); err == nil {
		t.Fatal("delete live agent37 instance with another installation succeeded")
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete live agent37 instance: %v", err)
	}
	for {
		observation, err := observer.ObserveRuntimeState(deleteCtx, runtimeTarget)
		if err == nil && observation.State == providers.RuntimeStateTerminated {
			break
		}
		select {
		case <-deleteCtx.Done():
			t.Fatalf("deleted live agent37 instance never terminated: %+v %v", observation, err)
		case <-time.After(time.Second):
		}
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx, installationID, machineID, provisioning, result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete terminated live agent37 instance again: %v", err)
	}
}

type liveTestAPI struct {
	apiClient
}

const liveStandInDaemon = "exec /bin/sh -c 'while :; do sleep 60; done' omnarad-live-stand-in"

func (a liveTestAPI) Exec(ctx context.Context, id, command string) (execResult, error) {
	command = strings.Replace(
		command,
		shellQuote(providers.ManagedDaemonLauncherArgs()[2]),
		shellQuote(liveStandInDaemon),
		1,
	)
	return a.apiClient.Exec(ctx, id, command)
}

func (a liveTestAPI) CreateInstance(ctx context.Context, request createInstanceRequest) (instance, error) {
	request.Metadata[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return a.apiClient.CreateInstance(ctx, request)
}
