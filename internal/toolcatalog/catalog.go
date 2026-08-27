package toolcatalog

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const (
	ToolTypeBuiltIn       = "built_in"
	ToolTypeCustom        = "custom"
	ToolTypeMCP           = "mcp"
	ToolInputSchemaObject = "object"
)

func IsPlatformManagedToolType(toolType string) bool {
	return toolType == ToolTypeBuiltIn || toolType == ToolTypeMCP
}

const (
	runCommandToolDescription = "Run a daemon-backed shell command. Once the machine accepts the command, " +
		"every result includes a process_id; quick commands also return terminal output. " +
		"Use the process tools with that ID to inspect or control the process."
	writeProcessToolDescription   = "Write exact stdin/PTY bytes to a running process, close pipe stdin, or do both."
	stopProcessToolDescription    = "Interrupt or terminate a running process."
	readProcessToolDescription    = "Read retained process output, optionally waiting briefly for output or completion."
	listProcessesToolDescription  = "List active processes in the current agent, including process_id values."
	createMachineToolDescription  = "Request a pool-backed machine for this agent. First call list_machines to check for a suitable existing machine; use it if executable or wait for it if provisioning. machine_pool_name is only needed when multiple machine pools are available."
	deleteMachineToolDescription  = "Request deletion of a pool-backed machine."
	listMachinesToolDescription   = "List BYO and pool-backed machines currently associated with this agent, including machine_ref values and current availability."
	inspectMachineToolDescription = "Inspect a BYO or pool-backed machine. machine_ref is only needed when multiple machines are available."
	askQuestionToolDescription    = "Ask the human user one or more multiple-choice questions. " +
		"Omnara appends a text-capable Other choice to every question for free-form user responses."
	sendIntegrationMessageToolDescription = "Send a user-visible message to the current integration target. " +
		"When responding to a message received from an integration such as Slack, you must use this tool " +
		"for every user-visible response, including progress updates, questions, and final answers. " +
		"Normal assistant text is internal and is not delivered to the external user, so use this tool " +
		"to communicate with them."
	setIntegrationTargetToolDescription = "Set which integration target future integration messages " +
		"and prompts use by default."
	webSearchToolDescription = "Search the public web for up-to-date information beyond your training data. " +
		"Returns results with URLs, titles, and snippets; use web_fetch to read a result in full. " +
		"Prefer a few focused queries with varied phrasing over one broad query, and keep it to at most ~5 searches per task."
	webFetchToolDescription = "Fetch a public http(s) URL and return its readable content as markdown (read-only). " +
		"localhost and private or internal addresses are not reachable from this tool - use run_command " +
		"(e.g. curl) on the machine where the service runs instead."
)

type Catalog struct {
	tools map[string]Entry
}

type Entry struct {
	Name              string
	Description       string
	DefaultPermission toolpermission.Selection
	PermissionModes   []toolpermission.ModeDescriptor
	InputSchema       json.RawMessage
}

var defaultCatalog = sync.OnceValues(buildDefaultCatalog)

func Default() (Catalog, error) {
	return defaultCatalog()
}

