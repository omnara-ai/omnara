//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestMachineFailureRoute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	handler := newIntegrationHTTPHandler(mustNewServer(t, store).Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "machine-failure-report")
	now := time.Now().UTC()

	var machinePoolID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO machine_pools(
			org_id, name, management_kind, provider, default_machine_memory_mb,
			provider_auth_env_var, max_total_machines, max_total_memory_mb,
			max_machine_memory_mb, created_at, updated_at
		)
		VALUES ($1, 'machine-failure-report', 'cluster', 'test', 1024,
			'TEST_PROVIDER_TOKEN', 1, 1024, 1024, $2, $2)
		RETURNING id
	`, project.OrgUUID, now).Scan(&machinePoolID); err != nil {
		t.Fatalf("insert machine pool fixture: %v", err)
	}
	var machineID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO machines(
			org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
			lifecycle_changed_at, memory_mb, cwd, env, secret_env, provider_options, metadata,
			next_reconcile_after, provision_attempts, created_at, updated_at
		)
		VALUES ($1, $2, 'pool', 'failure report machine', 'test', 'provisioning',
			$3, 1024, '', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb,
			$3, 1, $3, $3)
		RETURNING id
	`, project.OrgUUID, machinePoolID, now).Scan(&machineID); err != nil {
		t.Fatalf("insert provisioning machine fixture: %v", err)
	}
	provisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            project.OrgUUID,
			MachineID:        machineID,
			ProvisionAttempt: 1,
			TokenName:        "failure reporter",
		},
	)
	if err != nil {
		t.Fatalf("begin provider provisioning: %v", err)
	}
	path := "/api/v1/daemon/failures"
	requestFailure := func(stage string, exitStatus, captureStatus int, output, token string, wantStatus int) {
		t.Helper()
		req := httptest.NewRequest(
			http.MethodPost,
			fmt.Sprintf(
				"%s?stage=%s&exit_status=%d&capture_status=%d",
				path,
				stage,
				exitStatus,
				captureStatus,
			),
			strings.NewReader(output),
		)
		req.Header.Set("Content-Type", "text/plain")
		for key, value := range authHeaders(token) {
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("failure request status=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
		}
	}
	firstOutput := "zfirst\x00\xff" + strings.Repeat(
		"x",
		executionstore.MaxMachineFailureReportOutputBytes-len("first\x00\xff"),
	)
	requestFailure("startup_script", 7, 0, firstOutput, provisioning.DaemonToken.Token, http.StatusNoContent)
	var firstFailure []byte
	if err := pool.QueryRow(
		ctx,
		`SELECT failure_report FROM machines WHERE org_id = $1 AND id = $2`,
		project.OrgUUID,
		machineID,
	).Scan(&firstFailure); err != nil {
		t.Fatalf("load failure report: %v", err)
	}
	var stored struct {
		Stage           string    `json:"stage"`
		ExitStatus      int       `json:"exit_status"`
		OutputTail      string    `json:"output_tail"`
		OutputTruncated bool      `json:"output_truncated"`
		ReportedAt      time.Time `json:"reported_at"`
	}
	if err := json.Unmarshal(firstFailure, &stored); err != nil {
		t.Fatalf("decode failure report: %v", err)
	}
	if stored.Stage != "startup_script" || stored.ExitStatus != 7 ||
		len(stored.OutputTail) != executionstore.MaxMachineFailureReportOutputBytes ||
		!strings.HasPrefix(stored.OutputTail, "first??") || strings.ContainsRune(stored.OutputTail, '\x00') ||
		!stored.OutputTruncated || stored.ReportedAt.IsZero() {
		t.Fatalf("stored failure report = %+v", stored)
	}

	requestFailure("daemon_install", 9, 0, "", provisioning.DaemonToken.Token, http.StatusNoContent)
	var replayedFailure []byte
	if err := pool.QueryRow(
		ctx,
		`SELECT failure_report FROM machines WHERE org_id = $1 AND id = $2`,
		project.OrgUUID,
		machineID,
	).Scan(&replayedFailure); err != nil {
		t.Fatalf("load replayed failure report: %v", err)
	}
	if err := json.Unmarshal(replayedFailure, &stored); err != nil {
		t.Fatalf("decode replayed failure report: %v", err)
	}
	if stored.Stage != "daemon_install" || stored.ExitStatus != 9 || stored.OutputTail != "" ||
		stored.OutputTruncated {
		t.Fatalf("stored replayed failure report = %+v", stored)
	}

	requestFailure(
		"startup_script",
		7,
		0,
		strings.Repeat("x", executionstore.MaxMachineFailureReportOutputBytes+2),
		provisioning.DaemonToken.Token,
		http.StatusBadRequest,
	)
	requestFailure("bogus", 7, 0, "tail", provisioning.DaemonToken.Token, http.StatusBadRequest)
	requestFailure("startup_script", 7, 0, "tail", "invalid-machine-token", http.StatusUnauthorized)

	if _, err := store.Execution().RecordPoolMachineProvisioningResource(
		ctx,
		executionstore.RecordPoolMachineProvisioningResourceInput{
			OrgID:              project.OrgUUID,
			MachineID:          machineID,
			ProviderResourceID: "bootstrap-failure-resource",
			ProvisionAttempt:   1,
		},
	); err != nil {
		t.Fatalf("record provider resource: %v", err)
	}
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		project.OrgUUID,
		machineID,
		"bootstrap-failure-resource",
		"",
		1,
	); err != nil {
		t.Fatalf("complete provisioning: %v", err)
	}
	var activeFailure []byte
	if err := pool.QueryRow(
		ctx,
		`SELECT failure_report FROM machines WHERE org_id = $1 AND id = $2`,
		project.OrgUUID,
		machineID,
	).Scan(&activeFailure); err != nil {
		t.Fatalf("load active machine failure report: %v", err)
	}
	if string(activeFailure) != string(replayedFailure) {
		t.Fatalf("provisioning completion changed failure report: before=%s after=%s", replayedFailure, activeFailure)
	}

	byoMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          project.OrgUUID,
			DisplayName:    "BYO failure report machine",
			IdempotencyKey: "byo-failure-report-machine",
		},
	)
	if err != nil {
		t.Fatalf("create BYO machine: %v", err)
	}
	byoToken := executionstore.MachineDaemonTokenPlaintextPrefix + "byo-failure-report"
	if _, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     project.OrgUUID,
			MachineID: byoMachine.ID,
			Name:      "failure reporter",
			Token:     byoToken,
		},
	); err != nil {
		t.Fatalf("create BYO machine token: %v", err)
	}
	requestFailure("daemon_install", 11, 0, "BYO install failed", byoToken, http.StatusNoContent)
	var byoFailure []byte
	if err := pool.QueryRow(
		ctx,
		`SELECT failure_report FROM machines WHERE org_id = $1 AND id = $2`,
		project.OrgUUID,
		byoMachine.ID,
	).Scan(&byoFailure); err != nil {
		t.Fatalf("load BYO machine failure report: %v", err)
	}
	if err := json.Unmarshal(byoFailure, &stored); err != nil {
		t.Fatalf("decode BYO machine failure report: %v", err)
	}
	if stored.Stage != "daemon_install" || stored.ExitStatus != 11 ||
		stored.OutputTail != "BYO install failed" || stored.OutputTruncated || stored.ReportedAt.IsZero() {
		t.Fatalf("stored BYO machine failure report = %+v", stored)
	}
}
