package machinepool

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/blaxel"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/daytona"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/modal"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/unikraft"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestDefaultCatalogProviders(t *testing.T) {
	catalog := DefaultCatalog()
	if len(catalog.definitions) != 4 {
		t.Fatalf("default catalog providers = %d, want 4", len(catalog.definitions))
	}
	for _, test := range []struct {
		name       string
		definition any
	}{
		{name: "blaxel", definition: blaxel.Definition{}},
		{name: "daytona", definition: daytona.Definition{}},
		{name: "modal", definition: modal.Definition{}},
		{name: "unikraft", definition: unikraft.Definition{}},
	} {
		definition, ok := catalog.definition(test.name)
		if !ok {
			t.Fatalf("default catalog is missing provider %q", test.name)
		}
		if reflect.TypeOf(definition) != reflect.TypeOf(test.definition) {
			t.Fatalf("default catalog provider %q definition = %T, want %T", test.name, definition, test.definition)
		}
	}
}

func TestCatalogMergesProviderOptionOverlays(t *testing.T) {
	options, err := DefaultCatalog().ResolveMachineProviderOptions(
		"unikraft",
		map[string]json.RawMessage{
			"image":          json.RawMessage(`"registry.example/base:latest"`),
			"metro":          json.RawMessage(`"sfo"`),
			"startup_script": json.RawMessage(`"echo base"`),
		},
		map[string]json.RawMessage{
			"image":          json.RawMessage(`"registry.example/project:latest"`),
			"startup_script": json.RawMessage(`"echo project"`),
		},
		map[string]json.RawMessage{
			"image":          json.RawMessage(`"registry.example/agent:latest"`),
			"metro":          json.RawMessage(`"iad"`),
			"startup_script": json.RawMessage(`"echo agent"`),
		},
	)
	if err != nil {
		t.Fatalf("resolve provider options: %v", err)
	}
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatalf("marshal provider options: %v", err)
	}
	assertJSONEqual(
		t,
		raw,
		json.RawMessage(
			`{"image":"registry.example/agent:latest","metro":"iad","startup_script":"echo agent"}`,
		),
	)
}

func TestValidateProviderConfigRejectsUnknownProvider(t *testing.T) {
	if err := DefaultCatalog().ValidatePool(
		"missing",
		executionstore.MachinePoolProviderPolicy{},
	); err == nil {
		t.Fatal("expected unknown provider to fail")
	}
}

func TestCatalogRejectsProtectedPoolWithoutRuntimeObserver(t *testing.T) {
	catalog := Catalog{definitions: map[string]providers.Definition{
		"unsupported": runtimeObservationOmittedDefinition{Definition: blaxel.Definition{}},
	}}
	err := catalog.ValidatePool(
		"unsupported",
		executionstore.MachinePoolProviderPolicy{RuntimeProtectionEnabled: true},
	)
	if err == nil || !strings.Contains(err.Error(), "does not support runtime protection") {
		t.Fatalf("protected unsupported provider error = %v", err)
	}
}

type runtimeObservationOmittedDefinition struct {
	providers.Definition
}

func assertJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON %s: %v", got, err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON %s: %v", want, err)
	}
	if diff := cmp.Diff(wantValue, gotValue); diff != "" {
		t.Fatalf("JSON value mismatch (-want +got):\n%s", diff)
	}
}
