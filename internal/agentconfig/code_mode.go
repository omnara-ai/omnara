package agentconfig

import "errors"

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

// A nil override falls back to the agent default.
func (compiled *CodeModeCompiled) allows(override *bool) bool {
	if compiled == nil || !compiled.Enabled {
		return false
	}
	if override != nil {
		return *override
	}
	return compiled.DefaultAllowed == nil || *compiled.DefaultAllowed
}

func (contract RuntimeContract) CodeModeEnabled() bool {
	return contract.CodeMode != nil && contract.CodeMode.Enabled
}

func (contract RuntimeContract) CodeModeAllows(override *bool) bool {
	return contract.CodeMode.allows(override)
}
