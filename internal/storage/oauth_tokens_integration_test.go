//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

const (
	oauthTestClientID    = "https://client.example/oauth/client.json"
	oauthTestRedirectURI = "http://127.0.0.1:3000/callback"
	oauthTestResource    = "https://omnara.test/api/mcp"
	oauthTestVerifier    = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

type oauthGrantFixture struct {
	user    identitystore.UserRecord
	session identitystore.BrowserSessionRecord
}

func newOAuthGrantFixture(t *testing.T, ctx context.Context, store *Store, slug string) oauthGrantFixture {
	t.Helper()
	user := mustCreateIdentityUser(t, ctx, store, slug+"@example.com", "OAuth "+slug)
	session, err := store.Identity().CreateBrowserSession(ctx, identitystore.CreateBrowserSessionInput{
		UserID:    user.ID,
		Token:     "session-" + slug,
		CSRFToken: "csrf-" + slug,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	return oauthGrantFixture{user: user, session: session}
}

func (f oauthGrantFixture) approvalInput() identitystore.CreateOAuthAuthorizationCodeInput {
	return identitystore.CreateOAuthAuthorizationCodeInput{
		UserID:           f.user.ID,
		BrowserSessionID: f.session.ID,
		ClientID:         oauthTestClientID,
		ClientName:       "Example MCP Client",
		RedirectURI:      oauthTestRedirectURI,
		CodeChallenge:    identitystore.PKCES256Challenge(oauthTestVerifier),
		Resource:         oauthTestResource,
	}
}

func (f oauthGrantFixture) approve(t *testing.T, ctx context.Context, store *Store) string {
	t.Helper()
	code, err := store.Identity().CreateOAuthAuthorizationCode(ctx, f.approvalInput())
	if err != nil {
		t.Fatalf("create authorization code: %v", err)
	}
	return code
}

func oauthExchangeInput(code string) identitystore.ExchangeOAuthAuthorizationCodeInput {
	return identitystore.ExchangeOAuthAuthorizationCodeInput{
		Code:         code,
		ClientID:     oauthTestClientID,
		RedirectURI:  oauthTestRedirectURI,
		CodeVerifier: oauthTestVerifier,
		Resource:     oauthTestResource,
	}
}

func oauthRefreshInput(refreshToken string) identitystore.RefreshOAuthAccessTokenInput {
	return identitystore.RefreshOAuthAccessTokenInput{
		RefreshToken: refreshToken,
		ClientID:     oauthTestClientID,
		Resource:     oauthTestResource,
	}
}

func (f oauthGrantFixture) issueTokens(t *testing.T, ctx context.Context, store *Store) identitystore.OAuthTokenSetRecord {
	t.Helper()
	tokens, err := store.Identity().ExchangeOAuthAuthorizationCode(ctx, oauthExchangeInput(f.approve(t, ctx, store)))
	if err != nil {
		t.Fatalf("exchange authorization code: %v", err)
	}
	return tokens
}

func lockUserForOAuthContention(t *testing.T, ctx context.Context, tx pgx.Tx, userID ID) {
	t.Helper()
	if _, err := dbsqlc.New(tx).LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: userID}); err != nil {
		t.Fatalf("lock user for contention: %v", err)
	}
}

func TestOAuthRefreshLocksUserBeforeTokenAndObservesRevokeAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	fixture := newOAuthGrantFixture(t, ctx, store, "oauth-refresh-lock-order")
	tokens := fixture.issueTokens(t, ctx, store)

	controlTx := integrationdb.BeginTx(t, ctx, pool)
	lockUserForOAuthContention(t, ctx, controlTx, fixture.user.ID)
	refreshDone := integrationdb.RunAsync(func() (identitystore.OAuthTokenSetRecord, error) {
		return store.Identity().RefreshOAuthAccessToken(context.Background(), oauthRefreshInput(tokens.RefreshToken))
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 1)

	if _, err := controlTx.Exec(
		ctx,
		`SELECT id FROM oauth_access_tokens WHERE refresh_token_hash = $1 FOR UPDATE NOWAIT`,
		identitystore.HashBearerToken(tokens.RefreshToken),
	); err != nil {
		t.Fatalf("token row locked before the blocked user lock: %v", err)
	}
	if err := store.Identity().RevokeUserAuthTokensTx(ctx, controlTx, fixture.user.ID); err != nil {
		t.Fatalf("revoke user auth tokens: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("commit revoke-all: %v", err)
	}
	result := integrationdb.Await(t, refreshDone, "blocked oauth refresh")
	if !errors.Is(result.Err, storeerr.ErrUnauthorized) {
		t.Fatalf("refresh after revoke-all error = %v, want ErrUnauthorized", result.Err)
	}
	if _, err := store.Identity().AuthenticateOAuthAccessToken(ctx, tokens.AccessToken); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("revoked access token authenticate error = %v, want ErrUnauthorized", err)
	}
}

func TestOAuthExchangeLocksUserBeforeAuthorizationCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	fixture := newOAuthGrantFixture(t, ctx, store, "oauth-exchange-lock-order")
	code := fixture.approve(t, ctx, store)

	controlTx := integrationdb.BeginTx(t, ctx, pool)
	lockUserForOAuthContention(t, ctx, controlTx, fixture.user.ID)
	exchangeDone := integrationdb.RunAsync(func() (identitystore.OAuthTokenSetRecord, error) {
		return store.Identity().ExchangeOAuthAuthorizationCode(context.Background(), oauthExchangeInput(code))
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 1)

	if _, err := controlTx.Exec(
		ctx,
		`SELECT id FROM oauth_authorization_codes WHERE code_hash = $1 FOR UPDATE NOWAIT`,
		identitystore.HashBearerToken(code),
	); err != nil {
		t.Fatalf("authorization code locked before the blocked user lock: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release user lock: %v", err)
	}
	tokens := integrationdb.AwaitSuccess(t, exchangeDone, "blocked oauth exchange")
	if _, err := store.Identity().AuthenticateOAuthAccessToken(ctx, tokens.AccessToken); err != nil {
		t.Fatalf("authenticate exchanged access token: %v", err)
	}
}

func TestRevokeAllConsumesOutstandingOAuthAuthorizationCodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	fixture := newOAuthGrantFixture(t, ctx, store, "oauth-revoke-all-codes")
	code := fixture.approve(t, ctx, store)

	controlTx := integrationdb.BeginTx(t, ctx, pool)
	lockUserForOAuthContention(t, ctx, controlTx, fixture.user.ID)
	exchangeDone := integrationdb.RunAsync(func() (identitystore.OAuthTokenSetRecord, error) {
		return store.Identity().ExchangeOAuthAuthorizationCode(context.Background(), oauthExchangeInput(code))
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 1)
	if err := store.Identity().RevokeUserAuthTokensTx(ctx, controlTx, fixture.user.ID); err != nil {
		t.Fatalf("revoke user auth tokens: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("commit revoke-all: %v", err)
	}
	result := integrationdb.Await(t, exchangeDone, "blocked oauth exchange")
	if !errors.Is(result.Err, storeerr.ErrUnauthorized) {
		t.Fatalf("exchange after revoke-all error = %v, want ErrUnauthorized", result.Err)
	}
}

func TestOAuthApprovalWaitsForUserLockAndObservesRevokeAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	fixture := newOAuthGrantFixture(t, ctx, store, "oauth-approval-lock-order")

	controlTx := integrationdb.BeginTx(t, ctx, pool)
	lockUserForOAuthContention(t, ctx, controlTx, fixture.user.ID)
	approvalDone := integrationdb.RunAsync(func() (string, error) {
		return store.Identity().CreateOAuthAuthorizationCode(context.Background(), fixture.approvalInput())
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 1)
	if err := store.Identity().RevokeUserAuthTokensTx(ctx, controlTx, fixture.user.ID); err != nil {
		t.Fatalf("revoke user auth tokens: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("commit revoke-all: %v", err)
	}
	result := integrationdb.Await(t, approvalDone, "blocked oauth approval")
	if !errors.Is(result.Err, storeerr.ErrUnauthorized) {
		t.Fatalf("approval after revoke-all error = %v, want ErrUnauthorized", result.Err)
	}
	var outstanding int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)::int FROM oauth_authorization_codes WHERE user_id = $1 AND consumed_at IS NULL`,
		fixture.user.ID,
	).Scan(&outstanding); err != nil {
		t.Fatalf("count outstanding authorization codes: %v", err)
	}
	if outstanding != 0 {
		t.Fatalf("outstanding authorization codes after revoke-all = %d, want 0", outstanding)
	}
}
