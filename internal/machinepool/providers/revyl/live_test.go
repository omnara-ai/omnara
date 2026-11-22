package revyl

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestRevylProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("REVYL_API_KEY"))
	if token == "" {
		t.Skip("a Revyl API key is required")
	}
	config := json.RawMessage(`{}`)
	if baseURL := strings.TrimSpace(os.Getenv("OMNARA_REVYL_TEST_API_BASE_URL")); baseURL != "" {
		config = mustRawJSON(t, map[string]string{"api_base_url": baseURL})
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
		providers.RuntimeConfig{OmnaraAPIURL: omnaraPublicAPIURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new live revyl provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Revyl provider has an unexpected implementation")
	}
	provisioning := executionstore.MachineProvisioningConfig{
		ProviderOptions: map[string]json.RawMessage{"lifetime": mustRawJSON(t, "15m")},
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	if err := machineProvider.CheckConnection(checkCtx, provisioning); err != nil {
		t.Fatalf("check live revyl connection: %v", err)
	}
	installationID := uuid.New()
	machineID := uuid.New()
	cleanupResourceID, err := machineComputerID(installationID, machineID)
	if err != nil {
		t.Fatalf("build live revyl computer id: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx,
			installationID,
			machineID,
			provisioning,
			cleanupResourceID.String(),
		); err != nil {
			t.Errorf("delete live revyl computer: %v", err)
		}
	})
	provisionCtx, provisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	result, err := machineProvider.ProvisionMachine(
		provisionCtx,
		installationID,
		machineID,
		provisioning,
		"live-smoke-token",
		map[string]string{"REVYL_LIVE_TEST": providercontract.LiveResourceValue},
		true,
	)
	provisionCancel()
	if err != nil {
		t.Fatalf("provision live revyl computer: %v", err)
	}
	if result.ProviderResourceID != cleanupResourceID.String() {
		t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, cleanupResourceID)
	}
	execCtx, execCancel := context.WithTimeout(context.Background(), time.Minute)
	launched, err := concreteProvider.api.Exec(execCtx, cleanupResourceID, execRequest{
		Argv: []string{
			"/bin/sh", "-c",
			`launchctl print "gui/$(id -u)/ai.omnara.daemon" >/dev/null && grep -q "export REVYL_LIVE_TEST=" "$HOME/.omnara-revyl/managed-daemon.sh"`,
		},
		TimeoutMS: 30_000,
	})
	execCancel()
	if err != nil || launched.ExitCode != 0 {
		t.Fatalf("live revyl daemon job = exit %d error %v", launched.ExitCode, err)
	}
	reprovisionCtx, reprovisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	reprovisioned, err := machineProvider.ProvisionMachine(
		reprovisionCtx,
		installationID,
		machineID,
		provisioning,
		"live-smoke-token",
		map[string]string{"REVYL_LIVE_TEST": providercontract.LiveResourceValue},
		false,
	)
	reprovisionCancel()
	if err != nil {
		t.Fatalf("reprovision live revyl computer: %v", err)
	}
	if reprovisioned.ProviderResourceID != result.ProviderResourceID {
		t.Fatalf("reprovisioned resource id = %q, want %q", reprovisioned.ProviderResourceID, result.ProviderResourceID)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	if _, found, err := machineProvider.InspectMachine(
		inspectCtx,
		installationID,
		machineID,
		provisioning,
		result.ProviderResourceID,
	); err != nil || !found {
		t.Fatalf("inspect live revyl computer = found %v error %v", found, err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("revyl provider does not implement runtime observation")
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
		t,
		observationCtx,
		runtimeTarget,
		func() (providers.RuntimeObservation, error) {
			return observer.ObserveRuntimeState(observationCtx, runtimeTarget)
		},
	)
	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingID, err := machineComputerID(installationID, missingTarget.MachineID)
	if err != nil {
		t.Fatalf("build missing revyl computer id: %v", err)
	}
	missingTarget.ProviderResourceID = missingID.String()
	missingObservation, err := observer.ObserveRuntimeState(observationCtx, missingTarget)
	if err != nil {
		t.Fatalf("observe missing live revyl computer: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, missingTarget, missingObservation, providers.RuntimeStateTerminated)
}
