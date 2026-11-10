package modelcontext

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/programoutput"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// unknownProgramOutputSchema describes what commands get from an MCP tool
// whose server declares no output schema.
const unknownProgramOutputSchema = `{"description":"The server declares no output schema. Commands get the ` +
	`tool's structured result when it returns one, otherwise its text as a JSON string."}`

const programmaticToolCallsRunCommandDescription = "\n\nCommands can call this agent's other tools with " +
	"`\"$OMNARA_BIN\" call <tool> '<json input>'`; pass `-` as the input to read it from stdin. On success it " +
	"prints one JSON value to stdout: the tool's structured result, or its text as a JSON string when it has " +
	"none. `--output=text` prints a text result as plain text instead, and `--output=envelope` prints the full result " +
	"envelope. It exits 1 if the tool fails or is denied, 2 if the tool cannot be called from commands, and 3 " +
	"if the call could not be made, with the error on stderr. Results go only to the command, not to you, so " +
	"chain calls with pipes, jq, loops or scripts and print only what you need. This works only on machines " +
	"where programmatic tool calls are enabled."

const programmaticToolCallsTypesHeading = "\n\nWhat these tools print, as TypeScript types (their inputs are " +
	"the same as when you call them):\n```ts\n"

const programmaticToolCallsSearchDescription = "\n\nFind other tools, including ones not loaded here, with " +
	"their input and output schemas: `\"$OMNARA_BIN\" call tool_search '{\"pattern\":\"<regex>\"}'`."

const programmaticToolCallsGuidance = "Commands you start with `run_command` can call your other tools with " +
	"`\"$OMNARA_BIN\" call`; the `run_command` description explains what it prints and its exit codes. Use it " +
	"instead of calling tools one at a time when a task needs many calls, or results you would only filter, " +
	"join, count or reshape: results reach only the command and stay out of your context, so print only what " +
	"you need. Before relying on fields of a tool's result that `run_command` doesn't show, check its " +
	"`output_schema` with `tool_search`. Examples:\n```sh\n"

// programmaticParallelExample uses xargs because it owns the calls it starts:
// it runs at most 16 at once, returns only when all have finished, and exits
// nonzero if any failed. Background jobs started in a pipeline's loop would
// outlive a `wait` in the outer shell, and end with the command.
const programmaticParallelExample = "# Run independent calls in parallel; a command can have 16 calls in flight.\n" +
	"# xargs waits for every call and exits nonzero if one fails\n" +
	"\"$OMNARA_BIN\" call storage_list '{\"pattern\":\"/memory/notes/*.md\"}' | jq -r '.entries[]?.path' |\n" +
	"  tr '\\n' '\\0' | xargs -0 -n 1 -P 16 sh -c 'jq -nc --arg path \"$1\" \"{path: \\$path}\" |\n" +
	"    \"$OMNARA_BIN\" call storage_read - > \"$(basename \"$1\").json\"' sh\n"

// programmaticToolCallsExamples are shown in the system prompt when the agent
// may call every tool they use from commands.
var programmaticToolCallsExamples = []struct {
	tools   []string
	example string
}{
	{
		example: "# Find tools you can call, with what they take and print\n" +
			"\"$OMNARA_BIN\" call tool_search '{\"pattern\":\"issue|ticket\"}' | " +
			"jq -c '.tools[] | {name, input_schema, output_schema}'\n",
	},
	{
		tools: []string{toolcatalog.ToolNameStorageList},
		example: "# Keep only the fields you need\n" +
			"\"$OMNARA_BIN\" call storage_list '{\"pattern\":\"/memory/**\"}' | " +
			"jq -r '.entries[]? | select(.type == \"file\") | .path'\n",
	},
	{
		tools: []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch},
		example: "# Build inputs with jq so values are escaped, pass them on stdin, and chain calls\n" +
			"jq -n --arg q \"$TOPIC\" '{query: $q, num_results: 5}' | \"$OMNARA_BIN\" call web_search - |\n" +
			"  jq -c '.results[]? | {url}' |\n" +
			"  while read -r input; do printf '%s' \"$input\" | \"$OMNARA_BIN\" call web_fetch - | " +
			"jq -r '.title'; done\n",
	},
	{
		tools:   []string{toolcatalog.ToolNameStorageList, toolcatalog.ToolNameStorageRead},
		example: programmaticParallelExample,
	},
	{
		example: "# For longer logic, wrap calls in a function; a failed or denied call exits nonzero\n" +
			"python3 - <<'PY'\n" +
			"import json, os, subprocess\n\n" +
			"def call(tool, **tool_input):\n" +
			"    done = subprocess.run([os.environ[\"OMNARA_BIN\"], \"call\", tool, \"-\"],\n" +
			"                          input=json.dumps(tool_input), capture_output=True, text=True)\n" +
			"    if done.returncode != 0:\n" +
			"        raise RuntimeError(f\"{tool} exited {done.returncode}: {done.stderr.strip()}\")\n" +
			"    return json.loads(done.stdout)\n\n" +
			"tools = call(\"tool_search\", pattern=\".\")[\"tools\"]\n" +
			"print(len(tools), \"tools\")\n" +
			"PY\n",
	},
}

