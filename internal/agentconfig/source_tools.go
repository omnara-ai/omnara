package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"
)

func addSourceTools(
	format SourceFormat, raw []byte, root *yaml.Node, names []string,
) ([]byte, error) {
	jsonSource, _, err := sourceJSON(format, raw)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	expected, err := editJSONObject(jsonSource, "tools", func(tools json.RawMessage) ([]byte, error) {
		if len(tools) == 0 {
			tools = json.RawMessage(`{}`)
		}
		for _, name := range names {
			tools, err = editJSONObject(tools, name, func(_ json.RawMessage) ([]byte, error) {
				return []byte(`{}`), nil
			})
			if err != nil {
				return nil, err
			}
		}
		return tools, nil
	})
	if err != nil || format == SourceFormatJSON {
		return expected, err
	}
	object := root.Content[0]
	tools, _ := yamlChild(object, "tools")
	if tools == nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(expected, &fields); err != nil {
			return nil, err
		}
		var node yaml.Node
		if err := yaml.Unmarshal(fields["tools"], &node); err != nil {
			return nil, err
		}
		object.Content = append(object.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tools"}, node.Content[0],
		)
	} else {
		tools = resolveYAMLAlias(tools)
		for _, name := range names {
			tools.Content = append(tools.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
				&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle},
			)
		}
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, err
	}
	candidate, _, err := sourceJSON(SourceFormatYAML, output.Bytes())
	if err == nil && sameSourceJSON(candidate, expected) {
		return output.Bytes(), nil
	}
	var fallback bytes.Buffer
	if err := json.Indent(&fallback, expected, "", "  "); err != nil {
		return nil, err
	}
	return fallback.Bytes(), nil
}

func editJSONObject(raw []byte, key string, edit func(json.RawMessage) ([]byte, error)) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	count := 0
	start, end := -1, -1
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		count++
		if token == key {
			end = int(decoder.InputOffset())
			start = end - len(value)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if start >= 0 {
		replacement, err := edit(raw[start:end])
		if err != nil {
			return nil, err
		}
		out := append([]byte(nil), raw[:start]...)
		out = append(out, replacement...)
		return append(out, raw[end:]...), nil
	}
	value, err := edit(nil)
	if err != nil {
		return nil, err
	}
	end = int(decoder.InputOffset()) - 1
	out := append([]byte(nil), raw[:end]...)
	if count > 0 {
		out = append(out, ',')
	}
	encodedKey, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	out = append(out, encodedKey...)
	out = append(out, ':')
	out = append(out, value...)
	return append(out, raw[end:]...), nil
}

func sameSourceJSON(a, b []byte) bool {
	var left, right any
	decode := func(raw []byte, target *any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(target)
	}
	return decode(a, &left) == nil && decode(b, &right) == nil && reflect.DeepEqual(left, right)
}
