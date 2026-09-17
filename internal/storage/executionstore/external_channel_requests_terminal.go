package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ExpireExternalChannelRequests selects without locking, then follows the same
// lifecycle order as completion. Each transaction only terminalizes a durable
// obligation; no callback, provider operation or future attempt is scheduled.
func (s *Store) ExpireExternalChannelRequests(ctx context.Context, limit int32) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, storeerr.InvalidRequest(errors.New("expiration batch limit must be 1..1000"))
	}
	candidates, err := s.q.ListExpiredExternalChannelRequests(ctx, dbsqlc.ListExpiredExternalChannelRequestsParams{
		RowLimit: limit,
	})
	if err != nil {
		return 0, err
	}
	var count int64
	for _, candidate := range candidates {
		expired, err := s.expireExternalChannelRequest(ctx, externalChannelRequestRecord(candidate))
		if err != nil {
			return count, err
		}
		if expired {
			count++
		}
	}
	return count, nil
}

func (s *Store) expireExternalChannelRequest(
	ctx context.Context, candidate ExternalChannelRequestRecord,
) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	request, err := s.lockExternalChannelRequestTx(ctx, tx, candidate)
	if errors.Is(err, storeerr.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if request.State != ExternalChannelRequestPending {
		return false, nil
	}
	q := s.q.WithTx(tx)
	row, err := q.ExpireExternalChannelRequest(ctx, dbsqlc.ExpireExternalChannelRequestParams{
		ProjectID: request.ProjectID, IntegrationInstallID: request.IntegrationInstallID, ID: request.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	request = externalChannelRequestRecord(row)
	notifications := s.newTxNotifications()
	if request.ToolCallID != uuid.Nil {
		completion, err := externalChannelTimeoutCompletion(request)
		if err != nil {
			return false, err
		}
		if _, err := finishExternalChannelToolTx(ctx, tx, notifications, request, completion); err != nil {
			return false, err
		}
	}
	// Presentation expiration intentionally does not resolve its interaction or
	// call the canonical tool completion helper, which closes open interactions.
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "expire external channel request"); err != nil {
		return false, err
	}
	return true, nil
}

// cancelExternalChannelRequestsForInstallationTx is called only after the
// installation owner has locked all affected agents. It must not acquire an
// installation gate or cancel an unrelated turn/channel operation.
func cancelExternalChannelRequestsForInstallationTx(
	ctx context.Context, tx pgx.Tx, projectID, installID uuid.UUID,
) error {
	rows, err := dbsqlc.New(tx).CancelExternalChannelRequestsForInstallation(ctx,
		dbsqlc.CancelExternalChannelRequestsForInstallationParams{
			ProjectID: projectID, IntegrationInstallID: installID, Reason: "connection_deleted",
		})
	if err != nil {
		return err
	}
	for _, row := range rows {
		request := externalChannelRequestRecord(row)
		if request.ToolCallID == uuid.Nil {
			continue
		}
		completion, err := externalChannelTerminalCompletion(request, ToolResultOutcomeCanceled,
			"The connection was removed. The provider outcome is unknown; do not assume it is safe to resend.")
		if err != nil {
			return err
		}
		// The installation facade owns the commit. Result/event/wakeup records
		// are durable even when its transaction has no optional hint publisher.
		if _, err := finishExternalChannelToolTx(ctx, tx, nil, request, completion); err != nil {
			return err
		}
	}
	return nil
}

func finishExternalChannelToolTx(
	ctx context.Context,
	tx pgx.Tx,
	notifications *notifications.TxNotifications,
	request ExternalChannelRequestRecord,
	completion ToolCallCompletionInput,
) (ToolCallRecord, error) {
	if request.ToolCallID == uuid.Nil || request.InteractionID != uuid.Nil || request.NoticeKey != "" {
		return ToolCallRecord{}, storeerr.ErrInvalidToolCallDisposition
	}
	q := dbsqlc.New(tx)
	_, err := q.CompleteToolCallFromExternalChannelRequest(ctx, dbsqlc.CompleteToolCallFromExternalChannelRequestParams{
		ProjectID: request.ProjectID, AgentID: request.AgentID, ID: request.ID, Outcome: string(completion.Outcome),
	})
	if err != nil {
		return ToolCallRecord{}, externalChannelRequestError(err)
	}
	record, err := getToolCallTx(ctx, tx, request.ProjectID, request.AgentID, request.ToolCallID)
	if err != nil {
		return ToolCallRecord{}, err
	}
	record.Outcome = completion.Outcome
	return finishCompletedToolCallTx(ctx, notifications, tx, q, record, toolCallResultInput(completion))
}

func externalChannelTimeoutCompletion(request ExternalChannelRequestRecord) (ToolCallCompletionInput, error) {
	detail := "The connector did not report before the deadline. "
	if request.StateReasonCode == "grant_revoked" {
		detail = "Channel access was revoked before the request completed. "
	}
	return externalChannelTerminalCompletion(request, ToolResultOutcomeFailed,
		detail+"The provider outcome is unknown; do not assume it is safe to resend.")
}

func externalChannelTerminalCompletion(
	request ExternalChannelRequestRecord,
	outcome ToolResultOutcome,
	detail string,
) (ToolCallCompletionInput, error) {
	requestID, err := publicid.Encode(publicid.KindExternalChannelRequest, request.ID)
	if err != nil {
		return ToolCallCompletionInput{}, err
	}
	result, err := json.Marshal(struct {
		RequestID string                            `json:"request_id"`
		Status    channelconnector.OperationOutcome `json:"status"`
		Code      string                            `json:"code"`
		Detail    string                            `json:"detail"`
	}{requestID, channelconnector.OperationUnknown, request.StateReasonCode, detail})
	if err != nil {
		return ToolCallCompletionInput{}, err
	}
	parts, err := ToolResultContentParts(result)
	return ToolCallCompletionInput{Outcome: outcome, ResultContentParts: parts}, err
}
