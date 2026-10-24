package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/resourcename"
)

func TestAgentMCPOAuthSecretNameIsUniquePerAgent(t *testing.T) {
	first, err := agentMCPOAuthSecretName("calendar", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	second, err := agentMCPOAuthSecretName("calendar", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two agents got the same secret name %q", first)
	}
	if !strings.HasPrefix(first, "calendar-oauth-agt_") {
		t.Fatalf("secret name = %q, want calendar-oauth-<agent id>", first)
	}

	long, err := agentMCPOAuthSecretName(strings.Repeat("ö", 80), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(long) > resourcename.MaxCodePoints || !utf8.ValidString(long) {
		t.Fatalf("long server key produced %d runes (valid=%v), want at most %d",
			utf8.RuneCountInString(long), utf8.ValidString(long), resourcename.MaxCodePoints)
	}
	if _, err := resourcename.CanonicalizeRequired("secret name", long); err != nil {
		t.Fatalf("long secret name is not a valid resource name: %v", err)
	}
}
