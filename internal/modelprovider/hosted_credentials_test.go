package modelprovider

import (
	"strings"
	"testing"
)

func TestValidateHostedAPIToken(t *testing.T) {
	t.Parallel()
	if err := ValidateHostedAPIToken(strings.Repeat("a", MinimumHostedAPITokenBytes-1)); err == nil {
		t.Fatal("short hosted API token accepted")
	}
	if err := ValidateHostedAPIToken(strings.Repeat("a", MinimumHostedAPITokenBytes)); err != nil {
		t.Fatalf("valid hosted API token: %v", err)
	}
}

func TestValidateHostedCredentialValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "valid", value: "sk-openrouter", valid: true},
		{name: "empty"},
		{name: "surrounding whitespace", value: " sk-openrouter"},
		{name: "control character", value: "sk-openrouter\n"},
		{name: "too large", value: strings.Repeat("x", hostedCredentialMaxSecretBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateHostedCredentialValue(test.value)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateHostedCredentialValue error = %v, valid = %v", err, test.valid)
			}
		})
	}
}
