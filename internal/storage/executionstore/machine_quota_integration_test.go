//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func assertMachineQuotaTail(t *testing.T, statements []string) {
	t.Helper()
	quota := slices.IndexFunc(statements, func(sql string) bool {
		return strings.HasPrefix(sql, "-- name: LockResourceCreation ")
	})
	require.Positive(t, quota)
	require.Equal(t, "SET CONSTRAINTS ALL IMMEDIATE", statements[quota-1])
	var tail []string
	for _, sql := range statements[quota+1:] {
		tail = append(tail, strings.Split(sql, "\n")[0])
	}
	require.Equal(t, []string{"-- name: GetEffectiveResourceLimits :one",
		"-- name: CountLiveMachinesForOrg :one", "commit"}, tail)
}

func TestDaemonMachineQuotaFinalPhase(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	var statements []string
	config := pool.Config()
	config.ConnConfig.Tracer = &integrationdb.QueryTrace{OnQuery: func(sql string) {
		statements = append(statements, sql)
	}}
	traced, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer traced.Close()
	_, err = newIntegrationStore(traced).Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID: testOrgID, DisplayName: "quota machine",
	})
	require.NoError(t, err)
	assertMachineQuotaTail(t, statements)
}

func TestConcurrentMachineQuotaExactness(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	config := pool.Config()
	config.MaxConns = 12
	machines, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer machines.Close()
	store := newIntegrationStore(machines)
	const attempts = 8
	for i := range attempts - 1 {
		_, err = store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
			OrgID: testOrgID, DisplayName: fmt.Sprintf("existing %d", i),
		})
		require.NoError(t, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO org_resource_limit_overrides(org_id,max_live_machines_per_org) VALUES($1,$2)`,
		testOrgID, attempts)
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, ctx, pool)
	require.NoError(t, dbsqlc.New(blocker).LockResourceCreation(ctx, dbsqlc.LockResourceCreationParams{
		ResourceKind: "machines", Scope: testOrgID.String(),
	}))
	outcomes := make(chan error, attempts)
	for i := range attempts {
		go func() {
			var err error
			if i%2 == 0 {
				_, err = store.Execution().ConnectBYOMachine(ctx, executionstore.ConnectBYOMachineInput{
					OrgID: testOrgID, DisplayName: fmt.Sprintf("racing %d", i),
					ProjectIDs: []uuid.UUID{testProjectID}, TokenName: "quota token",
				})
			} else {
				_, err = store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
					OrgID: testOrgID, DisplayName: fmt.Sprintf("racing %d", i),
				})
			}
			outcomes <- err
		}()
	}
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockResourceCreation", attempts)
	require.NoError(t, blocker.Commit(ctx))
	succeeded := 0
	for range attempts {
		err := integrationdb.Await(t, outcomes, "machine quota")
		if err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, storeerr.ErrConflict)
			require.EqualError(t, err, "live machines limit of 8 reached: resource conflict")
		}
	}
	require.Equal(t, 1, succeeded)
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM machines").Scan(&count))
	require.Equal(t, attempts, count)
	for _, table := range []string{"project_machine_grants", "machine_daemon_tokens"} {
		require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count))
		require.LessOrEqual(t, count, 1, table)
	}
}

func TestConnectMachineQuotaFinalPhase(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	var statements []string
	config := pool.Config()
	config.ConnConfig.Tracer = &integrationdb.QueryTrace{OnQuery: func(sql string) {
		statements = append(statements, sql)
	}}
	traced, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer traced.Close()
	connected, err := newIntegrationStore(traced).Execution().ConnectBYOMachine(ctx, executionstore.ConnectBYOMachineInput{
		OrgID: testOrgID, DisplayName: "connected quota", ProjectIDs: []uuid.UUID{testProjectID}, TokenName: "quota token",
	})
	require.NoError(t, err)
	quota := slices.IndexFunc(statements, func(sql string) bool {
		return strings.HasPrefix(sql, "-- name: LockResourceCreation ")
	})
	require.Positive(t, quota)
	require.Equal(t, "SET CONSTRAINTS ALL IMMEDIATE", statements[quota-1])
	var tail []string
	for _, sql := range statements[quota:] {
		tail = append(tail, strings.Split(sql, "\n")[0])
	}
	require.Equal(t, []string{
		"-- name: LockResourceCreation :exec", "-- name: LockResourceCreation :exec",
		"-- name: GetEffectiveResourceLimits :one", "-- name: CountActiveMachineDaemonTokensForMachine :one",
		"-- name: CountLiveMachinesForOrg :one", "commit",
	}, tail)
	statements = nil
	_, err = newIntegrationStore(traced).Execution().CreateBYOMachineDaemonToken(ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID: testOrgID, MachineID: connected.Machine.ID, Name: "second quota token",
		})
	require.NoError(t, err)
	quota = slices.IndexFunc(statements, func(sql string) bool {
		return strings.HasPrefix(sql, "-- name: LockResourceCreation ")
	})
	require.Positive(t, quota)
	require.Equal(t, "SET CONSTRAINTS ALL IMMEDIATE", statements[quota-1])
	tail = nil
	for _, sql := range statements[quota:] {
		tail = append(tail, strings.Split(sql, "\n")[0])
	}
	require.Equal(t, []string{
		"-- name: LockResourceCreation :exec", "-- name: GetEffectiveResourceLimits :one",
		"-- name: CountActiveMachineDaemonTokensForMachine :one", "commit",
	}, tail)
}
