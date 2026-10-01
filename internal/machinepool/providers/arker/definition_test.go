package arker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestArkerValidatePoolAcceptsAWellFormedPool(t *testing.T) {
	err := Definition{}.ValidatePool(testPolicy(
		json.RawMessage(`{"allowed_sources":["ubuntu-base"],"allowed_regions":["aws-us-west-2"]}`),
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

func TestArkerValidateMachineProvisioningRejectsADisallowedRegion(t *testing.T) {
	options := testOptions()
	options["region"] = json.RawMessage(`"gcp-us-central1"`)
	err := Definition{}.ValidateMachineProvisioning(
		testPolicy(json.RawMessage(`{}`), testOptions()),
		testProvisioning(options),
	)
	if err == nil || !strings.Contains(err.Error(), "allowed_regions") {
		t.Fatalf("disallowed region error = %v", err)
	}
}

func TestArkerRequiresARegionThatIsADNSLabel(t *testing.T) {
	for _, region := range []string{"", "attacker.example.com/", "attacker.example.com#", "UPPER", "has_underscore"} {
		options := testOptions()
		options["region"] = json.RawMessage(`"` + region + `"`)
		if _, err := parseProviderOptions(options); err == nil {
			t.Fatalf("region %q was accepted", region)
		}
	}
	options := testOptions()
	delete(options, "region")
	if _, err := parseProviderOptions(options); err == nil {
		t.Fatal("a missing region was accepted")
	}
}

func TestArkerRejectsUnknownProviderOptionsAndConfig(t *testing.T) {
	options := testOptions()
	options["provider"] = json.RawMessage(`"aws"`)
	if _, err := parseProviderOptions(options); err == nil {
		t.Fatal("an unknown provider option must be rejected")
	}
	if _, err := parseProviderConfig(json.RawMessage(`{"base_url":"https://arker.example/api"}`)); err == nil {
		t.Fatal("an unknown provider config key must be rejected")
	}
}

func TestArkerProviderConfigRejectsPlainHTTP(t *testing.T) {
	_, err := parseProviderConfig(json.RawMessage(`{"api_base_url":"http://arker.example/api"}`))
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http api_base_url error = %v", err)
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
