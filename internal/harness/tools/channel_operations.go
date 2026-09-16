package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/publicid"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type preparedChannelOperation struct {
	request channelconnector.OperationRequest
	owner   executionstore.PreparedChannelOperation
	access  integrationstore.ChannelAccess
	binding integrationstore.IntegrationTargetBindingRecord
}

// channelOperationToolResult is a terminal observation of this tool operation.
// A failed operation can retain known publication facts without claiming that a
// continuation registered or that a published message is safe to resend.
type channelOperationToolResult struct {
	RequestID string                               `json:"request_id,omitempty"`
	Status    channelconnector.OperationOutcome    `json:"status"`
	Message   *channelconnector.MessageObservation `json:"message,omitempty"`
	Code      string                               `json:"code,omitempty"`
	Detail    string                               `json:"detail,omitempty"`
}

func (e Executor) prepareChannelOperation(
	ctx context.Context,
	call asyncToolContext,
	channelID uuid.UUID, kind channelconnector.OperationKind,
) (preparedChannelOperation, error) {
	if e.ChannelOperations == nil {
		return preparedChannelOperation{}, errors.New("managed channel operations are not configured")
	}
	operation := integrationstore.ChannelBindingOperation(kind)
	owner, err := e.Store.Execution().PrepareChannelOperation(ctx, executionstore.PrepareChannelOperationInput{
		ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
			ProjectID: call.Turn.ProjectID, AgentID: call.Turn.AgentID,
			ToolCallID: call.ToolCallID, RuntimeLockID: call.Turn.RuntimeLockID,
		},
		TurnID: call.Turn.TurnID, ChannelID: channelID, Operation: operation,
	})
	if err != nil {
		return preparedChannelOperation{}, err
	}
	access, binding := owner.Access(), owner.Binding()
	requestID, err := publicid.Encode(publicid.KindToolCall, call.ToolCallID)
	if err != nil {
		return preparedChannelOperation{}, err
	}
	request, err := newChannelOperationRequest(access, requestID, kind)
	if err != nil {
		return preparedChannelOperation{}, err
	}
	return preparedChannelOperation{access: access, binding: binding, owner: owner, request: request}, nil
}

func newChannelOperationRequest(
	access integrationstore.ChannelAccess, requestID string, kind channelconnector.OperationKind,
) (channelconnector.OperationRequest, error) {
	request := channelconnector.OperationRequest{
		RequestID: requestID, Kind: kind,
		Capability: channelconnector.Capability{ConnectorKey: access.ConnectorKey, Provider: access.Provider},
	}
	for _, field := range []struct {
		kind publicid.Kind
		id   uuid.UUID
		out  *string
	}{
		{publicid.KindProject, access.ProjectID, &request.Scope.ProjectID},
		{publicid.KindAgent, access.AgentID, &request.Scope.AgentID},
		{publicid.KindIntegrationApp, access.IntegrationAppID, &request.Scope.IntegrationAppID},
		{publicid.KindIntegrationInstall, access.IntegrationInstallID, &request.Scope.IntegrationInstallID},
		{publicid.KindIntegrationTarget, access.ChannelID, &request.Scope.ChannelID},
	} {
		value, err := publicid.Encode(field.kind, field.id)
		if err != nil {
			return channelconnector.OperationRequest{}, err
		}
		*field.out = value
	}
	return request, nil
}

func channelOperationDestination(access integrationstore.ChannelAccess) channelconnector.OperationDestination {
	return channelconnector.OperationDestination{
		ImplementationKey: access.ImplementationKey, ProviderRef: access.ProviderRef,
		ProviderRefKind: access.ProviderRefKind, ProviderMetadata: access.ProviderMetadata,
	}
}

func (e Executor) channelOperationArtifacts(
	ctx context.Context,
	turn Turn,
	ids []string,
) ([]channelconnector.OperationArtifact, error) {
	artifacts := make([]channelconnector.OperationArtifact, 0, len(ids))
	for _, id := range ids {
		artifactID, err := publicid.Decode(publicid.KindArtifact, id)
		if err != nil {
			return nil, err
		}
		record, err := e.Store.Artifacts().GetArtifact(ctx, turn.ProjectID, turn.AgentID, artifactID)
		if err != nil {
			return nil, fmt.Errorf("artifact is not available to this agent: %w", err)
		}
		// Artifact filenames are display metadata, never source paths. Preserve
		// the existing media fallback while removing directory components.
		filename := path.Base(strings.ReplaceAll(modelcontext.MediaFilename(record.Filename, record.ContentType), "\\", "/"))
		artifacts = append(artifacts, channelconnector.OperationArtifact{
			ID: id, Filename: filename, ContentType: record.ContentType,
			Open: func(ctx context.Context) (io.ReadCloser, error) {
				body, _, err := e.Store.Artifacts().OpenArtifactBlob(ctx, turn.ProjectID, turn.AgentID, artifactID)
				return body, err
			},
		})
	}
	return artifacts, nil
}

func channelOperationFailure(
	requestID, code, detail string,
	outcome channelconnector.OperationOutcome,
) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(channelOperationToolResult{
		RequestID: requestID, Status: outcome, Code: code, Detail: detail,
	})
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, errors.New(code)), nil
}

func channelTransportFailure(
	requestID string,
	result channelconnector.OperationResult,
	err error,
) (asyncPhaseResult, error) {
	outcome := channelconnector.OperationUnknown
	var operationError *channelconnector.OperationError
	if errors.As(err, &operationError) {
		outcome = operationError.Outcome
	} else if err == nil && result.RequestID == requestID {
		outcome = result.Outcome
	}
	if result.RequestID == requestID && result.Outcome == outcome {
		if failure, decodeErr := channelconnector.DecodeOperationFailure(result.Payload); decodeErr == nil &&
			failure.MatchesOutcome(outcome) {
			content, encodeErr := structuredToolResultContent(channelOperationToolResult{
				RequestID: requestID, Status: outcome, Code: string(failure.Code),
				Detail: failure.Detail(),
			})
			if encodeErr != nil {
				return nil, encodeErr
			}
			return failAsynchronously(content, errors.New(string(failure.Code))), nil
		}
	}
	if outcome == channelconnector.OperationFailed {
		return channelOperationFailure(requestID, "channel_operation_failed",
			"The channel operation failed without a confirmed publication.", channelconnector.OperationFailed)
	}
	// An unclassified transport error cannot establish a safe resend.
	return channelOperationFailure(requestID, "channel_operation_unknown",
		"The channel operation outcome is unknown; do not assume it is safe to resend.", channelconnector.OperationUnknown)
}
