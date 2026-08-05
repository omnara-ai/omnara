package identitystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) EnsureUserPrincipalStillActiveTx(
	ctx context.Context,
	tx pgx.Tx,
	principal PrincipalRecord,
) error {
	return ensureUserPrincipalStillActive(ctx, s.q.WithTx(tx), principal)
}

func ensureUserPrincipalStillActive(
	ctx context.Context,
	q *dbsqlc.Queries,
	principal PrincipalRecord,
) error {
	if principal.Type == "" {
		return nil
	}
	if principal.Type != PrincipalTypeUser || isNilID(principal.ID) || isNilID(principal.BrowserSessionID) {
		return storeerr.ErrUnauthorized
	}
	_, err := q.GetActiveBrowserSessionForUserByID(ctx, dbsqlc.GetActiveBrowserSessionForUserByIDParams{
		ID:                 principal.BrowserSessionID,
		UserID:             principal.ID,
		IdleTimeoutSeconds: int64(browserSessionIdleDuration / time.Second),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("revalidate browser session: %w", err)
	}
	return nil
}
