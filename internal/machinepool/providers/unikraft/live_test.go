package unikraft

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestUnikraftProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("UNIKRAFT_API_KEY"))
	if apiKey == "" {
		t.Skip("UNIKRAFT_API_KEY is required")
	}
	image := strings.TrimSpace(os.Getenv("OMNARA_UNIKRAFT_TEST_IMAGE"))
	if image == "" {
		image = "nginx:latest"
	}
	metro := strings.TrimSpace(os.Getenv("OMNARA_UNIKRAFT_TEST_METRO"))
	if metro == "" {
		metro = "fra"
	}
	omnaraPublicURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_URL"))
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	omnaraPublicAPIURL := strings.TrimSpace(os.Getenv("OMNARA_PUBLIC_API_URL"))
	if omnaraPublicAPIURL == "" {
		omnaraPublicAPIURL = omnaraPublicURL + "/api/v1"
	}
	machineProvisioning := testMachineProvisioning(t, map[string]any{
		"provider_options": map[string]any{"image": image, "metro": metro},
	})
	machineProvider, err := (Definition{}).NewProvider(
		mustRawJSON(t, map[string]any{"allowed_images": []string{image}, "allowed_metros": []string{"*"}}),
		providers.RuntimeConfig{
			OmnaraAPIURL:      omnaraPublicAPIURL,
			ProviderAuthToken: apiKey,
		},
	)
	if err != nil {
		t.Fatalf("new unikraft provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("Unikraft provider has an unexpected implementation")
	}
	providerAPI := concreteProvider.apiForMetro(metro)
	restAPI, ok := providerAPI.(*restClient)
	if !ok {
		t.Fatal("Unikraft provider has an unexpected API client")
	}
	concreteProvider.api = liveTestAPI{
		apiClient: providerAPI,
	}
	machineID := uuid.New()
	cleanupResourceID := uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx,
			testInstallationID(),
			machineID,
			machineProvisioning,
			cleanupResourceID,
		); err != nil {
			t.Errorf("delete live unikraft instance: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	providerResourceID, err := machineProvider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		"live-smoke-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision live unikraft instance: %v", err)
	}
	if providerResourceID.ProviderResourceID == "" {
		t.Fatal("expected provider resource id")
	}
	cleanupResourceID = providerResourceID.ProviderResourceID
	instanceEnv, err := liveInstanceEnvironment(ctx, restAPI, cleanupResourceID)
	if err != nil {
		t.Fatalf("get marked live Unikraft instance: %v", err)
	}
	if marker := instanceEnv[providercontract.LiveResourceEnv]; marker != providercontract.LiveResourceValue {
		t.Fatalf("live Unikraft instance test marker = %q", marker)
	}
	_, found, err := machineProvider.InspectMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID.ProviderResourceID,
	)
	if err != nil {
		t.Fatalf("inspect live unikraft instance: %v", err)
	}
	if !found {
		t.Fatal("expected live instance to be found")
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("unikraft provider does not implement runtime observation")
	}
	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      testInstallationID(),
		MachineID:           machineID,
		ProviderResourceID:  providerResourceID.ProviderResourceID,
		MachineProvisioning: machineProvisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, runtimeTarget, func() (
		providers.RuntimeObservation,
		error,
	) {
		return observer.ObserveRuntimeState(ctx, runtimeTarget)
	})
	observations, err := observer.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{runtimeTarget})
	if err != nil {
		t.Fatalf("bulk observe live unikraft instance runtime: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("bulk live unikraft runtime observations = %d, want 1", len(observations))
	}
	providercontract.AssertRuntimeObservation(
		t,
		runtimeTarget,
		observations[0],
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
	)

	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = uuid.NewString()
	observations, err = observer.ObserveRuntimeStates(
		ctx,
		[]providers.RuntimeTarget{runtimeTarget, missingTarget},
	)
	if err != nil {
		t.Fatalf("observe mixed live Unikraft runtime batch: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("mixed live Unikraft observations = %d, want 2", len(observations))
	}
	providercontract.AssertRuntimeObservation(
		t,
		runtimeTarget,
		observations[0],
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
	)
	providercontract.AssertRuntimeObservation(
		t,
		missingTarget,
		observations[1],
		providers.RuntimeStateTerminated,
	)
	missingObservation, err := observer.ObserveRuntimeState(ctx, missingTarget)
	if err != nil {
		t.Fatalf("exact observe missing live Unikraft instance: %v", err)
	}
	providercontract.AssertRuntimeObservation(
		t,
		missingTarget,
		missingObservation,
		providers.RuntimeStateTerminated,
	)
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateInstance(
	ctx context.Context,
	request createInstanceRequest,
) (instance, error) {
	request.Env[providercontract.LiveResourceEnv] = providercontract.LiveResourceValue
	return a.apiClient.CreateInstance(ctx, request)
}

func liveInstanceEnvironment(
	ctx context.Context,
	client *restClient,
	instanceID string,
) (map[string]string, error) {
	var response struct {
		Instances []struct {
			UUID string            `json:"uuid"`
			Env  map[string]string `json:"env"`
		} `json:"instances"`
	}
	envelope, err := client.doRequest(
		ctx,
		http.MethodGet,
		"/v1/instances/"+url.PathEscape(instanceID)+"?details=true",
		nil,
		&response,
		true,
	)
	if err != nil {
		return nil, err
	}
	if !instanceBatchFromEnvelope(envelope, nil).cleanEnvelope() ||
		len(response.Instances) != 1 || response.Instances[0].UUID != instanceID {
		return nil, errors.New("unikraft test instance lookup returned an unexpected identity")
	}
	return response.Instances[0].Env, nil
}
