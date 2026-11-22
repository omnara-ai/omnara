package revyl

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestParseProviderConfigDefaultsToRevylBackend(t *testing.T) {
	config, err := parseProviderConfig(nil)
	if err != nil {
		t.Fatalf("parse empty config: %v", err)
	}
	if config.APIBaseURL != apiBaseURL {
		t.Fatalf("api base url = %q, want %q", config.APIBaseURL, apiBaseURL)
	}
	config, err = parseProviderConfig(json.RawMessage(`{"api_base_url":"https://staging.example.com/"}`))
	if err != nil || config.APIBaseURL != "https://staging.example.com" {
		t.Fatalf("parse custom config = %+v, %v", config, err)
	}
}

func TestParseProviderConfigRejectsUnsafeBaseURLs(t *testing.T) {
	for _, raw := range []string{
		`{"api_base_url":"http://backend.revyl.ai"}`,
		`{"api_base_url":"https://user:pass@backend.revyl.ai"}`,
		`{"api_base_url":"https://backend.revyl.ai?x=1"}`,
		`{"workspace":"x"}`,
	} {
		if _, err := parseProviderConfig(json.RawMessage(raw)); err == nil {
			t.Errorf("parseProviderConfig(%s) succeeded, want error", raw)
		}
	}
}

func TestParseProviderOptions(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]json.RawMessage
		want    providerOptions
		wantErr string
	}{
		{name: "empty defaults to two hours", options: nil, want: providerOptions{Lifetime: 2 * time.Hour}},
		{
			name:    "startup script only defaults to two hours",
			options: map[string]json.RawMessage{"startup_script": json.RawMessage(`"brew install swiftlint"`)},
			want:    providerOptions{Lifetime: 2 * time.Hour, StartupScript: "brew install swiftlint"},
		},
		{
			name: "lifetime and startup script",
			options: map[string]json.RawMessage{
				"lifetime":       json.RawMessage(`"24h"`),
				"startup_script": json.RawMessage(`"brew install swiftlint"`),
			},
			want: providerOptions{Lifetime: 24 * time.Hour, StartupScript: "brew install swiftlint"},
		},
		{
			name:    "lifetime below minimum",
			options: map[string]json.RawMessage{"lifetime": json.RawMessage(`"59s"`)},
			wantErr: "lifetime must be whole seconds between 1m0s and 24h0m0s",
		},
		{
			name:    "lifetime above maximum",
			options: map[string]json.RawMessage{"lifetime": json.RawMessage(`"24h0m1s"`)},
			wantErr: "lifetime must be whole seconds",
		},
		{
			name:    "lifetime with fractional seconds",
			options: map[string]json.RawMessage{"lifetime": json.RawMessage(`"90.5s"`)},
			wantErr: "lifetime must be whole seconds",
		},
		{
			name:    "lifetime as a number",
			options: map[string]json.RawMessage{"lifetime": json.RawMessage("7200")},
			wantErr: "decode revyl provider_options.lifetime",
		},
		{
			name:    "unknown option",
			options: map[string]json.RawMessage{"image": json.RawMessage(`"macos"`)},
			wantErr: "unknown field",
		},
		{
			name: "startup script too large",
			options: map[string]json.RawMessage{
				"startup_script": mustRawJSON(t, strings.Repeat("x", 64*1024+1)),
			},
			wantErr: "startup_script must be at most",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseProviderOptions(test.options)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseProviderOptions error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("parseProviderOptions = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
}

func TestValidatePoolRejectsMachineSizes(t *testing.T) {
	cpu := 4
	policy := executionstore.MachinePoolProviderPolicy{}
	policy.DefaultProvisioning.CPU = &cpu
	if err := (Definition{}).ValidatePool(policy); err == nil {
		t.Fatal("ValidatePool accepted a default cpu, want error")
	}
	if err := (Definition{}).ValidatePool(executionstore.MachinePoolProviderPolicy{}); err != nil {
		t.Fatalf("ValidatePool(empty) = %v", err)
	}
}
