package identitystore

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	OAuthAuthorizationCodeTTL   = 5 * time.Minute
	OAuthAccessTokenTTL         = time.Hour
	OAuthRefreshTokenTTL        = 30 * 24 * time.Hour
	OAuthRefreshTokenReuseGrace = 30 * time.Second
	oauthURLFieldMaxBytes       = 2048
)

func (s *Store) CreateOAuthAuthorizationCode(
	ctx context.Context,
	input CreateOAuthAuthorizationCodeInput,
) (string, error) {
	if isNilID(input.UserID) || isNilID(input.BrowserSessionID) || input.ClientID == "" || input.ClientName == "" ||
		input.RedirectURI == "" || input.CodeChallenge == "" || input.Resource == "" {
		return "", storeerr.ErrUnauthorized
	}
	clientName, err := resourcename.CanonicalizeRequired("client_name", input.ClientName)
	if err != nil {
		return "", storeerr.InvalidRequest(err)
	}
	for field, value := range map[string]string{
		"client_id":    input.ClientID,
		"redirect_uri": input.RedirectURI,
		"resource":     input.Resource,
	} {
		if err := validateOAuthURLField(field, value); err != nil {
			return "", storeerr.InvalidRequest(err)
		}
	}
	code, err := randomTokenPart(32)
	if err != nil {
		return "", fmt.Errorf("generate authorization code: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", fmt.Errorf("begin create authorization code: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if err := lockActiveOAuthUserTx(ctx, qtx, input.UserID); err != nil {
		return "", err
	}
	if _, err := qtx.GetActiveBrowserSessionForUserByID(
		ctx,
		dbsqlc.GetActiveBrowserSessionForUserByIDParams{
			ID:                 input.BrowserSessionID,
			UserID:             input.UserID,
			IdleTimeoutSeconds: int64(browserSessionIdleDuration / time.Second),
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return "", storeerr.ErrUnauthorized
	} else if err != nil {
		return "", fmt.Errorf("revalidate authorization browser session: %w", err)
	}
	if _, err := qtx.CreateOAuthAuthorizationCode(ctx, dbsqlc.CreateOAuthAuthorizationCodeParams{
		CodeHash:      HashBearerToken(code),
		UserID:        input.UserID,
		ClientID:      input.ClientID,
		ClientName:    clientName,
		RedirectUri:   input.RedirectURI,
		CodeChallenge: input.CodeChallenge,
		Resource:      input.Resource,
		TtlSeconds:    int64(OAuthAuthorizationCodeTTL / time.Second),
	}); err != nil {
		return "", fmt.Errorf("create authorization code: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit create authorization code: %w", err)
	}
	return code, nil
}

func (s *Store) ExchangeOAuthAuthorizationCode(
	ctx context.Context,
	input ExchangeOAuthAuthorizationCodeInput,
) (OAuthTokenSetRecord, error) {
	if input.Code == "" || input.ClientID == "" || input.RedirectURI == "" || input.CodeVerifier == "" {
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("begin authorization code exchange: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	codeHash := HashBearerToken(input.Code)
	observedUserID, err := qtx.GetActiveOAuthAuthorizationCodeUserByHash(
		ctx,
		dbsqlc.GetActiveOAuthAuthorizationCodeUserByHashParams{CodeHash: codeHash},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("load authorization code user: %w", err)
	}
	if err := lockActiveOAuthUserTx(ctx, qtx, observedUserID); err != nil {
		return OAuthTokenSetRecord{}, err
	}
	code, err := qtx.ConsumeOAuthAuthorizationCode(
		ctx,
		dbsqlc.ConsumeOAuthAuthorizationCodeParams{CodeHash: codeHash},
	)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && code.UserID != observedUserID) {
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("consume authorization code: %w", err)
	}
	if code.ClientID != input.ClientID || code.RedirectUri != input.RedirectURI ||
		(input.Resource != "" && code.Resource != input.Resource) ||
		!pkceS256Matches(input.CodeVerifier, code.CodeChallenge) {
		if err := tx.Commit(ctx); err != nil {
			return OAuthTokenSetRecord{}, fmt.Errorf("commit rejected authorization code: %w", err)
		}
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	tokens, err := newOAuthTokenPair()
	if err != nil {
		return OAuthTokenSetRecord{}, err
	}
	if _, err := qtx.CreateOAuthAccessToken(ctx, dbsqlc.CreateOAuthAccessTokenParams{
		UserID:            code.UserID,
		ClientID:          code.ClientID,
		ClientName:        code.ClientName,
		Resource:          code.Resource,
		TokenHash:         HashBearerToken(tokens.AccessToken),
		RefreshTokenHash:  HashBearerToken(tokens.RefreshToken),
		AccessTtlSeconds:  int64(OAuthAccessTokenTTL / time.Second),
		RefreshTtlSeconds: int64(OAuthRefreshTokenTTL / time.Second),
	}); err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("create oauth access token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("commit authorization code exchange: %w", err)
	}
	tokens.Resource = code.Resource
	return tokens, nil
}

func (s *Store) RefreshOAuthAccessToken(
	ctx context.Context,
	input RefreshOAuthAccessTokenInput,
) (OAuthTokenSetRecord, error) {
	if input.RefreshToken == "" || input.ClientID == "" {
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	tokens, err := newOAuthTokenPair()
	if err != nil {
		return OAuthTokenSetRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("begin oauth token refresh: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	presentedRefreshTokenHash := HashBearerToken(input.RefreshToken)
	observedUserID, err := qtx.GetOAuthAccessTokenUserByRefreshToken(
		ctx,
		dbsqlc.GetOAuthAccessTokenUserByRefreshTokenParams{PresentedRefreshTokenHash: presentedRefreshTokenHash},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("load oauth refresh token user: %w", err)
	}
	if err := lockActiveOAuthUserTx(ctx, qtx, observedUserID); err != nil {
		return OAuthTokenSetRecord{}, err
	}
	rotated, err := qtx.RotateOAuthAccessToken(ctx, dbsqlc.RotateOAuthAccessTokenParams{
		TokenHash:                 HashBearerToken(tokens.AccessToken),
		RefreshTokenHash:          HashBearerToken(tokens.RefreshToken),
		AccessTtlSeconds:          int64(OAuthAccessTokenTTL / time.Second),
		RefreshTtlSeconds:         int64(OAuthRefreshTokenTTL / time.Second),
		PresentedRefreshTokenHash: presentedRefreshTokenHash,
		ClientID:                  input.ClientID,
		ReuseGraceSeconds:         int64(OAuthRefreshTokenReuseGrace / time.Second),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := qtx.RevokeOAuthAccessTokenForRefreshTokenReuse(
			ctx,
			dbsqlc.RevokeOAuthAccessTokenForRefreshTokenReuseParams{
				PresentedRefreshTokenHash: presentedRefreshTokenHash,
			},
		); err != nil {
			return OAuthTokenSetRecord{}, fmt.Errorf("revoke oauth access token for refresh token reuse: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return OAuthTokenSetRecord{}, fmt.Errorf("commit rejected oauth token refresh: %w", err)
		}
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("rotate oauth access token: %w", err)
	}
	if rotated.UserID != observedUserID || (input.Resource != "" && rotated.Resource != input.Resource) {
		return OAuthTokenSetRecord{}, storeerr.ErrUnauthorized
	}
	if err := tx.Commit(ctx); err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("commit oauth token refresh: %w", err)
	}
	tokens.Resource = rotated.Resource
	return tokens, nil
}

func (s *Store) AuthenticateOAuthAccessToken(
	ctx context.Context,
	token string,
) (OAuthAccessTokenAuthentication, error) {
	if err := bearertoken.Validate(token, bearertoken.KindOAuthAccess); err != nil {
		return OAuthAccessTokenAuthentication{}, storeerr.ErrUnauthorized
	}
	row, err := s.q.AuthenticateOAuthAccessToken(
		ctx,
		dbsqlc.AuthenticateOAuthAccessTokenParams{
			TokenHash:            HashBearerToken(token),
			TouchIntervalSeconds: int64(bearerTokenTouchInterval / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthAccessTokenAuthentication{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return OAuthAccessTokenAuthentication{}, fmt.Errorf("authenticate oauth access token: %w", err)
	}
	return OAuthAccessTokenAuthentication{
		Principal: NewOAuthAccessTokenPrincipal(row.UserID, row.OauthAccessTokenID),
		Resource:  row.Resource,
	}, nil
}

func lockActiveOAuthUserTx(ctx context.Context, qtx *dbsqlc.Queries, userID ID) error {
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrUnauthorized
		}
		return fmt.Errorf("lock user for oauth token: %w", err)
	}
	return nil
}

func validateOAuthURLField(field, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if len(value) > oauthURLFieldMaxBytes {
		return fmt.Errorf("%s cannot exceed %d bytes", field, oauthURLFieldMaxBytes)
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%s cannot contain control characters", field)
	}
	return nil
}

func newOAuthTokenPair() (OAuthTokenSetRecord, error) {
	accessToken, err := bearertoken.Generate(bearertoken.KindOAuthAccess)
	if err != nil {
		return OAuthTokenSetRecord{}, err
	}
	refreshToken, err := randomTokenPart(32)
	if err != nil {
		return OAuthTokenSetRecord{}, fmt.Errorf("generate refresh token: %w", err)
	}
	return OAuthTokenSetRecord{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    OAuthAccessTokenTTL,
	}, nil
}

func PKCES256Challenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func pkceS256Matches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(PKCES256Challenge(verifier)), []byte(challenge)) == 1
}
