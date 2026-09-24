//go:build integration

package integration

import (
	"bufio"
	"bytes"
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
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
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
	pool             *pgxpool.Pool
	store            *storage.Store
	integrationSetup integrationstore.ProjectIntegrationRecord
	version          uuid.UUID
}

func newDiscordRuntimeFixture(t *testing.T) discordRuntimeFixture {
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
	integration, err := store.Integrations().CreateProjectIntegration(ctx, integrationstore.SaveProjectIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "discord", IntegrationType: integrationdefinition.DiscordThread,
	})
	require.NoError(t, err)
	integrationSetup, err := store.Integrations().ConfigureProjectIntegration(
		ctx,
		integrationstore.ConfigureProjectIntegrationInput{
			OrgID: ids.OrgID, ProjectID: ids.ProjectID, IntegrationID: integration.ID,
			InstalledByUserID: ids.ProviderAdminUserID, Provider: integrationstore.IntegrationProviderDiscord,
			ProviderTenantID: "123", ProviderAccountRef: "456", CredentialSecretID: secret.ID,
			CredentialVersionID: version.ID, ExpectedSetupRevision: integration.SetupRevision,
		},
	)
	require.NoError(t, err)
	return discordRuntimeFixture{pool: pool, store: store, integrationSetup: integrationSetup, version: version.ID}
}

