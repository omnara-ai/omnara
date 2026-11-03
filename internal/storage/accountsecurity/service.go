package accountsecurity

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Service struct {
	pool     *storeutil.Pool
	identity *identitystore.Store
}

func New(
	pool *pgxpool.Pool,
	identity *identitystore.Store,
) *Service {
	db := storeutil.WrapPool(pool)
	return &Service{
		pool:     db,
		identity: identity,
	}
}

func (s *Service) RevokeUserTokensForCompromiseWithPasswordIfPresent(
	ctx context.Context,
	userID uuid.UUID,
	currentPassword string,
) error {
	if userID == uuid.Nil {
		return storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin compromise revocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.identity.ValidateCompromiseRevocationTx(ctx, tx, userID, currentPassword); err != nil {
		return err
	}
	if err := s.identity.RevokeUserAuthTokensTx(ctx, tx, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit compromise token revocation: %w", err)
	}
	return nil
}