// ProgramCallable reports whether commands may call spec: the agent config
// enables programmatic tool calls without excluding it, and it is an MCP tool
// or a tool whose result type commands know.
func ProgramCallable(contract agentconfig.RuntimeContract, spec ToolSpec) bool {
	return !contract.ProgrammaticToolExcluded(spec.Name) && programResultKnown(spec)
}

func programResultKnown(spec ToolSpec) bool {
	switch spec.Type {
	case toolcatalog.ToolTypeMCP:
		return toolcatalog.IsMCPRuntimeToolName(spec.Name)
	case toolcatalog.ToolTypeBuiltIn:
		if _, operation, ok := toolcatalog.SplitIntegrationToolName(spec.Name); ok {
			return operation == toolcatalog.IntegrationOperationRead &&
				programoutput.HasIntegrationRead(spec.IntegrationKind)
		}
		return programoutput.HasBuiltIn(spec.Name)
	default:
		return false
	}
}

// ProgramOutputSchema returns the schema of the JSON value commands get from
// spec, or nil when commands cannot call it.
func ProgramOutputSchema(spec ToolSpec) (json.RawMessage, error) {
	if !programResultKnown(spec) {
		return nil, nil
	}
	if spec.Type == toolcatalog.ToolTypeMCP {
		if len(spec.OutputSchema) > 0 {
			return spec.OutputSchema, nil
		}
		return json.RawMessage(unknownProgramOutputSchema), nil
	}
	if _, _, ok := toolcatalog.SplitIntegrationToolName(spec.Name); ok {
		return programoutput.IntegrationRead(spec.IntegrationKind)
	}
	return programoutput.BuiltIn(spec.Name)
}

// ProgramCallableTools keeps the specs commands may call under contract,
// with the output schemas of their results. It does not modify specs.
func ProgramCallableTools(contract agentconfig.RuntimeContract, specs []ToolSpec) ([]ToolSpec, error) {
	callable := make([]ToolSpec, 0, len(specs))
	var err error
	for _, spec := range specs {
		if !ProgramCallable(contract, spec) {
			continue
		}
		if spec.OutputSchema, err = ProgramOutputSchema(spec); err != nil {
			return nil, err
		}
		callable = append(callable, spec)
	}
	return callable, nil
}

// programmaticToolCallsDescription is appended to run_command's description:
// how to call tools and the result types of the built-ins the agent may call
// from commands. Integration and MCP tools are left to tool_search, which
// returns their schemas. Examples are in the system prompt.
func programmaticToolCallsDescription(contract agentconfig.RuntimeContract, specs []ToolSpec) (string, error) {
	var types strings.Builder
	for _, spec := range specs {
		if spec.Type != toolcatalog.ToolTypeBuiltIn || !ProgramCallable(contract, spec) {
			continue
		}
		if _, _, ok := toolcatalog.SplitIntegrationToolName(spec.Name); ok {
			continue
		}
		schema, err := programoutput.BuiltIn(spec.Name)
		if err != nil {
			return "", err
		}
		rendered, err := renderSchemaTypeScript(schema)
		if err != nil {
			return "", err
		}
		types.WriteString(spec.Name + ": " + rendered + ";\n")
	}
	description := programmaticToolCallsRunCommandDescription
	if types.Len() > 0 {
		description += programmaticToolCallsTypesHeading + types.String() + "```"
	}
	return description + programmaticToolCallsSearchDescription, nil
}

