package httpapi

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var (
	errUnsupportedDaemonMessage = errors.New(
		"unsupported daemon websocket message type",
	)
	errDaemonProcessOfferUnavailable = errors.New(
		"daemon process offer is no longer available",
	)
	errDaemonActionOfferUnavailable = errors.New(
		"daemon process action offer is no longer available",
	)
)

func errorResponseForMessage(msg daemonprotocol.Message, err error) daemonprotocol.Message {
	response := daemonprotocol.Message{
		Type:            daemonprotocol.MessageError,
		Error:           err.Error(),
		ErrorCode:       daemonErrorCode(err),
		ProcessID:       msg.ProcessID,
		ProcessActionID: msg.ProcessActionID,
	}
	if msg.Type == daemonprotocol.MessageReport {
		response.Type = daemonprotocol.MessageReportAck
		response.ReportID = msg.ReportID
		response.AckStatus = daemonAckStatus(err)
	}
	return response
}

func daemonAckStatus(err error) daemonprotocol.AckStatus {
	if daemonReportErrorPermanent(err) {
		return daemonprotocol.AckStatusPermanentReject
	}
	return daemonprotocol.AckStatusTransientError
}

func daemonErrorCode(err error) string {
	if errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		return daemonprotocol.ErrorCodeInvalidRuntime
	}
	if errors.Is(err, storeerr.ErrIdempotencyConflict) {
		return daemonprotocol.ErrorCodeIdempotencyConflict
	}
	if errors.Is(err, errDaemonProcessOfferUnavailable) {
		return daemonprotocol.ErrorCodeProcessOfferUnavailable
	}
	if errors.Is(err, errDaemonActionOfferUnavailable) {
		return daemonprotocol.ErrorCodeActionOfferUnavailable
	}
	if errors.Is(err, storeerr.ErrProcessActionReportBlocked) {
		return daemonprotocol.ErrorCodeProcessActionBlocked
	}
	if errors.Is(err, errUnsupportedDaemonMessage) {
		return daemonprotocol.ErrorCodeValidationFailed
	}
	if daemonReportErrorPermanent(err) {
		return daemonprotocol.ErrorCodeValidationFailed
	}
	return daemonprotocol.ErrorCodeStorageUnavailable
}

func daemonReportErrorPermanent(err error) bool {
	if errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		return false
	}
	if errors.Is(err, errDaemonReportValidation) ||
		errors.Is(err, storeerr.ErrProcessExecutionNotGranted) ||
		errors.Is(err, storeerr.ErrIdempotencyConflict) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23502" || pgErr.Code == "23514"
	}
	return false
}
