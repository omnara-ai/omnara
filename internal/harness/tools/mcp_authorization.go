package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/mcpauthorization"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

const mcpAuthorizationRequiredErrorCode = "mcp_authorization_required"

// mcpAuthorizationPendingError reports that an MCP server rejected a tool call for
// OAuth authorization the user can grant; the call waits on an authorization
// interaction instead of failing.
type mcpAuthorizationPendingError struct {
	request mcpauthorization.Request
	cause   error
}

func (p *mcpAuthorizationPendingError) Error() string {
	return fmt.Sprintf("mcp server %q requires authorization: %v", p.request.ServerKey, p.cause)
}

func (p *mcpAuthorizationPendingError) Unwrap() error { return p.cause }

type awaitAuthorizationAsync struct {
	request mcpauthorization.Request
}

func (awaitAuthorizationAsync) asyncPhaseResult() {}

// mcpAuthorizationFailure handles an authorization challenge from tools/call.
// OAuth servers, and servers with no credential configured, get one
// authorization interaction per tool call; anything else fails with a
// structured error the model can relay.
func (e Executor) mcpAuthorizationFailure(
	ctx context.Context,
	turn Turn,
	toolCallID uuid.UUID,
	server agentconfig.RuntimeMCPServer,
	remoteName string,
	challenge mcp.AuthorizationChallenge,
	cause error,
) (toolResultContent, error) {
	if server.Auth != nil && server.Auth.Type != agentconfig.MCPAuthTypeOAuth {
		return mcpAuthorizationRequiredToolResult(server.ServerKey, remoteName,
			"The MCP server rejected the configured credential. Update the server's credential in the agent config.",
			challenge, cause)
	}
	previous, found, err := e.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, turn.ProjectID, turn.AgentID, toolCallID, executionstore.AgentInteractionKindAuthorization,
	)
	if err != nil {
		return toolResultContent{}, err
	}
	if found {
		message := "The MCP server still rejected the call after authorization."
		if previous.State == executionstore.AgentInteractionStateOpen {
			message = "The MCP server is waiting for authorization."
		}
		return mcpAuthorizationRequiredToolResult(server.ServerKey, remoteName, message, challenge, cause)
	}
	contextRow, found, err := e.Store.Execution().GetModelCallContext(
		ctx, turn.ProjectID, turn.AgentID, turn.ModelCallContextID,
	)
	if err != nil {
		return toolResultContent{}, err
	}
	if !found {
		return toolResultContent{}, fmt.Errorf("model call context %s not found", turn.ModelCallContextID)
	}
	secretName, err := agentMCPOAuthSecretName(server.ServerKey, turn.AgentID)
	if err != nil {
		return toolResultContent{}, err
	}
	input := mcpauthorization.NewRequestInput{
		ServerKey:      server.ServerKey,
		ToolName:       remoteName,
		MCPURL:         server.URL,
		SecretName:     secretName,
		AgentConfigID:  contextRow.AgentConfigID,
		RequiredScopes: challenge.Scopes,

		ResourceMetadataURL: challenge.ResourceMetadataURL,
	}
	if server.Auth != nil {
		secret, err := e.Store.Secrets().GetSecret(ctx, turn.OrgID, server.Auth.SecretID)
		if err != nil {
			return toolResultContent{}, fmt.Errorf("read mcp oauth secret for %q: %w", server.ServerKey, err)
		}
		payload, err := e.Store.Secrets().ReadProjectAvailableSecretPayload(
			ctx,
			secretstore.ReadProjectAvailableSecretPayloadInput{
				OrgID:     turn.OrgID,
				ProjectID: turn.ProjectID,
				SecretID:  server.Auth.SecretID,
				Kind:      secrets.KindOAuthTokenSet,
			},
		)
		if err != nil {
			return toolResultContent{}, fmt.Errorf("read mcp oauth scopes for %q: %w", server.ServerKey, err)
		}
		input.SecretID = secret.ID
		input.SecretName = secret.Name
		input.GrantedScopes = strings.Fields(payload.Payload[secrets.KeyScopes])
	}
	request, err := mcpauthorization.NewRequest(input)
	if err != nil {
		return toolResultContent{}, err
	}
	return toolResultContent{}, &mcpAuthorizationPendingError{request: request, cause: cause}
}

