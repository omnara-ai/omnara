package arker

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
)

func TestArkerProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ARKER_API_KEY"))
	if apiKey == "" {
		t.Skip("ARKER_API_KEY is required")
	}
	source := liveEnvOr("OMNARA_ARKER_TEST_SOURCE", "ubuntu-base")
	region := liveEnvOr("OMNARA_ARKER_TEST_REGION", "aws-us-west-2")
	config, err := json.Marshal(map[string]any{
		"allowed_sources": []string{source},
		"allowed_regions": []string{region},
	})
	if err != nil {
		t.Fatalf("marshal provider config: %v", err)
	}
	machineProvider, err := (Definition{}).NewProvider(config, providers.RuntimeConfig{
		OmnaraAPIURL:      liveEnvOr("OMNARA_PUBLIC_API_URL", "https://api.omnara.com/v1"),
		ProviderAuthToken: apiKey,
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	observer, ok := machineProvider.(providers.RuntimeStateObserver)
	if !ok {
		t.Fatal("arker provider does not observe runtime state")
	}
	provisioning := testProvisioning(map[string]json.RawMessage{
		"source": json.RawMessage(`"` + source + `"`),
		"region": json.RawMessage(`"` + region + `"`),
	})
	installationID := uuid.New()
	machineID := uuid.New()

	cleanupResourceID, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("build allocation name: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := machineProvider.DeleteMachine(
			ctx, installationID, machineID, provisioning, cleanupResourceID,
		); err != nil {
			t.Errorf("cleanup delete: %v", err)
		}
	})

	var resourceID string
	for attempt := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), machineProvider.ProvisioningTimeout())
		result, err := machineProvider.ProvisionMachine(
			ctx, installationID, machineID, provisioning, "live-smoke-token", nil, true,
		)
		cancel()
		if err != nil {
			t.Fatalf("provision attempt %d: %v", attempt+1, err)
		}
		if resourceID != "" && result.ProviderResourceID != resourceID {
			t.Fatalf("attempt %d built %s, want %s", attempt+1, result.ProviderResourceID, resourceID)
		}
		resourceID = result.ProviderResourceID
		cleanupResourceID = resourceID
	}

	ctx := context.Background()
	p := &provider{apiToken: apiKey}
	runs, err := p.apiFor(p.regionBaseURL(region)).ListRuns(ctx, resourceID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if !slices.ContainsFunc(runs, func(listed run) bool {
		return listed.Command == daemonLauncherCommand
	}) {
		t.Fatalf("no listed run has the exact daemon command: %+v", runs)
	}
	for _, lookup := range []string{resourceID, ""} {
		id, found, err := machineProvider.InspectMachine(ctx, installationID, machineID, provisioning, lookup)
		if err != nil || !found || id != resourceID {
			t.Fatalf("inspect %q = (%q, %v, %v), want %s", lookup, id, found, err, resourceID)
		}
	}
	if _, _, err := machineProvider.InspectMachine(ctx, uuid.New(), uuid.New(), provisioning, resourceID); err == nil {
		t.Fatal("inspect under another machine identity must fail")
	}
	target := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  resourceID,
		MachineProvisioning: provisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, target, func() (providers.RuntimeObservation, error) {
		return observer.ObserveRuntimeState(ctx, target)
	})
	missingTarget := target
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID = "vmh-missing-" + uuid.NewString()
	observations, err := observer.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{target, missingTarget})
	if err != nil {
		t.Fatalf("observe live arker runtime batch: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("live arker runtime observations = %d, want 2", len(observations))
	}
	providercontract.AssertRuntimeObservation(
		t, target, observations[0], providers.RuntimeStateRunning, providers.RuntimeStateInactive,
	)
	providercontract.AssertRuntimeObservation(t, missingTarget, observations[1], providers.RuntimeStateTerminated)

	for range 2 {
		if err := machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, resourceID); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	observation, err := observer.ObserveRuntimeState(ctx, target)
	if err != nil {
		t.Fatalf("observe after delete: %v", err)
	}
	providercontract.AssertRuntimeObservation(t, target, observation, providers.RuntimeStateTerminated)
}

func liveEnvOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
