package httpapi

import (
	"context"
	"slices"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const channelRecipientListKind = "channel_recipients"

func (s strictOpenAPIServer) LookupChannelConnectorRecipients(
	ctx context.Context,
	request openapi.LookupChannelConnectorRecipientsRequestObject,
) (openapi.LookupChannelConnectorRecipientsResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	install, err := s.channelConnectorInstallationByPublicID(
		ctx, scope, request.IntegrationAppID, request.IntegrationInstallID,
	)
	if err != nil {
		return nil, err
	}
	body := *request.Body
	lease, err := channelInputReceipt(body.Receipt)
	if err != nil {
		return nil, err
	}
	limit := 50
	if body.Limit != nil {
		limit = *body.Limit
	}
	if limit < 1 || limit > 100 {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "limit must be between 1 and 100")
	}
	keys := append([]string{}, body.InputKeys...)
	slices.Sort(keys)
	extra := struct {
		ProviderRef string
		Receipt     executionstore.ChannelEventLease
		InputKeys   []string
	}{body.ProviderRef, lease, keys}
	scopeKey := install.ProjectID.String() + "/" + install.ID.String()
	sort := "agent_id"
	options, err := parseResourceListQuery(resourceListQueryInput{
		Sort: &sort, Cursor: body.Cursor, ListKind: channelRecipientListKind, Scope: scopeKey,
		IDKind: publicid.KindAgent, AllowedSorts: sortSet(sort), Extra: extra,
	})
	if err != nil || options.After.IsNull || options.After.Key != "" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid recipient cursor")
	}
	result, err := s.server.store.Execution().LookupChannelRecipients(ctx, executionstore.LookupChannelRecipientsInput{
		ProjectID: install.ProjectID, IntegrationInstallID: install.ID, ProviderRef: body.ProviderRef,
		Receipt: lease, InputKeys: body.InputKeys, AfterAgentID: options.After.ID, Limit: int32(limit),
		Capabilities: scope.Capabilities,
	})
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response := openapi.LookupChannelConnectorRecipientsResponse{
		HasReceiveBindingHistory: result.HasReceiveBindingHistory, WorkflowStarted: result.WorkflowStarted,
		Recipients: make([]openapi.ChannelConnectorRecipient, 0, len(result.Recipients)),
	}
	response.ChannelId, err = idOrNil(publicid.KindIntegrationTarget, result.ChannelID)
	if err != nil {
		return nil, err
	}
	response.ParentChannelId, err = idOrNil(publicid.KindIntegrationTarget, result.ParentChannelID)
	if err != nil {
		return nil, err
	}
	var after listing.Cursor
	for _, recipient := range result.Recipients {
		agentID, err := publicID(publicid.KindAgent, recipient.AgentID)
		if err != nil {
			return nil, err
		}
		bindingID, err := publicID(publicid.KindIntegrationBinding, recipient.BindingID)
		if err != nil {
			return nil, err
		}
		response.Recipients = append(response.Recipients, openapi.ChannelConnectorRecipient{
			AgentId: agentID, BindingId: bindingID, InputKeys: append([]string{}, recipient.InputKeys...),
		})
		after = listing.Cursor{Set: true, ID: recipient.AgentID}
	}
	next, err := encodeResourceListNextCursor(result.HasMore, after, options,
		channelRecipientListKind, scopeKey, publicid.KindAgent, extra)
	if err != nil {
		return nil, err
	}
	response.NextCursor = nullableFromPtr(next)
	return openapi.LookupChannelConnectorRecipients200JSONResponse(response), nil
}
