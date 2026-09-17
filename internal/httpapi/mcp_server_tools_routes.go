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
		manager.Execution = s.server.store.Execution()
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
	result := &agentconfig.RuntimeMCPAuth{Type: authType}
	var secretRef string
	switch authType {
	case "none":
		return nil, nil
	case agentconfig.MCPAuthTypeBearer:
		auth, decodeErr := input.AsMCPServerAuthBearer()
		err, secretRef = decodeErr, auth.SecretId
	case agentconfig.MCPAuthTypeOAuth:
		auth, decodeErr := input.AsMCPServerAuthOAuth()
		err, secretRef = decodeErr, auth.SecretId
	case agentconfig.MCPAuthTypeSigV4:
		auth, decodeErr := input.AsMCPServerAuthSigV4()
		err, secretRef = decodeErr, auth.SecretId
		result.Service, result.Region = strings.TrimSpace(auth.Service), strings.TrimSpace(auth.Region)
		if result.Service == "" || result.Region == "" {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "auth.service and auth.region are required for sigv4")
			return nil, &apiErr
		}
	default:
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, fmt.Sprintf("unsupported auth type %q", authType))
		return nil, &apiErr
	}
	if err == nil {
		result.SecretID, err = publicid.Decode(publicid.KindSecret, secretRef)
	}
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "auth.secret_id must be a secret public id")
		return nil, &apiErr
	}
	return result, nil
}

func (s strictOpenAPIServer) validateMCPServerAuthSecret(
	ctx context.Context,
	scope projectScopeRecord,
	auth *agentconfig.RuntimeMCPAuth,
) *apierror.ResponseError {
	expectedKind, err := mcpServerAuthSecretKind(auth.Type)
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		return &apiErr
	}
	err = s.server.store.Secrets().ValidateProjectSecretReference(
		ctx,
		scope.org.ID,
		scope.project.ID,
		auth.SecretID,
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
	status, hasStatus := mcp.HTTPStatus(err)
	switch {
	case errors.Is(err, ssrf.ErrBlockedAddress):
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, message).WithCause(err)
	case storeerr.IsNotFound(err):
		return nil, apierror.FromCode(
			openapi.ErrorCodeNotFound,
			"auth.secret_id is not available to the project",
		).WithCause(err)
	case errors.Is(err, context.Canceled):
		return nil, apierror.FromCode(openapi.ErrorCodeUnprocessable, "mcp server request was canceled").WithCause(err)
	case errors.Is(err, mcp.ErrInternal):
		logpkg.Error(ctx, fmt.Errorf("mcp tool discovery: %w", err))
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error").WithCause(err)
	case errors.Is(err, mcp.ErrRefreshBusy):
		return nil, apierror.FromCode(
			openapi.ErrorCodeConflict,
			"another refresh for this mcp server or its secret is in progress; retry shortly",
		).WithCause(err)
	case errors.Is(err, mcp.ErrCredential) && mcp.IsRetryableConnectionFailure(err):
		return nil, apierror.FromCode(apierror.CodeUpstreamUnavailable, message).WithCause(err)
	case errors.Is(err, mcp.ErrCredential):
		return mcpServerUnreachableResponse("the configured auth secret could not be used: " + message), nil
	case hasStatus && (status == http.StatusUnauthorized || status == http.StatusForbidden):
		return s.mcpServerAuthRequired(ctx, endpoint, message)
	case mcp.IsRetryableConnectionFailure(err):
		return nil, apierror.FromCode(apierror.CodeUpstreamUnavailable, message).WithCause(err)
	default:
		logpkg.LoggerFromContext(ctx).WarnContext(ctx, "mcp tool discovery failed", "error", err)
		return mcpServerUnreachableResponse(message), nil
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
		return mcpServerUnreachableResponse(message), nil
	case err == nil && requirement.AuthorizationServer != nil:
		hint := openapi.MCPServerAuthHint{
			Type:                openapi.MCPServerAuthHintTypeOauth,
			Scopes:              optionalNonEmptySlice(requirement.Scopes),
			AuthorizationServer: optionalNonEmpty(requirement.AuthorizationServer.Issuer),
		}
		return mcpServerAuthRequiredResponse(hint, "mcp server requires OAuth authorization: "+message), nil
	case err != nil && mcp.IsRetryableConnectionFailure(err):
		return nil, apierror.FromCode(
			apierror.CodeUpstreamUnavailable,
			message+"; auth probe failed: "+err.Error(),
		).WithCause(err)
	case err == nil, errors.Is(err, mcp.ErrOAuthMetadataUnavailable):
		hint := openapi.MCPServerAuthHint{Type: openapi.MCPServerAuthHintTypeBearer}
		return mcpServerAuthRequiredResponse(hint, "mcp server requires a bearer token: "+message), nil
	default:
		logpkg.LoggerFromContext(ctx).WarnContext(ctx, "mcp auth probe failed", "error", err)
		return mcpServerUnreachableResponse(message + "; auth probe failed: " + err.Error()), nil
	}
}

func mcpServerAuthRequiredResponse(
	hint openapi.MCPServerAuthHint,
	message string,
) openapi.ListMCPServerTools422JSONResponse {
	return openapi.ListMCPServerTools422JSONResponse(openapi.MCPServerAuthRequiredError{
		Auth:  &hint,
		Code:  openapi.MCPServerAuthRequiredErrorCodeUnprocessable,
		Error: textutil.TruncateRunes(message, mcpServerToolsErrorRunesLimit),
	})
}

func mcpServerUnreachableResponse(message string) openapi.ListMCPServerTools422JSONResponse {
	return openapi.ListMCPServerTools422JSONResponse(openapi.MCPServerAuthRequiredError{
		Code: openapi.MCPServerAuthRequiredErrorCodeUnprocessable,
		Error: textutil.TruncateRunes(
			"could not discover tools from the mcp server: "+message,
			mcpServerToolsErrorRunesLimit,
		),
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
