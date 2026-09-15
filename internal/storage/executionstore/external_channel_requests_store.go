package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func externalChannelRequestRecord(row dbsqlc.ExternalChannelRequest) ExternalChannelRequestRecord {
	request := ExternalChannelRequestRecord{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, TurnID: row.TurnID,
		ToolCallID: idFromSQLCPtr(row.ToolCallID), InteractionID: idFromSQLCPtr(row.InteractionID),
		NoticeKey: stringFromSQLCText(row.NoticeKey), IntegrationInstallID: row.IntegrationInstallID,
		IntegrationTargetID: row.IntegrationTargetID, IntegrationTargetBindingID: row.IntegrationTargetBindingID,
		Operation: channelconnector.OperationKind(row.Operation),
		Payload:   row.Payload, Deadline: row.DeadlineAt, CreatedAt: row.CreatedAt,
		State:           ExternalChannelRequestState(row.State),
		StateReasonCode: stringFromSQLCText(row.StateReasonCode), TerminalAt: row.TerminalAt,
	}
	if row.Result != nil {
		request.Result = *row.Result
	}
	request.CreatesReplyChannel = acceptedExternalReplyGrants(request.Payload) != nil
	return request
}

func insertExternalChannelRequest(
	ctx context.Context,
	q *dbsqlc.Queries,
	request ExternalChannelRequestRecord,
	timeout time.Duration,
) (ExternalChannelRequestRecord, error) {
	row, err := q.InsertExternalChannelRequest(ctx, dbsqlc.InsertExternalChannelRequestParams{
		ProjectID: request.ProjectID, AgentID: request.AgentID, TurnID: request.TurnID,
		ToolCallID: sqlcIDFromNil(request.ToolCallID), InteractionID: sqlcIDFromNil(request.InteractionID),
		NoticeKey: sqlcTextFromEmpty(request.NoticeKey), IntegrationInstallID: request.IntegrationInstallID,
		IntegrationTargetID: request.IntegrationTargetID, IntegrationTargetBindingID: request.IntegrationTargetBindingID,
		Operation: string(request.Operation),
		Payload:   request.Payload, DeadlineMicroseconds: timeout.Microseconds(),
	})
	if err != nil {
		return ExternalChannelRequestRecord{}, externalChannelRequestError(err)
	}
	return externalChannelRequestRecord(row), nil
}

func externalChannelRequestByOwner(
	ctx context.Context,
	q *dbsqlc.Queries,
	request ExternalChannelRequestRecord,
) (ExternalChannelRequestRecord, error) {
	row, err := q.GetExternalChannelRequestByOwner(ctx, dbsqlc.GetExternalChannelRequestByOwnerParams{
		ProjectID: request.ProjectID, AgentID: request.AgentID, TurnID: request.TurnID,
		ToolCallID: sqlcIDFromNil(request.ToolCallID), InteractionID: sqlcIDFromNil(request.InteractionID),
		NoticeKey: sqlcTextFromEmpty(request.NoticeKey),
	})
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	return externalChannelRequestRecord(row), nil
}

func (s *Store) GetExternalChannelRequest(
	ctx context.Context,
	projectID, installID, requestID ID,
) (ExternalChannelRequestRecord, error) {
	if isNilID(projectID) || isNilID(installID) || isNilID(requestID) {
		return ExternalChannelRequestRecord{}, storeerr.InvalidRequest(
			errors.New("project, connection and request are required"))
	}
	row, err := s.q.GetExternalChannelRequest(ctx, dbsqlc.GetExternalChannelRequestParams{
		ProjectID: projectID, IntegrationInstallID: installID, ID: requestID,
	})
	if err != nil {
		return ExternalChannelRequestRecord{}, externalChannelRequestError(err)
	}
	return externalChannelRequestRecord(row), nil
}

