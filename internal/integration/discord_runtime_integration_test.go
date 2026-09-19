//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type discordRuntimeFixture struct {
	pool       *pgxpool.Pool
	store      *storage.Store
	connection integrationstore.IntegrationConnectionRecord
	version    uuid.UUID
}

func newDiscordRuntimeFixture(t *testing.T, shards int) discordRuntimeFixture {
	t.Helper()
	ctx := t.Context()
	_, file, _, _ := runtime.Caller(0)
	pool := integrationdb.OpenMigratedPool(t, ctx, filepath.Join(filepath.Dir(file), "../../migrations"))
	ids := storagefixture.ProjectIDs{
		OrgID:                   uuid.New(),
		ProjectID:               uuid.New(),
		ProviderAdminUserID:     uuid.New(),
		ProviderSecretID:        uuid.New(),
		ProviderSecretVersionID: uuid.New(),
		ProviderConfigID:        uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	_, err := pool.Exec(
		ctx,
		`INSERT INTO org_memberships(org_id,user_id,role,created_at) VALUES($1,$2,'owner',now())`,
		ids.OrgID,
		ids.ProviderAdminUserID,
	)
	require.NoError(t, err)
	wrapper, err := secrets.NewLocalKeyWrapper(
		"runtime-test",
		map[string][]byte{"runtime-test": []byte("0123456789abcdef0123456789abcdef")},
	)
	require.NoError(t, err)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper))
	secret, version, err := store.Secrets().
		CreateSecret(
			ctx,
			secretstore.CreateSecretInput{
				OrgID:          ids.OrgID,
				OwnerKind:      secretstore.SecretOwnerProject,
				OwnerProjectID: ids.ProjectID,
				Name:           "discord",
				Actor:          identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
				Material:       secrets.GenericMaterial{Value: "test-bot-token"},
			},
		)
	require.NoError(t, err)
	connection, err := store.Integrations().
		CreateIntegrationConnection(
			ctx,
			integrationstore.SaveIntegrationConnectionInput{
				OrgID:              ids.OrgID,
				ProjectID:          ids.ProjectID,
				InstalledByUserID:  ids.ProviderAdminUserID,
				Provider:           integrationstore.IntegrationProviderDiscord,
				ProviderTenantID:   "123",
				ProviderAccountRef: "456",
				CredentialSecretID: secret.ID,
				State:              integrationstore.IntegrationConnectionStateActive,
				ProviderConfig:     json.RawMessage(fmt.Sprintf(`{"shard_count":%d}`, shards)),
			},
		)
	require.NoError(t, err)
	return discordRuntimeFixture{pool: pool, store: store, connection: connection, version: version.ID}
}

