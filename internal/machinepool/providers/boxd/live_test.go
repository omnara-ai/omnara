package boxd

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

const liveTestMarkerEnv = "LIVE_TEST_MARKER"

func TestBoxdProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("BOXD_API_KEY"))
	if token == "" {
		t.Skip("a boxd API key is required")
	}
	config := map[string]any{"allowed_snapshots": []string{"*"}}
	provisional := executionstore.MachineProvisioningConfig{
		ProviderOptions: testOptions(t, strings.TrimSpace(os.Getenv("OMNARA_BOXD_TEST_SNAPSHOT")), ""),
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
		mustRawJSON(t, config),
		providers.RuntimeConfig{OmnaraAPIURL: omnaraPublicAPIURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new live boxd provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("boxd provider has an unexpected implementation")
	}
	prepareCtx, prepareCancel := context.WithTimeout(context.Background(), 30*time.Second)
	facts, err := machineProvider.PrepareProvisioning(prepareCtx, provisional)
	prepareCancel()
	if err != nil {
		t.Fatalf("prepare live boxd provisioning: %v", err)
	}
	t.Logf("resolved machine size cpu=%d memory_mb=%d", *facts.CPU, *facts.MemoryMB)
	provisioning := provisional
	provisioning.CPU = facts.CPU
	provisioning.MemoryMB = facts.MemoryMB
	installationID := uuid.New()
	machineID := uuid.New()
	cleanupResourceID, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("build live boxd machine name: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx,
			installationID,
			machineID,
			provisioning,
			cleanupResourceID,
		); err != nil {
			t.Errorf("delete live boxd machine: %v", err)
		}
	})
	provisionCtx, provisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	resourceID, err := machineProvider.ProvisionMachine(
		provisionCtx,
		installationID,
		machineID,
		provisioning,
		"live-smoke-token",
		map[string]string{liveTestMarkerEnv: providercontract.LiveResourceValue},
		true,
	)
	provisionCancel()
	if err != nil {
		t.Fatalf("provision live boxd machine: %v", err)
	}
	cleanupResourceID = resourceID.ProviderResourceID
	t.Logf("provisioned boxd machine %s as %s", cleanupResourceID, resourceID.ProviderResourceID)
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 90*time.Second)
	probe, err := concreteProvider.api.Exec(
		probeCtx,
		resourceID.ProviderResourceID,
		`p=$(cat "$HOME/.omnara-boxd/pid") && test -n "$p" || { echo "bootstrap not launched" >&2; exit 1; }; `+
			`test ! -e "$HOME/.omnara-boxd/boot" || { echo "boot payload left on disk" >&2; exit 1; }; `+
			`i=0; while [ $i -lt 60 ]; do `+
			`grep -q 'OMNARA_MACHINE_TOKEN was rejected' "$HOME/.omnara-boxd/log" 2>/dev/null && `+
			`{ echo "bootstrap pid $p reached the API"; exit 0; }; `+
			`i=$((i+1)); sleep 1; done; `+
			`echo "bootstrap did not reach the API; log:" >&2; cat "$HOME/.omnara-boxd/log" >&2; exit 1`,
		nil,
	)
	probeCancel()
	if err != nil || probe.ExitCode != 0 {
		t.Fatalf("live boxd bootstrap probe = %+v error %v", probe, err)
	}
	reprovisionCtx, reprovisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	reprovisioned, err := machineProvider.ProvisionMachine(
		reprovisionCtx,
		installationID,
		machineID,
		provisioning,
		"live-smoke-token",
		nil,
		true,
	)
	reprovisionCancel()
	if err != nil {
		t.Fatalf("reprovision live boxd machine: %v", err)
	}
	if reprovisioned.ProviderResourceID != resourceID.ProviderResourceID {
		t.Fatalf(
			"reprovisioned resource id = %q, want %q",
			reprovisioned.ProviderResourceID,
			resourceID.ProviderResourceID,
		)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	_, found, err := machineProvider.InspectMachine(
		inspectCtx,
		installationID,
		machineID,
		provisioning,
		resourceID.ProviderResourceID,
	)
	if err != nil || !found {
		t.Fatalf("inspect live boxd machine = found %v error %v", found, err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("boxd provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  resourceID.ProviderResourceID,
		MachineProvisioning: provisioning,
	}
	observationCtx, observationCancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer observationCancel()
	providercontract.WaitForPresentRuntimeObservation(
		t,
		observationCtx,
		runtimeTarget,
		func() (providers.RuntimeObservation, error) {
			return observer.ObserveRuntimeState(observationCtx, runtimeTarget)
		},
	)
	providercontract.WaitForPresentRuntimeObservation(
		t,
		observationCtx,
		runtimeTarget,
		func() (providers.RuntimeObservation, error) {
			observations, err := observer.ObserveRuntimeStates(
				observationCtx,
				[]providers.RuntimeTarget{runtimeTarget},
			)
			if err != nil {
				return providers.RuntimeObservation{}, err
			}
			if len(observations) != 1 {
				return providers.RuntimeObservation{}, fmt.Errorf(
					"bulk live boxd runtime observations = %d, want 1",
					len(observations),
				)
			}
			return observations[0], nil
		},
	)
	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = "vm-" + uuid.NewString()
	missingCtx, missingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer missingCancel()
	missingObservation, err := observer.ObserveRuntimeState(missingCtx, missingTarget)
	if err != nil {
		t.Fatalf("exact observe missing live boxd machine: %v", err)
	}
	providercontract.AssertRuntimeObservation(
		t,
		missingTarget,
		missingObservation,
		providers.RuntimeStateTerminated,
	)
}