// ListPendingExternalChannelRequests does not claim, acknowledge or reschedule
// work. Seeing the same ID again is not permission to repeat a provider mutation.
func (s *Store) ListPendingExternalChannelRequests(
	ctx context.Context,
	input ListExternalChannelRequestsInput,
) (ListExternalChannelRequestsResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.IntegrationInstallID) || input.Limit < 1 || input.Limit > 100 {
		return ListExternalChannelRequestsResult{}, storeerr.InvalidRequest(
			errors.New("connection and limit 1..100 are required"))
	}
	params := dbsqlc.ListPendingExternalChannelRequestsParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID, RowLimit: input.Limit + 1,
	}
	if input.After != nil {
		if input.After.CreatedAt.IsZero() || isNilID(input.After.ID) {
			return ListExternalChannelRequestsResult{}, storeerr.InvalidRequest(errors.New("invalid request cursor"))
		}
		params.CursorCreatedAt = &input.After.CreatedAt
		params.CursorID = &input.After.ID
	}
	rows, err := s.q.ListPendingExternalChannelRequests(ctx, params)
	if err != nil {
		return ListExternalChannelRequestsResult{}, externalChannelRequestError(err)
	}
	result := ListExternalChannelRequestsResult{Requests: make([]ExternalChannelRequestRecord, 0, len(rows))}
	result.HasMore = len(rows) > int(input.Limit)
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	for _, row := range rows {
		result.Requests = append(result.Requests, externalChannelRequestRecord(row))
	}
	return result, nil
}

// lockExternalChannelRequestTx deliberately enters lifecycle gates even when
// authority has been revoked. Cleanup still has to settle existing obligations.
// Callers may not hold an agent/child lock when entering this function.
func (s *Store) lockExternalChannelRequestTx(
	ctx context.Context,
	tx pgx.Tx,
	request ExternalChannelRequestRecord,
) (ExternalChannelRequestRecord, error) {
	q := s.q.WithTx(tx)
	orgID, err := q.GetExternalChannelRequestLifecycleScope(ctx, dbsqlc.GetExternalChannelRequestLifecycleScopeParams{
		ProjectID: request.ProjectID, IntegrationInstallID: request.IntegrationInstallID,
	})
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	if err := lifecyclelock.OrganizationShared(ctx, tx, orgID); err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	if err := q.LockProjectLifecycleShared(ctx,
		dbsqlc.LockProjectLifecycleSharedParams{ProjectID: request.ProjectID}); err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	if err := q.LockIntegrationInstallLifecycleShared(ctx,
		dbsqlc.LockIntegrationInstallLifecycleSharedParams{InstallID: request.IntegrationInstallID}); err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: request.ProjectID, ID: request.AgentID,
	}); err != nil {
		return ExternalChannelRequestRecord{}, externalChannelRequestError(err)
	}
	row, err := q.GetExternalChannelRequestForUpdate(ctx, dbsqlc.GetExternalChannelRequestForUpdateParams{
		ProjectID: request.ProjectID, IntegrationInstallID: request.IntegrationInstallID, ID: request.ID,
	})
	if err != nil {
		return ExternalChannelRequestRecord{}, externalChannelRequestError(err)
	}
	return externalChannelRequestRecord(row), nil
}

func validateExternalChannelAccess(
	ctx context.Context,
	q *dbsqlc.Queries,
	request ExternalChannelRequestRecord,
	access integrationstore.ChannelAccess,
) error {
	if !access.Active || access.IntegrationKind != integrationstore.IntegrationKindExternal ||
		access.IntegrationInstallID != request.IntegrationInstallID ||
		(request.CreatesReplyChannel && !access.Capabilities.CreatesReplyChannel) {
		return storeerr.ErrUnauthorized
	}
	switch request.Operation {
	case channelconnector.OperationRead:
		if access.Capabilities.Read {
			return nil
		}
	case channelconnector.OperationSend:
		if access.Capabilities.Send {
			return nil
		}
	case channelconnector.OperationInteraction:
		interaction, err := q.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
			ProjectID: request.ProjectID, AgentID: request.AgentID, ID: request.InteractionID,
		})
		if err != nil {
			return externalChannelRequestError(err)
		}
		if interaction.State != string(AgentInteractionStateOpen) || interaction.TurnID != request.TurnID ||
			idFromSQLCPtr(interaction.IntegrationTargetID) != request.IntegrationTargetID {
			return storeerr.ErrStateTransitionConflict
		}
		if (interaction.InteractionKind == string(AgentInteractionKindQuestion) && access.Capabilities.Questions) ||
			(interaction.InteractionKind == string(AgentInteractionKindPermission) && access.Capabilities.Permissions) {
			return nil
		}
	default:
		return storeerr.InvalidRequest(fmt.Errorf("invalid channel operation %q", request.Operation))
	}
	return storeerr.ErrUnauthorized
}
