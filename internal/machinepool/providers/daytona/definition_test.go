package daytona

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestDaytonaPrepareProvisioningResolvesSnapshotResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshots/team-snapshot" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(snapshot{
			Name:      "team-snapshot",
			State:     "active",
			CPU:       2,
			Memory:    4,
			RegionIDs: []string{"us"},
		})
	}))
	defer server.Close()

	runtimeProvider := newTestProvider(newRESTClient(server.URL, "test-token", server.Client()))
	facts, err := runtimeProvider.PrepareProvisioning(
		context.Background(),
		executionstore.MachineProvisioningConfig{
			ProviderOptions: testOptions(t, "team-snapshot", "us", "echo ready"),
		},
	)
	if err != nil {
		t.Fatalf("prepare daytona provisioning: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != 2 || facts.MemoryMB == nil || *facts.MemoryMB != 4096 {
		t.Fatalf("resolved resources = cpu %v memory %v", facts.CPU, facts.MemoryMB)
	}
}

func TestDaytonaPrepareProvisioningRejectsInvalidSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		snapshot snapshot
		want     string
	}{
		{
			name:     "inactive",
			snapshot: snapshot{Name: "team", State: "building", CPU: 1, Memory: 1},
			want:     "is not active",
		},
		{
			name:     "selector is not name",
			snapshot: snapshot{Name: "team-snapshot", State: "active", CPU: 1, Memory: 1},
			want:     "must be configured by name",
		},
		{
			name:     "wrong target",
			snapshot: snapshot{Name: "team", State: "active", CPU: 1, Memory: 1, RegionIDs: []string{"eu"}},
			want:     "not available",
		},
		{
			name:     "fractional cpu",
			snapshot: snapshot{Name: "team", State: "active", CPU: 1.5, Memory: 1},
			want:     "positive whole number",
		},
		{
			name:     "tiny positive cpu",
			snapshot: snapshot{Name: "team", State: "active", CPU: 0.000000001, Memory: 1},
			want:     "positive whole number",
		},
		{
			name:     "gpu",
			snapshot: snapshot{Name: "team", State: "active", CPU: 1, Memory: 1, GPU: 1},
			want:     "GPU snapshots are not supported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.snapshot)
			}))
			defer server.Close()
			runtimeProvider := newTestProvider(newRESTClient(server.URL, "token", server.Client()))
			_, err := runtimeProvider.PrepareProvisioning(
				context.Background(),
				executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team", "us", "")},
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDaytonaPrepareProvisioningClassifiesProviderFailure(t *testing.T) {
	tests := []struct {
		statusCode  int
		unavailable bool
	}{
		{statusCode: http.StatusNotFound},
		{statusCode: http.StatusTooManyRequests, unavailable: true},
		{statusCode: http.StatusServiceUnavailable, unavailable: true},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.statusCode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()
			runtimeProvider := newTestProvider(newRESTClient(server.URL, "token", server.Client()))
			_, err := runtimeProvider.PrepareProvisioning(
				context.Background(),
				executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team", "us", "")},
			)
			if err == nil || errors.Is(err, storeerr.ErrMachineProviderUnavailable) != tt.unavailable ||
				!strings.Contains(err.Error(), `get daytona snapshot "team"`) {
				t.Fatalf("error = %v, unavailable = %v", err, tt.unavailable)
			}
		})
	}
}

func TestParseDaytonaProviderConfigDefaults(t *testing.T) {
	config, err := parseProviderConfig(nil)
	if err != nil {
		t.Fatalf("parse empty daytona provider config: %v", err)
	}
	if config.APIBaseURL != apiBaseURL || config.AllowedSnapshots != nil || config.AllowedTargets != nil {
		t.Fatalf("default daytona provider config = %+v", config)
	}
}

func TestDaytonaBuildIntentLeavesSnapshotResourcesUnresolved(t *testing.T) {
	policy := daytonaPolicyForTest(
		t,
		"snap-default",
		json.RawMessage(`{"allowed_snapshots":["snap-default","snap-large"],"allowed_targets":["us"]}`),
	)
	defaultCPU := 2
	defaultMemoryMB := 4096
	configuredCPU := 4
	configuredMemoryMB := 8192
	policy.DefaultProvisioning.CPU = &defaultCPU
	policy.DefaultProvisioning.MemoryMB = &defaultMemoryMB
	intent, err := (Definition{}).BuildMachineProvisioningIntent(
		policy,
		executionstore.MachineProvisioningConfig{
			CPU:             &configuredCPU,
			MemoryMB:        &configuredMemoryMB,
			ProviderOptions: testOptions(t, "snap-large", "us", ""),
		},
	)
	if err != nil {
		t.Fatalf("build snapshot-derived intent: %v", err)
	}
	if intent.CPU != nil || intent.MemoryMB != nil {
		t.Fatalf("snapshot intent resources = cpu %v memory %v, want unresolved", intent.CPU, intent.MemoryMB)
	}
}

func TestDaytonaValidatePoolRejectsDisallowedSnapshot(t *testing.T) {
	policy := daytonaPolicyForTest(t, "snap-default", json.RawMessage(`{"allowed_targets":["us"]}`))
	err := (Definition{}).ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{
			ProviderOptions: testOptions(t, "snap-large", "us", ""),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "allowed_snapshots") {
		t.Fatalf("disallowed snapshot error = %v", err)
	}
}

func daytonaPolicyForTest(
	t *testing.T,
	defaultSnapshot string,
	providerConfig json.RawMessage,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 8192
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			ProviderOptions: testOptions(t, defaultSnapshot, "us", ""),
		},
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: providerConfig,
	}
}
