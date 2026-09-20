package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) preparePublicAgentLaunch(
	ctx context.Context,
	project identitystore.ProjectRecord,
	principal identitystore.PrincipalRecord,
	body openapi.CreateAgentRequest,
	input executionstore.LaunchAgentInput,
) (executionstore.LaunchAgentInput, error) {
	if body.InitialInput != nil {
		if body.Message != nil {
			return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
				"initial_input is mutually exclusive with message")
		}
		initial, err := publicLaunchInitialInput(project, principal, *body.InitialInput)
		if err != nil {
			return input, err
		}
		input.InitialInput = initial
	}
	additions := agentconfig.AppCapabilitiesSource{}
	if body.Tools != nil {
		additions.Tools = *body.Tools
	}
	if body.Listeners != nil {
		additions.Listeners = *body.Listeners
	}
	if body.InteractionHandlers != nil {
		additions.InteractionHandlers = *body.InteractionHandlers
	}
	if len(additions.Tools) == 0 && len(additions.Listeners) == 0 && len(additions.InteractionHandlers) == 0 {
		return input, nil
	}
	if err := s.server.authorizeProject(ctx, project.OrgID, project.ID, identitystore.ProjectActionManage); err != nil {
		return input, *err
	}
	base, found, err := s.server.store.Execution().GetAgentConfig(ctx, project.ID, input.AgentConfigID)
	if err != nil {
		return input, apierror.ProjectScoped(err)
	}
	if !found {
		return input, apierror.FromCode(openapi.ErrorCodeNotFound, "agent config not found")
	}
	derived, err := agentconfigcompile.DeriveAppConfig(
		ctx, s.server.store, project.OrgID, project.ID, s.server.agentConfigOptions, base, additions,
	)
	if err != nil {
		return input, agentConfigCompileError(err)
	}
	config := derived.CreateInput(project.ID)
	input.DerivedBaseConfigID = base.ID
	input.AgentConfigID, input.DerivedConfig = uuid.Nil, &config
	return input, nil
}

func publicLaunchInitialInput(
	project identitystore.ProjectRecord,
	principal identitystore.PrincipalRecord,
	body openapi.AgentLaunchInitialInput,
) (*executionstore.LaunchInitialInput, error) {
	actor, err := requestActorParams(project, principal, body.Actor)
	if err != nil {
		return nil, err
	}
	blocks, err := rawJSONFromContentBlocks(body.ContentBlocks)
	if err != nil {
		return nil, err
	}
	return &executionstore.LaunchInitialInput{ContentBlocks: blocks, Actor: actor}, nil
}
