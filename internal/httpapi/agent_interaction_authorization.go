package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/mcpauthorization"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

const maxMCPAuthorizationConfigAttempts = 3

// rejectAuthorizationGrantByAnswer keeps Grant access out of the answer path:
// access is granted only by completing the OAuth flow, which resolves the
// interaction itself.
func rejectAuthorizationGrantByAnswer(
	record executionstore.AgentInteractionRecord,
	resolution interactionform.Resolution,
) *apierror.ResponseError {
	if record.InteractionKind != executionstore.AgentInteractionKindAuthorization {
		return nil
	}
	request, err := record.AuthorizationRequest()
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
		return &apiErr
	}
	granted, err := mcpauthorization.Granted(request, resolution)
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		return &apiErr
	}
	if granted {
		apiErr := apierror.FromCode(openapi.ErrorCodeConflict,
			"grant access by starting authorization for this interaction; only Decline can be answered here")
		return &apiErr
	}
	return nil
}

func openAPIInteractionAuthorization(request mcpauthorization.Request) (openapi.AgentInteractionAuthorization, error) {
	authorization := openapi.AgentInteractionAuthorization{
		McpServerKey:   request.ServerKey,
		McpToolName:    request.ToolName,
		McpUrl:         request.MCPURL,
		SecretName:     request.SecretName,
		RequiredScopes: request.RequiredScopes,
		GrantedScopes:  request.GrantedScopes,
	}
	if request.SecretID != nil {
		secretID, err := publicID(publicid.KindSecret, *request.SecretID)
		if err != nil {
			return openapi.AgentInteractionAuthorization{}, err
		}
		authorization.SecretId = &secretID
	}
	return authorization, nil
}

