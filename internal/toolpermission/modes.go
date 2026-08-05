package toolpermission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/jsonschema"
)

const (
	ModeAlwaysAllow = "always_allow"
	ModeAlwaysAsk   = "always_ask"
	ModeAlwaysDeny  = "always_deny"
)

var emptyParametersSchema = json.RawMessage(
	`{"type":"object","properties":{},"additionalProperties":false}`,
)

type Selection struct {
	Mode       string          `json:"mode"`
	Parameters json.RawMessage `json:"parameters"`
}

type ModeDescriptor struct {
	Name             string          `json:"name"`
	Label            string          `json:"label"`
	Description      string          `json:"description"`
	ParametersSchema json.RawMessage `json:"parameters_schema"`
}

func CommonModeDescriptors() []ModeDescriptor {
	return []ModeDescriptor{
		{
			Name:             ModeAlwaysAllow,
			Label:            "Always allow",
			Description:      "Run this tool without asking for approval.",
			ParametersSchema: cloneJSON(emptyParametersSchema),
		},
		{
			Name:             ModeAlwaysAsk,
			Label:            "Always ask",
			Description:      "Ask for approval before each tool call.",
			ParametersSchema: cloneJSON(emptyParametersSchema),
		},
		{
			Name:             ModeAlwaysDeny,
			Label:            "Always deny",
			Description:      "Reject every call to this tool.",
			ParametersSchema: cloneJSON(emptyParametersSchema),
		},
	}
}

func AlwaysAllowModeDescriptors() []ModeDescriptor {
	return CommonModeDescriptors()[:1]
}

func DefaultSelection(mode string) Selection {
	return Selection{Mode: mode, Parameters: json.RawMessage(`{}`)}
}

func NormalizeSelection(selection Selection) (Selection, error) {
	selection.Mode = strings.TrimSpace(selection.Mode)
	if selection.Mode == "" {
		return Selection{}, errors.New("permission mode is required")
	}
	if len(bytes.TrimSpace(selection.Parameters)) == 0 {
		selection.Parameters = json.RawMessage(`{}`)
	}
	parameters, err := canonicalJSON(selection.Parameters)
	if err != nil {
		return Selection{}, fmt.Errorf("permission parameters must be valid JSON: %w", err)
	}
	selection.Parameters = parameters
	return selection, nil
}

func ValidateSelection(selection Selection, descriptors []ModeDescriptor) (Selection, error) {
	normalized, err := NormalizeSelection(selection)
	if err != nil {
		return Selection{}, err
	}
	descriptor, ok := FindMode(descriptors, normalized.Mode)
	if !ok {
		return Selection{}, fmt.Errorf("unsupported permission mode %q", normalized.Mode)
	}
	if err := jsonschema.Validate(descriptor.ParametersSchema, normalized.Parameters); err != nil {
		return Selection{}, fmt.Errorf(
			"permission mode %q parameters: %w",
			normalized.Mode,
			err,
		)
	}
	return normalized, nil
}

func ValidateModeDescriptors(descriptors []ModeDescriptor) error {
	if len(descriptors) == 0 {
		return errors.New("at least one permission mode is required")
	}
	seen := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Name == "" {
			return errors.New("permission mode name is required")
		}
		if _, duplicate := seen[descriptor.Name]; duplicate {
			return fmt.Errorf("permission mode %q is declared more than once", descriptor.Name)
		}
		seen[descriptor.Name] = struct{}{}
		if strings.TrimSpace(descriptor.Label) == "" {
			return fmt.Errorf("permission mode %q label is required", descriptor.Name)
		}
		if strings.TrimSpace(descriptor.Description) == "" {
			return fmt.Errorf("permission mode %q description is required", descriptor.Name)
		}
		if err := jsonschema.ValidateSchema(descriptor.ParametersSchema); err != nil {
			return fmt.Errorf(
				"permission mode %q parameters schema: %w",
				descriptor.Name,
				err,
			)
		}
	}
	return nil
}

func FindMode(descriptors []ModeDescriptor, name string) (ModeDescriptor, bool) {
	index := slices.IndexFunc(descriptors, func(descriptor ModeDescriptor) bool {
		return descriptor.Name == name
	})
	if index < 0 {
		return ModeDescriptor{}, false
	}
	return descriptors[index], true
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	return jsoncanonical.Normalize(raw)
}

func cloneJSON(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
