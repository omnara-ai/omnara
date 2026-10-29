package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
)

var claudeRejectedCompositions = []string{"allOf", "anyOf", "oneOf"}

var alternativePhrases = map[string]string{
	"anyOf": "at least one of",
	"oneOf": "exactly one of",
}

func ClaudeCompatibleToolSpecs(specs []modelcontext.ToolSpec) []modelcontext.ToolSpec {
	var out []modelcontext.ToolSpec
	for index, spec := range specs {
		schema, note, changed := withoutTopLevelCompositions(spec.InputSchema)
		if !changed {
			if out != nil {
				out = append(out, spec)
			}
			continue
		}
		if out == nil {
			out = append(make([]modelcontext.ToolSpec, 0, len(specs)), specs[:index]...)
		}
		spec.InputSchema = schema
		if note != "" {
			spec.Description = strings.TrimSpace(spec.Description + " " + note)
		}
		out = append(out, spec)
	}
	if out == nil {
		return specs
	}
	return out
}

func withoutTopLevelCompositions(schema json.RawMessage) (json.RawMessage, string, bool) {
	if !bytes.HasPrefix(bytes.TrimSpace(schema), []byte("{")) {
		return schema, "", false
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return schema, "", false
	}
	if !slices.ContainsFunc(claudeRejectedCompositions, func(keyword string) bool {
		_, found := decoded[keyword]
		return found
	}) {
		return schema, "", false
	}
	flattener := schemaFlattener{definitions: localDefinitions(decoded)}
	flat, err := flattener.flatten(decoded)
	for _, keyword := range claudeRejectedCompositions {
		delete(decoded, keyword)
	}
	if err != nil {
		if flat, err = flattener.flatten(decoded); err != nil {
			return schema, "", false
		}
	}
	if _, typed := decoded["type"]; !typed {
		decoded["type"] = json.RawMessage(`"object"`)
	}
	if len(flat.properties) > 0 {
		properties, err := json.Marshal(flat.properties)
		if err != nil {
			return schema, "", false
		}
		decoded["properties"] = properties
	}
	if len(flat.required) > 0 {
		required, err := json.Marshal(flat.required)
		if err != nil {
			return schema, "", false
		}
		decoded["required"] = required
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return schema, "", false
	}
	return encoded, strings.Join(flat.notes, " "), true
}

type flatObject struct {
	properties map[string]json.RawMessage
	required   []string
	notes      []string
}

type schemaFlattener struct {
	definitions map[string]json.RawMessage
}

func localDefinitions(schema map[string]json.RawMessage) map[string]json.RawMessage {
	definitions := map[string]json.RawMessage{}
	for _, keyword := range []string{"definitions", "$defs"} {
		var decoded map[string]json.RawMessage
		if json.Unmarshal(schema[keyword], &decoded) != nil {
			continue
		}
		for name, definition := range decoded {
			definitions["#/"+keyword+"/"+name] = definition
		}
	}
	return definitions
}

func (f schemaFlattener) flatten(schema map[string]json.RawMessage) (flatObject, error) {
	flat := flatObject{properties: map[string]json.RawMessage{}}
	if raw, ok := schema["properties"]; ok {
		if err := json.Unmarshal(raw, &flat.properties); err != nil {
			return flatObject{}, fmt.Errorf("decode properties: %w", err)
		}
	}
	if raw, ok := schema["required"]; ok {
		if err := json.Unmarshal(raw, &flat.required); err != nil {
			return flatObject{}, fmt.Errorf("decode required: %w", err)
		}
	}
	for _, keyword := range claudeRejectedCompositions {
		raw, ok := schema[keyword]
		if !ok {
			continue
		}
		branches, err := f.branches(raw)
		if err != nil {
			return flatObject{}, fmt.Errorf("%s: %w", keyword, err)
		}
		if keyword == "allOf" {
			for _, branch := range branches {
				flat.mergeAll(branch)
			}
			continue
		}
		flat.mergeAlternatives(branches, alternativePhrases[keyword])
	}
	slices.Sort(flat.required)
	flat.required = slices.Compact(flat.required)
	return flat, nil
}

func (f schemaFlattener) branches(raw json.RawMessage) ([]flatObject, error) {
	var branches []json.RawMessage
	if err := json.Unmarshal(raw, &branches); err != nil {
		return nil, err
	}
	out := make([]flatObject, 0, len(branches))
	for _, branch := range branches {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(branch, &decoded); err != nil {
			return nil, err
		}
		if ref, ok := decoded["$ref"]; ok {
			var name string
			if err := json.Unmarshal(ref, &name); err != nil {
				return nil, err
			}
			definition, found := f.definitions[name]
			if !found {
				return nil, fmt.Errorf("unresolved $ref %q", name)
			}
			decoded = nil
			if err := json.Unmarshal(definition, &decoded); err != nil {
				return nil, err
			}
		}
		flat, err := f.flatten(decoded)
		if err != nil {
			return nil, err
		}
		out = append(out, flat)
	}
	return out, nil
}

func (o *flatObject) mergeAll(branch flatObject) {
	for name, property := range branch.properties {
		existing, found := o.properties[name]
		switch {
		case !found:
			o.properties[name] = property
		case !jsonEqual(existing, property):
			o.properties[name] = combinedSchema("allOf", existing, property)
		}
	}
	o.required = append(o.required, branch.required...)
	o.notes = append(o.notes, branch.notes...)
}

func (o *flatObject) mergeAlternatives(branches []flatObject, phrase string) {
	alternatives := map[string][]json.RawMessage{}
	for _, branch := range branches {
		for name, property := range branch.properties {
			if _, declared := o.properties[name]; declared {
				continue
			}
			if !slices.ContainsFunc(alternatives[name], func(seen json.RawMessage) bool {
				return jsonEqual(seen, property)
			}) {
				alternatives[name] = append(alternatives[name], property)
			}
		}
	}
	for name, schemas := range alternatives {
		if len(schemas) == 1 {
			o.properties[name] = schemas[0]
			continue
		}
		o.properties[name] = combinedSchema("anyOf", schemas...)
	}
	shared := slices.Clone(branches[0].required)
	for _, branch := range branches[1:] {
		shared = slices.DeleteFunc(shared, func(name string) bool {
			return !slices.Contains(branch.required, name)
		})
	}
	o.required = append(o.required, shared...)
	options := make([]string, 0, len(branches))
	for _, branch := range branches {
		names := slices.DeleteFunc(slices.Clone(branch.required), func(name string) bool {
			return slices.Contains(shared, name)
		})
		if len(names) == 0 {
			names = slices.Sorted(maps.Keys(branch.properties))
		}
		if len(names) == 0 {
			options = nil
			break
		}
		options = append(options, strings.Join(names, " + "))
	}
	if len(options) > 1 {
		o.notes = append(o.notes, fmt.Sprintf("Provide %s: %s.", phrase, strings.Join(options, "; ")))
	}
	for _, branch := range branches {
		o.notes = append(o.notes, branch.notes...)
	}
}

func combinedSchema(keyword string, schemas ...json.RawMessage) json.RawMessage {
	encoded, err := json.Marshal(map[string][]json.RawMessage{keyword: schemas})
	if err != nil {
		return schemas[0]
	}
	return encoded
}

func jsonEqual(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return bytes.Equal(a, b)
	}
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftEncoded, rightEncoded)
}