func TestDiscordRuntimePersistsResumeAndFencesRevokedCredentials(t *testing.T) {
	ctx := t.Context()
	f := newDiscordRuntimeFixture(t)
	pool, store, integrationSetup := f.pool, f.store, f.integrationSetup
	revision := integrationstore.IntegrationRuntimeRevision{
		ProjectID:           integrationSetup.ProjectID,
		IntegrationID:       integrationSetup.ID,
		Key:                 "discord/shard/0",
		SetupRevision:       integrationSetup.SetupRevision,
		CredentialVersionID: f.version,
	}
	claim, found, err := store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.True(t, found)
	updated, err := store.Integrations().UpdateProjectIntegration(
		ctx,
		integrationSetup.ID,
		integrationstore.SaveProjectIntegrationInput{
			OrgID: integrationSetup.OrgID, ProjectID: integrationSetup.ProjectID, Name: integrationSetup.Name,
			IntegrationType: integrationSetup.IntegrationType, Settings: integrationSetup.Settings,
		},
	)
	require.NoError(t, err)
	require.Equal(t, integrationSetup.SetupRevision, updated.SetupRevision)
	require.False(t, updated.UpdatedAt.Equal(integrationSetup.UpdatedAt))
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
		for i, data := range []string{
			`{"id":"792","channel_id":"100","author":{"id":"102"},"content":"direct message"}`,
			`{"id":"793","channel_id":"100","guild_id":"101","author":{"id":"102"},"type":18}`,
			`{"id":"794","channel_id":"100","guild_id":"101","author":{"id":"103","bot":true}}`,
		} {
			checkpoint.Sequence = int64(4 + i)
			require.NoError(t, commit(ctx, discord.Dispatch{
				Type: "MESSAGE_CREATE", Sequence: checkpoint.Sequence, Data: json.RawMessage(data),
			}, checkpoint))
		}
		return &discord.GatewayError{RetryAfter: time.Second}
	}
	r.run(ctx, integrationSetup, claim, time.Now(), slog.Default())
	var count int
	require.NoError(
		t,
		pool.QueryRow(
			ctx,
			`SELECT count(*) FROM integration_inbox WHERE integration_id=$1`,
			integrationSetup.ID,
		).Scan(&count),
	)
	require.Equal(t, 1, count)
	_, found, err = store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.False(t, found, "released shard respects retry delay")
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_runtime SET available_at=now() WHERE integration_id=$1`,
		integrationSetup.ID,
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
					OrgID:    integrationSetup.OrgID,
					SecretID: integrationSetup.CredentialSecretID,
					Actor:    identitystore.NewUserPrincipal(integrationSetup.InstalledByUserID),
					Material: secrets.GenericMaterial{Value: "rotated"},
				},
			)
		require.NoError(t, err)
		require.ErrorIs(t, config.BeforeConnect(ctx), integrationstore.ErrIntegrationRuntimeLeaseLost)
		checkpoint.Sequence++
		require.ErrorIs(
			t,
			commit(
				ctx,
				discord.Dispatch{
					Type:     "MESSAGE_CREATE",
					Sequence: checkpoint.Sequence,
					Data:     json.RawMessage(`{"id":"791","channel_id":"100","guild_id":"101","author":{"id":"102"}}`),
				},
				checkpoint,
			),
			integrationstore.ErrIntegrationRuntimeLeaseLost,
		)
		return context.Canceled
	}
	r.run(ctx, integrationSetup, claim, time.Now(), slog.Default())
	require.NoError(
		t,
		pool.QueryRow(
			ctx,
			`SELECT count(*) FROM integration_inbox WHERE integration_id=$1`,
			integrationSetup.ID,
		).Scan(&count),
	)
	require.Equal(t, 1, count, "revoked runtime never publishes input")
}

func TestDiscordRuntimePersistsOnlySafeFailureMessage(t *testing.T) {
	untrusted := "private upstream token=do-not-expose\x00" + strings.Repeat("界", 2000)
	for _, cause := range []error{
		errors.New(untrusted),
		fmt.Errorf("%s: %w", untrusted, &discord.APIError{
			Code: discord.PermanentFailure, StatusCode: http.StatusUnauthorized,
		}),
		fmt.Errorf("%s: %w", untrusted, &discord.GatewayError{CloseCode: 4014, Fatal: true}),
	} {
		f := newDiscordRuntimeFixture(t)
		ctx := t.Context()
		claim, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, integrationstore.IntegrationRuntimeRevision{
			ProjectID: f.integrationSetup.ProjectID, IntegrationID: f.integrationSetup.ID,
			Key: "discord/shard/0", SetupRevision: f.integrationSetup.SetupRevision, CredentialVersionID: f.version,
		}, discordRuntimeLease)
		require.NoError(t, err)
		require.True(t, found)
		r := DiscordRuntime{
			Integrations: f.store.Integrations(), Secrets: f.store.Secrets(),
			HTTPClient: &http.Client{Transport: discordRuntimeTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{"url":"wss://gateway.discord.gg","shards":1,
						"session_start_limit":{"max_concurrency":1}}`))}, nil
			})},
			runShard: func(context.Context, discord.ShardConfig, *discord.Checkpoint, discord.CommitDispatch) error {
				return cause
			},
		}
		var logs bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&logs, nil))
		log.Info("unrelated runtime diagnostic")
		r.run(ctx, f.integrationSetup, claim, time.Now(), log)
		failure, err := f.store.Integrations().GetIntegrationRuntimeFailure(ctx,
			f.integrationSetup.ProjectID, f.integrationSetup.ID, f.integrationSetup.SetupRevision)
		require.NoError(t, err, "untrusted text must not prevent recording a safe connection failure")
		require.Equal(t, discordRuntimeFailureMessage(cause), failure.Message)
		require.True(t, utf8.ValidString(failure.Message))
		require.LessOrEqual(t, len(failure.Message), 256)
		require.NotContains(t, failure.Message, "private")
		require.NotContains(t, failure.Message, "\x00")
		foundStopped := false
		lines := bufio.NewScanner(&logs)
		for lines.Scan() {
			var entry struct {
				Message string `json:"msg"`
				Error   string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(lines.Bytes(), &entry))
			if entry.Message == "Discord connection stopped" {
				foundStopped = true
				require.Equal(t, cause.Error(), entry.Error, "internal logs retain the full diagnostic")
			}
		}
		require.NoError(t, lines.Err())
		require.True(t, foundStopped, "runtime must log the connection failure")
	}
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
		{"READY identity mismatch", &discord.APIError{Code: discord.ScopeMismatch}, time.Hour},
		{"rate limit", &discord.APIError{Code: discord.RateLimited, RetryAfter: 2 * time.Hour}, 2 * time.Hour},
		{"disabled intent", &discord.GatewayError{Fatal: true}, time.Hour},
		{"session budget", discordIdentifyWaitError{After: 20 * time.Hour}, 20 * time.Hour},
		{"reconnect with permit wait", errors.Join(
			&discord.GatewayError{RetryAfter: time.Second}, discordIdentifyWaitError{After: 20 * time.Hour},
		), 20 * time.Hour},
		{"reconnect with API rate limit", errors.Join(
			&discord.GatewayError{RetryAfter: time.Second},
			&discord.APIError{Code: discord.RateLimited, RetryAfter: 2 * time.Hour},
		), 2 * time.Hour},
		{"network", errors.New("network unavailable"), time.Second},
	} {
		t.Run(
			test.name,
			func(t *testing.T) { require.GreaterOrEqual(t, discordReconnectDelay(test.err), test.minimum) },
		)
	}
}

