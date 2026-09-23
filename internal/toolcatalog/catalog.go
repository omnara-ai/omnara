package toolcatalog

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

const (
	ArtifactPageBytes        = 4 * 1024
	MaxReadableArtifactBytes = 48 * 1024 * 1024
	ReadFileDefaultLines     = 100
	ReadFileMaxLines         = 200
	ReadFileDefaultChars     = 512
	ReadFileMaxChars         = 4096
	SearchDefaultMatches     = 20
	SearchMaxMatches         = 100
	SearchMaxContextLines    = 5
	SearchMaxPatternBytes    = 1024
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
	createMachineToolDescription  = "Request a pool-backed machine for this agent. First call list_machines (if available) to check for a suitable existing machine; use it if executable or wait for it if provisioning. machine_pool_name is only needed when multiple machine pools are available."
	deleteMachineToolDescription  = "Request deletion of a pool-backed machine."
	listMachinesToolDescription   = "List BYO and pool-backed machines currently associated with this agent, including machine_id values and current availability. Pass next_cursor as cursor to continue listing."
	inspectMachineToolDescription = "Inspect a BYO or pool-backed machine. machine_id is only needed when multiple machines are available."
	askQuestionToolDescription    = "Ask the human user one or more multiple-choice questions. " +
		"Omnara appends a text-capable Other choice to every question for free-form user responses."
	webSearchToolDescription = "Search the public web for up-to-date information beyond your training data. " +
		"Returns results with URLs, titles, and snippets; use web_fetch to read a result in full. " +
		"Prefer a few focused queries with varied phrasing over one broad query, and keep it to at most ~5 searches per task."
	webFetchToolDescription = "Fetch a public http(s) URL and return its readable content as markdown (read-only). " +
		"localhost and private or internal addresses are not reachable from this tool - use run_command " +
		"(e.g. curl) on the machine where the service runs instead."
	readFileToolDescription = "Read a text file in Omnara's virtual filesystem. " +
		"Currently supports /artifacts/<artifact_id>; no machine is required. " +
		"Reads lines by default; supply offset_char or limit_chars to read by character. " +
		"For large files, call again with the next position returned in the result."
	searchFilesToolDescription = "Search text inside files in Omnara's virtual filesystem using a regular expression. " +
		"Currently searches one /artifacts/<artifact_id> path per call; no machine is required. " +
		"Returns matching lines with line numbers and optional surrounding lines. " +
		"Use read_file to read more around a match."
	uploadFileToolDescription = "Copy a file from an attached machine into Omnara's virtual filesystem. " +
		"Currently supports creating artifacts at /artifacts. The file must be regular, non-empty, and at most 10 MiB. " +
		"Successful uploads return the created file's path."
	downloadFileToolDescription = "Copy a file from Omnara's virtual filesystem to an attached machine. " +
		"Currently supports /artifacts/<artifact_id> as path; provide destination. " +
		"If the download is still running after the initial wait, use the returned process_id with the process tools."
	toolSearchToolDescription = "Search the tools that are declared but not loaded into this conversation, " +
		"and load the matches so they can be called as soon as the search returns. " +
		"Deferred tools are not callable until a search returns them."
	toolSearchPatternDescription = "A Python-style regular expression matched case-insensitively against each " +
		"deferred tool's name, description, argument names, and argument descriptions. " +
		"Prefer broad patterns such as \"weather\" or \"get_.*_data\" over exact names."
)

type Catalog struct {
	tools map[string]Entry
}

type Entry struct {
	Name              string
	Description       string
	Implicit          bool
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
	machineID := map[string]any{
		"type":        "string",
		"description": "Public machine_id (mch_...) returned by list_machines. Omit it when the target is unambiguous.",
	}
	deleteMachineID := map[string]any{
		"type":        "string",
		"description": "Public machine_id (mch_...) of the pool-backed machine to delete. Use list_machines first if you need the ID.",
	}
	machinePoolName := map[string]any{
		"type":        "string",
		"description": "Pass machine_pool_name only when there are multiple machine pools; otherwise omit it.",
	}
	processID := map[string]any{
		"type":        "string",
		"description": "Opaque process_id returned by a process-backed tool or list_processes. Copy it exactly.",
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
			"machine_id": machineID,
			"shell":      shellSelector,
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
		map[string]any{
			"machine_pool_name": machinePoolName,
			"cpu":               map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32, "description": "vCPU count for this machine. Omit to inherit the pool's effective default; must be supported and within pool limits."},
			"memory_mb":         map[string]any{"type": "integer", "minimum": 1, "maximum": math.MaxInt32, "description": "Memory in MB for this machine. Omit to inherit the pool's effective default; must be supported and within pool limits."},
		},
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameDeleteMachine], err = toolEntry(
		ToolNameDeleteMachine,
		deleteMachineToolDescription,
		[]string{"machine_id"},
		map[string]any{"machine_id": deleteMachineID},
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameListMachines], err = toolEntry(
		ToolNameListMachines,
		listMachinesToolDescription,
		nil,
		map[string]any{"cursor": map[string]any{
			"type": "string", "pattern": `^mch_[a-z2-7]{26}$`,
			"description": "next_cursor from the previous result. Omit it to start listing.",
		}},
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameInspectMachine], err = toolEntry(
		ToolNameInspectMachine,
		inspectMachineToolDescription,
		nil,
		map[string]any{"machine_id": machineID},
	); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameAskQuestion], err = askQuestionTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameWebSearch], err = webSearchTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameWebFetch], err = webFetchTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameReadFile], err = readFileTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameSearchFiles], err = searchFilesTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameUploadFile], err = uploadFileTool(machineID); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameDownloadFile], err = downloadFileTool(machineID); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameSkill], err = skillTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameSpawnAgent], err = spawnAgentTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameReadAgent], err = readAgentTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameSendAgentMessage], err = sendAgentMessageTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameStopAgent], err = stopAgentTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameListAgents], err = listAgentsTool(); err != nil {
		return Catalog{}, err
	}
	if entries[ToolNameToolSearch], err = toolSearchTool(); err != nil {
		return Catalog{}, err
	}
	interactionTools, err := interactionHandlerTools()
	if err != nil {
		return Catalog{}, err
	}
	for _, entry := range interactionTools {
		entries[entry.Name] = entry
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
		Implicit:          implicit(name),
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

func toolSearchTool() (Entry, error) {
	entry, err := toolEntry(
		ToolNameToolSearch,
		toolSearchToolDescription,
		[]string{"pattern"},
		map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   ToolSearchMaxPatternLength,
				"description": toolSearchPatternDescription,
			},
			"max_results": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     ToolSearchMaxResults,
				"description": fmt.Sprintf("Maximum tools to return. Defaults to %d.", ToolSearchDefaultResults),
			},
		},
	)
	if err != nil {
		return Entry{}, err
	}
	entry.PermissionModes = toolpermission.AlwaysAllowModeDescriptors()
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

