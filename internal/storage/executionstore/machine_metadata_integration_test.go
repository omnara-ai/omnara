//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestMachineMetadataRequiresJSONObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))

	machine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Object Metadata Machine",
		IdempotencyKey: "idem-object-metadata-machine",
		Metadata:       json.RawMessage(`{"team":"infra","nested":{"ok":true}}`),
	})
	if err != nil {
		t.Fatalf("create machine with object metadata: %v", err)
	}
	if !sameJSON(machine.Metadata, json.RawMessage(`{"team":"infra","nested":{"ok":true}}`)) {
		t.Fatalf("machine metadata = %s", machine.Metadata)
	}

	_, err = store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Bad Metadata Machine",
		IdempotencyKey: "idem-bad-metadata-machine",
		Metadata:       json.RawMessage(`[]`),
	})
	if err == nil || !strings.Contains(err.Error(), "machine metadata") {
		t.Fatalf("create machine invalid metadata error = %v", err)
	}

	_, _, err = store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachineID:      machine.ID,
		IdempotencyKey: "idem-bad-grant-metadata",
		Metadata:       json.RawMessage(`[]`),
	})
	if err == nil || !strings.Contains(err.Error(), "project machine grant metadata") {
		t.Fatalf("create project machine grant invalid metadata error = %v", err)
	}

	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(t, ctx, store, machinePoolInputWithDefaultMachineForTest(
			executionstore.CreateMachinePoolInput{
				OrgID:              testOrgID,
				Name:               "Metadata Pool",
				Provider:           "test.provider",
				MaxTotalMachines:   1,
				MaxTotalCPU:        intPtrForMachinePoolTest(32),
				MaxTotalMemoryMB:   intPtrForMachinePoolTest(65536),
				MaxMachineCPU:      intPtrForMachinePoolTest(32),
				MaxMachineMemoryMB: intPtrForMachinePoolTest(65536),
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineEnv:             json.RawMessage(`{}`),
				DefaultMachineProviderOptions: json.RawMessage(`{}`),
			},
		)))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	_, err = store.Execution().CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachinePoolID:  machinePool.ID,
		IdempotencyKey: "idem-bad-pool-grant-metadata",
		Metadata:       json.RawMessage(`[]`),
	})

	if err == nil || !strings.Contains(err.Error(), "project machine pool grant metadata") {
		t.Fatalf("create project machine pool grant invalid metadata error = %v", err)
	}

	var badMachineRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM machines WHERE org_id = $1 AND display_name = 'Bad Metadata Machine'`, testOrgID).
		Scan(&badMachineRows); err != nil {
		t.Fatalf("count invalid machine rows: %v", err)
	}
	if badMachineRows != 0 {
		t.Fatalf("invalid metadata persisted %d machine rows", badMachineRows)
	}
}