func TestDiscordRuntimeScanClaimsAvailableIntegrationsWithinCapacity(t *testing.T) {
	f := newDiscordRuntimeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	integrations := []integrationstore.ProjectIntegrationRecord{f.integrationSetup}
	for i := range 3 {
		integration, err := f.store.Integrations().CreateProjectIntegration(ctx, integrationstore.SaveProjectIntegrationInput{
			OrgID: f.integrationSetup.OrgID, ProjectID: f.integrationSetup.ProjectID,
			Name: fmt.Sprintf("discord-%d", i), IntegrationType: integrationdefinition.DiscordThread,
		})
		require.NoError(t, err)
		integrationSetup, err := f.store.Integrations().ConfigureProjectIntegration(
			ctx,
			integrationstore.ConfigureProjectIntegrationInput{
				OrgID: f.integrationSetup.OrgID, ProjectID: f.integrationSetup.ProjectID, IntegrationID: integration.ID,
				InstalledByUserID: f.integrationSetup.InstalledByUserID, Provider: integrationstore.IntegrationProviderDiscord,
				ProviderTenantID: fmt.Sprintf("%d", 124+i), ProviderAccountRef: f.integrationSetup.ProviderAccountRef,
				CredentialSecretID: f.integrationSetup.CredentialSecretID, CredentialVersionID: f.version,
				ExpectedSetupRevision: integration.SetupRevision,
			},
		)
		require.NoError(t, err)
		integrations = append(integrations, integrationSetup)
	}
	revision := integrationstore.IntegrationRuntimeRevision{
		ProjectID: f.integrationSetup.ProjectID, IntegrationID: f.integrationSetup.ID,
		Key: "discord/shard/0", SetupRevision: f.integrationSetup.SetupRevision,
		CredentialVersionID: f.version,
	}
	owner, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, revision, discordRuntimeLease)
	require.NoError(t, err)
	require.True(t, found)
	started := make(chan discord.ShardConfig, len(integrations))
	r := DiscordRuntime{
		Integrations: f.store.Integrations(),
		Secrets:      f.store.Secrets(),
		Redis:        integrationredis.OpenClient(t),
		Capacity:     2,
		HTTPClient: &http.Client{Transport: discordRuntimeTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"url":"wss://gateway.discord.gg","shards":1,
                    "session_start_limit":{"total":1000,"remaining":1000,"reset_after":0,"max_concurrency":1}}`))}, nil
		})},
		runShard: func(ctx context.Context, cfg discord.ShardConfig, _ *discord.Checkpoint, _ discord.CommitDispatch) error {
			started <- cfg
			<-ctx.Done()
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	seen := make([]string, 0, 2)
	for range 2 {
		select {
		case config := <-started:
			require.Zero(t, config.ShardID)
			require.Equal(t, 1, config.ShardCount)
			seen = append(seen, config.Credentials.ApplicationID)
		case <-ctx.Done():
			t.Fatal("available integrations did not start", ctx.Err())
		}
	}
	cancel()
	require.NoError(t, <-done)
	require.ElementsMatch(t, []string{integrations[1].ProviderTenantID, integrations[2].ProviderTenantID}, seen)
	require.Empty(t, started, "the capacity limit must leave the last integration unstarted")
	require.NoError(t, f.store.Integrations().RenewIntegrationRuntime(t.Context(), owner.Lease, discordRuntimeLease))
	var total, owned int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE claim_token IS NOT NULL)
		FROM integration_runtime WHERE project_id=$1`, f.integrationSetup.ProjectID).Scan(&total, &owned))
	require.Equal(t, 3, total, "only the other owner's integration and two local integrations should have runtimes")
	require.Equal(t, 1, owned, "shutdown must release the local integrations and preserve the other owner")
}

