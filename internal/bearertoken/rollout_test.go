package bearertoken

import (
	"strings"
	"testing"
)

// TestPreCanonicalAuthenticatorCutoverMatrix freezes the deliberate clean-break
// behavior of the API implementation immediately before canonical v1 tokens.
// These helpers are test-only compatibility oracles, not legacy support.
func TestPreCanonicalAuthenticatorCutoverMatrix(t *testing.T) {
	newPAT, err := format(KindPersonalAccess, strings.Repeat("A", secretLength))
	if err != nil {
		t.Fatal(err)
	}
	newOrg, err := format(KindOrganization, strings.Repeat("B", secretLength))
	if err != nil {
		t.Fatal(err)
	}
	newDaemon, err := format(KindDaemon, strings.Repeat("C", secretLength))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		token      string
		kind       Kind
		oldAccepts bool
		newAccepts bool
	}{
		{name: "old PAT", token: "omnara_pat_legacyid_legacysecret", kind: KindPersonalAccess, oldAccepts: true},
		{name: "new PAT", token: newPAT, kind: KindPersonalAccess, newAccepts: true},
		{name: "old org key", token: "omnara_org_legacyid_legacysecret", kind: KindOrganization, oldAccepts: true},
		{name: "new org key", token: newOrg, kind: KindOrganization, newAccepts: true},
		{name: "old daemon", token: "omnara_daemon_legacysecret", kind: KindDaemon, oldAccepts: true},
		{name: "new daemon", token: newDaemon, kind: KindDaemon, oldAccepts: true, newAccepts: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := preCanonicalAccepts(test.token, test.kind); got != test.oldAccepts {
				t.Fatalf("pre-canonical accepts = %t, want %t", got, test.oldAccepts)
			}
			got := Validate(test.token, test.kind) == nil
			if got != test.newAccepts {
				t.Fatalf("canonical v1 accepts = %t, want %t", got, test.newAccepts)
			}
		})
	}
}

func preCanonicalAccepts(token string, kind Kind) bool {
	switch kind {
	case KindPersonalAccess:
		return preCanonicalIDSecretToken(token, "omnara_pat_")
	case KindOrganization:
		return preCanonicalIDSecretToken(token, "omnara_org_")
	case KindDaemon:
		// The previous daemon path performed prefix routing and then looked up
		// the digest of the complete opaque token.
		return strings.HasPrefix(token, "omnara_daemon_")
	default:
		return false
	}
}

func preCanonicalIDSecretToken(token, prefix string) bool {
	if !strings.HasPrefix(token, prefix) {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(token, prefix), "_", 2)
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" &&
		!strings.Contains(parts[0], "_") && !strings.Contains(parts[1], "_")
}