// programmaticToolCallsSystemGuidance tells the model when to call tools
// from commands, with examples that use only tools it may call from them.
func programmaticToolCallsSystemGuidance(contract agentconfig.RuntimeContract, specs []ToolSpec) string {
	callable := map[string]bool{}
	for _, spec := range specs {
		callable[spec.Name] = ProgramCallable(contract, spec)
	}
	var guidance strings.Builder
	guidance.WriteString(programmaticToolCallsGuidance)
	for _, candidate := range programmaticToolCallsExamples {
		if !slices.ContainsFunc(candidate.tools, func(name string) bool { return !callable[name] }) {
			guidance.WriteString(candidate.example)
		}
	}
	guidance.WriteString("```")
	return guidance.String()
}

var typeScriptIdentifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// renderSchemaTypeScript renders a JSON schema as a compact TypeScript type
// that shows the model the shape of the JSON it will get. Descriptions and
// bounds are dropped, and shapes TypeScript cannot express become unknown.
func renderSchemaTypeScript(schema json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		return "", err
	}
	return schemaTypeScript(value), nil
}

func schemaTypeScript(value any) string {
	schema, ok := value.(map[string]any)
	if !ok {
		return "unknown"
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if alternatives, ok := schema[keyword].([]any); ok && len(alternatives) > 0 {
			rendered := make([]string, 0, len(alternatives))
			for _, alternative := range alternatives {
				rendered = append(rendered, schemaTypeScript(alternative))
			}
			return typeScriptUnion(rendered)
		}
	}
	if constant, ok := schema["const"]; ok {
		return typeScriptLiteral(constant)
	}
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		rendered := make([]string, 0, len(values))
		for _, value := range values {
			rendered = append(rendered, typeScriptLiteral(value))
		}
		return typeScriptUnion(rendered)
	}
	switch types := schema["type"].(type) {
	case string:
		return typedSchemaTypeScript(types, schema)
	case []any:
		rendered := make([]string, 0, len(types))
		for _, name := range types {
			if name, ok := name.(string); ok {
				rendered = append(rendered, typedSchemaTypeScript(name, schema))
			}
		}
		return typeScriptUnion(rendered)
	default:
		return "unknown"
	}
}

func typedSchemaTypeScript(name string, schema map[string]any) string {
	switch name {
	case "string", "boolean", "null":
		return name
	case "integer", "number":
		return "number"
	case "array":
		items, ok := schema["items"]
		if !ok {
			return "unknown[]"
		}
		return "Array<" + schemaTypeScript(items) + ">"
	case "object":
		return objectTypeScript(schema)
	default:
		return "unknown"
	}
}

func objectTypeScript(schema map[string]any) string {
	properties, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	if names, ok := schema["required"].([]any); ok {
		for _, name := range names {
			if name, ok := name.(string); ok {
				required[name] = true
			}
		}
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	fields := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		name := key
		if !typeScriptIdentifier.MatchString(key) {
			name = typeScriptLiteral(key)
		}
		if !required[key] {
			name += "?"
		}
		fields = append(fields, name+": "+schemaTypeScript(properties[key]))
	}
	switch additional := schema["additionalProperties"].(type) {
	case map[string]any:
		fields = append(fields, "[key: string]: "+schemaTypeScript(additional))
	case nil:
		if len(properties) == 0 {
			fields = append(fields, "[key: string]: unknown")
		}
	}
	if len(fields) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(fields, "; ") + " }"
}

// typeScriptUnion joins types with null last and duplicates removed.
func typeScriptUnion(types []string) string {
	var out []string
	hasNull := false
	for _, rendered := range types {
		switch {
		case rendered == "null":
			hasNull = true
		case !slices.Contains(out, rendered):
			out = append(out, rendered)
		}
	}
	if hasNull {
		out = append(out, "null")
	}
	if len(out) == 0 {
		return "unknown"
	}
	return strings.Join(out, " | ")
}

func typeScriptLiteral(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "unknown"
	}
	return string(encoded)
}
