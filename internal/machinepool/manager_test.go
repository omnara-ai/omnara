package machinepool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	count, err := runBoundedReconcile(context.Background(), 20, 3, func(_ context.Context, _ int) error {
		inFlight := current.Add(1)
		for {
			observedMax := maxSeen.Load()
			if inFlight <= observedMax || maxSeen.CompareAndSwap(observedMax, inFlight) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		current.Add(-1)
		return nil
	})
	if err != nil {
		t.Fatalf("run bounded reconcile: %v", err)
	}
	if count != 20 {
		t.Fatalf("count = %d, want 20", count)
	}
	if maxSeen.Load() > 3 {
		t.Fatalf("max concurrency = %d, want <= 3", maxSeen.Load())
	}
}
