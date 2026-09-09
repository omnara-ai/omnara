package agentconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const (
	MCPAuthTypeBearer = "bearer"
	MCPAuthTypeOAuth  = "oauth"
	MCPAuthTypeSigV4  = "sigv4"
)

type RuntimeMCPServer struct {
	ServerKey      string
	URL            string
	Auth           *RuntimeMCPAuth
	DefaultEnabled bool
	Permission     toolpermission.Selection
	Tools          map[string]RuntimeMCPTool
}

type RuntimeMCPAuth struct {
	Type     string
	SecretID string
	Service  string
	Region   string
}

type RuntimeMCPTool struct {
	RemoteName string
	Enabled    *bool
	Permission *toolpermission.Selection
}

// ResolveTool returns the effective permission for a remote tool and whether the
// tool should be exposed at all. ok is true only when the tool is enabled
// through its per-tool override or the server default.
func (s RuntimeMCPServer) ResolveTool(remoteName string) (toolpermission.Selection, bool) {
	specific, hasSpecific := s.Tools[remoteName]
	var enabled *bool
	var specificPermission *toolpermission.Selection
	if hasSpecific {
		enabled = specific.Enabled
		specificPermission = specific.Permission
	}
	permission, enabledValue := resolveToolPermission(
		enabled,
		specificPermission,
		s.DefaultEnabled,
		s.Permission,
	)
	if !enabledValue {
		return toolpermission.Selection{}, false
	}
	return permission, true
}

func compileMCPServers(
	source map[string]AgentConfigMCPSource,
	opts CompileOptions,
) (map[string]MCPServerCompiled, error) {
	compiled := make(map[string]MCPServerCompiled, len(source))
	for serverKey, server := range source {
		endpoint, err := ValidateMCPURL(server.URL, opts.AllowInsecureLocalMCPHTTP)
		if err != nil {
			return nil, issueAt(jsonPointer("mcp", serverKey, "url"), err)
		}
		defaultEnabled := server.DefaultEnabled == nil || *server.DefaultEnabled
		var tools map[string]MCPToolCompiled
		if len(server.Tools) > 0 {
			tools, err = compileMCPTools(serverKey, defaultEnabled, server.Tools)
			if err != nil {
				return nil, issueAt(jsonPointer("mcp", serverKey, "tools"), err)
			}
		}
		var auth *MCPAuthCompiled
		if server.Auth != nil {
			auth, err = compileMCPAuth(server.Auth, opts)
			if err != nil {
				return nil, issueAt(jsonPointer("mcp", serverKey, "auth"), err)
			}
		}
		permission := toolcatalog.DefaultMCPToolPermission()
		if server.Permission != nil {
			permission, err = toolpermission.ValidateSelection(
				*server.Permission,
				toolcatalog.MCPToolPermissionModes(),
			)
			if err != nil {
				return nil, issueAt(jsonPointer("mcp", serverKey, "permission"), err)
			}
		}
		compiled[serverKey] = MCPServerCompiled{
			URL:            endpoint,
			Auth:           auth,
			DefaultEnabled: defaultEnabled,
			Permission:     permission,
			Tools:          tools,
		}
	}
	return compiled, nil
}

func compileMCPAuth(source *AgentConfigMCPAuthSource, opts CompileOptions) (*MCPAuthCompiled, error) {
	secretID := strings.TrimSpace(source.SecretID)
	if _, err := publicid.Decode(publicid.KindSecret, secretID); err != nil {
		return nil, issuef(jsonPointer("secret_id"), "must be a secret public id: %w", err)
	}
	expectedKind, err := mcpAuthSecretKind(source.Type)
	if err != nil {
		return nil, issueAt(jsonPointer("type"), err)
	}
	service := strings.TrimSpace(source.Service)
	region := strings.TrimSpace(source.Region)
	switch source.Type {
	case MCPAuthTypeSigV4:
		if service == "" {
			return nil, issuef(jsonPointer("service"), "is required for sigv4")
		}
		if region == "" {
			return nil, issuef(jsonPointer("region"), "is required for sigv4")
		}
	default:
		if service != "" || region != "" {
			return nil, issuef(jsonPointer("type"), "service and region require type sigv4")
		}
	}
	if opts.ValidateSecretID != nil {
		if err := opts.ValidateSecretID(secretID, expectedKind); err != nil {
			return nil, issueOr(jsonPointer("secret_id"), err)
		}
	}
	return &MCPAuthCompiled{
		Type:     source.Type,
		SecretID: secretID,
		Service:  service,
		Region:   region,
	}, nil
}

func mcpAuthSecretKind(authType string) (secrets.Kind, error) {
	switch authType {
	case MCPAuthTypeBearer:
		return secrets.KindGeneric, nil
	case MCPAuthTypeOAuth:
		return secrets.KindOAuthTokenSet, nil
	case MCPAuthTypeSigV4:
		return secrets.KindAWSCredentials, nil
	default:
		return "", fmt.Errorf("unsupported mcp auth type %q", authType)
	}
}

