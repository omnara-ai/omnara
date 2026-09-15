//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestTransactionalChannelAccessFencesDefinitionPublication(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"reader_first", "publisher_first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f := newChannelAuthorityFixture(t, ctx, "definition-fence")
			grant := f.bindingInput("operation")
			grant.SendAllowed, grant.ReadAllowed = true, true
			_, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, grant)
			require.NoError(t, err)
			input := integrationstore.PublishChannelDefinitionInput{
				ProjectID: testProjectID, IntegrationInstallID: f.InstallID,
				ImplementationKey: f.Definition.ImplementationKey, Kind: f.Definition.Kind,
				SendParamsSchema:      json.RawMessage(`{"type":"object","required":["mode"],"properties":{"mode":{"const":"plain"}}}`),
				Capabilities:          integrationstore.ChannelCapabilities{Send: true, Text: true},
				ConnectorCapabilities: testChannelCapabilities(testChannelProvider),
			}
			reader, err := f.Store.pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = reader.Rollback(ctx) }()
			_, err = f.Store.Integrations().PrepareChannelBindingTx(ctx, reader, f.operationInput())
			require.NoError(t, err, "take scope/agent/binding locks before retaining the definition")
			if order == "reader_first" {
				access, err := f.Store.Integrations().GetAgentChannelAccessTx(ctx, reader, testProjectID, f.AgentID, f.Target.ID)
				require.NoError(t, err)
				require.JSONEq(t, string(f.Definition.SendParamsSchema), string(access.SendParamsSchema))
				require.True(t, access.Capabilities.Read)
				var readerPID int32
				require.NoError(t, reader.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&readerPID))
				done := make(chan error, 1)
				go func() {
					_, err := f.Store.Integrations().PublishConnectorChannelDefinition(ctx, input)
					done <- err
				}()
				integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "-- name: UpsertChannelDefinition ", readerPID)
				require.NoError(t, reader.Commit(ctx))
				require.NoError(t, <-done)
			} else {
				// Hold an already validated publication at its SQL mutation boundary.
				// The access helper initially sees the old committed row, then waits.
				writer, err := f.Store.pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = writer.Rollback(ctx) }()
				capabilities, err := json.Marshal(input.Capabilities)
				require.NoError(t, err)
				_, err = dbsqlc.New(writer).UpsertChannelDefinition(ctx, dbsqlc.UpsertChannelDefinitionParams{
					ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
					ImplementationKey: input.ImplementationKey, Kind: string(input.Kind),
					SendParamsSchema: input.SendParamsSchema, Capabilities: capabilities,
				})
				require.NoError(t, err)
				var writerPID int32
				require.NoError(t, writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID))
				type accessResult struct {
					access integrationstore.ChannelAccess
					err    error
				}
				done := make(chan accessResult, 1)
				go func() {
					access, err := f.Store.Integrations().GetAgentChannelAccessTx(ctx, reader, testProjectID, f.AgentID, f.Target.ID)
					done <- accessResult{access: access, err: err}
				}()
				integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "-- name: LockChannelDefinition ", writerPID)
				require.NoError(t, writer.Commit(ctx))
				result := <-done
				require.NoError(t, result.err)
				require.JSONEq(t, string(input.SendParamsSchema), string(result.access.SendParamsSchema),
					"waiting for publication must not return the pre-lock schema")
				require.False(t, result.access.Capabilities.Read, "capabilities are reread under the same definition lock")
				require.NoError(t, reader.Commit(ctx))
			}
			current, err := f.Store.Integrations().GetAgentChannelAccess(ctx, testProjectID, f.AgentID, f.Target.ID)
			require.NoError(t, err)
			require.JSONEq(t, string(input.SendParamsSchema), string(current.SendParamsSchema))
			require.False(t, current.Capabilities.Read)
		})
	}
}
