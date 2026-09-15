package executionstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// channelOperationCompletionInput is shared by managed and external completion.
// The caller holds the lifecycle/agent locks and has rechecked the exact binding.
// This helper performs no provider I/O and does not settle any execution owner.
type channelOperationCompletionInput struct {
	RequestID           string
	Operation           channelconnector.OperationKind
	Payload             json.RawMessage
	Access              integrationstore.ChannelAccess
	Binding             integrationstore.IntegrationTargetBindingRecord
	CreatesReplyChannel bool
	Result              channelconnector.OperationResult
}

func (s *Store) channelOperationCompletionTx(
	ctx context.Context,
	tx pgx.Tx,
	input channelOperationCompletionInput,
) (ToolCallCompletionInput, error) {
	if input.RequestID == "" || input.Result.RequestID != input.RequestID {
		return ToolCallCompletionInput{}, storeerr.InvalidRequest(errors.New("channel result request ID does not match"))
	}
	var result any
	outcome := ToolResultOutcomeSucceeded
	switch input.Result.Outcome {
	case channelconnector.OperationFailed, channelconnector.OperationUnknown:
		if err := validateChannelFailurePayload(input.Result.Payload); err != nil {
			return ToolCallCompletionInput{}, storeerr.InvalidRequest(err)
		}
		outcome = ToolResultOutcomeFailed
		failure := map[string]any{"request_id": input.RequestID, "code": "connector_" + string(input.Result.Outcome)}
		if input.Result.Outcome == channelconnector.OperationUnknown {
			failure["detail"] = "The provider outcome is unknown; do not assume it is safe to resend."
		}
		result = failure
	case channelconnector.OperationCompleted:
		switch input.Operation {
		case channelconnector.OperationSend:
			var err error
			result, outcome, err = s.applyChannelSendResultTx(ctx, tx, input)
			if err != nil {
				return ToolCallCompletionInput{}, err
			}
		case channelconnector.OperationRead:
			var err error
			result, err = s.applyChannelReadResultTx(ctx, tx, input)
			if err != nil {
				return ToolCallCompletionInput{}, err
			}
		default:
			return ToolCallCompletionInput{}, storeerr.InvalidRequest(errors.New("invalid tool channel operation"))
		}
	default:
		return ToolCallCompletionInput{}, storeerr.InvalidRequest(errors.New("invalid channel outcome"))
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return ToolCallCompletionInput{}, err
	}
	parts, err := ToolResultContentParts(raw)
	return ToolCallCompletionInput{Outcome: outcome, ResultContentParts: parts}, err
}

func (s *Store) applyChannelSendResultTx(
	ctx context.Context,
	tx pgx.Tx,
	input channelOperationCompletionInput,
) (channelconnector.SendMessageResult, ToolResultOutcome, error) {
	result := channelconnector.SendMessageResult{RequestID: input.RequestID}
	observed, err := channelconnector.DecodeSendResult(input.Result.Payload)
	if err != nil {
		return result, "", storeerr.InvalidRequest(err)
	}
	var sent channelconnector.SendPayload
	if err := json.Unmarshal(input.Payload, &sent); err != nil {
		return result, "", fmt.Errorf("decode accepted send payload: %w", err)
	}
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, input.Access.ChannelID)
	if err != nil {
		return result, "", err
	}
	result.Message = channelconnector.MessageObservation{
		Content: sent.Message, Publication: observed.Publication, ChannelID: channelID,
		MessageID: observed.MessageID, CreatedAt: observed.CreatedAt, Metadata: observed.Metadata,
	}
	message := &result.Message
	if observed.ReplyChannel == nil {
		return result, ToolResultOutcomeSucceeded, nil
	}
	// SQL registration failures can abort a transaction. A savepoint preserves
	// the accepted publication while rolling back all partial target/grant writes.
	registration, err := tx.Begin(ctx)
	if err != nil {
		return result, "", err
	}
	child, registrationErr := s.registerChannelReplyTx(ctx, registration, input, *observed.ReplyChannel)
	if registrationErr != nil {
		event := log.NewEvent(ctx, "channel.reply_registration.failed", log.Fields{
			"channel.request_id": input.RequestID,
			"project.id":         input.Access.ProjectID, "agent.id": input.Access.AgentID,
			"integration_install.id": input.Access.IntegrationInstallID,
			"channel.id":             input.Access.ChannelID, "channel.binding_id": input.Binding.ID,
		})
		event.Level(log.WarnLevel)
		event.Error(registrationErr)
		event.Done(ctx)
		if err := registration.Rollback(ctx); err != nil {
			return result, "", err
		}
		if observed.MessageChannel == channelconnector.MessageAtReplyChannel {
			message.ChannelID = ""
		}
		// Publication remains known. The failed tool outcome
		// reports local registration trouble, never a definitely-unsent message.
		result.ContinuationError = &channelconnector.ContinuationError{
			Code:    "reply_channel_registration_failed",
			Message: "Publication was reported, but its reply channel could not be registered. Do not resend the message.",
		}
		return result, ToolResultOutcomeFailed, nil
	}
	if err := registration.Commit(ctx); err != nil {
		return result, "", err
	}
	childID, err := publicid.Encode(publicid.KindIntegrationTarget, child.ID)
	if err != nil {
		return result, "", err
	}
	if observed.MessageChannel == channelconnector.MessageAtReplyChannel {
		message.ChannelID = childID
	}
	message.ReplyChannelID = childID
	return result, ToolResultOutcomeSucceeded, nil
}

