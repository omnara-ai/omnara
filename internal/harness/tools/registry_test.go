package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestBuiltInToolImplementationRegistryMatchesCatalog(t *testing.T) {
	type expectedTopology struct {
		transactional bool
		async         bool
		background    bool
	}
	expected := make(map[string]expectedTopology)
	add := func(names []string, topology expectedTopology) {
		for _, name := range names {
			expected[name] = topology
		}
	}
	add(
		[]string{
			"write_process",
			"stop_process",
			"list_processes",
		},
		expectedTopology{transactional: true},
	)
	add(
		[]string{"run_command", "read_process", "upload_artifact", "download_artifact"},
		expectedTopology{transactional: true, background: true},
	)
	add(
		[]string{"create_machine", "delete_machine"},
		expectedTopology{transactional: true, background: true},
	)
	add(
		[]string{"list_machines", "inspect_machine", "set_integration_target"},
		expectedTopology{transactional: true},
	)
	add(
		[]string{"ask_question"},
		expectedTopology{transactional: true, async: true},
	)
	add(
		[]string{"send_integration_message", "web_search", "web_fetch", "skill"},
		expectedTopology{async: true},
	)

	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("build public tool catalog: %v", err)
	}
	implementations, err := loadBuiltInToolImplementations()
	if err != nil {
		t.Fatalf("build tool implementation registry: %v", err)
	}
	entries := catalog.Entries()
	if len(entries) != len(expected) {
		t.Fatalf("public tool count = %d, want %d", len(entries), len(expected))
	}
	if len(implementations.tools) != len(expected) {
		t.Fatalf(
			"tool implementation count = %d, want %d",
			len(implementations.tools),
			len(expected),
		)
	}
	for _, definition := range entries {
		want, ok := expected[definition.Name]
		if !ok {
			t.Fatalf("public tool %q is missing from the expected implementation contract", definition.Name)
		}
		got, ok := implementations.Lookup(definition.Name)
		if !ok {
			t.Fatalf("public tool %q has no implementation", definition.Name)
		}
		if got.inputSchemaValidator == nil {
			t.Fatalf("tool %q has no compiled input schema", definition.Name)
		}
		if (got.handler.Transactional != nil) != want.transactional ||
			(got.handler.Async != nil) != want.async ||
			(got.handler.Background != nil) != want.background {
			t.Fatalf(
				"tool %q phases = transactional:%v async:%v background:%v, want transactional:%v async:%v background:%v",
				definition.Name,
				got.handler.Transactional != nil,
				got.handler.Async != nil,
				got.handler.Background != nil,
				want.transactional,
				want.async,
				want.background,
			)
		}
		if len(got.permissionModes) != len(definition.PermissionModes) {
			t.Fatalf(
				"tool %q permission handler count = %d, want %d",
				definition.Name,
				len(got.permissionModes),
				len(definition.PermissionModes),
			)
		}
		for _, mode := range definition.PermissionModes {
			if got.permissionModes[mode.Name] == nil {
				t.Fatalf("tool %q has no handler for permission mode %q", definition.Name, mode.Name)
			}
		}
	}
}

