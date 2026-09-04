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
	if !found || serverKey == "" || remoteName == "" {
		return "", "", false
	}
	if ValidateMCPRuntimeToolName(serverKey, remoteName) != nil {
		return "", "", false
	}
	return serverKey, remoteName, true
}

type MCPRuntimeToolNameTooLongError struct {
	ServerKey  string
	RemoteName string
}

func (e *MCPRuntimeToolNameTooLongError) RuntimeName() string {
	return MCPRuntimeToolName(e.ServerKey, e.RemoteName)
}

func (e *MCPRuntimeToolNameTooLongError) MaxServerKeyLength() int {
	return MaxMCPRuntimeToolNameLength - len(MCPRuntimeToolPrefix) - len(MCPToolNameSeparator) - len(e.RemoteName)
}

func (e *MCPRuntimeToolNameTooLongError) Error() string {
	name := e.RuntimeName()
	message := fmt.Sprintf(
		"tool %q becomes %q (%d characters) once the server name is prefixed, but the model only accepts tool names of %d characters or fewer",
		e.RemoteName,
		name,
		len(name),
		MaxMCPRuntimeToolNameLength,
	)
	if maxKey := e.MaxServerKeyLength(); maxKey >= 1 {
		return fmt.Sprintf("%s; shorten the server name %q to %d characters or fewer", message, e.ServerKey, maxKey)
	}
	return message + "; the tool name itself is too long to expose under any server name"
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
	if !mcpRemoteToolNamePattern.MatchString(remoteName) {
		return fmt.Errorf("mcp tool name %q must match %s", remoteName, MCPRemoteToolNamePattern)
	}
	if len(MCPRuntimeToolName(serverKey, remoteName)) > MaxMCPRuntimeToolNameLength {
		return &MCPRuntimeToolNameTooLongError{ServerKey: serverKey, RemoteName: remoteName}
	}
	return nil
}