func (s *Store) registerChannelReplyTx(
	ctx context.Context,
	tx pgx.Tx,
	input channelOperationCompletionInput,
	reply channelconnector.ReplyDestination,
) (integrationstore.IntegrationTargetRecord, error) {
	access, binding := input.Access, input.Binding
	if reply.ImplementationKey == access.ImplementationKey && reply.ProviderRef == access.ProviderRef &&
		reply.ProviderRefKind == access.ProviderRefKind {
		// A reply to the already addressed thread is ordinary send authority,
		// not a request to create a child or delegate more permissions.
		return s.integrations.GetIntegrationTargetTx(ctx, tx, access.ProjectID, access.ChannelID)
	}
	if !input.CreatesReplyChannel || !access.Capabilities.CreatesReplyChannel || binding.ReplyChannelGrants == nil {
		return integrationstore.IntegrationTargetRecord{}, fmt.Errorf(
			"reply channel delegation is not authorized: %w", storeerr.ErrUnauthorized)
	}
	definition, err := s.q.WithTx(tx).GetChannelDefinitionByImplementation(ctx,
		dbsqlc.GetChannelDefinitionByImplementationParams{
			ProjectID: access.ProjectID, IntegrationInstallID: access.IntegrationInstallID,
			ImplementationKey: reply.ImplementationKey,
		})
	if err != nil {
		return integrationstore.IntegrationTargetRecord{}, fmt.Errorf("load reply channel definition: %w", err)
	}
	registered, err := s.integrations.RegisterReplyChannelTx(ctx, tx, binding,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: access.ProjectID, IntegrationInstallID: access.IntegrationInstallID,
			ChannelDefinitionID: definition.ID, ParentChannelID: access.ChannelID,
			ProviderRef: reply.ProviderRef, ProviderRefKind: reply.ProviderRefKind,
			DisplayName: reply.DisplayName, ProviderMetadata: reply.ProviderMetadata,
		})
	return registered.Channel, err
}

