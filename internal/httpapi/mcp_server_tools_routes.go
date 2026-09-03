package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/textutil"
)

const (
	mcpServerToolsTimeout         = 30 * time.Second
	mcpServerToolsErrorRunesLimit = 2_000
)

func (s strictOpenAPIServer) ListMCPServerTools(
	ctx context.Context,
	request openapi.ListMCPServerToolsRequestObject,
) (openapi.ListMCPServerToolsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	endpoint, err := agentconfig.ValidateMCPURL(request.Body.Url, s.server.agentConfigOptions.AllowInsecureLocalMCPHTTP)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "url is invalid: "+err.Error())
	}
	if parsed, err := url.Parse(endpoint); err != nil || parsed.User != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "url must not contain credentials")
	}
	auth, apiErr := parseMCPServerAuth(request.Body.Auth)
	if apiErr != nil {
		return nil, *apiErr
	}
	if auth != nil {
		if s.server.store == nil {
			return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
		}
		if apiErr := s.validateMCPServerAuthSecret(ctx, scope, auth); apiErr != nil {
			return nil, *apiErr
		}
	}
	manager := mcp.Manager{
		Client:               s.server.mcpClient,
		SigV4CredentialCache: s.server.sigV4CredentialCache,
		OAuthHTTPClient:      s.server.mcpOAuthHTTPClient,
	}
	if s.server.store != nil {
		manager.Secrets = s.server.store.Secrets()
	}
	outboundCtx, cancel := context.WithTimeout(ctx, mcpServerToolsTimeout)
	defer cancel()
	discovered, err := manager.DiscoverTools(outboundCtx, scope.org.ID, scope.project.ID, endpoint, auth)
	if err != nil {
		return s.mcpServerToolsFailure(outboundCtx, endpoint, err)
	}
	return openapi.ListMCPServerTools200JSONResponse(mcpServerToolsResponse(discovered)), nil
}

func parseMCPServerAuth(input openapi.MCPServerAuth) (*agentconfig.RuntimeMCPAuth, *apierror.ResponseError) {
	authType, err := input.Discriminator()
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid auth")
		return nil, &apiErr
	}
	switch authType {
	case "none":
		return nil, nil
	case agentconfig.MCPAuthTypeBearer:
		auth, err := input.AsMCPServerAuthBearer()
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid bearer auth")
			return nil, &apiErr
		}
		return &agentconfig.RuntimeMCPAuth{Type: agentconfig.MCPAuthTypeBearer, SecretID: auth.SecretId}, nil
	case agentconfig.MCPAuthTypeOAuth:
		auth, err := input.AsMCPServerAuthOAuth()
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid oauth auth")
			return nil, &apiErr
		}
		return &agentconfig.RuntimeMCPAuth{Type: agentconfig.MCPAuthTypeOAuth, SecretID: auth.SecretId}, nil
	case agentconfig.MCPAuthTypeSigV4:
		auth, err := input.AsMCPServerAuthSigV4()
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid sigv4 auth")
			return nil, &apiErr
		}
		service := strings.TrimSpace(auth.Service)
		region := strings.TrimSpace(auth.Region)
		if service == "" || region == "" {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "auth.service and auth.region are required for sigv4")
			return nil, &apiErr
		}
		return &agentconfig.RuntimeMCPAuth{
			Type:     agentconfig.MCPAuthTypeSigV4,
			SecretID: auth.SecretId,
			Service:  service,
			Region:   region,
		}, nil
	default:
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, fmt.Sprintf("unsupported auth type %q", authType))
		return nil, &apiErr
	}
}

func (s strictOpenAPIServer) validateMCPServerAuthSecret(
	ctx context.Context,
	scope projectScopeRecord,
	auth *agentconfig.RuntimeMCPAuth,
) *apierror.ResponseError {
	secretID, err := publicid.Decode(publicid.KindSecret, auth.SecretID)
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "auth.secret_id must be a secret public id")
		return &apiErr
	}
	expectedKind, err := mcpServerAuthSecretKind(auth.Type)
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		return &apiErr
	}
	err = s.server.store.Secrets().ValidateProjectSecretReference(
		ctx,
		scope.org.ID,
		scope.project.ID,
		secretID,
		expectedKind,
	)
	switch {
	case err == nil:
		return nil
	case storeerr.IsNotFound(err):
		apiErr := apierror.FromCode(openapi.ErrorCodeNotFound, "auth.secret_id is not available to the project")
		return &apiErr
	case errors.Is(err, storeerr.ErrInvalidSecretRequest):
		apiErr := apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			fmt.Sprintf("auth.secret_id must reference a %s secret", expectedKind),
		)
		return &apiErr
	default:
		logpkg.Error(ctx, fmt.Errorf("validate mcp auth secret: %w", err))
		apiErr := apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
		return &apiErr
	}
}

func mcpServerAuthSecretKind(authType string) (secrets.Kind, error) {
	switch authType {
	case agentconfig.MCPAuthTypeBearer:
		return secrets.KindGeneric, nil
	case agentconfig.MCPAuthTypeOAuth:
		return secrets.KindOAuthTokenSet, nil
	case agentconfig.MCPAuthTypeSigV4:
		return secrets.KindAWSCredentials, nil
	default:
		return "", fmt.Errorf("unsupported auth type %q", authType)
	}
}

