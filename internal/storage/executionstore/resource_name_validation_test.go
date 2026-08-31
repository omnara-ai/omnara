package executionstore

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

func TestCreateDaemonMachineRejectsWhitespaceDisplayName(t *testing.T) {
	store := &Store{}
	_, err := store.CreateDaemonMachine(context.Background(), CreateDaemonMachineInput{
		OrgID:       uuid.New(),
		DisplayName: " \t\n ",
	})
	if err == nil || !strings.Contains(err.Error(), "display name") {
		t.Fatalf("CreateDaemonMachine whitespace display name error = %v, want display name rejection", err)
	}
}

func TestPoolMachineDisplayNameStaysWithinResourceNameLimit(t *testing.T) {
	displayName := poolMachineDisplayName(strings.Repeat("😀", resourcename.MaxCodePoints))
	if got := len([]rune(displayName)); got != resourcename.MaxCodePoints {
		t.Fatalf("pool machine display name length = %d, want %d", got, resourcename.MaxCodePoints)
	}
	if _, err := resourcename.CanonicalizeRequired("machine display name", displayName); err != nil {
		t.Fatalf("pool machine display name is invalid: %v", err)
	}
}

func TestPoolMachineDisplayNameDoesNotCreateTrailingSpaceWhenTruncated(t *testing.T) {
	poolName := strings.Repeat("a", 51) + " " + strings.Repeat("b", 12)
	displayName := poolMachineDisplayName(poolName)
	if strings.HasSuffix(displayName, " ") {
		t.Fatalf("pool machine display name %q has a trailing space", displayName)
	}
	if _, err := resourcename.CanonicalizeRequired("machine display name", displayName); err != nil {
		t.Fatalf("pool machine display name is invalid: %v", err)
	}
}

func TestCreateMachinePoolRejectsClusterManagedKind(t *testing.T) {
	store := &Store{}
	cpu := 1
	memoryMB := 1024
	_, err := store.CreateMachinePool(context.Background(), CreateMachinePoolInput{
		OrgID:                  uuid.New(),
		Name:                   "cluster-pool",
		ManagementKind:         management.Cluster,
		Provider:               "test",
		DefaultMachineCPU:      &cpu,
		DefaultMachineMemoryMB: &memoryMB,
		MaxTotalMachines:       1,
		MaxTotalCPU:            &cpu,
		MaxTotalMemoryMB:       &memoryMB,
		MaxMachineCPU:          &cpu,
		MaxMachineMemoryMB:     &memoryMB,
	})
	if err == nil || !strings.Contains(err.Error(), "cluster-managed machine pools are reserved") {
		t.Fatalf("CreateMachinePool cluster management kind error = %v, want reserved management rejection", err)
	}
}
