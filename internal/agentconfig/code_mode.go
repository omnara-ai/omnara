package agentconfig

import "errors"

// CodeModeSource turns on calling tools from commands with `omnarad call`.
type CodeModeSource struct {
	Enabled        *bool `json:"enabled"`
	DefaultAllowed *bool `json:"default_allowed,omitempty"`
}

// CodeModeCompiled is present only when code mode is on. DefaultAllowed is set only when false.
type CodeModeCompiled struct {
	Enabled        bool  `json:"enabled"`
	DefaultAllowed *bool `json:"default_allowed,omitempty"`
}

func compileCodeMode(source *CodeModeSource, compiled *Compiled) error {
	if source == nil {
		return nil
	}
	if source.Enabled == nil {
		return issuef(jsonPointer("code_mode", "enabled"), "is required")
	}
	if !*source.Enabled {
		return nil
	}
	compiled.CodeMode = &CodeModeCompiled{Enabled: true}
	if source.DefaultAllowed != nil && !*source.DefaultAllowed {
		compiled.CodeMode.DefaultAllowed = new(false)
	}
	return nil
}

func validateCompiledCodeMode(compiled *CodeModeCompiled) error {
	if compiled == nil {
		return nil
	}
	if !compiled.Enabled {
		return errors.New("compiled code mode must be enabled")
	}
	if compiled.DefaultAllowed != nil && *compiled.DefaultAllowed {
		return errors.New("compiled code mode default_allowed must be false or absent")
	}
	return nil
}

// allows reports whether commands can call a tool that supports code mode. override is the
// tool's own setting; nil falls back to the agent default.
func (compiled *CodeModeCompiled) allows(override *bool) bool {
	if compiled == nil || !compiled.Enabled {
		return false
	}
	if override != nil {
		return *override
	}
	return compiled.DefaultAllowed == nil || *compiled.DefaultAllowed
}

// CodeModeEnabled reports whether commands the agent runs can call its tools.
func (contract RuntimeContract) CodeModeEnabled() bool {
	return contract.CodeMode != nil && contract.CodeMode.Enabled
}

// CodeModeAllows reports whether commands can call a tool that supports code mode, given the
// tool's own setting.
func (contract RuntimeContract) CodeModeAllows(override *bool) bool {
	return contract.CodeMode.allows(override)
}
