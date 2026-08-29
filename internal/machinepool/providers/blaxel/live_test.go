package blaxel

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestBlaxelProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("BL_API_KEY"))
	workspace := strings.TrimSpace(os.Getenv("BL_WORKSPACE"))
	if token == "" || workspace == "" {
		t.Skip("Blaxel API token and workspace are required")
	}
	image := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_TEST_IMAGE"))
	if image == "" {
		image = "blaxel/base-image:latest"
	}
	region := strings.TrimSpace(os.Getenv("OMNARA_BLAXEL_TEST_REGION"))
	if region == "" {
		region = "us-pdx-1"
	}
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	machineProvisioning := testMachineProvisioning(t, map[string]any{
		"image": image, "region": region,
	})
	machineProvider, err := (Definition{}).NewProvider(
		mustRawJSON(t, map[string]any{
			"workspace":       workspace,
			"allowed_images":  []string{image},
			"allowed_regions": []string{region},
		}),
		providers.RuntimeConfig{Omnara: providers.OmnaraURLs{APIURL: omnaraPublicURL + "/api/v1", InstallerURL: omnaraPublicURL + "/install/omnarad.sh"}, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new blaxel provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Blaxel provider has an unexpected implementation")
	}
	providerAPI := concreteProvider.apiClient()
	lister, ok := providerAPI.(sandboxLister)
	if !ok {
		t.Fatal("blaxel API client does not implement sandbox listing")
	}
	concreteProvider.api = liveTestAPI{apiClient: providerAPI, sandboxLister: lister}
	machineID := uuid.New()
	providerResourceID, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("build live blaxel sandbox name: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx,
			testInstallationID(),
			machineID,
			machineProvisioning,
			providerResourceID,
		); err != nil {
			t.Errorf("delete live blaxel sandbox: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bootstrap := "live-smoke-token"
	firstResourceID, err := machineProvider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		bootstrap,
		nil,
	)
	if err != nil {
		t.Fatalf("provision live blaxel sandbox: %v", err)
	}
	if firstResourceID.ProviderResourceID != providerResourceID {
		t.Fatalf("provider resource id = %q, want %q", firstResourceID.ProviderResourceID, providerResourceID)
	}
	created, found, err := concreteProvider.apiClient().GetSandbox(ctx, providerResourceID)
	if err != nil || !found {
		t.Fatalf("get marked live Blaxel sandbox = found %v error %v", found, err)
	}
	if created.Metadata.Labels[providercontract.LiveResourceLabel] != providercontract.LiveResourceValue {
		t.Fatalf("live Blaxel sandbox is missing its test marker: %+v", created.Metadata.Labels)
	}
	secondResourceID, err := machineProvider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		bootstrap,
		nil,
	)
	if err != nil {
		t.Fatalf("reprovision live blaxel sandbox: %v", err)
	}
	if secondResourceID.ProviderResourceID != providerResourceID {
		t.Fatalf(
			"reprovisioned resource id = %q, want %q",
			secondResourceID.ProviderResourceID,
			providerResourceID,
		)
	}
	inspectedResourceID, found, err := machineProvider.InspectMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID,
	)
	if err != nil {
		t.Fatalf("inspect live blaxel sandbox: %v", err)
	}
	if !found || inspectedResourceID != providerResourceID {
		t.Fatalf("inspect live blaxel sandbox = (%q, %t), want (%q, true)", inspectedResourceID, found, providerResourceID)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("blaxel provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      testInstallationID(),
		MachineID:           machineID,
		ProviderResourceID:  providerResourceID,
		MachineProvisioning: machineProvisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, runtimeTarget, func() (
		providers.RuntimeObservation,
		error,
	) {
		return observer.ObserveRuntimeState(ctx, runtimeTarget)
	})
	bulkTargets := liveBulkRuntimeTargets(t, runtimeTarget)
	validIndexes := make([]int, len(bulkTargets))
	for index := range validIndexes {
		validIndexes[index] = index
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, runtimeTarget, func() (
		providers.RuntimeObservation,
		error,
	) {
		listed, err := listTargetSandboxes(ctx, lister, bulkTargets, validIndexes)
		if err != nil {
			return providers.RuntimeObservation{}, err
		}
		matches := listed[runtimeTarget.ProviderResourceID]
		observation := runtimeTarget.UnknownObservation()
		if len(matches) == 1 && sandboxOwnedBy(
			matches[0],
			runtimeTarget.ProviderResourceID,
			runtimeTarget.InstallationID,
			runtimeTarget.MachineID,
		) {
			observation.State = normalizedSandboxRuntimeState(matches[0])
		}
		return observation, nil
	})
	observations, err := observer.ObserveRuntimeStates(ctx, bulkTargets)
	if err != nil {
		t.Fatalf("bulk observe live Blaxel sandbox runtime: %v", err)
	}
	if len(observations) != len(bulkTargets) {
		t.Fatal(
			"Blaxel bulk runtime observation returned an unexpected result count",
		)
	}
	providercontract.AssertRuntimeObservation(
		t,
		runtimeTarget,
		observations[0],
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
	)
	for index := 1; index < len(observations); index++ {
		providercontract.AssertRuntimeObservation(
			t,
			bulkTargets[index],
			observations[index],
			providers.RuntimeStateUnknown,
		)
	}
	missingObservation, err := observer.ObserveRuntimeState(ctx, bulkTargets[1])
	if err != nil {
		t.Fatalf("exact observe missing live Blaxel sandbox: %v", err)
	}
	providercontract.AssertRuntimeObservation(
		t,
		bulkTargets[1],
		missingObservation,
		providers.RuntimeStateTerminated,
	)
	if err := machineProvider.DeleteMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID,
	); err != nil {
		t.Fatalf("delete live blaxel sandbox: %v", err)
	}
	deleted = true
}

type liveTestAPI struct {
	apiClient
	sandboxLister
}

func (a liveTestAPI) CreateSandbox(
	ctx context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	request.Metadata.Labels[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	request.Spec.Runtime.Envs = append(request.Spec.Runtime.Envs, sandboxEnv{
		Name:  providercontract.LiveResourceEnv,
		Value: providercontract.LiveResourceValue,
	})
	return a.apiClient.CreateSandbox(ctx, request)
}

func liveBulkRuntimeTargets(
	t *testing.T,
	existing providers.RuntimeTarget,
) []providers.RuntimeTarget {
	t.Helper()
	targets := make([]providers.RuntimeTarget, targetedRuntimeObservationLimit+1)
	targets[0] = existing
	for index := 1; index < len(targets); index++ {
		machineID := uuid.New()
		name, err := providers.MachineAllocationName(existing.InstallationID, machineID)
		if err != nil {
			t.Fatalf("build live Blaxel bulk target: %v", err)
		}
		targets[index] = providers.RuntimeTarget{
			InstallationID:     existing.InstallationID,
			MachineID:          machineID,
			ProviderResourceID: name,
		}
	}
	return targets
}
