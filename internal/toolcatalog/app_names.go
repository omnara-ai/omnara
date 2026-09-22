package toolcatalog

import (
	"fmt"
	"strings"
)

const (
	AppToolPrefix  = "app__"
	AppNamePattern = MCPServerKeyPattern
)

func ValidateAppName(name string) error {
	if !mcpServerKeyPattern.MatchString(name) {
		return fmt.Errorf("app name %q must match %s", name, AppNamePattern)
	}
	return nil
}

func AppToolName(app, operation string) string {
	return AppToolPrefix + app + MCPToolNameSeparator + operation
}

func UsesAppToolNamespace(name string) bool {
	return strings.HasPrefix(name, AppToolPrefix)
}

func ValidateAppToolName(app, operation string) error {
	if err := ValidateAppName(app); err != nil {
		return err
	}
	if !mcpRemoteToolNamePattern.MatchString(operation) {
		return fmt.Errorf("app operation %q must match %s", operation, MCPRemoteToolNamePattern)
	}
	if name := AppToolName(app, operation); len(name) > MaxMCPRuntimeToolNameLength {
		return fmt.Errorf(
			"app tool %q exceeds %d characters; shorten the app or operation name",
			name, MaxMCPRuntimeToolNameLength,
		)
	}
	return nil
}

func SplitAppToolName(name string) (app, operation string, ok bool) {
	key, found := strings.CutPrefix(name, AppToolPrefix)
	if !found {
		return "", "", false
	}
	app, operation, found = strings.Cut(key, MCPToolNameSeparator)
	if !found || ValidateAppToolName(app, operation) != nil {
		return "", "", false
	}
	return app, operation, true
}
