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

func TestArkerProviderLiveSmoke(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ARKER_API_KEY"))
	if apiKey == "" {
		t.Skip("ARKER_API_KEY is required; this test creates real VMs")
	}
	daemonSettleWindow = defaultDaemonSettleWindow
	daemonStartTimeout = defaultDaemonStartTimeout
	t.Cleanup(func() {
		daemonSettleWindow = liveTestSettleWindow
		daemonStartTimeout = liveTestStartTimeout
	})

	source := liveEnvOr("OMNARA_ARKER_TEST_SOURCE", "ubuntu-base")
	placementProvider := liveEnvOr("OMNARA_ARKER_TEST_PROVIDER", "aws")
	placementRegion := liveEnvOr("OMNARA_ARKER_TEST_REGION", "us-west-2")
	omnaraAPIURL := liveEnvOr("OMNARA_PUBLIC_API_URL", "https://api.omnara.com/v1")

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

// The id-recovery path against a real deployment. The unit test drives a fake,
// so it proves the branch is taken; this proves Arker actually resolves an
// allocation name through `GET /v1/vms/{id}` and hands back the id the
// reconciler needs to terminate a VM nothing is pointing at any more.
func TestArkerProviderLiveRecoversALostVMID(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ARKER_API_KEY"))
	if apiKey == "" {
		t.Skip("ARKER_API_KEY is required; this test creates real VMs")
	}
	daemonSettleWindow = defaultDaemonSettleWindow
	daemonStartTimeout = defaultDaemonStartTimeout
	t.Cleanup(func() {
		daemonSettleWindow = liveTestSettleWindow
		daemonStartTimeout = liveTestStartTimeout
	})

	source := liveEnvOr("OMNARA_ARKER_TEST_SOURCE", "ubuntu-base")
	placementProvider := liveEnvOr("OMNARA_ARKER_TEST_PROVIDER", "aws")
	placementRegion := liveEnvOr("OMNARA_ARKER_TEST_REGION", "us-west-2")
	omnaraAPIURL := liveEnvOr("OMNARA_PUBLIC_API_URL", "https://api.omnara.com/v1")

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
	observer, ok := machineProvider.(providers.RuntimeProvider)
	if !ok {
		t.Fatal("arker provider does not implement RuntimeProvider")
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
		ctx, installationID, machineID, provisioning, "live-recovery", nil,
	)
	cancel()
	resourceID = result.ProviderResourceID
	if resourceID == "" {
		t.Fatalf("provision returned no resource id: %v", err)
	}
	t.Logf("provisioned %s", resourceID)

	// The machine whose id was never persisted: everything the reconciler has,
	// minus the id itself.
	observation, err := observer.ObserveRuntimeState(context.Background(), providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  "",
		MachineProvisioning: provisioning,
	})
	if err != nil {
		t.Fatalf("observe with no resource id: %v", err)
	}
	if observation.ProviderResourceID != resourceID {
		t.Fatalf(
			"recovered id = %q, want %q -- without it cleanup has nothing to delete",
			observation.ProviderResourceID,
			resourceID,
		)
	}
	if observation.State != providers.RuntimeStateRunning {
		t.Fatalf("state = %q, want running", observation.State)
	}
	t.Logf("arker resolved the allocation name and returned %s", observation.ProviderResourceID)

	// And the negative: a machine that never existed must not look alive.
	ghost, err := observer.ObserveRuntimeState(context.Background(), providers.RuntimeTarget{
		InstallationID:      uuid.New(),
		MachineID:           uuid.New(),
		ProviderResourceID:  "",
		MachineProvisioning: provisioning,
	})
	if err != nil {
		t.Fatalf("observe an unknown machine: %v", err)
	}
	if ghost.State != providers.RuntimeStateTerminated {
		t.Fatalf("unknown machine state = %q, want terminated", ghost.State)
	}
	t.Log("a machine that never existed reports terminated, not running")
}

