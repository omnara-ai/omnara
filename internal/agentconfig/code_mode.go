package agentconfig

import "errors"

// CodeModeSource turns on calling tools from commands with `omnarad call`.
type CodeModeSource struct {
	Enabled *bool `json:"enabled"`
}

// CodeModeCompiled is present only when code mode is on.
type CodeModeCompiled struct {
	Enabled bool `json:"enabled"`
}

func codeModeEnabled(source CodeModeSource) (bool, error) {
	if source.Enabled == nil {
		return false, issuef(jsonPointer("code_mode", "enabled"), "is required")
	}
	return *source.Enabled, nil
}

func validateCompiledCodeMode(compiled *CodeModeCompiled) error {
	if compiled != nil && !compiled.Enabled {
		return errors.New("compiled code mode must be enabled")
	}
	return nil
}

// CodeModeEnabled reports whether commands the agent runs can call its tools.
func (contract RuntimeContract) CodeModeEnabled() bool {
	return contract.CodeMode != nil && contract.CodeMode.Enabled
}
