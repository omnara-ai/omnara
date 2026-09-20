//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
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
	stores := newMaintenanceInboxStore(t, pool, ids)
	store := stores.Integrations()
	app := createMaintenanceInboxApp(t, stores, ids, "slack", appdefinition.Slack).ID
	var receipts []integrationstore.IntegrationInboxRecord
	plannedAgent, plannedArtifact := uuid.New(), uuid.New()
	for _, key := range []string{"one", "two"} {
		_, _, err := store.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID:  ids.ProjectID,
			AppID:      app,
			ReceiptKey: key,
			Payload:    []byte(`{"token":"private-provider-payload"}`),
		})
		require.NoError(t, err)
		receipt, found, err := store.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, AppID: app, LeaseDuration: time.Minute,
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

// Maintenance fixtures use ordinary credential/setup methods so active-app
// checks exercise the same state that ingress and recovery see in production.
func newMaintenanceInboxStore(t *testing.T, pool *pgxpool.Pool, ids storagefixture.ProjectIDs) *storage.Store {
	t.Helper()
	wrapper, err := secrets.NewLocalKeyWrapper("maintenance-test", map[string][]byte{
		"maintenance-test": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO org_memberships(org_id,user_id,role,created_at)
        VALUES($1,$2,'owner',now())`, ids.OrgID, ids.ProviderAdminUserID)
	require.NoError(t, err)
	return storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper))
}

func createMaintenanceInboxApp(
	t *testing.T, store *storage.Store, ids storagefixture.ProjectIDs, name, definitionID string,
) integrationstore.ProjectAppRecord {
	t.Helper()
	ctx := t.Context()
	input := integrationstore.ConfigureProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, InstalledByUserID: ids.ProviderAdminUserID,
	}
	var material secrets.Material
	switch definitionID {
	case appdefinition.Slack:
		input.Provider, input.ProviderTenantID, input.ProviderAccountRef = "slack", "T123", "A123"
		input.OAuthFlowID = uuid.Must(uuid.NewV7())
		material = secrets.SlackAppCredentialsMaterial{
			AccessToken:   "xoxb-maintenance",
			ClientID:      "maintenance",
			ClientSecret:  "fixture-client",
			SigningSecret: "fixture-signing",
		}
	case appdefinition.GitHub:
		input.Provider, input.ProviderTenantID, input.ProviderAccountRef = "github", "123", "456"
		input.CredentialAppID = 123
		material = secrets.GitHubAppCredentialsMaterial{
			AppID: "123", PrivateKey: "fixture-key-verified-by-caller", WebhookSecret: "fixture-webhook",
		}
	default:
		t.Fatalf("unsupported maintenance app definition %q", definitionID)
	}
	credential, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: ids.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: ids.ProjectID,
		Name: name + "-credentials", Actor: identitystore.NewUserPrincipal(ids.ProviderAdminUserID), Material: material,
	})
	require.NoError(t, err)
	app, err := store.Integrations().CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: name, DefinitionID: definitionID,
	})
	require.NoError(t, err)
	input.AppID, input.ExpectedSetupRevision = app.ID, app.SetupRevision
	input.CredentialSecretID, input.CredentialVersionID = credential.ID, version.ID
	app, err = store.Integrations().ConfigureProjectApp(ctx, input)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ProjectAppStateActive, app.State)
	require.Equal(t, credential.ID, app.CredentialSecretID)
	return app
}
