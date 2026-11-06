package agentconfig

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const MaxProgrammaticToolCallExcludes = 256

var builtInToolNamePattern = regexp.MustCompile(toolcatalog.ToolNamePattern)

// ProgrammaticToolCallsSource lets commands started by run_command call the
// agent's other tools with `omnarad call`. Excluded names are not checked
// against the agent's tools because MCP tool lists are only known at runtime.
type ProgrammaticToolCallsSource struct {
	Enabled bool     `json:"enabled"`
	Exclude []string `json:"exclude,omitempty"`
}

type ProgrammaticToolCallsCompiled struct {
	Enabled bool     `json:"enabled"`
	Exclude []string `json:"exclude,omitempty"`
}

// compileProgrammaticToolCalls validates the section even when it is
// disabled. Callers store only an enabled result, so a disabled section leaves
// no trace in the compiled definition.
func compileProgrammaticToolCalls(source ProgrammaticToolCallsSource) (ProgrammaticToolCallsCompiled, error) {
	if len(source.Exclude) > MaxProgrammaticToolCallExcludes {
		return ProgrammaticToolCallsCompiled{}, issuef(
			jsonPointer("programmatic_tool_calls", "exclude"),
			"must list at most %d tools", MaxProgrammaticToolCallExcludes,
		)
	}
	seen := make(map[string]bool, len(source.Exclude))
	for i, name := range source.Exclude {
		pointer := jsonPointer("programmatic_tool_calls", "exclude", i)
		if err := validateProgrammaticToolName(name); err != nil {
			return ProgrammaticToolCallsCompiled{}, NewIssue(pointer, err)
		}
		if seen[name] {
			return ProgrammaticToolCallsCompiled{}, issuef(pointer, "duplicate tool %q", name)
		}
		seen[name] = true
	}
	compiled := ProgrammaticToolCallsCompiled{Enabled: source.Enabled}
	if source.Enabled && len(source.Exclude) > 0 {
		compiled.Exclude = slices.Sorted(slices.Values(source.Exclude))
	}
	return compiled, nil
}

func validateCompiledProgrammaticToolCalls(compiled *ProgrammaticToolCallsCompiled) error {
	if compiled == nil {
		return nil
	}
	if !compiled.Enabled {
		return errors.New("compiled programmatic tool calls must be enabled")
	}
	if len(compiled.Exclude) > MaxProgrammaticToolCallExcludes {
		return fmt.Errorf("compiled programmatic tool calls exclude more than %d tools", MaxProgrammaticToolCallExcludes)
	}
	for i, name := range compiled.Exclude {
		if err := validateProgrammaticToolName(name); err != nil {
			return fmt.Errorf("compiled programmatic tool call exclude: %w", err)
		}
		if i > 0 && compiled.Exclude[i-1] >= name {
			return errors.New("compiled programmatic tool call excludes must be sorted and unique")
		}
	}
	return nil
}

func validateProgrammaticToolName(name string) error {
	switch {
	case name == "":
		return errors.New("tool name is required")
	case len(name) > toolcatalog.MaxMCPRuntimeToolNameLength:
		return fmt.Errorf("tool name %q exceeds %d characters", name, toolcatalog.MaxMCPRuntimeToolNameLength)
	case toolcatalog.UsesMCPRuntimeNamespace(name):
		if !toolcatalog.IsMCPRuntimeToolName(name) {
			return fmt.Errorf("MCP tool name %q must have the form mcp__<server>__<tool>", name)
		}
	case toolcatalog.UsesIntegrationToolNamespace(name):
		if _, _, ok := toolcatalog.SplitIntegrationToolName(name); !ok {
			return fmt.Errorf("integration tool name %q must have the form int__<integration>__<operation>", name)
		}
	case !builtInToolNamePattern.MatchString(name):
		return fmt.Errorf("tool name %q must match %s", name, toolcatalog.ToolNamePattern)
	}
	return nil
}

// ProgrammaticToolCallsEnabled reports whether commands can call this agent's
// tools with `omnarad call`.
func (contract RuntimeContract) ProgrammaticToolCallsEnabled() bool {
	return contract.ProgrammaticToolCalls != nil && contract.ProgrammaticToolCalls.Enabled
}

// ProgrammaticToolExcluded reports whether commands cannot call the named
// tool: programmatic tool calls are disabled or the config excludes it.
func (contract RuntimeContract) ProgrammaticToolExcluded(name string) bool {
	if !contract.ProgrammaticToolCallsEnabled() {
		return true
	}
	_, excluded := slices.BinarySearch(contract.ProgrammaticToolCalls.Exclude, name)
	return excluded
}
