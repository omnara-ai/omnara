package machinepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestManagerValidatesDefaultMachinePool(t *testing.T) {
	cpu := 1
	memoryMB := 1024
	manager := Manager{
		Catalog:   DefaultCatalog(),
		PublicURL: "https://app.omnara.test",
	}
	err := manager.ValidateDefaultMachinePool(executionstore.DefaultMachinePoolTemplate{
		Name:                          "provider-default-pool",
		Provider:                      "unikraft",
		ProviderAuthEnvVar:            "TEST_DEFAULT_POOL_TOKEN",
		DefaultMachineCPU:             &cpu,
		DefaultMachineMemoryMB:        &memoryMB,
		DefaultMachineEnv:             json.RawMessage(`{}`),
		DefaultMachineProviderOptions: json.RawMessage(`{"image":"registry.example/daemon:latest","metro":"sfo"}`),
		ProviderConfig:                json.RawMessage(`{}`),
		MaxTotalMachines:              1,
		MaxTotalCPU:                   &cpu,
		MaxTotalMemoryMB:              &memoryMB,
		MaxMachineCPU:                 &cpu,
		MaxMachineMemoryMB:            &memoryMB,
	})
	if err != nil {
		t.Fatalf("validate default machine pool: %v", err)
	}
}

func TestManagerRejectsDefaultMachineConfigExceedingCaps(t *testing.T) {
	defaultCPU := 8
	maxMachineCPU := 4
	memoryMB := 1024
	manager := Manager{
		Catalog:   DefaultCatalog(),
		PublicURL: "https://app.omnara.test",
	}
	err := manager.ValidateDefaultMachinePool(executionstore.DefaultMachinePoolTemplate{
		Name:                          "provider-default-pool",
		Provider:                      "unikraft",
		ProviderAuthEnvVar:            "TEST_DEFAULT_POOL_TOKEN",
		DefaultMachineCPU:             &defaultCPU,
		DefaultMachineMemoryMB:        &memoryMB,
		DefaultMachineEnv:             json.RawMessage(`{}`),
		DefaultMachineProviderOptions: json.RawMessage(`{"image":"registry.example/daemon:latest","metro":"sfo"}`),
		ProviderConfig:                json.RawMessage(`{}`),
		MaxTotalMachines:              1,
		MaxTotalCPU:                   &defaultCPU,
		MaxTotalMemoryMB:              &memoryMB,
		MaxMachineCPU:                 &maxMachineCPU,
		MaxMachineMemoryMB:            &memoryMB,
	})
	if err == nil {
		t.Fatal("expected default machine pool config exceeding caps to fail")
	}
	if !strings.Contains(err.Error(), "max_machine_cpu") {
		t.Fatalf("expected max_machine_cpu error, got %v", err)
	}
}

func TestManagerRejectsInvalidDefaultMachineConfig(t *testing.T) {
	cpu := 1
	memoryMB := 1024
	manager := Manager{
		Catalog:   DefaultCatalog(),
		PublicURL: "https://app.omnara.test",
	}
	err := manager.ValidateDefaultMachinePool(executionstore.DefaultMachinePoolTemplate{
		Name:                   "provider-default-pool",
		Provider:               "unikraft",
		ProviderAuthEnvVar:     "TEST_DEFAULT_POOL_TOKEN",
		DefaultMachineCPU:      &cpu,
		DefaultMachineMemoryMB: &memoryMB,
		DefaultMachineEnv:      json.RawMessage(`{}`),
		ProviderConfig:         json.RawMessage(`{}`),
		MaxTotalMachines:       1,
		MaxTotalCPU:            &cpu,
		MaxTotalMemoryMB:       &memoryMB,
		MaxMachineCPU:          &cpu,
		MaxMachineMemoryMB:     &memoryMB,
	})
	if err == nil {
		t.Fatal("expected invalid default machine pool config to fail")
	}
}

