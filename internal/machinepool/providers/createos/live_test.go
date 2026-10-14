package createos

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

func TestCreateOSProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("CREATEOS_API_KEY"))
	if token == "" {
		t.Skip("a CreateOS API key is required")
	}
	shape := strings.TrimSpace(os.Getenv("OMNARA_CREATEOS_TEST_SHAPE"))
	if shape == "" {
		shape = "s-1vcpu-1gb"
	}
	rootfs := strings.TrimSpace(os.Getenv("OMNARA_CREATEOS_TEST_ROOTFS"))
	if rootfs == "" {
		rootfs = "devbox:1"
	}
	config := mustRawJSON(t, map[string]any{
		"allowed_shapes":   []string{"*"},
		"allowed_rootfses": []string{"*"},
	})
	provisional := executionstore.MachineProvisioningConfig{
		ProviderOptions: testOptions(t, shape, rootfs, ""),
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
		providers.RuntimeConfig{
			OmnaraAPIURL:      omnaraPublicAPIURL,
			ProviderAuthToken: token,
		},
	)
	if err != nil {
		t.Fatalf("new live createos provider: %v", err)
	}
	concreteProvider, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("CreateOS provider has an unexpected implementation")
	}
	concreteProvider.api = liveTestAPI{apiClient: concreteProvider.api}
	facts, err := machineProvider.PrepareProvisioning(context.Background(), provisional)
	if err != nil {
		t.Fatalf("prepare live createos provisioning: %v", err)
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
			cleanupCtx,
			installationID,
			machineID,
			provisioning,
			cleanupResourceID,
		); err != nil {
			t.Errorf("delete live createos sandbox: %v", err)
		}
	})
	provisionCtx, provisionCancel := context.WithTimeout(context.Background(), provisioningTimeout)
	result, err := machineProvider.ProvisionMachine(
		provisionCtx,
		installationID,
		machineID,
		provisioning,
		"live-smoke-token",
		nil,
		true,
	)
	provisionCancel()
	cleanupResourceID = result.ProviderResourceID
	if err != nil {
		t.Fatalf("provision live createos sandbox: %v", err)
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
		t.Fatalf("reprovision live createos sandbox: %v", err)
	}
	if reprovisioned.ProviderResourceID != result.ProviderResourceID {
		t.Fatalf(
			"reprovisioned resource id = %q, want %q",
			reprovisioned.ProviderResourceID,
			result.ProviderResourceID,
		)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	_, found, err := machineProvider.InspectMachine(
		inspectCtx,
		installationID,
		machineID,
		provisioning,
		result.ProviderResourceID,
	)
	if err != nil || !found {
		t.Fatalf("inspect live createos sandbox = found %v error %v", found, err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("createos provider does not implement runtime observation")
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
					"bulk live CreateOS runtime observations = %d, want 1",
					len(observations),
				)
			}
			return observations[0], nil
		},
	)
	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = "sb-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	missingCtx, missingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer missingCancel()
	missingObservation, err := observer.ObserveRuntimeState(missingCtx, missingTarget)
	if err != nil {
		t.Fatalf("observe missing live createos sandbox: %v", err)
	}
	providercontract.AssertRuntimeObservation(
		t,
		missingTarget,
		missingObservation,
		providers.RuntimeStateTerminated,
	)
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), time.Minute)
	defer deleteCancel()
	if err := machineProvider.DeleteMachine(
		deleteCtx,
		uuid.New(),
		machineID,
		provisioning,
		result.ProviderResourceID,
	); err == nil {
		t.Fatal("delete live createos sandbox with another installation succeeded")
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx,
		installationID,
		machineID,
		provisioning,
		result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete live createos sandbox: %v", err)
	}
	for {
		observation, err := observer.ObserveRuntimeState(deleteCtx, runtimeTarget)
		if err == nil && observation.State == providers.RuntimeStateTerminated {
			break
		}
		select {
		case <-deleteCtx.Done():
			t.Fatalf("deleted live createos sandbox never terminated: %+v %v", observation, err)
		case <-time.After(time.Second):
		}
	}
	if err := machineProvider.DeleteMachine(
		deleteCtx,
		installationID,
		machineID,
		provisioning,
		result.ProviderResourceID,
	); err != nil {
		t.Fatalf("delete terminated live createos sandbox again: %v", err)
	}
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateSandbox(
	ctx context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	if request.Envs == nil {
		request.Envs = map[string]string{}
	}
	request.Envs[providercontract.LiveResourceEnv] = providercontract.LiveResourceValue
	return a.apiClient.CreateSandbox(ctx, request)
}
