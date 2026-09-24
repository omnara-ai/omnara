//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type deploymentGatewayPacket struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
}

type deploymentGatewaySocket struct {
	conn   *websocket.Conn
	auth   deploymentGatewayPacket
	closed <-chan error
}

func newDeploymentGateway(t *testing.T) (*http.Client, <-chan deploymentGatewaySocket) {
	t.Helper()
	connections := make(chan deploymentGatewaySocket, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":1000}}`)); err != nil {
			t.Error(err)
			return
		}
		closed := make(chan error, 1)
		authenticated := false
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				closed <- err
				return
			}
			var packet deploymentGatewayPacket
			if err := json.Unmarshal(raw, &packet); err != nil {
				t.Error(err)
				return
			}
			if packet.Op == 1 {
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"op":11,"d":null}`)); err != nil {
					closed <- err
					return
				}
				continue
			}
			if authenticated || (packet.Op != 2 && packet.Op != 6) {
				t.Errorf("unexpected gateway packet: %s", raw)
				return
			}
			authenticated = true
			connections <- deploymentGatewaySocket{conn: conn, auth: packet, closed: closed}
		}
	}))
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	transport := server.Client().Transport
	return &http.Client{Transport: discordRuntimeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == "https://discord.com/api/v10/gateway/bot" {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"url":"wss://gateway.discord.gg","shards":1,
				"session_start_limit":{"total":1000,"remaining":1000,"reset_after":60000,"max_concurrency":1}}`))}, nil
		}
		if r.URL.Host != "gateway.discord.gg" || (r.URL.Path != "" && r.URL.Path != "/") {
			return nil, fmt.Errorf("test refused external request: %s", r.URL.Redacted())
		}
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host, r.Host = base.Scheme, base.Host, base.Host
		return transport.RoundTrip(r)
	})}, connections
}

func waitDeploymentResult[T any](t *testing.T, results <-chan T) T {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("deployment operation did not finish")
		var zero T
		return zero
	}
}

func (s deploymentGatewaySocket) dispatch(t *testing.T, kind string, sequence int64, data any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"op": 0, "t": kind, "s": sequence, "d": data})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.conn.Write(ctx, websocket.MessageText, raw))
}

func (s deploymentGatewaySocket) requireResumableClose(t *testing.T) {
	t.Helper()
	err := waitDeploymentResult(t, s.closed)
	require.Error(t, err)
	code := websocket.CloseStatus(err)
	require.NotEqual(t, websocket.StatusNormalClosure, code, "1000 invalidates the Discord session")
	require.NotEqual(t, websocket.StatusGoingAway, code, "1001 invalidates the Discord session")
}

func TestDiscordDeploymentGracefulHandoff(t *testing.T) {
	testDiscordDeploymentHandoff(t, "graceful")
}

func TestDiscordDeploymentCrashLeaseExpiry(t *testing.T) {
	testDiscordDeploymentHandoff(t, "crash")
}

func TestDiscordDeploymentCancelDuringCommit(t *testing.T) {
	testDiscordDeploymentHandoff(t, "cancel commit")
}