func (s *Store) applyChannelReadResultTx(
	ctx context.Context,
	tx pgx.Tx,
	input channelOperationCompletionInput,
) (channelconnector.ReadResult, error) {
	var result channelconnector.ReadResult
	var request channelconnector.ReadPayload
	if err := json.Unmarshal(input.Payload, &request); err != nil {
		return result, fmt.Errorf("decode accepted read payload: %w", err)
	}
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, input.Access.ChannelID)
	if err != nil {
		return result, err
	}
	history, err := channelconnector.DecodeReadResult(input.Result.Payload, request.Limit)
	if err != nil {
		return result, storeerr.InvalidRequest(err)
	}
	messages := make([]channelconnector.MessageObservation, 0, len(history.Messages))
	for _, message := range history.Messages {
		for _, artifactID := range message.Content.ArtifactIDs {
			id, err := publicid.Decode(publicid.KindArtifact, artifactID)
			if err != nil {
				return result, storeerr.InvalidRequest(err)
			}
			if _, err := s.q.WithTx(tx).GetArtifact(ctx, dbsqlc.GetArtifactParams{
				ProjectID: input.Access.ProjectID, AgentID: input.Access.AgentID, ID: id,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return result, storeerr.ErrUnauthorized
				}
				return result, err
			}
		}
		observation := channelconnector.MessageObservation{
			Content: message.Content, Publication: message.Publication, ChannelID: channelID,
			MessageID: message.MessageID, Author: message.Author, CreatedAt: message.CreatedAt, Metadata: message.Metadata,
		}
		if message.ReplyChannel != nil {
			observation.ReplyChannelID, err = s.knownChannelReferenceTx(ctx, tx, input.Access, *message.ReplyChannel)
			if err != nil {
				return result, err
			}
		}
		if reference := message.ReplyTo; reference != nil {
			replyChannelID := channelID
			if reference.Destination != nil {
				replyChannelID, err = s.knownChannelReferenceTx(ctx, tx, input.Access, *reference.Destination)
				if err != nil {
					return result, err
				}
			}
			if replyChannelID != "" {
				observation.ReplyTo = &channelconnector.MessageReference{
					ChannelID: replyChannelID, MessageID: reference.MessageID,
				}
			}
		}
		messages = append(messages, observation)
	}
	result.Messages, result.Coverage = messages, history.Coverage
	if history.CoverageReason != "" {
		result.CoverageReason = history.CoverageReason
	}
	if history.NextCursor != "" {
		projectID, err := publicid.Encode(publicid.KindProject, input.Access.ProjectID)
		if err != nil {
			return result, err
		}
		agentID, err := publicid.Encode(publicid.KindAgent, input.Access.AgentID)
		if err != nil {
			return result, err
		}
		cursor, err := json.Marshal(map[string]string{
			"project_id": projectID, "agent_id": agentID, "channel_id": channelID,
			"provider_cursor": base64.RawURLEncoding.EncodeToString([]byte(history.NextCursor)),
		})
		if err != nil {
			return result, err
		}
		result.NextCursor = base64.RawURLEncoding.EncodeToString(cursor)
	}
	return result, nil
}

// Reading can project a known authorized address, but cannot register a target
// or grant access. Missing/unbound references are omitted without losing content.
func (s *Store) knownChannelReferenceTx(
	ctx context.Context,
	tx pgx.Tx,
	owner integrationstore.ChannelAccess,
	reference channelconnector.ReplyDestination,
) (string, error) {
	target, err := s.q.WithTx(tx).GetIntegrationTargetByProviderRef(ctx, dbsqlc.GetIntegrationTargetByProviderRefParams{
		ProjectID: owner.ProjectID, IntegrationInstallID: owner.IntegrationInstallID, ProviderRef: reference.ProviderRef,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	access, err := s.integrations.GetAgentChannelAccessTx(ctx, tx, owner.ProjectID, owner.AgentID, target.ID)
	if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrUnauthorized) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !access.Active || access.IntegrationInstallID != owner.IntegrationInstallID ||
		access.ProviderRefKind != reference.ProviderRefKind || access.ImplementationKey != reference.ImplementationKey ||
		(!access.ReceiveAllowed && !access.Capabilities.Read && !access.Capabilities.Send) {
		return "", nil
	}
	return publicid.Encode(publicid.KindIntegrationTarget, target.ID)
}

func validateChannelFailurePayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	fields, err := jsoncanonical.ParseObject(raw, int(channelconnector.MaxOperationResponseBytes))
	if err != nil {
		return err
	}
	for name, value := range fields {
		text, ok := value.(string)
		if (name != "code" && name != "detail") || !ok || len(text) > 1024 {
			return errors.New("failed or unknown result accepts only bounded code and detail")
		}
	}
	return nil
}
