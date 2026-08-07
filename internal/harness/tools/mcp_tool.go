package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	maxMCPToolResultMediaBytes = 10 * 1024 * 1024
	maxMCPToolResultParts      = 100
)

func runMCPToolAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	content, err := call.Executor.dispatchMCPTool(
		ctx,
		call.Turn,
		call.ToolCallID,
		call.Call,
	)
	if err != nil {
		return failAsynchronously(content, err), nil
	}
	return completeAsynchronously(content), nil
}

func (e Executor) dispatchMCPTool(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	call model.ToolCall,
) (toolResultContent, error) {
	serverKey, remoteName, ok := toolcatalog.SplitMCPRuntimeToolName(call.Name)
	if !ok {
		return toolResultContent{}, fmt.Errorf("invalid mcp tool name %q", call.Name)
	}
	// Dispatch resolves the connection by current server key. This is safe only
	// because live config changes cannot alter MCP declarations yet
	// (validateLiveAgentConfigChangeTx rejects MCP diffs), so the connection a
	// pending call observes is always the one that produced it. When live MCP
	// changes land, tool calls must record their producing MCP connection
	// id/generation and dispatch must use that recorded identity instead.
	conn, found, err := e.Store.Execution().GetMCPConnection(ctx, turn.ProjectID, turn.AgentID, serverKey)
	if err != nil {
		return toolResultContent{}, err
	}
	if !found || conn.State != executionstore.MCPConnectionStateReady {
		return toolResultContent{}, fmt.Errorf("mcp server %q is not ready", serverKey)
	}
	if e.MCP == nil {
		return toolResultContent{}, fmt.Errorf("mcp client is required to call mcp tool %q", call.Name)
	}
	server, err := e.runtimeMCPServerForTool(ctx, turn, serverKey)
	if err != nil {
		return toolResultContent{}, err
	}
	seq, err := e.Store.Execution().NextMCPRequestSequence(ctx, turn.ProjectID, turn.AgentID, conn.ID)
	if err != nil {
		return toolResultContent{}, err
	}
	manager := mcp.Manager{
		Execution:       e.Store.Execution(),
		Secrets:         e.Store.Secrets(),
		Client:          e.MCP,
		Backoff:         e.MCPInitializationBackoff,
		OAuthHTTPClient: e.MCPAuthHTTPClient,
	}
	wireConn, err := manager.Connection(
		ctx,
		turn.OrgID,
		turn.ProjectID,
		conn,
		server,
		conn.MCPSessionID,
		conn.ProtocolVersion,
	)
	if err != nil {
		return toolResultContent{}, err
	}
	if err := e.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.RuntimeLockID,
	); err != nil {
		return toolResultContent{}, err
	}
	result, err := e.MCP.CallTool(ctx, wireConn, seq, remoteName, call.Input)
	if errors.Is(err, mcp.ErrSessionExpired) {
		if err := e.Store.Execution().EnsureRuntimeLockActive(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			turn.RuntimeLockID,
		); err != nil {
			return toolResultContent{}, err
		}
		refresh, refreshErr := manager.RefreshExpired(
			ctx,
			turn.OrgID,
			turn.ProjectID,
			turn.AgentID,
			conn,
			server,
		)
		if cause := mcp.InitializationCause(refreshErr); cause != nil &&
			mcp.InitializationRecorded(refreshErr) {
			result, resultErr := mcpConnectionFailedToolResult(serverKey, remoteName, cause)
			if resultErr != nil {
				return toolResultContent{}, resultErr
			}
			return result, cause
		}
		if refreshErr != nil {
			return toolResultContent{}, refreshErr
		}
		seq, err = e.Store.Execution().NextMCPRequestSequence(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			refresh.Conn.ID,
		)
		if err != nil {
			return toolResultContent{}, err
		}
		wireConn, err = manager.Connection(
			ctx,
			turn.OrgID,
			turn.ProjectID,
			refresh.Conn,
			server,
			refresh.Conn.MCPSessionID,
			refresh.Conn.ProtocolVersion,
		)
		if err != nil {
			return toolResultContent{}, err
		}
		if err := e.Store.Execution().EnsureRuntimeLockActive(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			turn.RuntimeLockID,
		); err != nil {
			return toolResultContent{}, err
		}
		result, err = e.MCP.CallTool(ctx, wireConn, seq, remoteName, call.Input)
		if errors.Is(err, mcp.ErrSessionExpired) {
			if _, _, expireErr := e.Store.Execution().MarkMCPConnectionExpired(
				ctx,
				turn.ProjectID,
				turn.AgentID,
				refresh.Conn.ID,
				refresh.Conn.Generation,
			); expireErr != nil {
				return toolResultContent{}, expireErr
			}
			result, resultErr := mcpConnectionFailedToolResult(serverKey, remoteName, err)
			if resultErr != nil {
				return toolResultContent{}, resultErr
			}
			return result, err
		}
	}
	if err != nil {
		return toolResultContent{}, err
	}
	return e.mcpToolResultContent(
		ctx,
		turn,
		toolCallID,
		serverKey,
		remoteName,
		result,
	)
}

