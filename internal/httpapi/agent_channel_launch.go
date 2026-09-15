package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) authorizeLaunchChannels(
	ctx context.Context, project identitystore.ProjectRecord, principal identitystore.PrincipalRecord,
	requests []openapi.AttachAgentChannelRequest,
) ([]executionstore.LaunchChannelBinding, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	allowed, err := s.server.store.Identity().AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
		Principal: principal, OrgID: project.OrgID, ProjectID: project.ID, Action: identitystore.ProjectActionManage,
	})
	if err != nil {
		return nil, authorizationAPIError(ctx, err)
	}
	if !allowed {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden,
			"project management permission is required to grant channel access")
	}
	bindings := make([]executionstore.LaunchChannelBinding, 0, len(requests))
	for _, request := range requests {
		id, ok := parseOpenAPIPublicID(publicid.KindIntegrationTarget, request.ChannelId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid channel_id")
		}
		binding := executionstore.LaunchChannelBinding{ChannelID: id, Grants: launchChannelGrants(request.Grants)}
		if request.ReplyChannelGrants != nil {
			grants := launchChannelGrants(*request.ReplyChannelGrants)
			binding.ReplyChannelGrants = &grants
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func launchChannelGrants(grants openapi.ChannelGrants) integrationstore.ChannelGrants {
	return integrationstore.ChannelGrants{
		ReceiveAllowed: grants.Receive, ReadAllowed: grants.Read, SendAllowed: grants.Send,
	}
}
