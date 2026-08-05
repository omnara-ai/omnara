package identitystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateBrowserSessionInput struct {
	UserID    ID
	Token     string
	CSRFToken string
	TTL       time.Duration
}

type BrowserSessionRecord struct {
	ID            ID
	UserID        ID
	TokenHash     string
	CSRFTokenHash string
	CreatedAt     time.Time
	LastSeenAt    time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
}

const browserSessionIdleDuration = 7 * 24 * time.Hour

func (s *Store) CreateBrowserSession(
	ctx context.Context,
	input CreateBrowserSessionInput,
) (BrowserSessionRecord, error) {
	if isNilID(input.UserID) {
		return BrowserSessionRecord{}, errors.New("user id is required")
	}
	if input.Token == "" {
		return BrowserSessionRecord{}, errors.New("session token is required")
	}
	if input.CSRFToken == "" {
		return BrowserSessionRecord{}, errors.New("csrf token is required")
	}
	if input.TTL < time.Millisecond {
		return BrowserSessionRecord{}, errors.New("session ttl must be at least one millisecond")
	}
	row, err := s.q.CreateBrowserSession(ctx, dbsqlc.CreateBrowserSessionParams{
		UserID:          input.UserID,
		TokenHash:       HashBearerToken(input.Token),
		CsrfTokenHash:   HashBearerToken(input.CSRFToken),
		TtlMilliseconds: input.TTL.Milliseconds(),
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return BrowserSessionRecord{}, storeerr.ErrIdempotencyConflict
		}
		return BrowserSessionRecord{}, fmt.Errorf("create browser session: %w", err)
	}
	return browserSessionRecordFromSQLC(row), nil
}

func (s *Store) AuthenticateBrowserSession(
	ctx context.Context,
	token string,
) (PrincipalRecord, string, error) {
	if token == "" {
		return PrincipalRecord{}, "", storeerr.ErrUnauthorized
	}
	row, err := s.q.AuthenticateBrowserSession(
		ctx,
		dbsqlc.AuthenticateBrowserSessionParams{
			TokenHash:            HashBearerToken(token),
			IdleTimeoutSeconds:   int64(browserSessionIdleDuration / time.Second),
			TouchIntervalSeconds: int64(browserSessionTouchInterval / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PrincipalRecord{}, "", storeerr.ErrUnauthorized
	}
	if err != nil {
		return PrincipalRecord{}, "", fmt.Errorf("authenticate browser session: %w", err)
	}
	return PrincipalRecord{
		Type:             row.PrincipalType,
		ID:               row.UserID,
		BrowserSessionID: row.BrowserSessionID,
	}, row.CsrfTokenHash, nil
}

func (s *Store) RevokeBrowserSession(ctx context.Context, token string) error {
	if token == "" {
		return storeerr.ErrUnauthorized
	}
	if err := s.q.RevokeBrowserSession(
		ctx,
		dbsqlc.RevokeBrowserSessionParams{TokenHash: HashBearerToken(token)},
	); err != nil {
		return fmt.Errorf("revoke browser session: %w", err)
	}
	return nil
}
