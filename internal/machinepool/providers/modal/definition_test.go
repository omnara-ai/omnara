package modal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestDefinitionCreatesProvider(t *testing.T) {
	runtime, err := (Definition{}).NewProvider(
		json.RawMessage(`{"app":"agents","environment":"dev"}`),
		providers.RuntimeConfig{
			OmnaraAPIURL: "https://api.omnara.test/v1",
			ProviderAuthToken: `{
				"token_id":"ak-test",
				"token_secret":"as-test"
			}`,
		},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	target, ok := runtime.(*provider)
	if !ok {
		t.Fatalf("provider type = %T, want *provider", runtime)
	}
	if target.app != "agents" || target.environment != "dev" ||
		target.credential.TokenID != "ak-test" || target.credential.TokenSecret != "as-test" {
		t.Fatalf("provider = %+v", target)
	}
}

func TestDefinitionDefaultsEnvironment(t *testing.T) {
	config, err := parseProviderConfig(json.RawMessage(`{"app":"agents"}`))
	if err != nil {
		t.Fatalf("parse provider config: %v", err)
	}
	if config.Environment != "main" {
		t.Fatalf("environment = %q, want main", config.Environment)
	}
}

func TestDefinitionDefaultsApp(t *testing.T) {
	for _, raw := range []string{"", "null", `{}`, `{"app":""}`, `{"app":"  "}`} {
		t.Run(raw, func(t *testing.T) {
			config, err := parseProviderConfig(json.RawMessage(raw))
			if err != nil {
				t.Fatalf("parse provider config: %v", err)
			}
			if config.App != "omnara" || config.Environment != "main" {
				t.Fatalf("unexpected defaults: %+v", config)
			}
		})
	}
}

func TestDefinitionSupportsRuntimeProtection(t *testing.T) {
	if _, ok := any(Definition{}).(providers.RuntimeProviderDefinition); !ok {
		t.Fatal("modal definition does not support runtime protection")
	}
}

func TestDefinitionRejectsInvalidConfigAndCredentials(t *testing.T) {
	for _, test := range []struct {
		name       string
		config     json.RawMessage
		credential string
		want       string
	}{
		{name: "unknown config", config: json.RawMessage(`{"app":"agents","unknown":true}`), credential: `{"token_id":"ak","token_secret":"as"}`, want: "unknown field"},
		{name: "invalid environment", config: json.RawMessage(`{"app":"agents","environment":"bad/env"}`), credential: `{"token_id":"ak","token_secret":"as"}`, want: "modal environment"},
		{name: "environment id", config: json.RawMessage(`{"app":"agents","environment":"en-production"}`), credential: `{"token_id":"ak","token_secret":"as"}`, want: "modal environment"},
		{name: "plain token", config: json.RawMessage(`{"app":"agents"}`), credential: "token", want: "decode modal provider credentials"},
		{name: "missing token secret", config: json.RawMessage(`{"app":"agents"}`), credential: `{"token_id":"ak"}`, want: "require token_id and token_secret"},
		{name: "unknown credential", config: json.RawMessage(`{"app":"agents"}`), credential: `{"token_id":"ak","token_secret":"as","extra":true}`, want: "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Definition{}).NewProvider(
				test.config,
				providers.RuntimeConfig{ProviderAuthToken: test.credential},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("new provider error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDefinitionValidatesOptionalRegionAndAllowlists(t *testing.T) {
	defaultProvisioning := testProvisioning(t, "us-east")
	policy := testPolicy(t, defaultProvisioning)
	policy.ProviderConfig = json.RawMessage(
		`{"app":"agents","allowed_images":["registry.example/daemon:latest","registry.example/other:latest"],"allowed_regions":["us-east"]}`,
	)
	if err := (Definition{}).ValidateMachineProvisioning(
		policy,
		testProvisioning(t, "us-east"),
	); err != nil {
		t.Fatalf("validate allowed region: %v", err)
	}
	disallowedRegion := testProvisioning(t, "eu-west")
	if err := (Definition{}).ValidateMachineProvisioning(policy, disallowedRegion); err == nil ||
		!strings.Contains(err.Error(), "provider_config.allowed_regions") {
		t.Fatalf("disallowed region error = %v", err)
	}
	disallowedImage := testProvisioning(t, "us-east")
	disallowedImage.ProviderOptions["image"] = json.RawMessage(`"registry.example/private:latest"`)
	if err := (Definition{}).ValidateMachineProvisioning(policy, disallowedImage); err == nil ||
		!strings.Contains(err.Error(), "provider_config.allowed_images") {
		t.Fatalf("disallowed image error = %v", err)
	}
}

func TestDefinitionDefaultsAllowlistsToPoolOptions(t *testing.T) {
	defaultProvisioning := testProvisioning(t, "")
	policy := testPolicy(t, defaultProvisioning)
	if err := (Definition{}).ValidateMachineProvisioning(policy, defaultProvisioning); err != nil {
		t.Fatalf("validate pool defaults: %v", err)
	}
	machineProvisioning := testProvisioning(t, "us-east")
	if err := (Definition{}).ValidateMachineProvisioning(policy, machineProvisioning); err == nil ||
		!strings.Contains(err.Error(), "provider_config.allowed_regions") {
		t.Fatalf("region override error = %v", err)
	}

	defaultProvisioning = testProvisioning(t, "us-east")
	policy = testPolicy(t, defaultProvisioning)
	machineProvisioning = testProvisioning(t, "")
	if err := (Definition{}).ValidateMachineProvisioning(policy, machineProvisioning); err == nil ||
		!strings.Contains(err.Error(), "provider_config.allowed_regions") {
		t.Fatalf("automatic placement override error = %v", err)
	}
}

func TestDefinitionAutomaticRegionRespectsAllowlist(t *testing.T) {
	for _, test := range []struct {
		name      string
		config    string
		wantError string
	}{
		{"implicit", `{"app":"agents"}`, ""},
		{"wildcard", `{"app":"agents","allowed_regions":["*"]}`, ""},
		{"restricted", `{"app":"agents","allowed_regions":["us-east"]}`, "automatic region placement is not allowed"},
		{"empty", `{"app":"agents","allowed_regions":[]}`, "allowed_regions must not be empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := testPolicy(t, testProvisioning(t, ""))
			policy.ProviderConfig = json.RawMessage(test.config)
			err := (Definition{}).ValidatePool(policy)
			if (err != nil) != (test.wantError != "") {
				t.Fatalf("validate pool: %v, want error %v", err, test.wantError)
			}
			if err != nil && !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}