// The cancel branch of startDaemon, against a real deployment.
//
// The main smoke logs "a second attempt converged on the same machine", which
// looks like it proves this and does not: the boot fails within a couple of
// seconds, so by the second attempt the first run is already terminal and
// cancelStaleDaemons has nothing to skip past. This starts a daemon-command run
// that genuinely stays in flight, so the branch actually executes.
func TestArkerProviderLiveCancelsAnInFlightDaemon(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ARKER_API_KEY"))
	if apiKey == "" {
		t.Skip("ARKER_API_KEY is required; this test creates real VMs")
	}
	daemonSettleWindow = defaultDaemonSettleWindow
	daemonStartTimeout = defaultDaemonStartTimeout
	t.Cleanup(func() {
		daemonSettleWindow = liveTestSettleWindow
		daemonStartTimeout = liveTestStartTimeout
	})

	source := liveEnvOr("OMNARA_ARKER_TEST_SOURCE", "ubuntu-base")
	placementProvider := liveEnvOr("OMNARA_ARKER_TEST_PROVIDER", "aws")
	placementRegion := liveEnvOr("OMNARA_ARKER_TEST_REGION", "us-west-2")
	omnaraAPIURL := liveEnvOr("OMNARA_PUBLIC_API_URL", "https://api.omnara.com/v1")

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
	result, _ := machineProvider.ProvisionMachine(
		ctx, installationID, machineID, provisioning, "live-cancel", nil,
	)
	cancel()
	resourceID = result.ProviderResourceID
	if resourceID == "" {
		t.Fatal("provision returned no resource id")
	}

	sdk, err := arkersdk.New(arkersdk.Options{APIKey: apiKey, BaseURL: result.SandboxURL})
	if err != nil {
		t.Fatalf("sdk client: %v", err)
	}
	vm := sdk.VM(resourceID)

	// A boot script that does not exit, run under the exact command
	// cancelStaleDaemons matches on.
	bg, bgCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer bgCancel()
	if err := vm.WriteFile(bg, bootPath, []byte("sleep 600\n")); err != nil {
		t.Fatalf("write long-running boot script: %v", err)
	}
	stale, err := vm.Run(bg, arkersdk.RunRequest{
		Command:          daemonCommand,
		SessionIdx:       arkersdk.Ptr(daemonSessionIdx),
		TimeToBackground: arkersdk.Ptr(0),
	})
	if err != nil {
		t.Fatalf("start a long-running daemon: %v", err)
	}
	t.Logf("in-flight daemon run %s", stale.RunID)

	record, err := vm.GetRun(bg, stale.RunID)
	if err != nil {
		t.Fatalf("read the in-flight run: %v", err)
	}
	if terminalRunStates[record.State] {
		t.Fatalf("setup run is already %s; it needed to still be in flight", record.State)
	}
	t.Logf("state before the retry: %s", record.State)

	// The retry. This is what has to cancel the run above rather than queue.
	ctx2, cancel2 := context.WithTimeout(context.Background(), machineProvider.ProvisioningTimeout())
	_, _ = machineProvider.ProvisionMachine(
		ctx2, installationID, machineID, provisioning, "live-cancel-retry", nil,
	)
	cancel2()

	after, err := vm.GetRun(bg, stale.RunID)
	if err != nil {
		t.Fatalf("re-read the stale run: %v", err)
	}
	if !terminalRunStates[after.State] {
		t.Fatalf("stale daemon is %q after the retry, want terminal -- the retry queued behind it", after.State)
	}
	t.Logf("the stale daemon is now %s", after.State)

	listed, err := vm.ListRuns(bg, arkersdk.ListRunsOptions{Limit: 100})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	live := 0
	for _, run := range listed.Runs {
		if run.Command == daemonCommand && !terminalRunStates[run.State] {
			live++
		}
	}
	if live > 1 {
		t.Fatalf("%d daemon runs still in flight, want at most 1", live)
	}
	t.Logf("daemon runs still in flight: %d", live)
}

