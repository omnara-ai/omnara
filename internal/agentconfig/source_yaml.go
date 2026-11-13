package agentconfig

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"gopkg.in/yaml.v3"
)

var yamlDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func decodeYAMLDocument(root *yaml.Node) (value any, err error) {
	// yaml.v3 can panic on collection keys inside merged mappings.
	defer func() {
		if recovered := recover(); recovered != nil {
			value, err = nil, errors.New("unsupported YAML mapping key")
		}
	}()
	err = root.Decode(&value)
	return value, err
}

func yamlJSONValue(root *yaml.Node) (any, error) {
	decoded, err := decodeYAMLDocument(root)
	if err != nil {
		return nil, yamlSyntaxIssue(err)
	}
	if err := validateYAMLKeys(decoded, ""); err != nil {
		return nil, err
	}
	active := make(map[*yaml.Node]bool)
	var decode func(*yaml.Node, string) (any, error)
	decode = func(node *yaml.Node, path string) (any, error) {
		if len(active) > 10000 {
			return nil, issuef(path, "YAML nests or aliases deeper than 10000 levels")
		}
		if active[node] {
			return nil, issuef(path, "YAML alias contains itself")
		}
		active[node] = true
		defer delete(active, node)
		switch node.Kind {
		case yaml.DocumentNode:
			if len(node.Content) == 0 {
				return nil, issuef(path, "YAML document must contain a value")
			}
			return decode(node.Content[0], path)
		case yaml.AliasNode:
			return decode(node.Alias, path)
		case yaml.SequenceNode:
			result := make([]any, len(node.Content))
			for i, child := range node.Content {
				value, err := decode(child, path+jsonPointer(i))
				if err != nil {
					return nil, err
				}
				result[i] = value
			}
			return result, nil
		case yaml.MappingNode:
			seen := make(map[string]bool)
			for i := 0; i < len(node.Content); i += 2 {
				key := node.Content[i]
				if key.ShortTag() == "!!merge" {
					continue
				}
				var name string
				if err := key.Decode(&name); err != nil {
					return nil, issueAt(path, err)
				}
				if seen[name] {
					return nil, issuef(path+jsonPointer(name), "duplicate mapping key")
				}
				seen[name] = true
			}
			var entries map[string]yaml.Node
			if err := node.Decode(&entries); err != nil {
				return nil, issueAt(path, err)
			}
			result := make(map[string]any, len(entries))
			for name, child := range entries {
				value, err := decode(&child, path+jsonPointer(name))
				if err != nil {
					return nil, err
				}
				result[name] = value
			}
			return result, nil
		case yaml.ScalarNode:
			value, err := exactYAMLScalar(node)
			if err != nil {
				return nil, issueAt(path, err)
			}
			return value, nil
		default:
			return nil, issuef(path, "unsupported YAML node")
		}
	}
	return decode(root, "")
}

func validateYAMLKeys(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if err := validateYAMLKeys(child, path+jsonPointer(key)); err != nil {
				return err
			}
		}
	case map[any]any:
		for key, child := range typed {
			name, ok := key.(string)
			if !ok {
				return issuef(path, "mapping key %v is not a string", key)
			}
			if err := validateYAMLKeys(child, path+jsonPointer(name)); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := validateYAMLKeys(child, path+jsonPointer(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func exactYAMLScalar(node *yaml.Node) (any, error) {
	var value any
	if err := node.Decode(&value); err != nil {
		return nil, err
	}
	float, isFloat := value.(float64)
	if !isFloat {
		if _, isString := value.(string); isString && node.Style == 0 && yamlOverflowedNumber(node.Value) {
			return nil, errors.New("number is outside the supported range; quote it to keep a string")
		}
		return value, nil
	}
	if math.IsNaN(float) || math.IsInf(float, 0) {
		return nil, errors.New("number must be finite")
	}
	var untagged any
	if err := (&yaml.Node{Kind: yaml.ScalarNode, Value: node.Value}).Decode(&untagged); err != nil {
		return nil, err
	}
	switch untagged.(type) {
	case int, int64, uint64:
		return untagged, nil
	case float64:
		text := strings.TrimPrefix(strings.ReplaceAll(node.Value, "_", ""), "+")
		return jsoncanonical.Number(text)
	}
	return value, nil
}

func yamlOverflowedNumber(text string) bool {
	if text == "" {
		return false
	}
	switch first := text[0]; {
	case first == '.':
	case first == '+' || first == '-' || (first >= '0' && first <= '9'):
		text = strings.ReplaceAll(text, "_", "")
		if !yamlDecimal.MatchString(text) {
			return false
		}
	default:
		return false
	}
	_, err := strconv.ParseFloat(text, 64)
	return errors.Is(err, strconv.ErrRange)
}