func buildDefaultCatalog() (Catalog, error) {
	shellSelector := map[string]any{
		"type":        "string",
		"enum":        []string{"default", "sh", "bash", "zsh", "pwsh", "powershell", "cmd"},
		"description": "Shell family to use. Omit unless a specific shell is required.",
	}
	machineRef := map[string]any{
		"type":        "string",
		"description": "Exact machine_ref returned by list_machines. Omit it when the target is unambiguous.",
	}
	deleteMachineRef := map[string]any{
		"type":        "string",
		"description": "Exact machine_ref of the pool-backed machine to delete. Use list_machines first if you need the ref.",
	}
	machinePoolName := map[string]any{
		"type":        "string",
		"description": "Pass machine_pool_name only when there are multiple machine pools; otherwise omit it.",
	}
	processID := map[string]any{
		"type":        "string",
		"description": "Opaque process_id returned by run_command or list_processes. Copy it exactly.",
	}
	entries := map[string]Entry{}
	var err error
	entries[ToolNameRunCommand], err = toolEntry(
		ToolNameRunCommand,
		runCommandToolDescription,
		[]string{"command"},
		map[string]any{
			"command": map[string]any{
				"type": "string",
				"description": "Shell command to run. Once execution is granted, the result includes a process_id " +
					"that can be used with the process tools, even if the command has already ended.",
			},
			"machine_ref": machineRef,
			"shell":       shellSelector,
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory. Omit to use the assigned machine default; do not guess /.",
			},
			"wait_ms": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"maximum": processaction.MaxWaitMilliseconds,
				"description": "Maximum time to wait for initial output or command exit before yielding. " +
					"This is not a lifetime timeout.",
			},
			"io_mode": map[string]any{
				"type": "string",
				"enum": []string{"pipe", "pty"},
				"description": "Process I/O transport. Omit for pipe. " +
					"Use pty for interactive terminal programs or persistent shells.",
			},
		},
	)
	if err != nil {
		return Catalog{}, err
	}
	actionSpecs := []struct {
		name        string
		description string
		required    []string
		properties  map[string]any
	}{
		{
			ToolNameWriteProcess,
			writeProcessToolDescription,
			[]string{"process_id"},
			map[string]any{
				"process_id": processID,
				"data": map[string]any{
					"type": "string",
					"description": "Exact stdin/PTY bytes to write. " +
						"Include newlines explicitly when the running command expects Enter.",
				},
				"close_stdin": map[string]any{
					"type":        "boolean",
					"description": "Close pipe stdin after writing data. Not supported for PTY processes.",
				},
			},
		},
		{
			ToolNameStopProcess,
			stopProcessToolDescription,
			[]string{"process_id", "mode"},
			map[string]any{
				"process_id": processID,
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"interrupt", "terminate"},
					"description": "interrupt requests a graceful Ctrl-C-style stop; terminate forcibly ends the process tree.",
				},
			},
		},
	}
	for _, spec := range actionSpecs {
		entries[spec.name], err = toolEntry(spec.name, spec.description, spec.required, spec.properties)
		if err != nil {
			return Catalog{}, err
		}
	}
	observationProperties := map[string]any{
		"process_id": processID,
		"cursor": map[string]any{
			"type":        "integer",
			"minimum":     0,
			"description": "Output cursor returned by a previous run_command or read_process result.",
		},
		"max_bytes": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     processaction.MaxObservationBytes,
			"description": "Maximum output bytes to return.",
		},
		"wait_ms": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     processaction.MaxWaitMilliseconds,
			"description": "When no output is immediately available, wait up to this long for output or process completion.",
		},
	}
	if entries[ToolNameReadProcess], err = toolEntry(
		ToolNameReadProcess,
		readProcessToolDescription,
		[]string{"process_id"},
		observationProperties,
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameListProcesses], err = toolEntry(
		ToolNameListProcesses,
		listProcessesToolDescription,
		nil,
		nil,
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameCreateMachine], err = toolEntry(
		ToolNameCreateMachine,
		createMachineToolDescription,
		nil,
		map[string]any{"machine_pool_name": machinePoolName},
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameDeleteMachine], err = toolEntry(
		ToolNameDeleteMachine,
		deleteMachineToolDescription,
		[]string{"machine_ref"},
		map[string]any{"machine_ref": deleteMachineRef},
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameListMachines], err = toolEntry(
		ToolNameListMachines,
		listMachinesToolDescription,
		nil,
		nil,
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameInspectMachine], err = toolEntry(
		ToolNameInspectMachine,
		inspectMachineToolDescription,
		nil,
		map[string]any{"machine_ref": machineRef},
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameAskQuestion], err = askQuestionTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameSendIntegrationMessage], err = integrationSendTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameSetIntegrationTarget], err = integrationSetTargetTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameWebSearch], err = webSearchTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameWebFetch], err = webFetchTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameSkill], err = skillTool(); err != nil {
		return Catalog{}, err
	}
	for _, entry := range entries {
		if err := entry.validate(); err != nil {
			return Catalog{}, err
		}
	}
	return Catalog{tools: entries}, nil
}

func toolEntry(
	name string,
	description string,
	required []string,
	properties map[string]any,
) (Entry, error) {
	schema, err := json.Marshal(objectSchema(properties, required))
	if err != nil {
		return Entry{}, fmt.Errorf("marshal %s tool schema: %w", name, err)
	}
	return Entry{
		Name:              name,
		Description:       description,
		DefaultPermission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
		PermissionModes:   toolpermission.CommonModeDescriptors(),
		InputSchema:       schema,
	}, nil
}

