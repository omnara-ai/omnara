package httpapi

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// Shared transport fields have the same validation for workflow and bound input.
// Identity selection and durable admission remain with their respective stores.
type channelInputFields struct {
	Receipt                executionstore.ChannelEventLease
	ProviderUserID         string
	ActorDisplayName       string
	Metadata               json.RawMessage
	DeliveryMode           executionstore.AgentInputDeliveryMode
	CancelOpenInteractions bool
}

func channelDeliveryFields(
	receipt openapi.ChannelEventLease, author openapi.ChannelWorkflowAuthor, metadata json.RawMessage,
	mode *openapi.CreateAgentInputDeliveryMode, cancel *bool,
) (channelInputFields, error) {
	fields := channelInputFields{ProviderUserID: author.Ref, ActorDisplayName: author.DisplayName}
	var err error
	fields.Receipt, err = channelInputReceipt(receipt)
	if err != nil {
		return fields, err
	}
	if strings.TrimSpace(author.Ref) == "" || len(author.Ref) > 512 {
		return fields, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "author ref is empty or too large")
	}
	if err := dbsafe.Text(author.Ref); err != nil {
		return fields, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "author ref: "+err.Error())
	}
	if utf8.RuneCountInString(author.DisplayName) > executionstore.MaxActorDisplayNameLength {
		return fields, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "author display name is too large")
	}
	if err := dbsafe.Text(author.DisplayName); err != nil {
		return fields, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	fields.Metadata, err = channelconnector.NormalizeOpaqueObject(metadata)
	if err != nil {
		return fields, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "input metadata: "+err.Error())
	}
	if mode != nil {
		fields.DeliveryMode = executionstore.AgentInputDeliveryMode(*mode)
	}
	fields.CancelOpenInteractions = cancel != nil && *cancel
	if fields.CancelOpenInteractions && fields.DeliveryMode != executionstore.DeliveryModeSteering {
		return fields, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
			"cancel_open_interactions is allowed only for steering inputs")
	}
	return fields, nil
}

func channelInputReceipt(receipt openapi.ChannelEventLease) (executionstore.ChannelEventLease, error) {
	id, ok := parseOpenAPIPublicID(publicid.KindIntegrationEventReceipt, receipt.ReceiptId)
	if !ok {
		return executionstore.ChannelEventLease{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	lease := executionstore.ChannelEventLease{
		ReceiptID: id, LeaseToken: receipt.LeaseToken, LeaseGeneration: receipt.LeaseGeneration,
	}
	if lease.LeaseToken == uuid.Nil || lease.LeaseGeneration <= 0 {
		return lease, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "current receipt lease is required")
	}
	return lease, nil
}

func channelInputContentPlan(blocks []openapi.CreateAgentInputContentBlock) (inlineMediaPlan, error) {
	content, err := rawJSONFromContentBlocks(blocks)
	if err != nil {
		return inlineMediaPlan{}, err
	}
	if _, err := jsoncanonical.Decode(content); err != nil {
		return inlineMediaPlan{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	plan, err := preflightInlineMedia(content, inlineMediaAgentInput, maxContentBlocksPerInput)
	if err != nil {
		return inlineMediaPlan{}, mediaIngestAPIError(err)
	}
	return plan, nil
}

func publicChannelInputResult(
	result executionstore.ChannelInputResult,
) (openapi.ChannelConnectorInputResponse, error) {
	response := openapi.ChannelConnectorInputResponse{
		CreatedAgent: result.CreatedAgent, CreatedInput: result.CreatedInput,
	}
	for _, field := range []struct {
		kind publicid.Kind
		id   uuid.UUID
		out  *string
	}{
		{publicid.KindAgent, result.AgentInput.AgentID, &response.AgentId},
		{publicid.KindIntegrationTarget, result.ChannelID, &response.ChannelId},
		{publicid.KindAgentInput, result.AgentInput.ID, &response.AgentInputId},
	} {
		id, err := publicID(field.kind, field.id)
		if err != nil {
			return response, err
		}
		*field.out = id
	}
	if result.BindingID != uuid.Nil {
		id, err := publicID(publicid.KindIntegrationBinding, result.BindingID)
		if err != nil {
			return response, err
		}
		response.BindingId = &id
	}
	if len(result.CanceledInteractionIDs) > 0 {
		ids := make([]string, 0, len(result.CanceledInteractionIDs))
		for _, value := range result.CanceledInteractionIDs {
			id, err := publicID(publicid.KindAgentInteraction, value)
			if err != nil {
				return response, err
			}
			ids = append(ids, id)
		}
		response.CanceledInteractionIds = &ids
	}
	blocks, err := publicevents.AgentInputContentBlocks(result.ContentBlocks)
	response.ContentBlocks = blocks
	return response, err
}