func TestDeleteBackoff(t *testing.T) {
	tests := []struct {
		attempts int32
		want     time.Duration
	}{
		{attempts: 0, want: time.Minute},
		{attempts: 1, want: time.Minute},
		{attempts: 2, want: 5 * time.Minute},
		{attempts: 3, want: 30 * time.Minute},
		{attempts: 4, want: 24 * time.Hour},
		{attempts: 10, want: 24 * time.Hour},
	}
	for _, tc := range tests {
		if got := deleteBackoff(tc.attempts); got != tc.want {
			t.Fatalf("deleteBackoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

func TestRunBoundedReconcileContinuesAfterError(t *testing.T) {
	expectedErr := errors.New("boom")
	var ran atomic.Int32
	count, err := runBoundedReconcile(context.Background(), 5, 2, func(_ context.Context, i int) error {
		ran.Add(1)
		if i == 2 {
			return expectedErr
		}
		return nil
	})
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("err = %v, want %v", err, expectedErr)
	}
	if ran.Load() != 5 {
		t.Fatalf("ran = %d, want 5", ran.Load())
	}
}

func TestRunBoundedReconcileReturnsTaskPanic(t *testing.T) {
	count, err := runBoundedReconcile(context.Background(), 3, 2, func(_ context.Context, i int) error {
		if i == 1 {
			panic("provider failure")
		}
		return nil
	})
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	if err == nil || !strings.Contains(err.Error(), "machine reconciliation 1 panicked: provider failure") {
		t.Fatalf("err = %v, want contained provider panic", err)
	}
}

func TestRunBoundedReconcileLimitsConcurrency(t *testing.T) {
	var current atomic.Int32
	var maxSeen atomic.Int32
	arrived := make(chan struct{})
	release := make(chan struct{})
	type result struct {
		count int
		err   error
	}
	results := make(chan result, 1)
	go func() {
		count, err := runBoundedReconcile(context.Background(), 20, 3, func(_ context.Context, _ int) error {
			inFlight := current.Add(1)
			for {
				observedMax := maxSeen.Load()
				if inFlight <= observedMax || maxSeen.CompareAndSwap(observedMax, inFlight) {
					break
				}
			}
			select {
			case arrived <- struct{}{}:
			case <-release:
			}
			<-release
			current.Add(-1)
			return nil
		})
		results <- result{count: count, err: err}
	}()
	for range 3 {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("reconcile tasks did not reach the concurrency limit")
		}
	}
	close(release)
	res := <-results
	if res.err != nil {
		t.Fatalf("run bounded reconcile: %v", res.err)
	}
	if res.count != 20 {
		t.Fatalf("count = %d, want 20", res.count)
	}
	if got := maxSeen.Load(); got != 3 {
		t.Fatalf("max concurrency = %d, want 3", got)
	}
}

type provisionRetryProvider struct {
	provision func() (providers.ProvisionMachineResult, error)
}

func (p provisionRetryProvider) ProvisionMachine(
	context.Context,
	storage.ID,
	storage.ID,
	executionstore.MachineProvisioningConfig,
	string,
	map[string]string,
) (providers.ProvisionMachineResult, error) {
	return p.provision()
}

func (provisionRetryProvider) ProvisioningTimeout() time.Duration {
	return time.Minute
}

func (provisionRetryProvider) PrepareProvisioning(
	context.Context,
	executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	return executionstore.MachineResourceFacts{}, errors.New("not implemented")
}

func (provisionRetryProvider) InspectMachine(
	context.Context,
	storage.ID,
	storage.ID,
	executionstore.MachineProvisioningConfig,
	string,
) (string, bool, error) {
	return "", false, errors.New("not implemented")
}

func (provisionRetryProvider) DeleteMachine(
	context.Context,
	storage.ID,
	storage.ID,
	executionstore.MachineProvisioningConfig,
	string,
) error {
	return errors.New("not implemented")
}

func provisionWithRetryForTest(
	ctx context.Context,
	provision func() (providers.ProvisionMachineResult, error),
) (providers.ProvisionMachineResult, error) {
	return provisionMachineWithRetry(
		ctx,
		provisionRetryProvider{provision: provision},
		storage.ID{},
		storage.ID{},
		executionstore.MachineProvisioningConfig{},
		"",
		nil,
	)
}

func stubProvisionRetryDelays(t *testing.T) {
	t.Helper()
	saved := providerProvisionRetryDelays
	providerProvisionRetryDelays = [...]time.Duration{
		time.Millisecond,
		time.Millisecond,
		time.Millisecond,
	}
	t.Cleanup(func() {
		providerProvisionRetryDelays = saved
	})
}

func TestProvisionMachineWithRetrySucceedsAndPreservesObservation(t *testing.T) {
	stubProvisionRetryDelays(t)
	providerErr := errors.New("temporary provider failure")
	calls := 0
	result, err := provisionWithRetryForTest(
		context.Background(),
		func() (providers.ProvisionMachineResult, error) {
			calls++
			switch calls {
			case 1:
				return providers.ProvisionMachineResult{ProviderResourceID: "resource-1"}, providerErr
			case 2:
				return providers.ProvisionMachineResult{}, providerErr
			default:
				return providers.ProvisionMachineResult{ProviderResourceID: "resource-1", SandboxURL: "https://sandbox.test/"}, nil
			}
		},
	)
	if err != nil {
		t.Fatalf("provision with retry: %v", err)
	}
	if calls != 3 {
		t.Fatalf("provision calls = %d, want 3", calls)
	}
	if result.ProviderResourceID != "resource-1" || result.SandboxURL != "https://sandbox.test/" {
		t.Fatalf("provision result = %+v", result)
	}
}

func TestProvisionMachineWithRetryExhaustsAttempts(t *testing.T) {
	stubProvisionRetryDelays(t)
	providerErr := errors.New("provider unavailable")
	calls := 0
	result, err := provisionWithRetryForTest(
		context.Background(),
		func() (providers.ProvisionMachineResult, error) {
			calls++
			return providers.ProvisionMachineResult{ProviderResourceID: "resource-1"}, providerErr
		},
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("provision error = %v, want %v", err, providerErr)
	}
	if calls != providerProvisionRetryAttempts {
		t.Fatalf("provision calls = %d, want %d", calls, providerProvisionRetryAttempts)
	}
	if result.ProviderResourceID != "resource-1" {
		t.Fatalf("provision result = %+v, want observed resource", result)
	}
}

func TestProvisionMachineWithRetryPreservesProviderErrorWhenWaitExpires(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	calls := 0
	result, err := provisionWithRetryForTest(
		ctx,
		func() (providers.ProvisionMachineResult, error) {
			calls++
			return providers.ProvisionMachineResult{ProviderResourceID: "resource-1"}, providerErr
		},
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("provision error = %v, want %v", err, providerErr)
	}
	if calls != 1 {
		t.Fatalf("provision calls = %d, want 1", calls)
	}
	if result.ProviderResourceID != "resource-1" {
		t.Fatalf("provision result = %+v, want observed resource", result)
	}
}

func TestProviderProvisionRetryDelayHonorsRetryAfter(t *testing.T) {
	providerErr := providers.WithRetryAfter(
		errors.New("rate limited"),
		http.Header{"Retry-After": []string{"12"}},
	)
	if delay := providerProvisionRetryDelay(0, providerErr); delay != 12*time.Second {
		t.Fatalf("retry delay = %s, want 12s", delay)
	}
	if delay := providerProvisionRetryDelay(1, errors.New("provider unavailable")); delay != 2*time.Second {
		t.Fatalf("retry delay = %s, want 2s", delay)
	}
}

func TestProvisionMachineWithRetryDiscardsReplacedResource(t *testing.T) {
	stubProvisionRetryDelays(t)
	calls := 0
	result, err := provisionWithRetryForTest(
		context.Background(),
		func() (providers.ProvisionMachineResult, error) {
			calls++
			switch calls {
			case 1:
				return providers.ProvisionMachineResult{ProviderResourceID: "resource-1"},
					errors.New("delete stale sandbox: unavailable")
			case 2:
				return providers.ProvisionMachineResult{}, fmt.Errorf("sandbox was deleted: %w", providers.ErrResourceReplaced)
			default:
				return providers.ProvisionMachineResult{ProviderResourceID: "resource-2"}, nil
			}
		},
	)
	if err != nil {
		t.Fatalf("provision after replacement: %v", err)
	}
	if calls != 3 {
		t.Fatalf("provision calls = %d, want 3", calls)
	}
	if result.ProviderResourceID != "resource-2" {
		t.Fatalf("provision result = %+v, want replaced resource", result)
	}
}

func TestProvisionMachineWithRetryRejectsConflictingResources(t *testing.T) {
	stubProvisionRetryDelays(t)
	calls := 0
	result, err := provisionWithRetryForTest(
		context.Background(),
		func() (providers.ProvisionMachineResult, error) {
			calls++
			if calls == 1 {
				return providers.ProvisionMachineResult{ProviderResourceID: "resource-1"}, errors.New("ambiguous failure")
			}
			return providers.ProvisionMachineResult{ProviderResourceID: "resource-2"}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "conflicting resource ids") {
		t.Fatalf("provision error = %v, want conflicting resource ids", err)
	}
	if result.ProviderResourceID != "resource-1" {
		t.Fatalf("provision result = %+v, want first observed resource", result)
	}
}