func ValidateMCPURL(raw string, allowInsecureLocalHTTP bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("url is invalid: %w", err)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("url host is required")
	}
	host := classifyMCPURLHost(parsed.Hostname())

	switch parsed.Scheme {
	case "https":
	case "http":
		if !host.LocalDev {
			return "", errors.New("url scheme must be https")
		}
		if !allowInsecureLocalHTTP {
			return "", fmt.Errorf(
				"url %q targets loopback; enable AllowInsecureLocalMCPHTTP "+
					"(set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 on the API for local development) "+
					"to compile loopback MCP endpoints",
				raw,
			)
		}
	default:
		return "", errors.New("url scheme must be https")
	}
	if host.IP != nil && !ssrf.IsAllowedIP(host.IP, allowInsecureLocalHTTP && parsed.Scheme == "http") {
		return "", fmt.Errorf("url host IP is not permitted: %s", host.IP.String())
	}
	return parsed.String(), nil
}

type mcpURLHost struct {
	IP       net.IP
	LocalDev bool
}

func classifyMCPURLHost(host string) mcpURLHost {
	normalized := strings.TrimSuffix(strings.ToLower(host), ".")
	ip := net.ParseIP(normalized)
	return mcpURLHost{
		IP:       ip,
		LocalDev: normalized == "localhost" || (ip != nil && ip.IsLoopback()),
	}
}

func compileMCPTools(
	serverKey string,
	defaultEnabled bool,
	source map[string]AgentConfigMCPToolSource,
) (map[string]MCPToolCompiled, error) {
	compiled := make(map[string]MCPToolCompiled, len(source))
	for remoteName, tool := range source {
		enabled := defaultEnabled
		if tool.Enabled != nil {
			enabled = *tool.Enabled
		}
		if err := validateMCPToolName(serverKey, remoteName, enabled); err != nil {
			return nil, issueAt(jsonPointer(remoteName), err)
		}
		var permission *toolpermission.Selection
		if tool.Permission != nil {
			normalized, err := toolpermission.ValidateSelection(
				*tool.Permission,
				toolcatalog.MCPToolPermissionModes(),
			)
			if err != nil {
				return nil, issueAt(jsonPointer(remoteName, "permission"), err)
			}
			permission = &normalized
		}
		compiled[remoteName] = MCPToolCompiled{
			Enabled:    tool.Enabled,
			Permission: permission,
		}
	}
	return compiled, nil
}

func validateMCPToolName(serverKey, remoteName string, enabled bool) error {
	err := toolcatalog.ValidateMCPRuntimeToolName(serverKey, remoteName)
	var tooLong *toolcatalog.MCPRuntimeToolNameTooLongError
	if !enabled && errors.As(err, &tooLong) {
		return nil
	}
	return err
}

func runtimeMCPServers(compiled map[string]MCPServerCompiled) ([]RuntimeMCPServer, error) {
	if len(compiled) == 0 {
		return nil, nil
	}
	serverKeys := make([]string, 0, len(compiled))
	for serverKey := range compiled {
		serverKeys = append(serverKeys, serverKey)
	}
	sort.Strings(serverKeys)
	out := make([]RuntimeMCPServer, 0, len(serverKeys))
	for _, serverKey := range serverKeys {
		server := compiled[serverKey]
		permission, err := toolpermission.ValidateSelection(
			server.Permission,
			toolcatalog.MCPToolPermissionModes(),
		)
		if err != nil {
			return nil, fmt.Errorf("compiled mcp server %q permission: %w", serverKey, err)
		}
		runtime := RuntimeMCPServer{
			ServerKey:      serverKey,
			URL:            server.URL,
			DefaultEnabled: server.DefaultEnabled,
			Permission:     permission,
			Tools:          map[string]RuntimeMCPTool{},
		}
		if server.Auth != nil {
			runtime.Auth = &RuntimeMCPAuth{
				Type:     server.Auth.Type,
				SecretID: server.Auth.SecretID,
				Service:  server.Auth.Service,
				Region:   server.Auth.Region,
			}
		}
		toolNames := make([]string, 0, len(server.Tools))
		for remoteName := range server.Tools {
			toolNames = append(toolNames, remoteName)
		}
		sort.Strings(toolNames)
		for _, remoteName := range toolNames {
			tool := server.Tools[remoteName]
			var permission *toolpermission.Selection
			if tool.Permission != nil {
				normalized, err := toolpermission.ValidateSelection(
					*tool.Permission,
					toolcatalog.MCPToolPermissionModes(),
				)
				if err != nil {
					return nil, fmt.Errorf(
						"compiled mcp server %q tool %q permission: %w",
						serverKey,
						remoteName,
						err,
					)
				}
				permission = &normalized
			}
			runtime.Tools[remoteName] = RuntimeMCPTool{
				RemoteName: remoteName,
				Enabled:    tool.Enabled,
				Permission: permission,
			}
		}
		out = append(out, runtime)
	}
	return out, nil
}