func TestProcessToolImplementationValidatorBindings(t *testing.T) {
	tests := []struct {
		name    string
		valid   json.RawMessage
		invalid json.RawMessage
	}{
		{
			name:    "run_command",
			valid:   json.RawMessage(`{"command":"echo ok"}`),
			invalid: json.RawMessage(`{"process_id":"prc"}`),
		},
		{
			name:    "write_process",
			valid:   json.RawMessage(`{"process_id":"prc","data":"x"}`),
			invalid: json.RawMessage(`{"process_id":"prc"}`),
		},
		{
			name:    "read_process",
			valid:   json.RawMessage(`{"process_id":"prc","wait_ms":1}`),
			invalid: json.RawMessage(`{"wait_ms":1}`),
		},
		{
			name:    "stop_process",
			valid:   json.RawMessage(`{"process_id":"prc","mode":"terminate"}`),
			invalid: json.RawMessage(`{"process_id":"prc","data":"x"}`),
		},
		{
			name:    "list_processes",
			valid:   json.RawMessage(`{}`),
			invalid: json.RawMessage(`{"cursor":0}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRegisteredToolInput(test.name, test.valid); err != nil {
				t.Fatalf("valid input rejected: %v", err)
			}
			if err := validateRegisteredToolInput(test.name, test.invalid); err == nil {
				t.Fatal("tool-specific invalid input was accepted")
			}
		})
	}
}

func TestAskQuestionImplementationValidatorBinding(t *testing.T) {
	if err := validateRegisteredToolInput(
		"ask_question",
		json.RawMessage(
			`{"questions":[{"prompt":"Proceed?","options":[{"label":"Yes"},{"label":"No"}]}]}`,
		),
	); err != nil {
		t.Fatalf("valid question rejected: %v", err)
	}
	if err := validateRegisteredToolInput(
		"ask_question",
		json.RawMessage(
			`{"questions":[{"prompt":"","options":[{"label":"Yes"}]}]}`,
		),
	); err == nil {
		t.Fatal("empty question prompt was accepted")
	}
	if err := validateRegisteredToolInput(
		"ask_question",
		json.RawMessage(
			`{"questions":[{"prompt":"Proceed?","options":[]}]}`,
		),
	); err == nil {
		t.Fatal("optionless question was accepted")
	}
	if err := validateRegisteredToolInput(
		"ask_question",
		json.RawMessage(
			`{"questions":[{"prompt":"Proceed?","options":[{"label":"Yes","allows_text":true}]}]}`,
		),
	); err == nil {
		t.Fatal("model-provided text capability was accepted")
	}
}

func TestIntegrationMessageImplementationValidatorBinding(t *testing.T) {
	artifactID, err := publicid.Encode(
		publicid.KindArtifact,
		integrationToolTestID("integration-message-validator"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRegisteredToolInput(
		"send_integration_message",
		json.RawMessage(`{"text":"hello","artifact_id":"`+artifactID+`"}`),
	); err != nil {
		t.Fatalf("valid integration message rejected: %v", err)
	}
	if err := validateRegisteredToolInput(
		"send_integration_message",
		json.RawMessage(`{"text":"hello","artifact_id":""}`),
	); err != nil {
		t.Fatalf("empty artifact_id rejected: %v", err)
	}
	if err := validateRegisteredToolInput(
		"send_integration_message",
		json.RawMessage(`{"text":"hello","artifact_id":null}`),
	); err == nil {
		t.Fatal("null artifact_id accepted")
	}
}

func TestAskQuestionFormAddsTextCapableOtherOption(t *testing.T) {
	value, err := askQuestionForm(json.RawMessage(
		`{"questions":[{"prompt":"Database?","multiple":true,"options":[{"label":"Other"}]}]}`,
	))
	if err != nil {
		t.Fatalf("askQuestionForm() error = %v", err)
	}
	options := value.Questions[0].Options
	if !value.Questions[0].Multiple ||
		len(options) != 2 ||
		options[0].Label != "Other" ||
		options[0].AllowsText ||
		options[1].Label != "Other" ||
		!options[1].AllowsText {
		t.Fatalf("ask_question options = %+v", options)
	}
}

func TestToolImplementationRegistryRejectsInvalidConstruction(t *testing.T) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("build public tool catalog: %v", err)
	}
	tests := []struct {
		name   string
		change func([]toolRegistration) []toolRegistration
		want   string
	}{
		{
			name: "missing public tool",
			change: func(registrations []toolRegistration) []toolRegistration {
				return registrations[:len(registrations)-1]
			},
			want: "has no implementation",
		},
		{
			name: "unknown implementation",
			change: func(registrations []toolRegistration) []toolRegistration {
				return append(registrations, toolRegistration{
					name:                   "unknown",
					semanticInputValidator: validateSkillInput,
					handler:                toolHandler{Async: runMCPToolAsync},
				})
			},
			want: "missing from the public catalog",
		},
		{
			name: "duplicate implementation",
			change: func(registrations []toolRegistration) []toolRegistration {
				return append(registrations, registrations[0])
			},
			want: "registered more than once",
		},
		{
			name: "background-only topology",
			change: func(registrations []toolRegistration) []toolRegistration {
				registrations[0].handler = toolHandler{
					Background: provisionMachineInBackground,
				}
				return registrations
			},
			want: "requires a transactional or async phase",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newToolImplementationRegistry(
				catalog,
				test.change(builtInToolRegistrations()),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestMCPToolImplementationIsConstructedDynamically(t *testing.T) {
	name := toolcatalog.MCPRuntimeToolName("docs", "search")
	implementations, err := loadBuiltInToolImplementations()
	if err != nil {
		t.Fatalf("build tool implementation registry: %v", err)
	}
	if _, ok := implementations.Lookup(name); ok {
		t.Fatal("dynamic MCP tool was registered as a static built-in")
	}
	implementation, ok, err := toolImplementationFor(name)
	if err != nil {
		t.Fatalf("resolve dynamic MCP implementation: %v", err)
	}
	if !ok {
		t.Fatal("dynamic MCP tool has no implementation")
	}
	if implementation.handler.Transactional != nil || implementation.handler.Async == nil ||
		implementation.handler.Background != nil {
		t.Fatalf("MCP phase topology = %+v, want async only", implementation.handler)
	}
	if implementation.inputSchemaValidator != nil || implementation.semanticInputValidator != nil {
		t.Fatal("dynamic MCP arguments should be validated by the discovered tool contract and server")
	}
	if err := implementation.validateInput(json.RawMessage(`{"query":"test"}`)); err != nil {
		t.Fatalf("validate dynamic MCP input: %v", err)
	}
	if _, ok, err := toolImplementationFor("bash"); err != nil {
		t.Fatalf("resolve unsupported tool: %v", err)
	} else if ok {
		t.Fatal("unsupported tool has an implementation")
	}
}

func validateRegisteredToolInput(name string, input json.RawMessage) error {
	implementation, ok, err := toolImplementationFor(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tool %q has no implementation", name)
	}
	return implementation.validateInput(input)
}