func (s strictOpenAPIServer) mcpServerToolsFailure(
	ctx context.Context,
	endpoint string,
	err error,
) (openapi.ListMCPServerToolsResponseObject, error) {
	message := textutil.TruncateRunes(strings.ToValidUTF8(err.Error(), "�"), mcpServerToolsErrorRunesLimit)
	var httpErr *mcp.HTTPError
	switch {
	case errors.Is(err, ssrf.ErrBlockedAddress):
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, message).WithCause(err)
	case storeerr.IsNotFound(err):
		return nil, apierror.FromCode(
			openapi.ErrorCodeNotFound,
			"auth.secret_id is not available to the project",
		).WithCause(err)
	case errors.Is(err, context.Canceled):
		return nil, apierror.FromCode(openapi.ErrorCodeUpstreamError, "mcp server request was canceled").WithCause(err)
	case errors.As(err, &httpErr) &&
		(httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden):
		return s.mcpServerAuthRequired(ctx, endpoint, message)
	default:
		logpkg.LoggerFromContext(ctx).WarnContext(ctx, "mcp tool discovery failed", "error", err)
		return nil, apierror.FromCode(openapi.ErrorCodeUpstreamError, message).WithCause(err)
	}
}

func (s strictOpenAPIServer) mcpServerAuthRequired(
	ctx context.Context,
	endpoint string,
	message string,
) (openapi.ListMCPServerToolsResponseObject, error) {
	requirement, err := mcp.DetectAuth(ctx, endpoint, mcp.AuthOptions{HTTPClient: s.server.mcpOAuthHTTPClient})
	switch {
	case err == nil && !requirement.Required:
		return nil, apierror.FromCode(openapi.ErrorCodeUpstreamError, message)
	case err == nil && requirement.AuthorizationServer != nil:
		hint := openapi.MCPServerAuthHint{
			Type:                openapi.MCPServerAuthHintTypeOauth,
			Scopes:              optionalNonEmptySlice(requirement.Scopes),
			AuthorizationServer: optionalNonEmpty(requirement.AuthorizationServer.Issuer),
		}
		return mcpServerAuthRequiredResponse(hint, "mcp server requires OAuth authorization: "+message), nil
	case err == nil, errors.Is(err, mcp.ErrOAuthMetadataUnavailable):
		hint := openapi.MCPServerAuthHint{Type: openapi.MCPServerAuthHintTypeBearer}
		return mcpServerAuthRequiredResponse(hint, "mcp server requires a bearer token: "+message), nil
	default:
		logpkg.Error(ctx, fmt.Errorf("mcp auth probe failed: %w", err))
		return nil, apierror.FromCode(
			openapi.ErrorCodeUpstreamError,
			message+"; auth probe failed: "+err.Error(),
		).WithCause(err)
	}
}

func mcpServerAuthRequiredResponse(
	hint openapi.MCPServerAuthHint,
	message string,
) openapi.ListMCPServerTools422JSONResponse {
	return openapi.ListMCPServerTools422JSONResponse(openapi.MCPServerAuthRequiredError{
		Auth:  hint,
		Code:  openapi.MCPServerAuthRequiredErrorCodeUnprocessable,
		Error: textutil.TruncateRunes(message, mcpServerToolsErrorRunesLimit),
	})
}

func mcpServerToolsResponse(discovered mcp.DiscoveredServer) openapi.MCPServerToolsResponse {
	tools := make([]openapi.MCPServerTool, 0, len(discovered.Tools))
	for _, tool := range discovered.Tools {
		if tool == nil {
			continue
		}
		tools = append(tools, mcpServerToolResponse(tool))
	}
	return openapi.MCPServerToolsResponse{
		ProtocolVersion: discovered.ProtocolVersion,
		ServerInfo: openapi.MCPServerInfo{
			Name:        discovered.ServerInfo.Name,
			Version:     discovered.ServerInfo.Version,
			Title:       optionalNonEmpty(discovered.ServerInfo.Title),
			Description: optionalNonEmpty(discovered.ServerInfo.Description),
			WebsiteUrl:  optionalNonEmpty(discovered.ServerInfo.WebsiteURL),
		},
		Tools: tools,
	}
}

func mcpServerToolResponse(tool *sdkmcp.Tool) openapi.MCPServerTool {
	response := openapi.MCPServerTool{
		Name:        tool.Name,
		Title:       optionalNonEmpty(tool.Title),
		Description: optionalNonEmpty(tool.Description),
		InputSchema: jsonSchemaObject(tool.InputSchema),
	}
	if tool.OutputSchema != nil {
		outputSchema := jsonSchemaObject(tool.OutputSchema)
		response.OutputSchema = &outputSchema
	}
	if tool.Annotations != nil {
		response.Annotations = &openapi.MCPServerToolAnnotations{
			Title:           optionalNonEmpty(tool.Annotations.Title),
			ReadOnlyHint:    optionalTrue(tool.Annotations.ReadOnlyHint),
			DestructiveHint: tool.Annotations.DestructiveHint,
			IdempotentHint:  optionalTrue(tool.Annotations.IdempotentHint),
			OpenWorldHint:   tool.Annotations.OpenWorldHint,
		}
	}
	return response
}

func jsonSchemaObject(schema any) map[string]any {
	object, ok := schema.(map[string]any)
	if !ok || object == nil {
		return map[string]any{}
	}
	return object
}

func optionalTrue(value bool) *bool {
	if !value {
		return nil
	}
	return &value
}
