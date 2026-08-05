package accountsecurity

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Service struct {
	pool                *pgxpool.Pool
	identity            *identitystore.Store
	execution           *executionstore.Store
	postCommitPublisher notifications.PostCommitPublisher
}

func isNilID(id identitystore.ID) bool {
	return id == identitystore.NilID
}

func New(
	pool *pgxpool.Pool,
	identity *identitystore.Store,
	execution *executionstore.Store,
	postCommitPublisher notifications.PostCommitPublisher,
) *Service {
	return &Service{
		pool:                pool,
		identity:            identity,
		execution:           execution,
		postCommitPublisher: postCommitPublisher,
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
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin compromise revocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.identity.ValidateCompromiseRevocationTx(ctx, tx, userID, currentPassword); err != nil {
		return err
	}
	qtx := dbsqlc.New(tx)
	tokens, err := qtx.ListActiveBYOMachineDaemonTokensForUser(
		ctx,
		dbsqlc.ListActiveBYOMachineDaemonTokensForUserParams{UserID: &userID},
	)
	if err != nil {
		return fmt.Errorf("list compromised machine daemon tokens: %w", err)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].MachineID == tokens[j].MachineID {
			return tokens[i].ID.String() < tokens[j].ID.String()
		}
		return tokens[i].MachineID.String() < tokens[j].MachineID.String()
	})
	txNotifications := s.newTxNotifications()
	for _, token := range tokens {
		if _, err := s.execution.RevokeBYOMachineDaemonTokenTx(
			ctx,
			tx,
			txNotifications,
			token.OrgID,
			token.MachineID,
			token.ID,
			"user_compromise",
		); err != nil && !errors.Is(err, storeerr.ErrNotFound) {
			return fmt.Errorf("revoke compromised machine daemon token: %w", err)
		}
	}
	if err := s.identity.RevokeUserAuthTokensTx(ctx, tx, userID); err != nil {
		return err
	}
	return storeutil.CommitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		s.postCommitPublisher,
		"compromise token revocation",
	)
}

func (s *Service) newTxNotifications() *notifications.TxNotifications {
	return notifications.NewTxNotifications()
}