// authorizedRuntimeMCPServer lets a call that waited on a granted
// authorization use the credential the grant attached to the agent's current
// config, since the call itself stays pinned to the config it was planned
// under, which had none.
func (e Executor) authorizedRuntimeMCPServer(
	ctx context.Context,
	turn Turn,
	toolCallID uuid.UUID,
	server agentconfig.RuntimeMCPServer,
) (agentconfig.RuntimeMCPServer, error) {
	if server.Auth != nil {
		return server, nil
	}
	interaction, found, err := e.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, turn.ProjectID, turn.AgentID, toolCallID, executionstore.AgentInteractionKindAuthorization,
	)
	if err != nil || !found || interaction.State != executionstore.AgentInteractionStateResolved {
		return server, err
	}
	agent, err := e.Store.Execution().GetAgentInProject(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return server, err
	}
	config, found, err := e.Store.Execution().GetAgentConfig(ctx, turn.ProjectID, agent.CurrentConfigID)
	if err != nil || !found {
		return server, err
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(config.CompiledDefinition, config.EffectiveDefinitionHash)
	if err != nil {
		return server, err
	}
	for _, current := range contract.MCPServers {
		if current.ServerKey == server.ServerKey && current.URL == server.URL &&
			current.Auth != nil && current.Auth.Type == agentconfig.MCPAuthTypeOAuth {
			server.Auth = current.Auth
			return server, nil
		}
	}
	return server, nil
}

func (e Executor) openMCPAuthorization(
	ctx context.Context,
	call asyncToolContext,
	request mcpauthorization.Request,
) error {
	err := retryAsyncToolPersistence(ctx, func(ctx context.Context) error {
		_, err := e.Store.Execution().CreateAuthorizationInteraction(
			ctx,
			executionstore.CreateAuthorizationInteractionInput{
				ProjectID:     call.Turn.ProjectID,
				AgentID:       call.Turn.AgentID,
				ToolCallID:    call.ToolCallID,
				RuntimeLockID: call.Turn.RuntimeLockID,
				Request:       request,
			},
		)
		return err
	})
	if err == nil {
		return nil
	}
	content, contentErr := mcpAuthorizationRequiredToolResult(
		request.ServerKey,
		request.ToolName,
		"The MCP server requires authorization, but the request could not be recorded.",
		mcp.AuthorizationChallenge{Scopes: request.RequiredScopes},
		err,
	)
	if contentErr != nil {
		return errors.Join(err, contentErr)
	}
	return e.completeAsyncToolFailure(ctx, call.Turn, call.ToolCallID, content, err)
}

func mcpAuthorizationRequiredToolResult(
	serverKey, remoteName, message string,
	challenge mcp.AuthorizationChallenge,
	cause error,
) (toolResultContent, error) {
	result := map[string]any{
		"error_code":     mcpAuthorizationRequiredErrorCode,
		"error":          message,
		"message":        message,
		"mcp_server_key": serverKey,
		"mcp_tool_name":  remoteName,
		"retryable":      false,
	}
	if len(challenge.Scopes) > 0 {
		result["required_scopes"] = challenge.Scopes
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return toolResultContent{}, err
	}
	return content, cause
}

// agentMCPOAuthSecretName names the secret a first connect creates. The agent's
// public ID keeps it unique to that agent, so two agents connecting the same
// server never share or overwrite each other's credential.
func agentMCPOAuthSecretName(serverKey string, agentID uuid.UUID) (string, error) {
	publicAgentID, err := publicid.Encode(publicid.KindAgent, agentID)
	if err != nil {
		return "", err
	}
	suffix := "-oauth-" + publicAgentID
	name := serverKey
	for utf8.RuneCountInString(name)+len(suffix) > resourcename.MaxCodePoints {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name + suffix, nil
}
