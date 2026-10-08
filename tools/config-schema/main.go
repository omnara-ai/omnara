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
)

// configSchemaPath is the agent config JSON Schema for the web editor. It is
// derived from the AgentConfigDefinition schema in api/openapi/openapi.yaml.
const configSchemaPath = "internal/agentconfig/generated/agent_config.schema.json"

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
	config = append(config, '\n')
	path := filepath.Join(root, configSchemaPath)
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bytes.Equal(current, config) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is stale; run make openapi-generate", configSchemaPath)
	}
	return os.WriteFile(path, config, 0o644)
}
