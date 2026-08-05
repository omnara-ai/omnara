package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	sjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaResource = "urn:omnara:inline-json-schema"

type Validator struct {
	schema *sjsonschema.Schema
}

func Compile(raw json.RawMessage) (*Validator, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("JSON schema is required")
	}
	schema, err := compile(raw)
	if err != nil {
		return nil, fmt.Errorf("compile JSON schema: %w", err)
	}
	return &Validator{schema: schema}, nil
}

func ValidateSchema(raw json.RawMessage) error {
	_, err := Compile(raw)
	return err
}

func Validate(schemaJSON, valueJSON json.RawMessage) error {
	validator, err := Compile(schemaJSON)
	if err != nil {
		return err
	}
	return validator.Validate(valueJSON)
}

func (v *Validator) Validate(valueJSON json.RawMessage) error {
	value, err := sjsonschema.UnmarshalJSON(bytes.NewReader(valueJSON))
	if err != nil {
		return fmt.Errorf("decode JSON value: %w", err)
	}
	return v.schema.Validate(value)
}

func compile(raw json.RawMessage) (*sjsonschema.Schema, error) {
	document, err := sjsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	compiler := sjsonschema.NewCompiler()
	compiler.DefaultDraft(sjsonschema.Draft2020)
	compiler.UseLoader(sjsonschema.SchemeURLLoader{})
	if err := compiler.AddResource(schemaResource, document); err != nil {
		return nil, err
	}
	return compiler.Compile(schemaResource)
}
