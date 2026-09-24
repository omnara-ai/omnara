package toolcatalog

import (
	"fmt"
	"strings"
)

const (
	IntegrationToolPrefix  = "int__"
	IntegrationNamePattern = MCPServerKeyPattern
)

func ValidateIntegrationName(name string) error {
	if !mcpServerKeyPattern.MatchString(name) {
		return fmt.Errorf("integration name %q must match %s", name, IntegrationNamePattern)
	}
	return nil
}

func IntegrationToolName(integration, operation string) string {
	return IntegrationToolPrefix + integration + MCPToolNameSeparator + operation
}

func UsesIntegrationToolNamespace(name string) bool {
	return strings.HasPrefix(name, IntegrationToolPrefix)
}

func ValidateIntegrationToolName(integration, operation string) error {
	if err := ValidateIntegrationName(integration); err != nil {
		return err
	}
	if !mcpRemoteToolNamePattern.MatchString(operation) {
		return fmt.Errorf("integration operation %q must match %s", operation, MCPRemoteToolNamePattern)
	}
	if name := IntegrationToolName(integration, operation); len(name) > MaxMCPRuntimeToolNameLength {
		return fmt.Errorf(
			"integration tool %q exceeds %d characters; shorten the integration or operation name",
			name, MaxMCPRuntimeToolNameLength,
		)
	}
	return nil
}

func SplitIntegrationToolName(name string) (integration, operation string, ok bool) {
	key, found := strings.CutPrefix(name, IntegrationToolPrefix)
	if !found {
		return "", "", false
	}
	integration, operation, found = strings.Cut(key, MCPToolNameSeparator)
	if !found || ValidateIntegrationToolName(integration, operation) != nil {
		return "", "", false
	}
	return integration, operation, true
}