func TestDiscordRuntimeCheckpointDoesNotPreventCredentialOrProjectDeletion(t *testing.T) {
	for _, remove := range []string{"credential", "project"} {
		t.Run(remove, func(t *testing.T) {
			f := newDiscordRuntimeFixture(t)
			ctx := t.Context()
			claim, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, integrationstore.IntegrationRuntimeRevision{
				ProjectID: f.integrationSetup.ProjectID, IntegrationID: f.integrationSetup.ID,
				Key: "discord/shard/0", SetupRevision: f.integrationSetup.SetupRevision,
				CredentialVersionID: f.version,
			}, discordRuntimeLease)
			require.NoError(t, err)
			require.True(t, found)
			require.NoError(t, f.store.Integrations().CommitIntegrationRuntime(ctx, claim.Lease,
				json.RawMessage(`{"session_id":"retired"}`), nil))
			require.NoError(t, f.store.Integrations().ReleaseIntegrationRuntime(ctx, claim.Lease, 0, ""))
			actor := identitystore.NewUserPrincipal(f.integrationSetup.InstalledByUserID)
			if remove == "credential" {
				require.NoError(t, f.store.Integrations().DeleteProjectIntegration(ctx,
					f.integrationSetup.OrgID, f.integrationSetup.ProjectID, f.integrationSetup.ID))
				_, err = f.store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
					OrgID: f.integrationSetup.OrgID, SecretID: f.integrationSetup.CredentialSecretID, Actor: actor,
				})
			} else {
				_, err = f.store.Organizations().DeleteProject(ctx, f.integrationSetup.OrgID, f.integrationSetup.ProjectID, actor)
			}
			require.NoError(t, err)
			var remaining int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM integration_runtime
				WHERE integration_id=$1`, f.integrationSetup.ID).Scan(&remaining))
			require.Zero(t, remaining)
		})
	}
}

func TestDiscordRuntimeScanPassesOwnedPageAndWraps(t *testing.T) {
	f := newDiscordRuntimeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	integrations := []integrationstore.ProjectIntegrationRecord{f.integrationSetup}
	for i := range 100 {
		integration, err := f.store.Integrations().CreateProjectIntegration(ctx, integrationstore.SaveProjectIntegrationInput{
			OrgID: f.integrationSetup.OrgID, ProjectID: f.integrationSetup.ProjectID,
			Name: fmt.Sprintf("discord-%d", i), IntegrationType: integrationdefinition.DiscordThread,
		})
		require.NoError(t, err)
		integrationSetup, err := f.store.Integrations().ConfigureProjectIntegration(
			ctx,
			integrationstore.ConfigureProjectIntegrationInput{
				OrgID: f.integrationSetup.OrgID, ProjectID: f.integrationSetup.ProjectID, IntegrationID: integration.ID,
				InstalledByUserID: f.integrationSetup.InstalledByUserID, Provider: "discord",
				ProviderTenantID: f.integrationSetup.ProviderTenantID, ProviderAccountRef: f.integrationSetup.ProviderAccountRef,
				CredentialSecretID: f.integrationSetup.CredentialSecretID, CredentialVersionID: f.version,
				ExpectedSetupRevision: integration.SetupRevision,
			},
		)
		require.NoError(t, err)
		integrations = append(integrations, integrationSetup)
	}
	var firstOwner integrationstore.IntegrationRuntimeClaim
	for i, integrationSetup := range integrations[:100] {
		claim, found, err := f.store.Integrations().ClaimIntegrationRuntime(ctx, integrationstore.IntegrationRuntimeRevision{
			ProjectID: integrationSetup.ProjectID, IntegrationID: integrationSetup.ID, Key: "discord/shard/0",
			SetupRevision: integrationSetup.SetupRevision, CredentialVersionID: f.version,
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
			if cfg.Credentials.ApplicationID == integrations[100].ProviderTenantID {
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
			t.Fatal("scan did not reach available integration", ctx.Err())
		}
	}
	waitStarted(integrations[100].ProviderTenantID)
	require.NoError(t, f.store.Integrations().ReleaseIntegrationRuntime(ctx, firstOwner.Lease, 0, ""))
	close(releaseLast)
	waitStarted(integrations[0].ProviderTenantID)
	cancel()
	require.NoError(t, <-done)
}
