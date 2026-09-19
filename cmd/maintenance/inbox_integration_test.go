//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestInboxCommandsInspectRetryAndDiscardWithoutPayloadDisclosure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	store := storage.NewStore(pool).Integrations()
	connection := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO integration_connections
 (id,org_id,project_id,installed_by_user_id,provider,state,provider_tenant_id,
  provider_account_ref,created_at,updated_at)
 VALUES($1,$2,$3,$4,'slack','active','T123','A123',now(),now())`,
		connection, ids.OrgID, ids.ProjectID, ids.ProviderAdminUserID)
	require.NoError(t, err)
	var receipts []integrationstore.IntegrationInboxRecord
	plannedAgent, plannedArtifact := uuid.New(), uuid.New()
	for _, key := range []string{"one", "two"} {
		_, _, err := store.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID:    ids.ProjectID,
			ConnectionID: connection,
			ReceiptKey:   key,
			Payload:      []byte(`{"token":"private-provider-payload"}`),
		})
		require.NoError(t, err)
		receipt, found, err := store.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, ConnectionID: connection, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		plan, err := json.Marshal(map[string]any{"slot": map[string]any{
			"agent_id": plannedAgent, "artifact_ids": []uuid.UUID{plannedArtifact},
			"launch": map[string]any{"compiled_config": "private-config"},
			"input":  map[string]any{"text": "private-message"},
		}})
		require.NoError(t, err)
		require.NoError(
			t,
			store.WithIntegrationInboxLease(
				ctx,
				receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					if err := work.FreezePlan(ctx, plan); err != nil {
						return err
					}
					if err := work.PrepareSlot(
						ctx,
						"slot",
						json.RawMessage(`{"private":"private-preparation"}`),
					); err != nil {
						return err
					}
					return work.Fail(ctx, "diagnosable failure")
				},
			),
		)
		receipt, err = store.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
		require.NoError(t, err)
		receipts = append(receipts, receipt)
	}
	project, err := publicid.Encode(publicid.KindProject, ids.ProjectID)
	require.NoError(t, err)
	run := func(action string, flags ...string) map[string]json.RawMessage {
		t.Helper()
		args := append([]string{action, "--project", project}, flags...)
		command, err := parseInboxCommand(args, io.Discard)
		require.NoError(t, err)
		var output bytes.Buffer
		require.NoError(t, command.run(ctx, store, &output))
		for _, secret := range []string{
			"private-provider-payload", "private-config", "private-message", "private-preparation",
		} {
			require.NotContains(t, output.String(), secret)
		}
		var result map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(output.Bytes(), &result))
		return result
	}
	first := run("list", "--limit", "1")
	var cursor string
	require.NoError(t, json.Unmarshal(first["next"], &cursor))
	require.NotEmpty(t, cursor)
	second := run("list", "--limit", "1", "--after", cursor)
	require.NotEqual(t, first["receipts"], second["receipts"])
	require.NotContains(t, second, "next")
	shown := run("show", "--receipt", receipts[0].ID.String())
	var slots []inboxSlotView
	require.NoError(t, json.Unmarshal(shown["slots"], &slots))
	require.Len(t, slots, 1)
	require.Equal(t, plannedAgent, *slots[0].AgentID)
	require.Equal(t, []uuid.UUID{plannedArtifact}, slots[0].ArtifactIDs)
	require.True(t, slots[0].Prepared)
	require.False(t, slots[0].Committed)
	run("retry", "--receipt", receipts[0].ID.String())
	retried, err := store.GetIntegrationInbox(ctx, ids.ProjectID, receipts[0].ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxPending, retried.State)
	require.Equal(t, receipts[0].Plan, retried.Plan)
	require.Equal(t, receipts[0].Progress, retried.Progress)
	discard := run("discard", "--receipt", receipts[1].ID.String(), "--reason", "replaced failed selection")
	require.JSONEq(t, `"replaced failed selection"`, string(discard["reason"]))
	discarded, err := store.GetIntegrationInbox(ctx, ids.ProjectID, receipts[1].ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxDiscarded, discarded.State)
	repeated := run("discard", "--receipt", receipts[1].ID.String(), "--reason", "retry cleanup only")
	require.Equal(t, json.RawMessage(`true`), repeated["already_discarded"])
	unchanged, err := store.GetIntegrationInbox(ctx, ids.ProjectID, receipts[1].ID)
	require.NoError(t, err)
	require.Equal(t, discarded, unchanged)
	run("list", "--state", "discarded")
}
