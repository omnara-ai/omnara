package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const schemaHeaderAnnotation = "x-mcp-header"

const maxSafeHeaderInteger = 9007199254740991

type ToolHeader struct {
	Name string
	Path []string
}

func ToolHeaders(tool *sdkmcp.Tool) ([]ToolHeader, error) {
	if tool == nil {
		return nil, errors.New("mcp: tool is required")
	}
	schema, err := schemaObject(tool.InputSchema)
	if err != nil {
		return nil, err
	}
	collector := &toolHeaderCollector{seen: map[string]struct{}{}}
	if err := collector.walk(schema, true, nil); err != nil {
		return nil, err
	}
	return collector.headers, nil
}

func schemaObject(schema any) (map[string]any, error) {
	if schema == nil {
		return map[string]any{}, nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode tool input schema: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return map[string]any{}, nil
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, fmt.Errorf("mcp: tool input schema must be a JSON object: %w", err)
	}
	if object == nil {
		object = map[string]any{}
	}
	return object, nil
}

type toolHeaderCollector struct {
	headers []ToolHeader
	seen    map[string]struct{}
}

func (c *toolHeaderCollector) walk(node any, reachable bool, path []string) error {
	switch value := node.(type) {
	case map[string]any:
		return c.walkObject(value, reachable, path)
	case []any:
		for _, item := range value {
			if err := c.walk(item, false, path); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func (c *toolHeaderCollector) walkObject(node map[string]any, reachable bool, path []string) error {
	if annotation, annotated := node[schemaHeaderAnnotation]; annotated {
		if !reachable {
			return fmt.Errorf(
				"mcp: %s annotation on %q is not statically reachable through properties",
				schemaHeaderAnnotation,
				strings.Join(path, "."),
			)
		}
		if err := c.add(annotation, node, path); err != nil {
			return err
		}
	}
	for key, child := range node {
		if key == "properties" {
			properties, ok := child.(map[string]any)
			if !ok {
				continue
			}
			for name, property := range properties {
				if err := c.walk(property, reachable, append(append([]string{}, path...), name)); err != nil {
					return err
				}
			}
			continue
		}
		if err := c.walk(child, false, path); err != nil {
			return err
		}
	}
	return nil
}

func (c *toolHeaderCollector) add(annotation any, node map[string]any, path []string) error {
	name, ok := annotation.(string)
	if !ok || name == "" {
		return fmt.Errorf("mcp: %s on %q must be a non-empty string", schemaHeaderAnnotation, strings.Join(path, "."))
	}
	if !isHeaderToken(name) {
		return fmt.Errorf(
			"mcp: %s %q on %q is not a valid header token",
			schemaHeaderAnnotation,
			name,
			strings.Join(path, "."),
		)
	}
	folded := strings.ToLower(name)
	if _, duplicate := c.seen[folded]; duplicate {
		return fmt.Errorf("mcp: %s %q is declared more than once", schemaHeaderAnnotation, name)
	}
	if len(path) == 0 {
		return fmt.Errorf("mcp: %s %q must annotate a property, not the schema root", schemaHeaderAnnotation, name)
	}
	kind, ok := node["type"].(string)
	if !ok {
		return fmt.Errorf(
			"mcp: %s %q on %q requires a single primitive type",
			schemaHeaderAnnotation,
			name,
			strings.Join(path, "."),
		)
	}
	switch kind {
	case "string", "integer", "boolean":
	default:
		return fmt.Errorf(
			"mcp: %s %q on %q has unsupported type %q",
			schemaHeaderAnnotation,
			name,
			strings.Join(path, "."),
			kind,
		)
	}
	c.seen[folded] = struct{}{}
	c.headers = append(c.headers, ToolHeader{Name: name, Path: append([]string{}, path...)})
	return nil
}

func isHeaderToken(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func toolHeaderValues(headers []ToolHeader, arguments json.RawMessage) (map[string]string, error) {
	out := map[string]string{}
	if len(headers) == 0 {
		return out, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var root any
	if len(bytes.TrimSpace(arguments)) != 0 {
		if err := decoder.Decode(&root); err != nil {
			return nil, fmt.Errorf("mcp: decode tool arguments: %w", err)
		}
	}
	for _, header := range headers {
		value, present := lookupPath(root, header.Path)
		if !present || value == nil {
			continue
		}
		encoded, err := headerValueString(value)
		if err != nil {
			return nil, fmt.Errorf("mcp: header %s from %q: %w", header.Name, strings.Join(header.Path, "."), err)
		}
		out[header.Name] = encoded
	}
	return out, nil
}

func lookupPath(root any, path []string) (any, bool) {
	current := root
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, found := object[segment]
		if !found {
			return nil, false
		}
		current = next
	}
	return current, true
}

func headerValueString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return "", fmt.Errorf("value %s is not an integer", typed.String())
		}
		if integer > maxSafeHeaderInteger || integer < -maxSafeHeaderInteger {
			return "", fmt.Errorf("value %d is outside the safe integer range", integer)
		}
		return strconv.FormatInt(integer, 10), nil
	case float64:
		if typed != math.Trunc(typed) || math.Abs(typed) > maxSafeHeaderInteger {
			return "", fmt.Errorf("value %v is not a safe integer", typed)
		}
		return strconv.FormatInt(int64(typed), 10), nil
	default:
		return "", fmt.Errorf("value of type %T cannot be mirrored into a header", value)
	}
}
