//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func postDaemonSleepForTest(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("build sleep request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post sleep: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode sleep response: %v", err)
	}
	return response.StatusCode, body
}

func TestDaemonSleepRouteJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()))
	server := mustNewServer(t, store)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-sleep-route")
	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	fixture := createDaemonProcessFixture(t, ctx, pool, store, project, now, "daemon-sleep-route", "run_command")
	sleepURL := httpServer.URL + "/api/v1/daemon/runtimes/" + fixture.RuntimeID + "/sleep"

	status, body := postDaemonSleepForTest(t, sleepURL, fixture.Token)
	if status != http.StatusConflict || body["code"] != "pending_work" {
		t.Fatalf("sleep with queued process = %d %+v, want 409 pending_work", status, body)
	}

	if _, accepted, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		fixture.authority(),
		fixture.ProcessUUID,
	); err != nil || !accepted {
		t.Fatalf("accept process offer = %v/%v", accepted, err)
	}
	var sourceAt time.Time
	if err := pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&sourceAt); err != nil {
		t.Fatalf("read database time after accepting process: %v", err)
	}
	exitCode := 0
	applyDaemonReportForTest(t, ctx, store, project, fixture, daemonReportedEvent{
		Type:      "process_finished",
		ProcessID: fixture.ProcessID,
		State:     "exited",
		ExitCode:  &exitCode,
		StartedAt: sourceAt,
		EndedAt:   sourceAt,
	})

	status, body = postDaemonSleepForTest(t, sleepURL, fixture.Token)
	if status != http.StatusConflict || body["code"] != "not_wake_capable" {
		t.Fatalf("sleep without sandbox url = %d %+v, want 409 not_wake_capable", status, body)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET sandbox_url = 'https://sleep-route.test/' WHERE org_id = $1 AND id = $2`,
		project.OrgUUID,
		fixture.MachineUUID,
	); err != nil {
		t.Fatalf("set machine sandbox url: %v", err)
	}

	status, body = postDaemonSleepForTest(t, sleepURL, fixture.Token)
	if status != http.StatusOK || body["state"] != "ended" || body["state_reason_code"] != "machine_asleep" {
		t.Fatalf("sleep = %d %+v, want 200 ended/machine_asleep", status, body)
	}
	machine, err := store.Execution().GetMachine(ctx, project.OrgUUID, fixture.MachineUUID)
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	if machine.ConnectionState != "asleep" {
		t.Fatalf("machine connection state = %q, want asleep", machine.ConnectionState)
	}

	status, _ = postDaemonSleepForTest(t, sleepURL, fixture.Token)
	if status != http.StatusGone {
		t.Fatalf("sleep on ended runtime = %d, want 410", status)
	}

	if _, err := store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            project.OrgUUID,
			MachineID:        fixture.MachineUUID,
			DaemonTokenID:    daemonTokenIDForSleepTest(t, ctx, pool, project.OrgUUID, fixture.MachineUUID),
			DaemonInstanceID: httpTestID("daemon-sleep-route-waker"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("register daemon runtime after wake: %v", err)
	}
	machine, err = store.Execution().GetMachine(ctx, project.OrgUUID, fixture.MachineUUID)
	if err != nil {
		t.Fatalf("get machine after wake: %v", err)
	}
	if machine.ConnectionState != "online" {
		t.Fatalf("machine connection state after wake = %q, want online", machine.ConnectionState)
	}
}

func daemonTokenIDForSleepTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgID, machineID storage.ID,
) storage.ID {
	t.Helper()
	var id storage.ID
	if err := pool.QueryRow(
		ctx,
		`SELECT id FROM machine_daemon_tokens WHERE org_id = $1 AND machine_id = $2 AND revoked_at IS NULL LIMIT 1`,
		orgID,
		machineID,
	).Scan(&id); err != nil {
		t.Fatalf("load daemon token: %v", err)
	}
	return id
}