func readFileTool() (Entry, error) {
	return toolEntry(
		ToolNameReadFile,
		readFileToolDescription,
		[]string{"path"},
		map[string]any{
			"path": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Exact VFS path /artifacts/<artifact_id>.",
			},
			"offset_line": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"description": "Starting line number; defaults to 1 in line mode. " +
					"To continue, use next_offset_line from the previous result. " +
					"Cannot be combined with offset_char or limit_chars.",
			},
			"limit_lines": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     ReadFileMaxLines,
				"description": "Maximum number of lines to return. Defaults to 100 in line mode; each response contains at most 4 KiB of text.",
			},
			"offset_char": map[string]any{
				"type":    "integer",
				"minimum": 0,
				"description": "Starting character position, counting Unicode code points from 0; defaults to 0 in character mode. " +
					"To continue, use next_offset_char from the previous result. " +
					"Cannot be combined with offset_line or limit_lines.",
			},
			"limit_chars": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     ReadFileMaxChars,
				"description": "Maximum Unicode code points to return. Defaults to 512 in character mode; each response contains at most 4 KiB of text.",
			},
		},
	)
}

func searchFilesTool() (Entry, error) {
	return toolEntry(
		ToolNameSearchFiles,
		searchFilesToolDescription,
		[]string{"path", "pattern"},
		map[string]any{
			"path": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Exact VFS path /artifacts/<artifact_id>.",
			},
			"pattern": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   SearchMaxPatternBytes,
				"description": "RE2 regex, at most 1024 UTF-8 bytes. Case-sensitive by default; use (?i) for case-insensitive matching.",
			},
			"max_matches": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     SearchMaxMatches,
				"default":     SearchDefaultMatches,
				"description": "Maximum matching lines, not occurrences.",
			},
			"offset_line": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"default": 1,
				"description": "Line number to start searching from, starting at 1. " +
					"To continue, use next_offset_line from the previous result with the same pattern.",
			},
			"context_lines": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"maximum":     SearchMaxContextLines,
				"default":     0,
				"description": "Lines before and after each matching line.",
			},
		},
	)
}

func uploadFileTool(machineID map[string]any) (Entry, error) {
	return toolEntry(
		ToolNameUploadFile,
		uploadFileToolDescription,
		[]string{"path", "source"},
		map[string]any{
			"path": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Destination path in Omnara's virtual filesystem. Currently supports /artifacts, which creates a new artifact.",
				"enum":        []string{ArtifactVFSRoot},
			},
			"source": map[string]any{
				"type":      "string",
				"minLength": 1,
				"description": "Path to a regular file on the selected machine. " +
					"Relative paths use the machine working directory; ~ expands to the machine user's home directory.",
			},
			"machine_id": machineID,
		},
	)
}

func downloadFileTool(machineID map[string]any) (Entry, error) {
	return toolEntry(
		ToolNameDownloadFile,
		downloadFileToolDescription,
		[]string{"path", "destination"},
		map[string]any{
			"path": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Source path in Omnara's virtual filesystem. Currently supports /artifacts/<artifact_id>.",
				"pattern":     "^" + ArtifactVFSRoot + "/art_[a-z2-7]{26}$",
			},
			"destination": map[string]any{
				"type":      "string",
				"minLength": 1,
				"description": "Destination path on the selected machine. " +
					"The parent directory must exist; an existing destination is replaced atomically. " +
					"Relative paths use the machine working directory; ~ expands to the machine user's home directory.",
			},
			"machine_id": machineID,
		},
	)
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
