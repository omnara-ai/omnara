package modal

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestModalProviderLiveSmoke(t *testing.T) {
	tokenID := strings.TrimSpace(os.Getenv("MODAL_TOKEN_ID"))
	tokenSecret := strings.TrimSpace(os.Getenv("MODAL_TOKEN_SECRET"))
	app := strings.TrimSpace(os.Getenv("OMNARA_MODAL_TEST_APP"))
	if tokenID == "" || tokenSecret == "" || app == "" {
		t.Skip("MODAL_TOKEN_ID, MODAL_TOKEN_SECRET, and OMNARA_MODAL_TEST_APP are required")
	}
	image := strings.TrimSpace(os.Getenv("OMNARA_MODAL_TEST_IMAGE"))
	if image == "" {
		image = "alpine:latest"
	}
	credential := providerCredential{TokenID: tokenID, TokenSecret: tokenSecret}
	config := providerConfig{App: app, Environment: strings.TrimSpace(os.Getenv("OMNARA_MODAL_TEST_ENVIRONMENT"))}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	rawCredential, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProvider(rawConfig, providers.RuntimeConfig{
		ProviderAuthToken: string(rawCredential), OmnaraAPIURL: "https://api.omnara.test/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	machineID := uuid.New()
	provisioning := testProvisioning(t, strings.TrimSpace(os.Getenv("OMNARA_MODAL_TEST_REGION")))
	rawImage, err := json.Marshal(image)
	if err != nil {
		t.Fatal(err)
	}
	provisioning.ProviderOptions["image"] = rawImage
	provisioning.ProviderOptions["startup_script"] = json.RawMessage(`"sleep 300"`)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		id, found, err := p.InspectMachine(ctx, testInstallationID(), machineID, provisioning, "")
		if err != nil {
			t.Errorf("find live sandbox for cleanup: %v", err)
			return
		}
		if !found {
			return
		}
		if err := p.DeleteMachine(ctx, testInstallationID(), machineID, provisioning, id); err != nil {
			t.Errorf("cleanup live sandbox: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	type outcome struct {
		result providers.ProvisionMachineResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "live-smoke-token", nil)
			outcomes <- outcome{result, err}
		}()
	}
	first, second := <-outcomes, <-outcomes
	if first.err != nil || second.err != nil || first.result.ProviderResourceID != second.result.ProviderResourceID {
		t.Fatalf("concurrent provision: %+v, %+v", first, second)
	}
	created := first.result
	adopted, err := p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "live-smoke-token", nil)
	if err != nil || adopted.ProviderResourceID != created.ProviderResourceID {
		t.Fatalf("adopt: %+v, %v", adopted, err)
	}
	id, found, err := p.InspectMachine(ctx, testInstallationID(), machineID, provisioning, created.ProviderResourceID)
	if err != nil || !found || id != created.ProviderResourceID {
		t.Fatalf("inspect: %q, %v, %v", id, found, err)
	}
	target := providers.RuntimeTarget{
		InstallationID: testInstallationID(), MachineID: machineID,
		ProviderResourceID: id, MachineProvisioning: provisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, target, func() (providers.RuntimeObservation, error) {
		return p.ObserveRuntimeState(ctx, target)
	})
	observations, err := p.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{target})
	if err != nil || len(observations) != 1 || observations[0].State != providers.RuntimeStateRunning {
		t.Fatalf("bulk observation: %+v, %v", observations, err)
	}
	foreign := target
	foreign.MachineID = uuid.New()
	observation, err := p.ObserveRuntimeState(ctx, foreign)
	if err != nil || observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("foreign observation: %+v, %v", observation, err)
	}
	if err := p.DeleteMachine(ctx, testInstallationID(), foreign.MachineID, provisioning, id); err == nil {
		t.Fatal("deletion with mismatched ownership succeeded")
	}
	observation, err = p.ObserveRuntimeState(ctx, target)
	if err != nil || observation.State != providers.RuntimeStateRunning {
		t.Fatalf("sandbox after refused deletion: %+v, %v", observation, err)
	}
	unauthorized := *p
	unauthorized.credential.TokenSecret = "intentionally-invalid"
	authCtx, stopAuth := context.WithTimeout(ctx, 10*time.Second)
	observation, err = unauthorized.ObserveRuntimeState(authCtx, target)
	stopAuth()
	if err == nil || observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("unauthorized observation: %+v, %v", observation, err)
	}
	for range 2 {
		if err := p.DeleteMachine(ctx, testInstallationID(), machineID, provisioning, id); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	terminationStarted := time.Now()
	terminationCtx, stopWaiting := context.WithTimeout(ctx, 2*time.Minute)
	defer stopWaiting()
	for {
		observation, err = p.ObserveRuntimeState(terminationCtx, target)
		if err != nil {
			t.Fatalf("terminated observation: %+v, %v", observation, err)
		}
		if observation.State == providers.RuntimeStateTerminated {
			break
		}
		select {
		case <-terminationCtx.Done():
			t.Fatalf("sandbox did not terminate: %+v", observation)
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Logf("termination observed after %s", time.Since(terminationStarted).Round(time.Millisecond))
	recreated, err := p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "live-smoke-token", nil)
	if err != nil || recreated.ProviderResourceID == "" || recreated.ProviderResourceID == id {
		t.Fatalf("reprovision terminated sandbox: %+v, %v", recreated, err)
	}
	err = p.DeleteMachine(ctx, testInstallationID(), machineID, provisioning, recreated.ProviderResourceID)
	if err != nil {
		t.Fatalf("delete replacement sandbox: %v", err)
	}
}
