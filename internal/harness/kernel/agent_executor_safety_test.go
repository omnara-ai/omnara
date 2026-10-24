package kernel

import (
	"testing"

	"github.com/google/uuid"
)

func TestSafetyIdentifierIsPerOrg(t *testing.T) {
	executor := AgentExecutor{SafetyIdentifierKey: []byte("kernel-test-safety-identifier-key")}
	orgA, orgB := uuid.New(), uuid.New()
	if a := executor.safetyIdentifier(orgA); a == "" || a != executor.safetyIdentifier(orgA) {
		t.Fatalf("org identifier %q is not stable and non-empty", a)
	}
	if executor.safetyIdentifier(orgA) == executor.safetyIdentifier(orgB) {
		t.Fatal("different orgs share a safety identifier")
	}
	if id := (AgentExecutor{}).safetyIdentifier(orgA); id != "" {
		t.Fatalf("identifier without a key = %q, want none", id)
	}
}
