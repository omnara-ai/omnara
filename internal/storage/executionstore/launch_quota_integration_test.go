//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestLaunchQuotaFinalPhase(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "quota", "Quota", subagentParentYAML)
	parent, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: testProjectID, AgentConfigID: profile.CurrentConfigID, LaunchedBy: systemPrincipalForTest(testOrgID)})
	require.NoError(t, err)
	var statements []string
	trace := &integrationdb.QueryTrace{OnQuery: func(sql string) { statements = append(statements, sql) }}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer traced.Close()
	_, err = spawnSubagentForTest(t, ctx, newIntegrationStore(traced), parent.Agent,
		profile.CurrentConfigID, "child", "final-quota", nil)
	require.NoError(t, err)
	quota := slices.IndexFunc(statements, func(sql string) bool {
		return strings.HasPrefix(sql, "-- name: LockResourceCreation ")
	})
	require.Positive(t, quota)
	require.Equal(t, "SET CONSTRAINTS ALL IMMEDIATE", statements[quota-1])
	require.Len(t, statements[quota+1:], 3)
	require.True(t, strings.HasPrefix(statements[quota+1], "-- name: GetEffectiveResourceLimits "))
	require.True(t, strings.HasPrefix(statements[quota+2], "-- name: CountActiveAgentsForProject "))
	require.Equal(t, "commit", statements[quota+3])
}

func TestLaunchQuotaWaitDoesNotBlockOtherParent(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newIntegrationStore(pool)
			profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "quota", "Quota", subagentParentYAML)
			parents := make([]executionstore.AgentRecord, 2)
			for i := range parents {
				launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
					ProjectID: testProjectID, AgentConfigID: profile.CurrentConfigID, LaunchedBy: systemPrincipalForTest(testOrgID)})
				require.NoError(t, err)
				parents[i] = launch.Agent
			}
			slices.SortFunc(parents, func(a, b executionstore.AgentRecord) int {
				return strings.Compare(a.ID.String(), b.ID.String())
			})
			if reverse {
				slices.Reverse(parents)
			}
			blocker := integrationdb.BeginTx(t, ctx, pool)
			require.NoError(t, dbsqlc.New(blocker).LockResourceCreation(ctx, dbsqlc.LockResourceCreationParams{
				ResourceKind: "agents", Scope: testProjectID.String(),
			}))
			var done []<-chan integrationdb.AsyncResult[executionstore.LaunchAgentResult]
			for i, parent := range parents {
				done = append(done, integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
					return store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
						ProjectID: testProjectID, AgentConfigID: profile.CurrentConfigID, LaunchedBy: systemPrincipalForTest(parent.ID),
						Subagent: &executionstore.SubagentLaunch{ParentAgentID: parent.ID, Key: "child", MaxDepth: 3}})
				}))
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockResourceCreation", i+1)
			}
			require.NoError(t, blocker.Commit(ctx))
			for _, result := range done {
				require.NoError(t, integrationdb.Await(t, result, "launch quota").Err)
			}
		})
	}
}

func TestConcurrentLaunchQuotaExactness(t *testing.T) {
	for _, configSource := range []bool{false, true} {
		t.Run(fmt.Sprintf("config_source=%t", configSource), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			config := pool.Config()
			config.MaxConns = 12
			launchPool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer launchPool.Close()
			store := newIntegrationStore(pool)
			profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "quota", "Quota", subagentParentYAML)
			const launches = 8
			for range launches - 1 {
				_, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
					ProjectID: testProjectID, AgentConfigID: profile.CurrentConfigID, LaunchedBy: systemPrincipalForTest(testOrgID)})
				require.NoError(t, err)
			}
			quotaKind := "agents"
			if configSource {
				quotaKind = "agent_configs"
				_, err = pool.Exec(ctx, `INSERT INTO org_resource_limit_overrides(org_id,max_agent_configs_per_project)
		VALUES($1,2)`, testOrgID)
			} else {
				_, err = pool.Exec(ctx, `INSERT INTO org_resource_limit_overrides(org_id,max_active_agents_per_project)
		VALUES($1,$2)`, testOrgID, launches)
			}
			require.NoError(t, err)
			blocker := integrationdb.BeginTx(t, ctx, pool)
			require.NoError(t, dbsqlc.New(blocker).LockResourceCreation(ctx, dbsqlc.LockResourceCreationParams{
				ResourceKind: quotaKind, Scope: testProjectID.String(),
			}))
			outcomes := make(chan error, launches)
			for i := range launches {
				input := executionstore.LaunchAgentInput{
					ProjectID: testProjectID, ProfileID: profile.ID,
					LaunchedBy: systemPrincipalForTest(testOrgID), IdempotencyKey: fmt.Sprint(i),
				}
				if configSource {
					source := strings.Replace(subagentParentYAML, "Coordinate helpers.", fmt.Sprintf("Helper %d.", i), 1)
					compiled := mustCompileAgentYAMLResolved(t, ctx, store, source)
					input.DerivedConfig = &executionstore.CreateAgentConfigInput{
						ProjectID: testProjectID, Source: source, ConfiguredModelID: parseConfiguredModelID(t, compiled),
						CompiledDefinition: compiled.CanonicalJSON, EffectiveDefinitionHash: compiled.Hash,
					}
				}
				go func() {
					_, err := newIntegrationStore(launchPool).Execution().IntegrationLaunchAgentOnce(ctx, input)
					outcomes <- err
				}()
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockResourceCreation", launches)
			require.NoError(t, blocker.Commit(ctx))
			var succeeded int
			for range launches {
				err := integrationdb.Await(t, outcomes, "quota result")
				if err == nil {
					succeeded++
				} else {
					require.ErrorIs(t, err, storeerr.ErrConflict)
				}
			}
			require.Equal(t, 1, succeeded)
			for _, table := range []string{"agents", "agent_execution_state", "agent_inputs", "agent_events"} {
				var count int
				require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count))
				require.Equal(t, launches, count, table)
			}
			var configs int
			require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM agent_configs").Scan(&configs))
			if configSource {
				require.Equal(t, 2, configs)
			} else {
				require.Equal(t, 1, configs)
			}
		})
	}
}