func TestDiscordRuntimePersistsResumeAndFencesRevokedCredentials(t *testing.T) {
	ctx := t.Context()
	f := newDiscordRuntimeFixture(t, 1)
	pool, store, connection := f.pool, f.store, f.connection
	revision := integrationstore.RuntimeRevision{
		ProjectID:           connection.ProjectID,
		ConnectionID:        connection.ID,
		Key:                 "discord/shard/0",
		ConnectionUpdatedAt: connection.UpdatedAt,
		CredentialVersionID: f.version,
	}
	claim, found, err := store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.True(t, found)
	r := DiscordRuntime{
		Integrations: store.Integrations(),
		Secrets:      store.Secrets(),
		Redis:        integrationredis.OpenClient(t),
		HTTPClient: &http.Client{
			Transport: discordRuntimeTransport(func(request *http.Request) (*http.Response, error) {
				require.Equal(t, "https://discord.com/api/v10/gateway/bot", request.URL.String())
				require.Equal(t, "Bot test-bot-token", request.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(
						strings.NewReader(
							`{"url":"wss://gateway.discord.gg","shards":1,"session_start_limit":{"total":1000,"remaining":1000,"reset_after":0,"max_concurrency":1}}`,
						),
					),
				}, nil
			}),
		},
	}
	checkpoint := discord.Checkpoint{
		ApplicationID: "123",
		BotUserID:     "456",
		ShardCount:    1,
		SessionID:     "session",
		ResumeURL:     "wss://gateway.discord.gg",
		Sequence:      1,
	}
	r.runShard = func(
		ctx context.Context, config discord.ShardConfig, prior *discord.Checkpoint, commit discord.CommitDispatch,
	) error {
		require.Nil(t, prior)
		require.NoError(t, config.BeforeConnect(ctx))
		require.NoError(t, config.BeforeIdentify(ctx), "fresh bot with zero reset_after can connect")
		require.NoError(t, commit(ctx, discord.Dispatch{Type: "READY", Sequence: 1}, checkpoint))
		checkpoint.Sequence = 2
		require.NoError(
			t,
			commit(
				ctx,
				discord.Dispatch{
					Type:     "MESSAGE_CREATE",
					Sequence: 2,
					Data: json.RawMessage(
						`{"id":"789","channel_id":"100","guild_id":"101","author":{"id":"102"},"content":"hello"}`,
					),
				},
				checkpoint,
			),
		)
		checkpoint.Sequence = 3
		require.NoError(
			t,
			commit(
				ctx,
				discord.Dispatch{
					Type:     "MESSAGE_CREATE",
					Sequence: 3,
					Data: json.RawMessage(
						`{"id":"790","channel_id":"100","author":{"id":"456","bot":true},"content":"ignore self"}`,
					),
				},
				checkpoint,
			),
		)
		return &discord.GatewayError{RetryAfter: time.Second}
	}
	r.run(ctx, connection, claim, 0, 1, time.Now(), slog.Default())
	var count int
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox WHERE connection_id=$1`, connection.ID).Scan(&count),
	)
	require.Equal(t, 1, count)
	_, found, err = store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.False(t, found, "released shard respects retry delay")
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_connection_runtime SET available_at=now() WHERE connection_id=$1`,
		connection.ID,
	)
	require.NoError(t, err)
	claim, found, err = store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.True(t, found)
	r.runShard = func(
		ctx context.Context, config discord.ShardConfig, prior *discord.Checkpoint, commit discord.CommitDispatch,
	) error {
		require.Equal(t, &checkpoint, prior, "restart resumes from committed provider checkpoint")
		_, _, err := store.Secrets().
			CreateSecretVersion(
				ctx,
				secretstore.CreateSecretVersionInput{
					OrgID:    connection.OrgID,
					SecretID: connection.CredentialSecretID,
					Actor:    identitystore.NewUserPrincipal(connection.InstalledByUserID),
					Material: secrets.GenericMaterial{Value: "rotated"},
				},
			)
		require.NoError(t, err)
		require.ErrorIs(t, config.BeforeConnect(ctx), integrationstore.ErrIntegrationRuntimeLeaseLost)
		checkpoint.Sequence = 4
		require.ErrorIs(
			t,
			commit(
				ctx,
				discord.Dispatch{
					Type:     "MESSAGE_CREATE",
					Sequence: 4,
					Data:     json.RawMessage(`{"id":"791","channel_id":"100","author":{"id":"102"}}`),
				},
				checkpoint,
			),
			integrationstore.ErrIntegrationRuntimeLeaseLost,
		)
		return context.Canceled
	}
	r.run(ctx, connection, claim, 0, 1, time.Now(), slog.Default())
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox WHERE connection_id=$1`, connection.ID).Scan(&count),
	)
	require.Equal(t, 1, count, "revoked runtime never publishes input")
}

type discordRuntimeTransport func(*http.Request) (*http.Response, error)

func (f discordRuntimeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDiscordReconnectDelayHonorsProviderFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		minimum time.Duration
	}{
		{"revoked token", &discord.APIError{Code: discord.PermanentFailure, StatusCode: 401}, time.Hour},
		{"missing permissions", &discord.APIError{Code: discord.PermanentFailure, StatusCode: 403}, time.Hour},
		{"rate limit", &discord.APIError{Code: discord.RateLimited, RetryAfter: 2 * time.Hour}, 2 * time.Hour},
		{"disabled intent", &discord.GatewayError{Fatal: true}, time.Hour},
		{"session budget", discordIdentifyWaitError{After: 20 * time.Hour}, 20 * time.Hour},
		{"network", errors.New("network unavailable"), time.Second},
	} {
		t.Run(
			test.name,
			func(t *testing.T) { require.GreaterOrEqual(t, discordReconnectDelay(test.err), test.minimum) },
		)
	}
}

// A busy shard must not hide siblings, and the local capacity is a hard limit.
// Cancellation waits for every shard and releases only this process's leases.
func TestDiscordRuntimeScanClaimsAvailableShardsWithinCapacity(t *testing.T) {
	f := newDiscordRuntimeFixture(t, 4)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	revision := integrationstore.RuntimeRevision{
		ProjectID: f.connection.ProjectID, ConnectionID: f.connection.ID,
		Key: "discord/shard/0", ConnectionUpdatedAt: f.connection.UpdatedAt,
		CredentialVersionID: f.version,
	}
	owner, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.True(t, found)
	started := make(chan int, 4)
	r := DiscordRuntime{
		Integrations: f.store.Integrations(),
		Secrets:      f.store.Secrets(),
		Redis:        integrationredis.OpenClient(t),
		Capacity:     2,
		HTTPClient: &http.Client{Transport: discordRuntimeTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"url":"wss://gateway.discord.gg","shards":4,
                    "session_start_limit":{"total":1000,"remaining":1000,"reset_after":0,"max_concurrency":1}}`))}, nil
		})},
		runShard: func(ctx context.Context, cfg discord.ShardConfig, _ *discord.Checkpoint, _ discord.CommitDispatch) error {
			if cfg.ShardCount != 4 {
				return errors.New("configured shard count was not preserved")
			}
			started <- cfg.ShardID
			<-ctx.Done()
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	seen := make([]int, 0, 2)
	for range 2 {
		select {
		case shard := <-started:
			seen = append(seen, shard)
		case <-ctx.Done():
			t.Fatal("available shards did not start", ctx.Err())
		}
	}
	cancel()
	require.NoError(t, <-done)
	require.ElementsMatch(t, []int{1, 2}, seen)
	require.Empty(t, started, "the capacity limit must prevent starting shard 3")
	require.NoError(t, f.store.Integrations().RenewIntegrationRuntime(t.Context(), owner.Lease, discordRuntimeLease))
	var owned int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_connection_runtime
        WHERE connection_id=$1 AND claim_token IS NOT NULL`, f.connection.ID).Scan(&owned))
	require.Equal(t, 1, owned, "shutdown must release the local shards and preserve the other owner")
}

func TestDiscordRuntimeCheckpointDoesNotPreventCredentialOrProjectDeletion(t *testing.T) {
	for _, remove := range []string{"credential", "project"} {
		t.Run(remove, func(t *testing.T) {
			f := newDiscordRuntimeFixture(t, 1)
			ctx := t.Context()
			claim, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, integrationstore.RuntimeRevision{
				ProjectID: f.connection.ProjectID, ConnectionID: f.connection.ID,
				Key: "discord/shard/0", ConnectionUpdatedAt: f.connection.UpdatedAt,
				CredentialVersionID: f.version,
			}, discordRuntimeLease)
			require.NoError(t, err)
			require.True(t, found)
			require.NoError(t, f.store.Integrations().CommitIntegrationRuntime(ctx, claim.Lease,
				json.RawMessage(`{"session_id":"retired"}`), nil))
			require.NoError(t, f.store.Integrations().ReleaseIntegrationRuntime(ctx, claim.Lease, 0, ""))
			actor := identitystore.NewUserPrincipal(f.connection.InstalledByUserID)
			if remove == "credential" {
				require.NoError(t, f.store.Integrations().DeleteIntegrationConnection(ctx,
					f.connection.ProjectID, f.connection.ID))
				_, err = f.store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
					OrgID: f.connection.OrgID, SecretID: f.connection.CredentialSecretID, Actor: actor,
				})
			} else {
				_, err = f.store.Organizations().DeleteProject(ctx, f.connection.OrgID, f.connection.ProjectID, actor)
			}
			require.NoError(t, err)
			var remaining int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM integration_connection_runtime
				WHERE connection_id=$1`, f.connection.ID).Scan(&remaining))
			require.Zero(t, remaining)
		})
	}
}

