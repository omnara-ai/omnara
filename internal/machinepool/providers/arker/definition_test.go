package arker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestArkerValidatePoolAcceptsAWellFormedPool(t *testing.T) {
	err := Definition{}.ValidatePool(testPolicy(
		json.RawMessage(`{"allowed_sources":["ubuntu-base"],"allowed_providers":["aws"],"allowed_regions":["us-west-2"]}`),
		testOptions(),
	))
	if err != nil {
		t.Fatalf("validate pool: %v", err)
	}
}

func TestArkerValidatePoolRejectsADisallowedSource(t *testing.T) {
	err := Definition{}.ValidatePool(testPolicy(
		json.RawMessage(`{"allowed_sources":["something-else"]}`),
		testOptions(),
	))
	if err == nil || !strings.Contains(err.Error(), "allowed_sources") {
		t.Fatalf("disallowed source error = %v", err)
	}
}

func TestArkerValidatePoolRejectsAPlacementThatIsNotADNSLabel(t *testing.T) {
	for _, hostile := range []string{
		"attacker.example.com/",
		"attacker.example.com#",
		"UPPER",
		"has_underscore",
	} {
		options := testOptions()
		options["provider"] = json.RawMessage(`"` + hostile + `"`)
		if _, err := parseProviderOptions(options); err == nil {
			t.Fatalf("provider %q was accepted and would be spliced into the api hostname", hostile)
		}
	}
	config := json.RawMessage(`{"allowed_providers":["attacker.example.com/"]}`)
	if _, err := parseProviderConfig(config); err == nil {
		t.Fatal("an allowlist entry that is not a DNS label was accepted")
	}
}

func TestArkerProviderAndRegionMustBeSetTogether(t *testing.T) {
	options := testOptions()
	delete(options, "region")
	if _, err := parseProviderOptions(options); err == nil ||
		!strings.Contains(err.Error(), "together") {
		t.Fatalf("provider without region error = %v", err)
	}
}

func TestArkerValidatePoolRejectsBothBaseURLAndPlacement(t *testing.T) {
	err := Definition{}.ValidatePool(testPolicy(
		json.RawMessage(`{"base_url":"https://aws-us-west-2.arker.ai/api"}`),
		testOptions(),
	))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("base_url with placement error = %v", err)
	}
}

func TestArkerValidatePoolRequiresAPlacement(t *testing.T) {
	options := testOptions()
	delete(options, "provider")
	delete(options, "region")
	err := Definition{}.ValidatePool(testPolicy(json.RawMessage(`{}`), options))
	if err == nil || !strings.Contains(err.Error(), "placement") {
		t.Fatalf("missing placement error = %v", err)
	}
}

func TestArkerRejectsUnknownProviderOptions(t *testing.T) {
	options := testOptions()
	options["surprise"] = json.RawMessage(`"value"`)
	if _, err := parseProviderOptions(options); err == nil {
		t.Fatal("an unknown provider option must be rejected, not ignored")
	}
	if _, err := parseProviderConfig(json.RawMessage(`{"surprise":true}`)); err == nil {
		t.Fatal("an unknown provider config key must be rejected, not ignored")
	}
}

func TestArkerProviderConfigRejectsPlainHTTP(t *testing.T) {
	_, err := parseProviderConfig(json.RawMessage(`{"base_url":"http://arker.example/api"}`))
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http base_url error = %v", err)
	}
}

func TestArkerBuildIntentKeepsTheRequestedSize(t *testing.T) {
	intent, err := Definition{}.BuildMachineProvisioningIntent(
		testPolicy(json.RawMessage(`{}`), testOptions()),
		testProvisioning(testOptions()),
	)
	if err != nil {
		t.Fatalf("build intent: %v", err)
	}
	if intent.CPU == nil || *intent.CPU != 2 || intent.MemoryMB == nil || *intent.MemoryMB != 4096 {
		t.Fatalf("intent dropped the requested size: %+v", intent)
	}
}

func TestArkerNewProviderRequiresAnAuthToken(t *testing.T) {
	_, err := Definition{}.NewProvider(json.RawMessage(`{}`), providers.RuntimeConfig{
		OmnaraAPIURL: "https://omnara.example",
	})
	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("missing auth token error = %v", err)
	}
}

func TestArkerClientRequiresAPlacement(t *testing.T) {
	machineProvider, err := Definition{}.NewProvider(
		json.RawMessage(`{}`),
		providers.RuntimeConfig{
			OmnaraAPIURL:      "https://omnara.example",
			ProviderAuthToken: "ark_test",
		},
	)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	concrete, ok := machineProvider.(*provider)
	if !ok {
		t.Fatalf("provider has an unexpected implementation %T", machineProvider)
	}
	if _, err := concrete.client(providerOptions{}); err == nil ||
		!strings.Contains(err.Error(), "placement") {
		t.Fatalf("missing placement error = %v", err)
	}
}
