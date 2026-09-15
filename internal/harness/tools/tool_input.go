package tools

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

// Validate without rewriting arguments. Built-ins use their registered validator;
// MCP and custom tools use the schema from the reconstructed runtime tool set.
func validateToolInput(turn Turn, call model.ToolCall, implementation toolImplementation) error {
	if err := modelenvelope.ValidateToolInput(call.Input); err != nil {
		return fmt.Errorf("tool %q arguments: %w", call.Name, err)
	}
	if implementation.inputSchemaValidator == nil {
		spec, found := turn.Tools[call.Name]
		if !found {
			// Availability owns the error for tools missing from the reconstructed
			// set. Strict JSON validation above still applies to their arguments.
			return nil
		}
		if len(spec.InputSchema) == 0 {
			return fmt.Errorf("tool %q has no runtime input schema", call.Name)
		}
		if err := jsonschema.Validate(spec.InputSchema, call.Input); err != nil {
			return fmt.Errorf("tool %q input: %w", call.Name, err)
		}
	}
	return implementation.validateInput(call.Input)
}
