package toolcatalog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	MCPToolNameSeparator        = "__"
	MaxMCPRuntimeToolNameLength = 64
	MCPServerKeyPattern         = `^[a-zA-Z][a-zA-Z0-9-]{0,31}$`
	MCPRemoteToolNamePattern    = `^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`
	MCPRuntimeToolPrefix        = "mcp" + MCPToolNameSeparator
)

var (
	mcpServerKeyPattern      = regexp.MustCompile(MCPServerKeyPattern)
	mcpRemoteToolNamePattern = regexp.MustCompile(MCPRemoteToolNamePattern)
)

func MCPRuntimeToolName(serverKey, remoteName string) string {
	return MCPRuntimeToolPrefix + serverKey + MCPToolNameSeparator + remoteName
}

func IsMCPRuntimeToolName(name string) bool {
	_, _, ok := SplitMCPRuntimeToolName(name)
	return ok
}

func UsesMCPRuntimeNamespace(name string) bool {
	return strings.HasPrefix(name, MCPRuntimeToolPrefix)
}

func SplitMCPRuntimeToolName(name string) (serverKey string, remoteName string, ok bool) {
	runtimeName, found := strings.CutPrefix(name, MCPRuntimeToolPrefix)
	if !found {
		return "", "", false
	}
	serverKey, remoteName, found = strings.Cut(runtimeName, MCPToolNameSeparator)
	if !found || serverKey == "" || remoteName == "" || strings.Contains(remoteName, MCPToolNameSeparator) {
		return "", "", false
	}
	if ValidateMCPRuntimeToolName(serverKey, remoteName) != nil {
		return "", "", false
	}
	return serverKey, remoteName, true
}

func ValidateMCPRuntimeToolName(serverKey, remoteName string) error {
	if serverKey == "" {
		return errors.New("mcp server key is required")
	}
	if !mcpServerKeyPattern.MatchString(serverKey) {
		return fmt.Errorf("mcp server key %q must match %s", serverKey, MCPServerKeyPattern)
	}
	if remoteName == "" {
		return errors.New("mcp tool name is required")
	}
	if strings.Contains(remoteName, MCPToolNameSeparator) {
		return fmt.Errorf("mcp tool name %q must not contain %s", remoteName, MCPToolNameSeparator)
	}
	if !mcpRemoteToolNamePattern.MatchString(remoteName) {
		return fmt.Errorf("mcp tool name %q must match %s", remoteName, MCPRemoteToolNamePattern)
	}
	name := MCPRuntimeToolName(serverKey, remoteName)
	if len(name) > MaxMCPRuntimeToolNameLength {
		return fmt.Errorf("mcp runtime tool name %q must be %d characters or fewer", name, MaxMCPRuntimeToolNameLength)
	}
	return nil
}