func (s strictOpenAPIServer) StartAgentInteractionAuthorization(
	ctx context.Context,
	request openapi.StartAgentInteractionAuthorizationRequestObject,
) (openapi.StartAgentInteractionAuthorizationResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := userPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	interactionID, ok := parseOpenAPIPublicID(publicid.KindAgentInteraction, request.InteractionID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, found, err := s.server.store.Execution().GetAgentInteraction(
		ctx, scope.project.ID, scope.agent.ID, interactionID,
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if !found {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if record.InteractionKind != executionstore.AgentInteractionKindAuthorization {
		return nil, apierror.FromCode(openapi.ErrorCodeConflict, "interaction is not an authorization request")
	}
	if record.State != executionstore.AgentInteractionStateOpen {
		return nil, apierror.FromCode(openapi.ErrorCodeConflict, "interaction is no longer open")
	}
	authorization, err := record.AuthorizationRequest()
	if err != nil {
		return nil, err
	}
	owner := mcpOAuthOwner{
		OrgID:          scope.project.OrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: scope.project.ID,
	}
	secretName := authorization.SecretName
	targetSecretID := authorization.SecretID
	if targetSecretID == nil {
		// An earlier grant on this agent may already have connected the server.
		connected, found, err := s.server.agentMCPOAuthSecretID(ctx, scope.project.ID, scope.agent.ID, authorization)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		if found {
			targetSecretID = &connected
		}
	}
	if targetSecretID != nil {
		secret, err := s.server.store.Secrets().GetSecret(ctx, scope.project.OrgID, *targetSecretID)
		if err != nil {
			return nil, secretAPIError(ctx, err)
		}
		owner.OwnerKind = secret.OwnerKind
		owner.OwnerProjectID = secret.OwnerProjectID
		owner.OwnerUserID = secret.OwnerUserID
		secretName = secret.Name
	} else {
		// Connecting a server for the first time also changes the agent config.
		if apiErr := s.server.authorizeProject(
			ctx, scope.project.OrgID, scope.project.ID, identitystore.ProjectActionManage,
		); apiErr != nil {
			return nil, *apiErr
		}
		if apiErr := s.server.checkMCPAuthorizationSecretName(ctx, owner, secretName); apiErr != nil {
			return nil, *apiErr
		}
	}
	if err := s.server.store.Secrets().AuthorizeSecretOwnerManage(ctx, owner.OrgID, secretstore.SecretOwner{
		Kind: owner.OwnerKind, ProjectID: owner.OwnerProjectID, UserID: owner.OwnerUserID,
	}, principal); err != nil {
		return nil, secretAPIError(ctx, err)
	}
	body := &openapi.MCPOAuthStartRequest{Name: secretName, McpUrl: authorization.MCPURL}
	if scopes := authorization.RequestedScopes(); len(scopes) > 0 {
		body.Scopes = &scopes
	}
	if request.Body != nil {
		body.ReturnTo = request.Body.ReturnTo
	}
	// The server already rejected the tool call with this challenge; discovery
	// may be anonymous, so re-probing would wrongly report no auth required.
	challenge := &mcp.AuthorizationChallenge{
		Scopes:              authorization.RequiredScopes,
		ResourceMetadataURL: authorization.ResourceMetadataURL,
	}
	response, apiErr, err := s.startMCPOAuthFlow(ctx, owner, principal, body, &mcpOAuthInteraction{
		ID: record.ID, ProjectID: scope.project.ID, AgentID: scope.agent.ID, SecretID: targetSecretID,
	}, challenge)
	if err != nil {
		return nil, err
	}
	if apiErr != nil {
		return nil, *apiErr
	}
	return openapi.StartAgentInteractionAuthorization201JSONResponse(response), nil
}

// checkMCPAuthorizationSecretName fails fast when a first connect's secret name
// is taken. The flow only ever creates that secret, so an existing one belongs
// to another credential and must not be overwritten.
func (s *Server) checkMCPAuthorizationSecretName(
	ctx context.Context,
	owner mcpOAuthOwner,
	name string,
) *apierror.ResponseError {
	_, err := s.store.Secrets().GetSecretByOwnerName(
		ctx, owner.OrgID, owner.OwnerKind, owner.OwnerProjectID, owner.OwnerUserID, name,
	)
	if errors.Is(err, storeerr.ErrNotFound) {
		return nil
	}
	if err != nil {
		apiErr := secretAPIError(ctx, err)
		return &apiErr
	}
	apiErr := apierror.FromCode(openapi.ErrorCodeConflict, fmt.Sprintf(
		"secret %q already exists for a different credential; configure the MCP server's auth in the agent config",
		name,
	))
	return &apiErr
}

// agentMCPOAuthSecretID returns the OAuth secret the agent's current config
// uses for the request's MCP server, if any.
func (s *Server) agentMCPOAuthSecretID(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	request mcpauthorization.Request,
) (uuid.UUID, bool, error) {
	agent, err := s.store.Execution().GetAgentInProject(ctx, projectID, agentID)
	if err != nil {
		return uuid.Nil, false, err
	}
	config, found, err := s.store.Execution().GetAgentConfig(ctx, projectID, agent.CurrentConfigID)
	if err != nil || !found {
		return uuid.Nil, false, err
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(config.CompiledDefinition, config.EffectiveDefinitionHash)
	if err != nil {
		return uuid.Nil, false, err
	}
	for _, server := range contract.MCPServers {
		if server.ServerKey == request.ServerKey && server.URL == request.MCPURL &&
			server.Auth != nil && server.Auth.Type == agentconfig.MCPAuthTypeOAuth {
			return server.Auth.SecretID, true, nil
		}
	}
	return uuid.Nil, false, nil
}

// saveMCPAuthorizationSecret stores an interaction flow's token in exactly the
// secret the flow was started for: a new version of a bound secret, or a new
// secret that fails rather than reusing an existing name.
func (s *Server) saveMCPAuthorizationSecret(
	ctx context.Context,
	flow mcp.OAuthFlowState,
	flowData mcpOAuthFlowData,
	material secrets.OAuthTokenSetMaterial,
) (uuid.UUID, error) {
	actor := identitystore.NewUserPrincipal(flowData.CreatedByUserID)
	if secretID := flowData.Interaction.SecretID; secretID != nil {
		existing, err := s.store.Secrets().GetSecret(ctx, flowData.OrgID, *secretID)
		if err != nil {
			return uuid.Nil, err
		}
		if existing.Kind != secretstore.SecretKindOAuthTokenSet {
			return uuid.Nil, fmt.Errorf("secret %s has kind %q, want oauth_token_set", existing.ID, existing.Kind)
		}
		metadata, err := mcpOAuthSecretMetadata(existing.Metadata, flowData.Metadata, flow.EndpointURL)
		if err != nil {
			return uuid.Nil, err
		}
		if _, _, err := s.store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
			OrgID:          flowData.OrgID,
			SecretID:       existing.ID,
			Material:       material,
			Actor:          actor,
			SecretMetadata: metadata,
			MCPOAuthFlowID: flowData.FlowID,
		}); err != nil {
			return uuid.Nil, err
		}
		return existing.ID, nil
	}
	metadata, err := mcpOAuthSecretMetadata(nil, flowData.Metadata, flow.EndpointURL)
	if err != nil {
		return uuid.Nil, err
	}
	record, _, err := s.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          flowData.OrgID,
		OwnerKind:      flowData.OwnerKind,
		OwnerProjectID: flowData.OwnerProjectID,
		OwnerUserID:    flowData.OwnerUserID,
		Name:           flowData.SecretName,
		Metadata:       metadata,
		Material:       material,
		Actor:          actor,
		MCPOAuthFlowID: flowData.FlowID,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return record.ID, nil
}

type mcpAuthorizationGrant struct {
	project     identitystore.ProjectRecord
	principal   identitystore.PrincipalRecord
	interaction executionstore.AgentInteractionRecord
	request     mcpauthorization.Request
}

// checkMCPAuthorizationGrant validates a completed OAuth flow against the
// interaction it grants before the token is stored. It returns a rejection
// code when the token cannot satisfy the request.
func (s *Server) checkMCPAuthorizationGrant(
	ctx context.Context,
	flowData mcpOAuthFlowData,
	token mcp.OAuthTokenSet,
) (mcpAuthorizationGrant, string, error) {
	link := flowData.Interaction
	project, err := s.store.Identity().GetProject(ctx, flowData.OrgID, link.ProjectID)
	if err != nil {
		return mcpAuthorizationGrant{}, "", err
	}
	interaction, found, err := s.store.Execution().GetAgentInteraction(ctx, link.ProjectID, link.AgentID, link.ID)
	if err != nil {
		return mcpAuthorizationGrant{}, "", err
	}
	if !found || interaction.InteractionKind != executionstore.AgentInteractionKindAuthorization {
		return mcpAuthorizationGrant{}, "", fmt.Errorf("authorization interaction %s not found", link.ID)
	}
	request, err := interaction.AuthorizationRequest()
	if err != nil {
		return mcpAuthorizationGrant{}, "", err
	}
	// A provider that reports no scopes leaves the server to judge the token.
	if len(token.Scopes) > 0 && len(request.MissingScopes(token.Scopes)) > 0 {
		return mcpAuthorizationGrant{}, "insufficient_scope_granted", nil
	}
	if link.SecretID != nil {
		payload, err := s.store.Secrets().ReadProjectAvailableSecretPayload(
			ctx,
			secretstore.ReadProjectAvailableSecretPayloadInput{
				OrgID:     flowData.OrgID,
				ProjectID: link.ProjectID,
				SecretID:  *link.SecretID,
				Kind:      secrets.KindOAuthTokenSet,
			},
		)
		if err != nil {
			return mcpAuthorizationGrant{}, "", err
		}
		if !sameIDTokenSubject(payload.Payload[secrets.KeyIDToken], token.IDToken) {
			return mcpAuthorizationGrant{}, "account_mismatch", nil
		}
	}
	return mcpAuthorizationGrant{
		project:     project,
		principal:   identitystore.NewUserPrincipal(flowData.CreatedByUserID),
		interaction: interaction,
		request:     request,
	}, "", nil
}

// finishMCPAuthorizationGrant attaches a newly created secret to the agent's
// config and resolves the interaction so the paused tool call runs again. The
// secret is already stored, so a failure here is reported, not rolled back.
func (s *Server) finishMCPAuthorizationGrant(
	ctx context.Context,
	flowData mcpOAuthFlowData,
	grant mcpAuthorizationGrant,
	secretID uuid.UUID,
) string {
	if flowData.Interaction.SecretID == nil {
		if err := s.attachMCPOAuthSecretToAgentConfig(ctx, grant, secretID); err != nil {
			logpkg.Error(ctx, fmt.Errorf("attach mcp oauth secret to agent config: %w", err))
			return "agent_config_update_failed"
		}
	}
	if grant.interaction.State != executionstore.AgentInteractionStateOpen {
		return ""
	}
	actor, err := executionstore.OmnaraActorParams(grant.project.OrgID, grant.principal)
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("resolve mcp authorization actor: %w", err))
		return "interaction_resolve_failed"
	}
	record, err := s.store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID:  flowData.Interaction.ProjectID,
		AgentID:    flowData.Interaction.AgentID,
		ID:         grant.interaction.ID,
		Resolution: mcpauthorization.GrantResolution(),
		Actor:      actor,
	})
	if err != nil {
		logpkg.LoggerFromContext(ctx).WarnContext(ctx, "mcp authorization interaction resolve failed",
			"interaction_id", grant.interaction.ID, "error", err)
		return "interaction_resolve_failed"
	}
	s.dismissInteractionAsync(ctx, record)
	return ""
}