func TestDiscordRuntimeScanPassesOwnedPageAndWraps(t *testing.T) {
	f := newDiscordRuntimeFixture(t, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	connections := []integrationstore.IntegrationConnectionRecord{f.connection}
	for i := range 100 {
		connection, err := f.store.Integrations().CreateIntegrationConnection(ctx,
			integrationstore.SaveIntegrationConnectionInput{
				OrgID: f.connection.OrgID, ProjectID: f.connection.ProjectID,
				InstalledByUserID: f.connection.InstalledByUserID,
				Provider:          "discord", State: integrationstore.IntegrationConnectionStateActive,
				ProviderTenantID: fmt.Sprint(1000 + i), ProviderAccountRef: fmt.Sprint(2000 + i),
				CredentialSecretID: f.connection.CredentialSecretID,
			})
		require.NoError(t, err)
		connections = append(connections, connection)
	}
	// Creation uses monotonic UUIDv7 IDs, matching the discovery cursor order.
	var firstOwner integrationstore.IntegrationRuntimeClaim
	for i, connection := range connections[:100] {
		claim, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, integrationstore.RuntimeRevision{
			ProjectID: connection.ProjectID, ConnectionID: connection.ID, Key: "discord/shard/0",
			ConnectionUpdatedAt: connection.UpdatedAt, CredentialVersionID: f.version,
		}, time.Minute)
		require.NoError(t, err)
		require.True(t, found)
		if i == 0 {
			firstOwner = claim
		}
	}
	started := make(chan string, 2)
	releaseLast := make(chan struct{})
	r := DiscordRuntime{
		Integrations: f.store.Integrations(),
		Secrets:      f.store.Secrets(),
		Redis:        integrationredis.OpenClient(t),
		Capacity:     1,
		HTTPClient: &http.Client{Transport: discordRuntimeTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"url":"wss://gateway.discord.gg","shards":1,
					"session_start_limit":{"total":1000,"remaining":1000,"reset_after":0,"max_concurrency":1}}`))}, nil
		})},
		runShard: func(ctx context.Context, cfg discord.ShardConfig, _ *discord.Checkpoint, _ discord.CommitDispatch) error {
			started <- cfg.Credentials.ApplicationID
			if cfg.Credentials.ApplicationID == connections[100].ProviderTenantID {
				select {
				case <-releaseLast:
					return &discord.GatewayError{Fatal: true}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitStarted := func(want string) {
		t.Helper()
		select {
		case application := <-started:
			require.Equal(t, want, application)
		case <-ctx.Done():
			t.Fatal("scan did not reach available connection", ctx.Err())
		}
	}
	waitStarted(connections[100].ProviderTenantID)
	require.NoError(t, f.store.Integrations().ReleaseIntegrationRuntime(ctx, firstOwner.Lease, 0, ""))
	close(releaseLast)
	waitStarted(connections[0].ProviderTenantID)
	cancel()
	require.NoError(t, <-done)
}