func TestSubagentSourcesSharedWithLaunchExclusiveWithConfig(t *testing.T) {
	for _, operation := range []string{"launch", "config"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newIntegrationStore(pool)
			profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "sources", "Sources", subagentParentYAML)
			parent, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: testProjectID, AgentConfigID: profile.CurrentConfigID, LaunchedBy: systemPrincipalForTest(testOrgID)})
			require.NoError(t, err)
			held, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			seen, sources := false, false
			trace := &integrationdb.QueryTrace{OnQuery: func(sql string) {
				if strings.HasPrefix(sql, "-- name: LockAgentMachineSources") {
					sources = true
				}
				if seen || !sources || !strings.HasPrefix(sql, "-- name: GetAgentConfig ") {
					return
				}
				seen = true
				close(held)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}}
			config := pool.Config()
			config.ConnConfig.Tracer = trace
			traced, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer traced.Close()
			defer unblock()
			first := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
				return spawnSubagentForTest(
					t, ctx, newIntegrationStore(traced), parent.Agent, profile.CurrentConfigID, "one", "one", nil,
				)
			})
			select {
			case <-held:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if operation == "launch" {
				_, err := spawnSubagentForTest(t, ctx, store, parent.Agent, profile.CurrentConfigID, "two", "two", nil)
				require.NoError(t, err)
				unblock()
			} else {
				writer := integrationdb.RunAsyncError(func() error {
					_, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
						CreateAgentConfigInput: changeInputFromRecord(parent.AgentConfig),
						AgentID:                parent.Agent.ID, ActorType: "system", Reason: "update", IdempotencyKey: "config",
					})
					return err
				})
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentMachineSources", 1)
				unblock()
				require.NoError(t, integrationdb.Await(t, writer, "config source gate"))
			}
			require.NoError(t, integrationdb.Await(t, first, "first launch").Err)
		})
	}
}

func TestDerivedLaunchQuotaChecks(t *testing.T) {
	for _, subagent := range []bool{false, true} {
		t.Run(fmt.Sprintf("subagent=%t", subagent), func(t *testing.T) {
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newIntegrationStore(pool)
			profile := mustCreateConfigAndProfileBookmarkFromYAML(
				t, ctx, store, "derived-quota", "Derived quota", subagentParentYAML,
			)
			parent, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: testProjectID, AgentConfigID: profile.CurrentConfigID, LaunchedBy: systemPrincipalForTest(testOrgID)})
			require.NoError(t, err)
			source := strings.Replace(subagentParentYAML, "Coordinate helpers.", "Coordinate different helpers.", 1)
			compiled := mustCompileAgentYAMLResolved(t, ctx, store, source)
			derived := executionstore.CreateAgentConfigInput{ProjectID: testProjectID, Source: source,
				ConfiguredModelID: parseConfiguredModelID(t, compiled), CompiledDefinition: compiled.CanonicalJSON,
				EffectiveDefinitionHash: compiled.Hash}
			_, err = pool.Exec(ctx, `INSERT INTO org_resource_limit_overrides(org_id,max_agent_configs_per_project)
		VALUES($1,(SELECT count(*) FROM agent_configs WHERE project_id=$2))`, testOrgID, testProjectID)
			require.NoError(t, err)
			input := executionstore.LaunchAgentInput{ProjectID: testProjectID, DerivedConfig: &derived,
				ProfileID: profile.ID, LaunchedBy: systemPrincipalForTest(testOrgID)}
			if subagent {
				input.LaunchedBy = systemPrincipalForTest(parent.Agent.ID)
				input.Subagent = &executionstore.SubagentLaunch{ParentAgentID: parent.Agent.ID, Key: "child", MaxDepth: 3}
			}
			_, err = store.Execution().LaunchAgent(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrConflict)
			_, err = store.Execution().CreateAgentConfig(ctx, derived)
			require.ErrorIs(t, err, storeerr.ErrConflict)
			for _, table := range []string{"agents", "agent_configs", "agent_execution_state", "agent_inputs", "agent_events"} {
				var count int
				require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count))
				require.Equal(t, 1, count, table)
			}
			_, err = pool.Exec(ctx, `DELETE FROM org_resource_limit_overrides WHERE org_id=$1`, testOrgID)
			require.NoError(t, err)
			var tail []string
			var quota bool
			trace := &integrationdb.QueryTrace{OnQuery: func(sql string) {
				if strings.HasPrefix(sql, "-- name: LockResourceCreation ") {
					quota = true
				}
				if quota {
					tail = append(tail, strings.Split(sql, "\n")[0])
				}
			}}
			config := pool.Config()
			config.ConnConfig.Tracer = trace
			traced, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer traced.Close()
			_, err = newIntegrationStore(traced).Execution().LaunchAgent(ctx, input)
			require.NoError(t, err)
			require.Equal(t, []string{
				"-- name: LockResourceCreation :exec", "-- name: LockResourceCreation :exec",
				"-- name: GetEffectiveResourceLimits :one", "-- name: CountAgentConfigsForProject :one",
				"-- name: CountActiveAgentsForProject :one", "commit",
			}, tail)
		})
	}
}
