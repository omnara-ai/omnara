package providers

import (
	"testing"

	"github.com/google/uuid"
)

func TestMachineAllocationNameIncludesInstallation(t *testing.T) {
	machineID := uuid.New()
	installationID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	first, err := MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("first allocation name: %v", err)
	}
	second, err := MachineAllocationName(uuid.New(), machineID)
	if err != nil {
		t.Fatalf("second allocation name: %v", err)
	}
	if first == second {
		t.Fatalf("allocation names for different installations are equal: %q", first)
	}
	if len(first) > 49 {
		t.Fatalf("allocation name length = %d, want at most 49", len(first))
	}
}
