//go:build integration

package executionstore

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) IntegrationClaimUnprepared(
	ctx context.Context, input ClaimNextAgentWorkInput,
) (ClaimedAgentWork, bool, error) {
	return s.claimNextAgentWork(ctx, input, false)
}

func (s *Store) IntegrationWrapTransactions(wrap func(pgx.Tx) pgx.Tx) {
	s.cell.WrapTransactionsForTest(wrap)
}

func (s *Store) IntegrationWrapQueries(wrap func(dbsqlc.DBTX) dbsqlc.DBTX) {
	s.q = dbsqlc.New(wrap(s.pool))
}