// The context-bounded daemon wait, and ValidatePool, against a real deployment.
//
// waitForDaemon takes the smaller of its own ceiling and the caller's remaining
// context, so a stuck boot reports the state it was stuck in rather than a bare
// cancellation. Reproducing "stuck" needs session 1 occupied by something
// cancelStaleDaemons will NOT cancel -- it matches on the daemon command -- so
// the daemon run queues behind it as `pending`.
func TestArkerProviderLiveReportsAStuckDaemonNotACancellation(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ARKER_API_KEY"))
	if apiKey == "" {
		t.Skip("ARKER_API_KEY is required; this test creates real VMs")
	}
	daemonSettleWindow = defaultDaemonSettleWindow
	daemonStartTimeout = defaultDaemonStartTimeout
	t.Cleanup(func() {
		daemonSettleWindow = liveTestSettleWindow
		daemonStartTimeout = liveTestStartTimeout
	})

	source := liveEnvOr("OMNARA_ARKER_TEST_SOURCE", "ubuntu-base")
	placementProvider := liveEnvOr("OMNARA_ARKER_TEST_PROVIDER", "aws")
	placementRegion := liveEnvOr("OMNARA_ARKER_TEST_REGION", "us-west-2")
	omnaraAPIURL := liveEnvOr("OMNARA_PUBLIC_API_URL", "https://api.omnara.com/v1")

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

	// ValidatePool on the real provider, with the defaults the contract now
	// requires. A pool missing them must be refused here as well as by OpenAPI.
	good := executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			CPU: ptr(2), MemoryMB: ptr(2048),
			ProviderOptions: map[string]json.RawMessage{
				"source":   json.RawMessage(`"` + source + `"`),
				"provider": json.RawMessage(`"` + placementProvider + `"`),
				"region":   json.RawMessage(`"` + placementRegion + `"`),
			},
		},
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU: ptr(64), MaxTotalMemoryMB: ptr(131072),
			MinMachineCPU: ptr(1), MaxMachineCPU: ptr(16),
			MinMachineMemoryMB: ptr(512), MaxMachineMemoryMB: ptr(32768),
		},
	}
	if err := (Definition{}).ValidatePool(good); err != nil {
		t.Fatalf("a pool with defaults was refused: %v", err)
	}
	missing := good
	missing.DefaultProvisioning.CPU = nil
	if err := (Definition{}).ValidatePool(missing); err == nil {
		t.Fatal("a pool with no default cpu was accepted; PoolDefault is meant to be required")
	} else {
		t.Logf("pool without a default cpu refused: %v", err)
	}

	provisioning := executionstore.MachineProvisioningConfig{
		CPU: ptr(2), MemoryMB: ptr(2048),
		ProviderOptions: map[string]json.RawMessage{
			"source":         json.RawMessage(`"` + source + `"`),
			"provider":       json.RawMessage(`"` + placementProvider + `"`),
			"region":         json.RawMessage(`"` + placementRegion + `"`),
			"startup_script": json.RawMessage(`""`),
		},
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
	result, _ := machineProvider.ProvisionMachine(
		ctx, installationID, machineID, provisioning, "live-stuck", nil,
	)
	cancel()
	resourceID = result.ProviderResourceID
	if resourceID == "" {
		t.Fatal("provision returned no resource id")
	}

	sdk, err := arkersdk.New(arkersdk.Options{APIKey: apiKey, BaseURL: result.SandboxURL})
	if err != nil {
		t.Fatalf("sdk client: %v", err)
	}
	vm := sdk.VM(resourceID)
	bg, bgCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer bgCancel()

	// Occupy session 1 with a command cancelStaleDaemons will not touch.
	blocker, err := vm.Run(bg, arkersdk.RunRequest{
		Command:          "sleep 600",
		SessionIdx:       arkersdk.Ptr(daemonSessionIdx),
		TimeToBackground: arkersdk.Ptr(0),
	})
	if err != nil {
		t.Fatalf("start the blocking run: %v", err)
	}
	t.Cleanup(func() { _, _ = vm.CancelRun(context.Background(), blocker.RunID) })
	t.Logf("session %d occupied by %s", daemonSessionIdx, blocker.RunID)

	// A deadline far shorter than daemonStartTimeout. Without the context
	// bounding this surfaces as "context deadline exceeded"; with it, the wait
	// ends first and names the state the daemon was stuck in.
	tight, tightCancel := context.WithTimeout(context.Background(), 12*time.Second)
	_, err = machineProvider.ProvisionMachine(
		tight, installationID, machineID, provisioning, "live-stuck-retry", nil,
	)
	tightCancel()
	if err == nil {
		t.Skip("the daemon settled despite the blocked session; nothing to assert")
	}
	t.Logf("error: %v", err)
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("surfaced as a bare cancellation, not the daemon state: %v", err)
	}
	if !strings.Contains(err.Error(), "omnara daemon") {
		t.Fatalf("error does not name the daemon: %v", err)
	}
}

