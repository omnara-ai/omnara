package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func AddSourceTools(
	format SourceFormat, raw []byte, names []string,
) ([]byte, error) {
	jsonSource, root, err := sourceJSON(format, raw)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	expected, err := editJSONObject(jsonSource, "tools", func(tools json.RawMessage) ([]byte, error) {
		if len(tools) == 0 {
			tools = json.RawMessage(`{}`)
		}
		for _, name := range names {
			tools, err = editJSONObject(tools, name, func(existing json.RawMessage) ([]byte, error) {
				if len(existing) > 0 {
					return nil, fmt.Errorf("tool %q is already configured", name)
				}
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
	fields := make([]string, 0, len(names))
	for _, name := range names {
		fields = append(fields, name+": {}")
	}
	if tools == nil {
		tools = object
		if _, merge := yamlChild(object, "<<"); merge == nil {
			fields = []string{"tools: {" + strings.Join(fields, ", ") + "}"}
		} else {
			var updated map[string]json.RawMessage
			if err := json.Unmarshal(expected, &updated); err != nil {
				return nil, err
			}
			fields = []string{"tools: " + string(updated["tools"])}
		}
	}
	output := insertYAMLFields(raw, tools, fields)
	candidate, _, err := sourceJSON(SourceFormatYAML, output)
	if err == nil && sameSourceJSON(candidate, expected) {
		return output, nil
	}
	var fallback bytes.Buffer
	if err := json.Indent(&fallback, expected, "", "  "); err != nil {
		return nil, err
	}
	return fallback.Bytes(), nil
}

func insertYAMLFields(raw []byte, node *yaml.Node, fields []string) []byte {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	position := node
	flow := node.Style&yaml.FlowStyle != 0
	if !flow {
		if len(node.Content) == 0 {
			return nil
		}
		position = node.Content[0]
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if position.Line < 1 || position.Line > len(lines) {
		return nil
	}
	line := lines[position.Line-1]
	runes := []rune(line)
	if position.Column < 1 || position.Column > len(runes) {
		return nil
	}
	prefix := string(runes[:position.Column-1])
	offset := len(strings.Join(lines[:position.Line-1], ""))
	var insertion string
	if flow {
		offset += len(prefix)
		if raw[offset] != '{' {
			return nil
		}
		offset++
		insertion = strings.Join(fields, ", ")
		if len(node.Content) > 0 {
			insertion += ", "
		}
	} else {
		if strings.TrimSpace(prefix) != "" {
			return nil
		}
		newline := "\n"
		if strings.HasSuffix(line, "\r\n") {
			newline = "\r\n"
		}
		insertion = prefix + strings.Join(fields, newline+prefix) + newline
	}
	output := append([]byte(nil), raw[:offset]...)
	output = append(output, insertion...)
	return append(output, raw[offset:]...)
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
