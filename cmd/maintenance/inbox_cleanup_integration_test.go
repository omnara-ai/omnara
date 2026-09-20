//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestInboxCleanupWarningAndTerminalRetry(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	store := newMaintenanceInboxStore(t, pool, ids)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	agentID, artifactID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO agents
 (id,org_id,project_id,state,current_config_id,created_at,updated_at)
 VALUES($1,$2,$3,'active',$4,now(),now())`, agentID, ids.OrgID, ids.ProjectID, base.ID)
	require.NoError(t, err)
	appID := createMaintenanceInboxApp(t, store, ids, "slack", appdefinition.Slack).ID
	inbox := store.Integrations()
	_, _, err = inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, AppID: appID, ReceiptKey: "cleanup",
		Payload: []byte(`{"private":"provider-payload"}`),
	})
	require.NoError(t, err)
	receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, AppID: appID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	plan, err := json.Marshal(integration.AppInboxPlan{"input": {
		AgentID: agentID, ArtifactIDs: []uuid.UUID{artifactID},
		Input: &executionstore.CreateAgentContentInputInput{ProjectID: ids.ProjectID, AgentID: agentID},
	}})
	require.NoError(t, err)
	require.NoError(t, inbox.WithIntegrationInboxLease(ctx, receipt.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if err := work.FreezePlan(ctx, plan); err != nil {
				return err
			}
			return work.Fail(ctx, "input admission failed after upload")
		}))
	project, err := publicid.Encode(publicid.KindProject, ids.ProjectID)
	require.NoError(t, err)
	args := []string{"inbox", "discard", "--project", project, "--receipt", receipt.ID.String(),
		"--reason", "abandon failed input"}
	// pgx retains the original DSN even after the fixture switches Database to
	// its isolated clone. Point the command's independent pool at that clone.
	databaseURL, err := url.Parse(pool.Config().ConnString())
	require.NoError(t, err)
	databaseURL.Path = "/" + pool.Config().ConnConfig.Database
	t.Setenv("OMNARA_DATABASE_URL", databaseURL.String())
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "")
	t.Setenv("OMNARA_REDIS_URL", "not-a-redis-endpoint")
	var output, diagnostics bytes.Buffer
	// Missing blob configuration does not prevent durable recovery or start
	// any network client. The retained receipt allows cleanup to be retried.
	require.NoError(t, runInboxCLI(ctx, args, &output, &diagnostics))
	require.Contains(t, diagnostics.String(), "discard remains committed")
	require.Contains(t, diagnostics.String(), "retry the same discard command")
	discarded, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxDiscarded, discarded.State)

	type observation struct {
		state        integrationstore.IntegrationInboxState
		method, path string
		err          error
	}
	requests := make(chan observation, 4)
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current, readErr := inbox.GetIntegrationInbox(r.Context(), ids.ProjectID, receipt.ID)
		requests <- observation{state: current.State, method: r.Method, path: r.URL.Path, err: readErr}
		if fail.Load() {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>fixture denied</Message></Error>`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "inbox-cleanup-test")
	t.Setenv("OMNARA_BLOB_S3_REGION", "us-east-1")
	t.Setenv("OMNARA_BLOB_S3_ENDPOINT", server.URL)
	t.Setenv("OMNARA_BLOB_S3_ACCESS_KEY_ID", "fixture-access-key")
	t.Setenv("OMNARA_BLOB_S3_SECRET_ACCESS_KEY", "fixture-secret-key")
	t.Setenv("OMNARA_BLOB_S3_USE_PATH_STYLE", "1")
	for _, shouldFail := range []bool{true, false} {
		fail.Store(shouldFail)
		output.Reset()
		diagnostics.Reset()
		require.NoError(t, runInboxCLI(ctx, args, &output, &diagnostics))
		require.Contains(t, output.String(), `"already_discarded":true`)
		require.Len(t, requests, 1, "one cleanup request per explicit retry: %s", diagnostics.String())
		request := <-requests
		require.NoError(t, request.err)
		require.Equal(t, integrationstore.IntegrationInboxDiscarded, request.state,
			"blob I/O observes committed discard from a separate database connection")
		require.Equal(t, http.MethodDelete, request.method)
		require.Equal(t, "/inbox-cleanup-test/artifacts/"+agentID.String()+"/"+artifactID.String(), request.path)
		if shouldFail {
			require.Contains(t, diagnostics.String(), "discard remains committed")
			require.Contains(t, diagnostics.String(), "AccessDenied")
		} else {
			require.Empty(t, diagnostics.String())
		}
		require.NotContains(t, diagnostics.String(), "fixture-secret-key")
		require.NotContains(t, output.String(), "provider-payload")
		unchanged, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
		require.NoError(t, err)
		require.Equal(t, discarded, unchanged, "cleanup retry never rewrites selection, progress or terminal age")
	}
}