func askQuestionTool() (Entry, error) {
	entry, err := toolEntry(
		ToolNameAskQuestion,
		askQuestionToolDescription,
		[]string{"questions"},
		map[string]any{
			"questions": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type":                 "object",
					"required":             []string{"prompt", "options"},
					"additionalProperties": false,
					"properties": map[string]any{
						"prompt": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Question text shown to the user.",
						},
						"multiple": map[string]any{
							"type":        "boolean",
							"default":     false,
							"description": "Whether the user may select more than one choice.",
						},
						"options": map[string]any{
							"type":        "array",
							"minItems":    1,
							"description": "Choices shown to the user before Omnara appends a text-capable Other choice.",
							"items": map[string]any{
								"type":                 "object",
								"required":             []string{"label"},
								"additionalProperties": false,
								"properties": map[string]any{
									"label": map[string]any{
										"type":        "string",
										"minLength":   1,
										"description": "Option text shown to the user.",
									},
								},
							},
						},
					},
				},
				"description": "Questions and choices for the human to answer together.",
			},
		},
	)
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func integrationSendTool() (Entry, error) {
	entry, err := toolEntry(
		ToolNameSendIntegrationMessage,
		sendIntegrationMessageToolDescription,
		[]string{"text"},
		map[string]any{
			"text": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "User-visible message text to send to the current integration target.",
			},
		},
	)
	if err != nil {
		return Entry{}, err
	}
	entry.PermissionModes = toolpermission.AlwaysAllowModeDescriptors()
	return entry, nil
}

func integrationSetTargetTool() (Entry, error) {
	entry, err := toolEntry(
		ToolNameSetIntegrationTarget,
		setIntegrationTargetToolDescription,
		[]string{"target_ref"},
		map[string]any{
			"target_ref": map[string]any{
				"type":      "string",
				"minLength": 1,
				"description": "Short target_ref of the attached integration target to make current for future " +
					"integration messages and prompts. Choose one of the integration_targets listed in context.",
			},
		},
	)
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func webSearchTool() (Entry, error) {
	entry, err := toolEntry(
		ToolNameWebSearch,
		webSearchToolDescription,
		[]string{"query"},
		map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query.",
			},
			"num_results": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     20,
				"description": "Number of results to return. Defaults to 5.",
			},
			"recency": map[string]any{
				"type":        "string",
				"enum":        []string{"day", "week", "month", "year"},
				"description": "Restrict results to a recent time window.",
			},
			"domains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Limit results to these domains. Prefix a domain with '-' to exclude it.",
			},
		},
	)
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func skillTool() (Entry, error) {
	return toolEntry(
		ToolNameSkill,
		"Return an attached skill's SKILL.md instructions and, when machines are attached, install its supporting files. Pass `name` to select an entry from the available_skills catalog.",
		[]string{"name"},
		map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Name of an attached skill from the available_skills catalog. Returns its instructions whether or not the agent has an attached machine.",
			},
		},
	)
}

func webFetchTool() (Entry, error) {
	entry, err := toolEntry(
		ToolNameWebFetch,
		webFetchToolDescription,
		[]string{"url"},
		map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute http(s) URL of a public web page.",
			},
			"format": map[string]any{
				"type":        "string",
				"enum":        []string{"markdown", "text"},
				"description": "Output format. Defaults to markdown.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     120,
				"description": "Maximum fetch time in seconds. Defaults to 30.",
			},
		},
	)
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (r Catalog) Lookup(name string) (Entry, bool) {
	entry, ok := r.tools[name]
	return entry, ok
}

func (r Catalog) Entries() []Entry {
	entries := make([]Entry, 0, len(r.tools))
	for _, entry := range r.tools {
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(left, right Entry) int {
		return cmp.Compare(left.Name, right.Name)
	})
	return entries
}

func (entry Entry) validate() error {
	if entry.Name == "" {
		return errors.New("tool catalog entry name is required")
	}
	if !toolNamePattern.MatchString(entry.Name) {
		return fmt.Errorf("tool catalog entry name %q must match %s", entry.Name, ToolNamePattern)
	}
	if UsesMCPRuntimeNamespace(entry.Name) {
		return fmt.Errorf("built-in tool %q uses the reserved MCP tool namespace", entry.Name)
	}
	if entry.Description == "" {
		return fmt.Errorf("tool %q description is required", entry.Name)
	}
	if err := toolpermission.ValidateModeDescriptors(entry.PermissionModes); err != nil {
		return fmt.Errorf("tool %q permission modes: %w", entry.Name, err)
	}
	if _, err := toolpermission.ValidateSelection(
		entry.DefaultPermission,
		entry.PermissionModes,
	); err != nil {
		return fmt.Errorf("tool %q default permission: %w", entry.Name, err)
	}
	if len(entry.InputSchema) == 0 {
		return fmt.Errorf("tool %q input schema is required", entry.Name)
	}
	if err := jsonschema.ValidateSchema(entry.InputSchema); err != nil {
		return fmt.Errorf("tool %q input schema: %w", entry.Name, err)
	}
	return nil
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 ToolInputSchemaObject,
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