// attachMCPOAuthSecretToAgentConfig points the interaction's MCP server at
// secretID in the agent's current config. It only fills in missing auth; a
// config changed meanwhile to use other auth is left alone.
func (s *Server) attachMCPOAuthSecretToAgentConfig(
	ctx context.Context,
	grant mcpAuthorizationGrant,
	secretID uuid.UUID,
) error {
	publicSecretID, err := publicID(publicid.KindSecret, secretID)
	if err != nil {
		return err
	}
	agentID := grant.interaction.AgentID
	for attempt := 1; ; attempt++ {
		agent, err := s.store.Execution().GetAgentInProject(ctx, grant.project.ID, agentID)
		if err != nil {
			return err
		}
		config, found, err := s.store.Execution().GetAgentConfig(ctx, grant.project.ID, agent.CurrentConfigID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(config.Source) == "" {
			return fmt.Errorf("agent %s current config has no source to update", agentID)
		}
		format := agentconfig.SourceFormat(config.SourceFormat)
		source, err := agentconfig.ParseSource(format, []byte(config.Source))
		if err != nil {
			return err
		}
		server, ok := source.MCP[grant.request.ServerKey]
		if !ok || strings.TrimSpace(server.URL) != grant.request.MCPURL {
			return fmt.Errorf("mcp server %q is no longer in the agent config", grant.request.ServerKey)
		}
		if server.Auth != nil {
			if server.Auth.Type == agentconfig.MCPAuthTypeOAuth && server.Auth.SecretID == publicSecretID {
				return nil
			}
			logpkg.LoggerFromContext(ctx).WarnContext(ctx, "mcp server auth changed before authorization completed",
				"agent_id", agentID, "mcp_server_key", grant.request.ServerKey)
			return nil
		}
		server.Auth = &agentconfig.AgentConfigMCPAuthSource{Type: agentconfig.MCPAuthTypeOAuth, SecretID: publicSecretID}
		source.MCP[grant.request.ServerKey] = server
		encoded, err := agentconfig.EncodeSource(source, format)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(encoded)
		if err != nil {
			return err
		}
		compiled, err := s.compileAgentConfigSourceForProject(ctx, grant.project, string(format), raw)
		if err != nil {
			return err
		}
		result, err := s.store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput:  compiled.CreateInput(grant.project.ID),
			AgentID:                 agentID,
			ExpectedCurrentConfigID: config.ID,
			ActorType:               grant.principal.Type,
			ActorID:                 grant.principal.ID,
			Reason:                  "mcp_authorization",
			IdempotencyKey:          "mcp-authorization:" + grant.interaction.ID.String(),
		})
		if (errors.Is(err, storeerr.ErrStateTransitionConflict) || errors.Is(err, storeerr.ErrConflict)) &&
			attempt < maxMCPAuthorizationConfigAttempts {
			continue
		}
		if err != nil {
			return err
		}
		s.startPoolMachineDeletion(ctx, result.DeleteMachines)
		return nil
	}
}

// sameIDTokenSubject reports whether two ID tokens name the same account. It
// compares unverified claims: both tokens came straight from the token
// endpoint, and the check only guards against reconnecting a different
// account. Missing tokens on either side cannot be compared and pass.
func sameIDTokenSubject(existing, next string) bool {
	existingIssuer, existingSubject, ok := idTokenSubject(existing)
	if !ok {
		return true
	}
	nextIssuer, nextSubject, ok := idTokenSubject(next)
	if !ok {
		return true
	}
	return existingIssuer == nextIssuer && existingSubject == nextSubject
}

func idTokenSubject(token string) (string, string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	var claims struct {
		Issuer  string `json:"iss"`
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Subject == "" {
		return "", "", false
	}
	return claims.Issuer, claims.Subject, true
}
