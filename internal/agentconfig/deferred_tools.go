package agentconfig

import "fmt"

const (
	DeferredToolsModeToolAdd  = "tool_add"
	DeferredToolsModeCodeMode = "code_mode"
)

type DeferredToolsSource struct {
	Mode string `json:"mode"`
}

type DeferredToolsCompiled struct {
	Mode string `json:"mode"`
}

func compileDeferredTools(source DeferredToolsSource) (DeferredToolsCompiled, error) {
	switch source.Mode {
	case DeferredToolsModeToolAdd, DeferredToolsModeCodeMode:
		return DeferredToolsCompiled(source), nil
	default:
		return DeferredToolsCompiled{}, issuef(
			jsonPointer("deferred_tools", "mode"),
			"must be %q or %q", DeferredToolsModeToolAdd, DeferredToolsModeCodeMode,
		)
	}
}

func validateCompiledDeferredTools(compiled *DeferredToolsCompiled) error {
	if compiled != nil && compiled.Mode != DeferredToolsModeCodeMode {
		return fmt.Errorf("compiled deferred tools mode must be %q", DeferredToolsModeCodeMode)
	}
	return nil
}

func (contract RuntimeContract) CodeMode() bool {
	return contract.DeferredTools != nil && contract.DeferredTools.Mode == DeferredToolsModeCodeMode
}