func testDiscordDeploymentHandoff(t *testing.T, scenario string) {
	t.Helper()
	ctx := t.Context()
	f := newDiscordRuntimeFixture(t)
	_, version, err := f.store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID: f.integrationSetup.OrgID, SecretID: f.integrationSetup.CredentialSecretID,
		Actor:    identitystore.NewUserPrincipal(f.integrationSetup.InstalledByUserID),
		Material: secrets.GenericMaterial{Value: "bot-token"},
	})
	require.NoError(t, err)
	f.version = version.ID
	integration, err := f.store.Integrations().CreateProjectIntegration(ctx, integrationstore.SaveProjectIntegrationInput{
		OrgID: f.integrationSetup.OrgID, ProjectID: f.integrationSetup.ProjectID,
		Name: "deployment", IntegrationType: f.integrationSetup.IntegrationType,
	})
	require.NoError(t, err)
	f.integrationSetup, err = f.store.Integrations().ConfigureProjectIntegration(
		ctx,
		integrationstore.ConfigureProjectIntegrationInput{
			OrgID: f.integrationSetup.OrgID, ProjectID: f.integrationSetup.ProjectID, IntegrationID: integration.ID,
			InstalledByUserID: f.integrationSetup.InstalledByUserID, Provider: "discord",
			ProviderTenantID: strconv.FormatInt(time.Now().UnixNano(), 10), ProviderAccountRef: "456",
			CredentialSecretID: f.integrationSetup.CredentialSecretID, CredentialVersionID: f.version,
			ExpectedSetupRevision: integration.SetupRevision,
		},
	)
	require.NoError(t, err)
	providerFixture, provider := newDiscordInboxFixture(t)
	providerFixture.integrationSetup = f.integrationSetup
	providerFixture.identity = discord.Identity{
		ApplicationID: f.integrationSetup.ProviderTenantID, BotUserID: f.integrationSetup.ProviderAccountRef,
	}
	providerFixture.message.Content = "<@456> survive deployment"
	providerFixture.message.Mentions = []discord.User{{ID: "456", Bot: true}}
	provider.integrations, provider.secrets = f.store.Integrations(), f.store.Secrets()
	config := storagefixture.SeedAgentConfig(t, ctx, f.store.Models(), f.store.Execution(),
		f.integrationSetup.OrgID, f.integrationSetup.ProjectID,
		"instruction: Help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := f.store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: f.integrationSetup.ProjectID, Name: "deployment", CurrentConfigID: config.ID,
	})
	require.NoError(t, err)
	f.integrationSetup, err = f.store.Integrations().UpdateProjectIntegration(
		ctx,
		f.integrationSetup.ID,
		integrationstore.SaveProjectIntegrationInput{
			OrgID: f.integrationSetup.OrgID, ProjectID: f.integrationSetup.ProjectID, Name: f.integrationSetup.Name,
			IntegrationType: f.integrationSetup.IntegrationType,
			Settings: integrationstore.ProjectIntegrationSettings{Launcher: &integrationstore.IntegrationLauncher{
				Trigger: "mention",
				Slots:   []integrationstore.IntegrationLaunchSlot{{Key: "helper", AgentProfileID: &profile.ID}},
			}},
		},
	)
	require.NoError(t, err)
	client, connections := newDeploymentGateway(t)
	redis := integrationredis.OpenClient(t)
	newRuntime := func() *DiscordRuntime {
		return &DiscordRuntime{
			Integrations: f.store.Integrations(), Secrets: f.store.Secrets(), Redis: redis, HTTPClient: client,
		}
	}
	newWorker := func() *IntegrationInboxWorker {
		router := NewIntegrationRouter(f.store.Execution(), f.store.Integrations())
		consumer := NewIntegrationInboxConsumer(router, f.store.Integrations(), nil,
			map[string]IntegrationInboxProvider{"discord": provider}, nil, testIntegrationLaunchWorkflow(router))
		return NewIntegrationInboxWorker(f.store.Integrations(), consumer, IntegrationInboxWorkerOptions{})
	}
	revision := integrationstore.IntegrationRuntimeRevision{
		ProjectID: f.integrationSetup.ProjectID, IntegrationID: f.integrationSetup.ID, Key: "discord/shard/0",
		SetupRevision: f.integrationSetup.SetupRevision, CredentialVersionID: f.version,
	}
	first, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.True(t, found)
	oldCtx, stopOld := context.WithCancel(ctx)
	defer stopOld()
	oldDone := make(chan error, 1)
	old := newRuntime()
	go func() {
		if scenario == "crash" {
			oldDone <- old.connect(oldCtx, f.integrationSetup, first)
			return
		}
		old.run(oldCtx, f.integrationSetup, first, time.Now(), slog.Default())
		oldDone <- nil
	}()
	one := waitDeploymentResult(t, connections)
	require.Equal(t, 2, one.auth.Op, "first worker must IDENTIFY through real Redis permit gating")
	one.dispatch(t, "READY", 1, map[string]any{
		"session_id": "deployment-session", "resume_gateway_url": "wss://gateway.discord.gg",
		"user":        map[string]any{"id": "456", "bot": true},
		"application": map[string]string{"id": f.integrationSetup.ProviderTenantID},
	})
	one.dispatch(t, "MESSAGE_CREATE", 2, providerFixture.message)
	waitSequence := func(want int64) {
		t.Helper()
		require.Eventually(t, func() bool {
			var sequence int64
			err := f.pool.QueryRow(ctx, `SELECT COALESCE((checkpoint->>'sequence')::bigint,-1)
				FROM integration_runtime WHERE integration_id=$1`, f.integrationSetup.ID).Scan(&sequence)
			return err == nil && sequence == want
		}, 5*time.Second, 10*time.Millisecond, "committed sequence %d", want)
	}
	waitSequence(2)
	worked, err := newWorker().RunOnce(ctx)
	require.NoError(t, err)
	require.True(t, worked)
	var agentID, inputID uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT agent_id,id FROM agent_inputs
		WHERE project_id=$1 AND input_kind='content'`, f.integrationSetup.ProjectID).Scan(&agentID, &inputID))
	reply := providerFixture.message
	reply.ID, reply.ChannelID, reply.Content, reply.Mentions = "501", "500", "pending during deployment", nil
	one.dispatch(t, "MESSAGE_CREATE", 3, reply)
	waitSequence(3)
	followup := reply
	followup.ID, followup.Content = "502", "after deployment"
	switch scenario {
	case "crash":
		require.NoError(t, one.conn.CloseNow())
		require.Error(t, waitDeploymentResult(t, oldDone))
		var token uuid.UUID
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT claim_token FROM integration_runtime WHERE integration_id=$1`,
			f.integrationSetup.ID).Scan(&token))
		require.Equal(t, first.Lease.Token, token, "crash must leave an unreleased lease")
		_, found, err = f.store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
		require.NoError(t, err)
		require.False(t, found, "replacement cannot steal an unexpired lease")
		_, err = f.pool.Exec(ctx, `UPDATE integration_runtime SET claim_expires_at=now()-interval '1 second'
			WHERE integration_id=$1`, f.integrationSetup.ID)
		require.NoError(t, err)
	case "cancel commit":
		lock, err := f.pool.Begin(ctx)
		require.NoError(t, err)
		defer lock.Rollback(ctx)
		_, err = lock.Exec(
			ctx,
			`SELECT runtime_key FROM integration_runtime WHERE integration_id=$1 FOR UPDATE`,
			f.integrationSetup.ID,
		)
		require.NoError(t, err)
		one.dispatch(t, "MESSAGE_CREATE", 4, followup)
		integrationdb.WaitForNamedLockWaiters(t, ctx, f.pool, "LockIntegrationRuntime", 1)
		stopOld()
		require.NoError(t, lock.Rollback(ctx))
		require.NoError(t, waitDeploymentResult(t, oldDone))
		one.requireResumableClose(t)
		waitSequence(3)
	default:
		stopOld()
		require.NoError(t, waitDeploymentResult(t, oldDone))
		one.requireResumableClose(t)
	}
	var captured, pending int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE state='pending')
		FROM integration_inbox WHERE integration_id=$1`, f.integrationSetup.ID).Scan(&captured, &pending))
	require.Equal(t, 2, captured, "canceled intake cannot leave a receipt without its checkpoint")
	require.Equal(t, 1, pending, "committed work remains available across deployment")
	var second integrationstore.IntegrationRuntimeClaim
	require.Eventually(t, func() bool {
		second, found, err = f.store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
		return err == nil && found
	}, 5*time.Second, 20*time.Millisecond, "replacement claims the released/expired lease")
	require.NotEqual(t, first.Lease.Token, second.Lease.Token)
	var checkpoint discord.Checkpoint
	require.NoError(t, json.Unmarshal(second.Checkpoint, &checkpoint))
	require.Equal(t, int64(3), checkpoint.Sequence)
	require.Equal(t, "deployment-session", checkpoint.SessionID)
	newCtx, stopNew := context.WithCancel(ctx)
	defer stopNew()
	newDone := make(chan error, 1)
	go func() {
		newRuntime().run(newCtx, f.integrationSetup, second, time.Now(), slog.Default())
		newDone <- nil
	}()
	two := waitDeploymentResult(t, connections)
	require.Equal(t, 6, two.auth.Op, "replacement must RESUME, not spend another IDENTIFY permit")
	var resume struct {
		Session  string `json:"session_id"`
		Sequence int64  `json:"seq"`
	}
	require.NoError(t, json.Unmarshal(two.auth.Data, &resume))
	require.Equal(t, "deployment-session", resume.Session)
	require.Equal(t, int64(3), resume.Sequence)
	require.ErrorIs(t, f.store.Integrations().RenewIntegrationRuntime(ctx, first.Lease, discordRuntimeLease),
		integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.ErrorIs(t, f.store.Integrations().CommitIntegrationRuntime(ctx, first.Lease,
		json.RawMessage(`{"sequence":999}`), &integrationstore.VerifiedIntegrationReceipt{
			ProjectID: f.integrationSetup.ProjectID, IntegrationID: f.integrationSetup.ID, ReceiptKey: "discord:stale",
			Payload: discordInboxPayload(t, reply),
		}), integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.ErrorIs(t, f.store.Integrations().ReleaseIntegrationRuntime(ctx, first.Lease, 0, ""),
		integrationstore.ErrIntegrationRuntimeLeaseLost)
	two.dispatch(t, "MESSAGE_CREATE", 2, providerFixture.message)
	two.dispatch(t, "MESSAGE_CREATE", 3, reply)
	two.dispatch(t, "MESSAGE_CREATE", 4, followup)
	waitSequence(4)
	worker := newWorker()
	for range 2 {
		worked, err = worker.RunOnce(ctx)
		require.NoError(t, err)
		require.True(t, worked)
	}
	worked, err = worker.RunOnce(ctx)
	require.NoError(t, err)
	require.False(t, worked, "committed replay must not reopen a completed receipt")
	stopNew()
	require.NoError(t, waitDeploymentResult(t, newDone))
	two.requireResumableClose(t)
	var receipts, completed, agents, inputs, original, attributed int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE state='completed')
		FROM integration_inbox WHERE integration_id=$1`, f.integrationSetup.ID).Scan(&receipts, &completed))
	require.Equal(t, 3, receipts)
	require.Equal(t, 3, completed)
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1`,
		f.integrationSetup.ProjectID).Scan(&agents))
	require.Equal(t, 1, agents)
	actorTenant := integrationTestActor(t, f.integrationSetup.ID, "33").ProviderTenantID
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE input.id=$2),
		count(*) FILTER (WHERE actor.provider='integration' AND actor.provider_tenant_id=$3 AND actor.provider_user_id='33')
		FROM agent_inputs input JOIN actors actor ON actor.id=input.actor_id
		WHERE input.agent_id=$1 AND input.input_kind='content'`, agentID, inputID, actorTenant).
		Scan(&inputs, &original, &attributed))
	require.Equal(t, 3, inputs, "one input per unique Discord message on the original agent")
	require.Equal(t, 1, original, "the committed launch input survives deployment")
	require.Equal(t, 3, attributed, "all inputs retain integration-scoped sender attribution")
	providerFixture.mu.Lock()
	posts := providerFixture.posts
	providerFixture.mu.Unlock()
	require.Equal(t, 1, posts, "replaying a committed event must not repeat thread creation")
}
