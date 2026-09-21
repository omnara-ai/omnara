// Command config-schema generates the agent config schema and its shared OpenAPI definitions.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

const (
	configSchemaPath = "internal/agentconfig/generated/agent_config.schema.json"
	openAPIPath      = "api/openapi/openapi.yaml"
	sectionStart     = "    # BEGIN GENERATED APP CAPABILITY SCHEMAS\n"
	sectionEnd       = "    # END GENERATED APP CAPABILITY SCHEMAS\n"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "config-schema:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("config-schema", flag.ContinueOnError)
	check := flags.Bool("check", false, "check generated files without writing them")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return generate(".", *check)
}

func generate(root string, check bool) error {
	schema, err := agentconfig.SourceJSONSchema()
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		return err
	}
	config, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	spec, err := os.ReadFile(filepath.Join(root, openAPIPath))
	if err != nil {
		return err
	}
	openAPI, err := renderOpenAPI(schema, spec)
	if err != nil {
		return err
	}
	// Render both outputs before writing so invalid input leaves both files intact.
	for _, output := range []struct {
		path string
		data []byte
	}{
		{configSchemaPath, append(config, '\n')},
		{openAPIPath, openAPI},
	} {
		path := filepath.Join(root, output.path)
		current, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if bytes.Equal(current, output.data) {
			continue
		}
		if check {
			return fmt.Errorf("%s is stale; run make openapi-generate", output.path)
		}
		if err := os.WriteFile(path, output.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func renderOpenAPI(schema, spec []byte) ([]byte, error) {
	text := string(spec)
	if strings.Count(text, sectionStart) != 1 || strings.Count(text, sectionEnd) != 1 {
		return nil, errors.New("OpenAPI must contain exactly one generated app schema marker pair")
	}
	before, rest, _ := strings.Cut(text, sectionStart)
	_, after, found := strings.Cut(rest, sectionEnd)
	if !found {
		return nil, errors.New("OpenAPI generated app schema markers are out of order")
	}
	// Decode separately: rewriting OpenAPI references must not change the config schema.
	var source struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(schema, &source); err != nil {
		return nil, err
	}
	components := map[string]map[string]any{}
	name := func(key string) string { return "Config" + strings.TrimPrefix(key, "AgentConfig") }
	var visit func(any) error
	add := func(key string) error {
		if _, seen := components[name(key)]; seen {
			return nil
		}
		definition, exists := source.Defs[key]
		if !exists {
			return fmt.Errorf("missing config schema %q", key)
		}
		components[name(key)] = definition
		return visit(definition)
	}
	visit = func(value any) error {
		switch node := value.(type) {
		case map[string]any:
			if ref, ok := node["$ref"].(string); ok {
				key, found := strings.CutPrefix(ref, "#/$defs/")
				if !found {
					return fmt.Errorf("unexpected config schema reference %q", ref)
				}
				node["$ref"] = "#/components/schemas/" + name(key)
				if err := add(key); err != nil {
					return err
				}
			}
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, root := range []string{"AgentConfigAppCapabilitySource", "AgentConfigToolSource"} {
		if err := add(root); err != nil {
			return nil, err
		}
		definition := components[name(root)]
		definition["x-go-type"] = "agentconfig." + root
		definition["x-go-type-import"] = map[string]any{"path": "github.com/omnara-ai/omnara/internal/agentconfig"}
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(components); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	section := sectionStart + "    " + strings.ReplaceAll(strings.TrimSuffix(out.String(), "\n"), "\n", "\n    ") + "\n"
	return []byte(before + section + sectionEnd + after), nil
}
