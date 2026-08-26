package accountsecurity

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/dbconn"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Service struct {
	db       dbconn.DB
	identity *identitystore.Store
}

func isNilID(id identitystore.ID) bool {
	return id == identitystore.NilID
}

func New(
	db dbconn.DB,
	identity *identitystore.Store,
) *Service {
	return &Service{
		db:       db,
		identity: identity,
	}
}

func (s *Service) RevokeUserTokensForCompromiseWithPasswordIfPresent(
	ctx context.Context,
	userID identitystore.ID,
	currentPassword string,
) error {
	if isNilID(userID) {
		return storeerr.ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
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
