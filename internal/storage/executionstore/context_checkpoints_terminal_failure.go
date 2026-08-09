package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type RecordTerminalCompactionFailureInput struct {
	ProjectID               ID
	AgentID                 ID
	RuntimeLockID           ID
	ModelCallContextID      ID
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ServedProviderModelSlug string
	ProviderRequestID       string
	ProviderResponseID      string
	ErrorKind               modelprotocol.ErrorKind
	ErrorCode               string
	ErrorMessage            string
	ErrorDetails            json.RawMessage
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
}

func (s *Store) RecordTerminalCompactionFailure(
	ctx context.Context,
	input RecordTerminalCompactionFailureInput,
) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		isNilID(input.ModelCallContextID) ||
		input.ErrorKind == "" || input.ErrorMessage == "" {
		return errors.New("project, agent, runtime, compaction context, and error are required")
	}
	var err error
	input.ErrorDetails, err = normalizedJSONObject(input.ErrorDetails, "compaction error details")
	if err != nil {
		return err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin terminal compaction failure: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return err
	}
	if err := recordTerminalCompactionFailureTx(
		ctx,
		txNotifications,
		tx,
		q,
		input,
		modelCallContextRuntimeOwned,
	); err != nil {
		return err
	}
	return s.commitTxWithNotifications(ctx, tx, txNotifications, "record terminal compaction failure")
}

func recordTerminalCompactionFailureTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	input RecordTerminalCompactionFailureInput,
	runtimeAuthority modelCallContextRuntimeAuthority,
) error {
	modelFailure := RecordModelCallErrorAndCompleteContextInput{}
	modelFailure.ProjectID = input.ProjectID
	modelFailure.AgentID = input.AgentID
	modelFailure.RuntimeLockID = input.RuntimeLockID
	modelFailure.ModelCallContextID = input.ModelCallContextID
	modelFailure.APIFormat = input.APIFormat
	modelFailure.APIVariant = input.APIVariant
	modelFailure.ServedProviderModelSlug = input.ServedProviderModelSlug
	modelFailure.ProviderRequestID = input.ProviderRequestID
	modelFailure.ProviderResponseID = input.ProviderResponseID
	modelFailure.ErrorKind = input.ErrorKind
	modelFailure.ErrorCode = input.ErrorCode
	modelFailure.ErrorMessage = input.ErrorMessage
	modelFailure.ErrorDetails = input.ErrorDetails
	modelFailure.Usage = input.Usage
	modelFailure.ProviderReportedCostUSD = input.ProviderReportedCostUSD
	_, err := recordTerminalModelCallFailureTx(
		ctx,
		txNotifications,
		tx,
		q,
		modelFailure,
		runtimeAuthority,
		ModelCallOperationCompaction,
	)
	return err
}