// The settle-window clamp, against a real daemon.
//
// Bounding the wait by the caller's context must not shorten it below the
// settle window: success is only concluded once the daemon has held `running`
// through that window, so a hard deadline inside it fails a boot that is doing
// nothing wrong. The unit test drives a fake; this drives a real daemon on a
// real VM with genuinely less context than settle + margin.
func TestArkerProviderLiveShortContextDoesNotFailAHealthyDaemon(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ARKER_API_KEY"))
	if apiKey == "" {
		t.Skip("ARKER_API_KEY is required; this test creates real VMs")
	}
	daemonSettleWindow = defaultDaemonSettleWindow
	daemonStartTimeout = defaultDaemonStartTimeout
	t.Cleanup(func() {
		daemonSettleWindow = liveTestSettleWindow
		daemonStartTimeout = liveTestStartTimeout
	})

	source := liveEnvOr("OMNARA_ARKER_TEST_SOURCE", "ubuntu-base")
	placementProvider := liveEnvOr("OMNARA_ARKER_TEST_PROVIDER", "aws")
	placementRegion := liveEnvOr("OMNARA_ARKER_TEST_REGION", "us-west-2")
	omnaraAPIURL := liveEnvOr("OMNARA_PUBLIC_API_URL", "https://api.omnara.com/v1")

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
		CPU: ptr(2), MemoryMB: ptr(2048),
		ProviderOptions: map[string]json.RawMessage{
			"source":         json.RawMessage(`"` + source + `"`),
			"provider":       json.RawMessage(`"` + placementProvider + `"`),
			"region":         json.RawMessage(`"` + placementRegion + `"`),
			"startup_script": json.RawMessage(`""`),
		},
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
	result, _ := machineProvider.ProvisionMachine(
		ctx, installationID, machineID, provisioning, "live-clamp", nil,
	)
	cancel()
	resourceID = result.ProviderResourceID
	if resourceID == "" {
		t.Fatal("provision returned no resource id")
	}

	sdk, err := arkersdk.New(arkersdk.Options{APIKey: apiKey, BaseURL: result.SandboxURL})
	if err != nil {
		t.Fatalf("sdk client: %v", err)
	}
	vm := sdk.VM(resourceID)
	bg, bgCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer bgCancel()

	// A daemon that genuinely stays up, under the command waitForDaemon polls.
	if err := vm.WriteFile(bg, bootPath, []byte("sleep 600\n")); err != nil {
		t.Fatalf("write long-running boot script: %v", err)
	}
	healthy, err := vm.Run(bg, arkersdk.RunRequest{
		Command:          daemonCommand,
		SessionIdx:       arkersdk.Ptr(daemonSessionIdx),
		TimeToBackground: arkersdk.Ptr(0),
	})
	if err != nil {
		t.Fatalf("start a healthy daemon: %v", err)
	}
	t.Cleanup(func() { _, _ = vm.CancelRun(context.Background(), healthy.RunID) })

	record, err := vm.GetRun(bg, healthy.RunID)
	if err != nil {
		t.Fatalf("read the daemon run: %v", err)
	}
	t.Logf("daemon state before the wait: %s", record.State)

	// Deliberately less than settle + margin. Unclamped, the hard deadline lands
	// inside the settle window and this healthy daemon is called stuck.
	short, shortCancel := context.WithTimeout(bg, daemonSettleWindow+daemonDeadlineMargin/2)
	err = waitForDaemon(short, vm, healthy.RunID)
	shortCancel()

	if err != nil && strings.Contains(err.Error(), "is still") {
		t.Fatalf("a healthy daemon was reported stuck on a short context: %v", err)
	}
	if err != nil {
		t.Logf("ended on the context rather than blaming the daemon: %v", err)
	} else {
		t.Log("the wait completed and accepted the healthy daemon")
	}
}
