package arker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	arkersdk "github.com/ArkerHQ/arker-sdk/go"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// ARKER_API_KEY=ark_... go test -run TestArkerProviderLiveSmoke ./internal/machinepool/providers/arker/
func TestArkerProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ARKER_API_KEY"))
	if apiKey == "" {
		t.Skip("ARKER_API_KEY is required; this test creates real VMs")
	}
	// Unit tests shorten this; the live run has to pay the real window or it
	// would report a daemon healthy before it has had time to die.
	daemonSettleWindow = defaultDaemonSettleWindow
	t.Cleanup(func() { daemonSettleWindow = liveTestSettleWindow })

	source := liveEnvOr("OMNARA_ARKER_TEST_SOURCE", "ubuntu-base")
	placementProvider := liveEnvOr("OMNARA_ARKER_TEST_PROVIDER", "aws")
	placementRegion := liveEnvOr("OMNARA_ARKER_TEST_REGION", "us-west-2")
	omnaraAPIURL := liveEnvOr("OMNARA_PUBLIC_API_URL", "https://app.omnara.com/api/v1")

	config, err := json.Marshal(map[string]any{
		"allowed_sources":   []string{source},
		"allowed_providers": []string{placementProvider},
		"allowed_regions":   []string{placementRegion},
	})
	if err != nil {
		t.Fatalf("marshal provider config: %v", err)
	}
	machineProvider, err := (Definition{}).NewProvider(config, providers.RuntimeConfig{
		OmnaraAPIURL:      omnaraAPIURL,
		ProviderAuthToken: apiKey,
	})
	if err != nil {
		t.Fatalf("new live arker provider: %v", err)
	}

	provisioning := executionstore.MachineProvisioningConfig{
		CPU:      ptr(2),
		MemoryMB: ptr(2048),
		ProviderOptions: map[string]json.RawMessage{
			"source":         json.RawMessage(`"` + source + `"`),
			"provider":       json.RawMessage(`"` + placementProvider + `"`),
			"region":         json.RawMessage(`"` + placementRegion + `"`),
			"startup_script": json.RawMessage(`""`),
		},
	}
	facts, err := machineProvider.PrepareProvisioning(context.Background(), provisioning)
	if err != nil {
		t.Fatalf("prepare live provisioning: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != 2 {
		t.Fatalf("prepare returned cpu %v, want the requested 2", facts.CPU)
	}

	installationID := uuid.New()
	machineID := uuid.New()

	var resourceID string
	t.Cleanup(func() {
		if resourceID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := machineProvider.DeleteMachine(
			ctx, installationID, machineID, provisioning, resourceID,
		); err != nil {
			t.Errorf("cleanup delete: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), machineProvider.ProvisioningTimeout())
	result, err := machineProvider.ProvisionMachine(
		ctx, installationID, machineID, provisioning, "live-attempt-1", nil,
	)
	cancel()
	resourceID = result.ProviderResourceID
	if err != nil {
		if resourceID == "" {
			t.Fatalf("provision failed without reporting a resource id: %v", err)
		}
		t.Logf("daemon did not come up (expected without a live Omnara): %v", err)
	}
	if resourceID == "" || result.SandboxURL == "" {
		t.Fatalf("provision returned %+v, want an id and an endpoint", result)
	}
	t.Logf("provisioned %s at %s", resourceID, result.SandboxURL)

	client, err := arkersdk.New(arkersdk.Options{APIKey: apiKey, BaseURL: result.SandboxURL})
	if err != nil {
		t.Fatalf("build sdk client: %v", err)
	}
	sessions, err := client.VM(resourceID).ListSessions(
		context.Background(),
		arkersdk.ListSessionsOptions{},
	)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions.Sessions) == 0 {
		t.Fatal("the guest has no session, so the daemon handoff never reached it")
	}
	t.Logf("daemon session present: %d session(s)", len(sessions.Sessions))

	if _, err := client.VM(resourceID).ReadFile(context.Background(), bootPath); err == nil {
		t.Fatalf("%s is still on the guest and it carries the machine token", bootPath)
	} else if !arkersdk.IsNotFound(err) {
		t.Logf("boot script read back as %v", err)
	}
	t.Log("the boot script removed itself from the guest")

	ctx, cancel = context.WithTimeout(context.Background(), machineProvider.ProvisioningTimeout())
	again, err := machineProvider.ProvisionMachine(
		ctx, installationID, machineID, provisioning, "live-attempt-2", nil,
	)
	cancel()
	if err != nil && again.ProviderResourceID == "" {
		t.Fatalf("second attempt reported no machine: %v", err)
	}
	if again.ProviderResourceID != resourceID {
		t.Fatalf("a second attempt built another machine: %s then %s",
			resourceID, again.ProviderResourceID)
	}
	t.Log("a second attempt converged on the same machine")

	got, found, err := machineProvider.InspectMachine(
		context.Background(), installationID, machineID, provisioning, resourceID,
	)
	if err != nil || !found || got != resourceID {
		t.Fatalf("inspect = (%q, %v, %v), want the provisioned id", got, found, err)
	}
	if _, _, err := machineProvider.InspectMachine(
		context.Background(), uuid.New(), uuid.New(),
		provisioning, resourceID,
	); err == nil {
		t.Fatal("inspect under another machine identity must not adopt the vm")
	}

	waker, ok := machineProvider.(providers.MachineWaker)
	if !ok {
		t.Fatal("arker provider does not implement MachineWaker")
	}
	for i := range 2 {
		if err := waker.WakeMachine(context.Background(), providers.WakeMachineInput{
			ProviderResourceID: resourceID,
			SandboxURL:         result.SandboxURL,
		}); err != nil {
			t.Fatalf("wake attempt %d: %v", i+1, err)
		}
	}
	t.Log("wake is idempotent")

	if err := machineProvider.DeleteMachine(
		context.Background(), installationID, machineID, provisioning, resourceID,
	); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := machineProvider.DeleteMachine(
		context.Background(), installationID, machineID, provisioning, resourceID,
	); err != nil {
		t.Fatalf("delete on an already-absent machine: %v", err)
	}
	if _, found, err := machineProvider.InspectMachine(
		context.Background(), installationID, machineID, provisioning, resourceID,
	); err != nil || found {
		t.Fatalf("inspect after delete = (%v, %v), want absent", found, err)
	}
	resourceID = ""
	t.Log("delete is retry-safe and absence is a value")

	ctx, cancel = context.WithTimeout(context.Background(), machineProvider.ProvisioningTimeout())
	spent, err := machineProvider.ProvisionMachine(
		ctx, installationID, machineID, provisioning, "live-attempt-3", nil,
	)
	cancel()
	if err == nil {
		resourceID = spent.ProviderResourceID
		t.Fatalf("a torn-down machine was provisioned again as %s", spent.ProviderResourceID)
	}
	t.Logf("a spent key is refused: %v", err)

	replacementMachine := uuid.New()
	ctx, cancel = context.WithTimeout(context.Background(), machineProvider.ProvisioningTimeout())
	replacement, err := machineProvider.ProvisionMachine(
		ctx, installationID, replacementMachine, provisioning, "live-attempt-1", nil,
	)
	cancel()
	if replacement.ProviderResourceID == "" {
		t.Fatalf("a replacement machine was not provisioned: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := machineProvider.DeleteMachine(
			cleanupCtx, installationID, replacementMachine, provisioning,
			replacement.ProviderResourceID,
		); err != nil {
			t.Errorf("cleanup replacement: %v", err)
		}
	})
	t.Logf("a replacement machine provisioned as %s", replacement.ProviderResourceID)
}

func liveEnvOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
