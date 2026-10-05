package agentconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	openAPIDefinitionBlockBegin = "    # BEGIN GENERATED AgentConfigDefinition components.\n"
	openAPIDefinitionBlockEnd   = "    # END GENERATED AgentConfigDefinition components.\n"
	openAPIDefinitionBlockNote  = "    # Projected from the agent config source schema. Regenerate with\n" +
		"    # OMNARA_REGEN_AGENT_CONFIG_SCHEMA=1 go test ./internal/agentconfig -run TestGenerated\n"
)

func TestGeneratedOpenAPIDefinitionComponentsAreCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "api", "openapi", "openapi.yaml")
	spec, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read openapi spec: %v", err)
	}
	beginAt := bytes.Index(spec, []byte(openAPIDefinitionBlockBegin))
	endAt := bytes.Index(spec, []byte(openAPIDefinitionBlockEnd))
	if beginAt < 0 || endAt < beginAt {
		t.Fatalf("openapi spec is missing the generated AgentConfigDefinition block markers")
	}
	block := renderOpenAPIDefinitionBlock(t)
	current := string(spec[beginAt : endAt+len(openAPIDefinitionBlockEnd)])
	if current == block {
		return
	}
	if os.Getenv("OMNARA_REGEN_AGENT_CONFIG_SCHEMA") != "1" {
		t.Fatal("generated AgentConfigDefinition components in api/openapi/openapi.yaml are stale; " +
			"rerun with OMNARA_REGEN_AGENT_CONFIG_SCHEMA=1 to refresh them")
	}
	updated := string(spec[:beginAt]) + block + string(spec[endAt+len(openAPIDefinitionBlockEnd):])
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatalf("write regenerated openapi spec: %v", err)
	}
}

func TestOpenAPIDefinitionComponentsDropValidationOnlyKeywords(t *testing.T) {
	components, err := OpenAPIDefinitionComponents()
	if err != nil {
		t.Fatalf("project components: %v", err)
	}
	var visit func(path string, node any)
	visit = func(path string, node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, value := range typed {
				if properties, ok := value.(map[string]any); ok && key == "properties" {
					for name, property := range properties {
						visit(path+"/properties/"+name, property)
					}
					continue
				}
				if openAPITypingOnlyDropKeywords[key] {
					t.Errorf("%s keeps validation-only keyword %q", path, key)
				}
				if ref, ok := value.(string); key == "$ref" && ok && !strings.HasPrefix(ref, "#/components/schemas/") {
					t.Errorf("%s keeps unprojected $ref %q", path, ref)
				}
				visit(path+"/"+key, value)
			}
		case []any:
			for _, item := range typed {
				visit(path, item)
			}
		}
	}
	for name, component := range components {
		visit(name, component)
	}
	machineSource, _ := components["AgentConfigDefinitionMachineSource"].(map[string]any)
	properties, _ := machineSource["properties"].(map[string]any)
	envOverlay, _ := properties["env_overlay"].(map[string]any)
	if got := envOverlay["type"]; !equalAny(got, []any{"object", "null"}) {
		t.Fatalf("nullable anyOf was not folded into a type list: %#v", got)
	}
}

func renderOpenAPIDefinitionBlock(t *testing.T) string {
	t.Helper()
	components, err := OpenAPIDefinitionComponents()
	if err != nil {
		t.Fatalf("project components: %v", err)
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(components); err != nil {
		t.Fatalf("encode components: %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("close encoder: %v", err)
	}
	var block strings.Builder
	block.WriteString(openAPIDefinitionBlockBegin)
	block.WriteString(openAPIDefinitionBlockNote)
	for line := range strings.Lines(out.String()) {
		block.WriteString("    ")
		block.WriteString(line)
	}
	block.WriteString(openAPIDefinitionBlockEnd)
	return block.String()
}

func equalAny(got any, want []any) bool {
	list, ok := got.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i := range list {
		if list[i] != want[i] {
			return false
		}
	}
	return true
}
