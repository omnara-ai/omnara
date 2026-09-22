package appdefinition

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var slackWorkspace = regexp.MustCompile(`^T[A-Z0-9]+$`)

func CanonicalLauncherScope(provider, kind, ref string) (string, string, error) {
	kind, ref = strings.TrimSpace(kind), strings.TrimSpace(ref)
	invalid := func() (string, string, error) {
		return "", "", fmt.Errorf("invalid %s launcher scope %q", provider, kind)
	}
	switch {
	case provider == ProviderSlack && kind == "workspace":
		if !slackWorkspace.MatchString(ref) {
			return invalid()
		}
		return kind, ref, nil
	case provider == ProviderGitHub && (kind == "installation" || kind == "repository"):
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil || id <= 0 {
			return invalid()
		}
		return kind, strconv.FormatInt(id, 10), nil
	case provider == ProviderDiscord:
		if kind != "" || ref != "" {
			return invalid()
		}
		return kind, ref, nil
	}
	scope, err := ParseConversation(provider, kind, ref)
	if err != nil {
		return invalid()
	}
	return scope.Conversation()
}
