package modelcontext

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type canonicalContentBlockType string

const (
	canonicalContentBlockText           canonicalContentBlockType = "text"
	canonicalContentBlockMediaRef       canonicalContentBlockType = "media_ref"
	canonicalContentBlockReasoning      canonicalContentBlockType = "reasoning"
	canonicalContentBlockError          canonicalContentBlockType = "error"
	canonicalContentBlockToolCall       canonicalContentBlockType = "tool_call"
	canonicalContentBlockStructuredData canonicalContentBlockType = "structured_data"
	canonicalContentBlockArtifactRef    canonicalContentBlockType = "artifact_ref"
)

func validateMessageContentBlocks(role modelprotocol.MessageRole, content json.RawMessage) error {
	if role != modelprotocol.RoleUser && role != modelprotocol.RoleAssistant {
		return fmt.Errorf("unsupported message role %q", role)
	}
	return validateContentBlockArray(content, func(index int, fields map[string]json.RawMessage) error {
		kind, err := requiredContentBlockString(fields, "type")
		if err != nil {
			return err
		}
		switch canonicalContentBlockType(kind) {
		case canonicalContentBlockText:
			return validateTextContentBlock(fields)
		case canonicalContentBlockMediaRef:
			return validateMediaRefContentBlock(fields)
		case canonicalContentBlockReasoning:
			if role != modelprotocol.RoleAssistant {
				return errors.New("reasoning is only valid on model outputs")
			}
			return validateReasoningContentBlock(fields)
		case canonicalContentBlockError:
			if role != modelprotocol.RoleAssistant {
				return errors.New("error is only valid on model outputs")
			}
			return validateTextContentBlock(fields)
		case canonicalContentBlockToolCall:
			if role != modelprotocol.RoleAssistant {
				return errors.New("tool_call is only valid on model outputs")
			}
			return validateToolCallContentBlock(fields)
		default:
			return fmt.Errorf("unsupported type %q", kind)
		}
	})
}

func validateToolResultContentBlocks(content json.RawMessage) error {
	return validateContentBlockArray(content, func(index int, fields map[string]json.RawMessage) error {
		kind, err := requiredContentBlockString(fields, "type")
		if err != nil {
			return err
		}
		switch canonicalContentBlockType(kind) {
		case canonicalContentBlockText:
			return validateTextContentBlock(fields)
		case canonicalContentBlockMediaRef:
			return validateMediaRefContentBlock(fields)
		case canonicalContentBlockStructuredData:
			return validateStructuredDataContentBlock(fields)
		case canonicalContentBlockArtifactRef:
			return validateArtifactRefContentBlock(fields)
		default:
			return fmt.Errorf("unsupported type %q", kind)
		}
	})
}

func validateArtifactRefContentBlock(fields map[string]json.RawMessage) error {
	if err := requireOnlyContentBlockFields(
		fields,
		"type",
		"artifact_id",
		"content_type",
		"size_bytes",
		"line_count",
	); err != nil {
		return err
	}
	for _, field := range []string{"artifact_id", "content_type"} {
		value, err := requiredContentBlockString(fields, field)
		if err != nil || value == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	for _, field := range []string{"size_bytes", "line_count"} {
		var value int64
		if len(fields[field]) == 0 || json.Unmarshal(fields[field], &value) != nil || value <= 0 {
			return fmt.Errorf("%s must be a positive integer", field)
		}
	}
	return nil
}

func validateContentBlockArray(
	content json.RawMessage,
	validate func(index int, fields map[string]json.RawMessage) error,
) error {
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil || blocks == nil {
		if err == nil {
			err = errors.New("must be a JSON array")
		}
		return err
	}
	for index, raw := range blocks {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return fmt.Errorf("content block %d must be a JSON object", index)
		}
		if err := validate(index, fields); err != nil {
			return fmt.Errorf("content block %d: %w", index, err)
		}
	}
	return nil
}

func validateTextContentBlock(fields map[string]json.RawMessage) error {
	if err := requireOnlyContentBlockFields(fields, "type", "text"); err != nil {
		return err
	}
	_, err := requiredContentBlockString(fields, "text")
	return err
}

func validateReasoningContentBlock(fields map[string]json.RawMessage) error {
	if err := requireOnlyContentBlockFields(fields, "type", "text"); err != nil {
		return err
	}
	_, err := requiredContentBlockString(fields, "text")
	return err
}

func validateMediaRefContentBlock(fields map[string]json.RawMessage) error {
	if err := requireOnlyContentBlockFields(fields, "type", "artifact_id"); err != nil {
		return err
	}
	artifactID, err := requiredContentBlockString(fields, "artifact_id")
	if err != nil {
		return err
	}
	if artifactID == "" {
		return errors.New("artifact_id is required")
	}
	return nil
}

func validateStructuredDataContentBlock(fields map[string]json.RawMessage) error {
	if err := requireOnlyContentBlockFields(fields, "type", "value"); err != nil {
		return err
	}
	if _, ok := fields["value"]; !ok {
		return errors.New("value is required")
	}
	return nil
}

func validateToolCallContentBlock(fields map[string]json.RawMessage) error {
	if err := requireOnlyContentBlockFields(fields, "type", "tool_call_id"); err != nil {
		return err
	}
	toolCallID, err := requiredContentBlockString(fields, "tool_call_id")
	if err != nil {
		return err
	}
	if toolCallID == "" {
		return errors.New("tool_call_id is required")
	}
	return nil
}

func requiredContentBlockString(
	fields map[string]json.RawMessage,
	name string,
) (string, error) {
	raw, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value, nil
}

func requireOnlyContentBlockFields(
	fields map[string]json.RawMessage,
	allowed ...string,
) error {
	for name := range fields {
		valid := false
		for _, allowedName := range allowed {
			if name == allowedName {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("unsupported field %q", name)
		}
	}
	return nil
}