func (e Executor) runtimeMCPServerForTool(
	ctx context.Context,
	turn Turn,
	serverKey string,
) (agentconfig.RuntimeMCPServer, error) {
	contract, err := e.runtimeContractForTurn(ctx, turn)
	if err != nil {
		return agentconfig.RuntimeMCPServer{}, err
	}
	for _, server := range contract.MCPServers {
		if server.ServerKey == serverKey {
			return server, nil
		}
	}
	return agentconfig.RuntimeMCPServer{}, fmt.Errorf(
		"mcp server %q is not declared in model context config",
		serverKey,
	)
}

func mcpConnectionFailedToolResult(
	serverKey, remoteName string,
	cause error,
) (toolResultContent, error) {
	message := fmt.Sprintf(
		"MCP connection %q failed while refreshing before tool %q.",
		serverKey,
		remoteName,
	)
	if cause != nil {
		message += " " + cause.Error()
	}
	return structuredToolResultContent(map[string]any{
		"error_code":     "mcp_connection_failed",
		"error":          message,
		"message":        message,
		"mcp_server_key": serverKey,
		"mcp_tool_name":  remoteName,
		"retryable":      mcp.IsRetryableConnectionFailure(cause),
	})
}

func (e Executor) mcpToolResultContent(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	serverKey, remoteName string,
	result *sdkmcp.CallToolResult,
) (toolResultContent, error) {
	if result == nil {
		return toolResultContent{}, errors.New("MCP tool returned no result")
	}
	partCount := len(result.Content)
	if result.StructuredContent != nil {
		partCount++
	}
	if partCount > maxMCPToolResultParts {
		return toolResultContent{}, fmt.Errorf(
			"MCP tool returned %d content parts; maximum is %d",
			partCount,
			maxMCPToolResultParts,
		)
	}
	parts := make([]toolResultPart, 0, len(result.Content)+1)
	for ordinal, content := range result.Content {
		part, err := e.mcpContentPart(ctx, turn, toolCallID, ordinal, content)
		if err != nil {
			return toolResultContent{}, fmt.Errorf(
				"normalize MCP tool %s/%s content %d: %w",
				serverKey,
				remoteName,
				ordinal,
				err,
			)
		}
		parts = append(parts, part)
	}
	if result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return toolResultContent{}, fmt.Errorf("marshal MCP structured content: %w", err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return toolResultContent{}, errors.New("MCP structured content must be a JSON object")
		}
		part, err := structuredToolResultPart(json.RawMessage(raw))
		if err != nil {
			return toolResultContent{}, err
		}
		parts = append(parts, part)
	}
	content := newToolResultContent(parts...)
	if result.IsError {
		return content, fmt.Errorf("MCP tool %s/%s returned isError=true", serverKey, remoteName)
	}
	return content, nil
}

func (e Executor) mcpContentPart(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	ordinal int,
	content sdkmcp.Content,
) (toolResultPart, error) {
	switch value := content.(type) {
	case *sdkmcp.TextContent:
		if value == nil {
			return nil, errors.New("MCP text content is nil")
		}
		return textToolResultPart(value.Text), nil
	case *sdkmcp.ImageContent:
		if value == nil {
			return nil, errors.New("MCP image content is nil")
		}
		part, err := e.mcpMediaPart(
			ctx,
			turn,
			toolCallID,
			ordinal,
			value.MIMEType,
			value.Data,
		)
		return part, err
	case *sdkmcp.AudioContent:
		if value == nil {
			return nil, errors.New("MCP audio content is nil")
		}
		part, err := e.mcpMediaPart(
			ctx,
			turn,
			toolCallID,
			ordinal,
			value.MIMEType,
			value.Data,
		)
		return part, err
	case *sdkmcp.EmbeddedResource:
		if value == nil || value.Resource == nil {
			return nil, errors.New("embedded resource is missing its content")
		}
		if len(value.Resource.Blob) > 0 {
			part, err := e.mcpMediaPart(
				ctx,
				turn,
				toolCallID,
				ordinal,
				value.Resource.MIMEType,
				value.Resource.Blob,
			)
			return part, err
		}
		return textToolResultPart(value.Resource.Text), nil
	case *sdkmcp.ResourceLink:
		if value == nil {
			return nil, errors.New("MCP resource link is nil")
		}
		link := map[string]any{
			"type": "mcp_resource_link",
			"uri":  value.URI,
			"name": value.Name,
		}
		if value.Title != "" {
			link["title"] = value.Title
		}
		if value.Description != "" {
			link["description"] = value.Description
		}
		if value.MIMEType != "" {
			link["mime_type"] = value.MIMEType
		}
		if value.Size != nil {
			link["size"] = *value.Size
		}
		return structuredToolResultPart(link)
	default:
		return nil, fmt.Errorf("unsupported MCP content type %T", content)
	}
}

func (e Executor) mcpMediaPart(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	ordinal int,
	contentType string,
	content []byte,
) (toolResultPart, error) {
	if e.Store == nil {
		return nil, errors.New("storage is required for MCP media")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	artifact, err := e.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:      turn.ProjectID,
		AgentID:        turn.AgentID,
		ContentType:    contentType,
		Content:        content,
		MaxBytes:       maxMCPToolResultMediaBytes,
		IdempotencyKey: fmt.Sprintf("mcp-tool-result:%s:%d", toolCallID, ordinal),
	})
	if err != nil {
		return nil, err
	}
	return mediaToolResultPart(artifact.ID)
}
