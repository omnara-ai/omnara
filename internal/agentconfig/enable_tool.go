package agentconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"gopkg.in/yaml.v3"
)

func EnableBuiltInTool(
	format SourceFormat,
	source []byte,
	compiledJSON json.RawMessage,
	compilerVersion string,
	definitionHash string,
	name string,
) (Result, error) {
	if _, err := RuntimeContractFromCompiled(compiledJSON, compilerVersion, definitionHash); err != nil {
		return Result{}, err
	}
	updatedSource, sourceTool, sourceHadTool, err := sourceWithEnabledBuiltInTool(format, source, name)
	if err != nil {
		return Result{}, err
	}
	catalog, err := toolcatalog.Default()
	if err != nil {
		return Result{}, err
	}
	compiledTool, err := compileBuiltInTool(name, sourceTool, true, catalog)
	if err != nil {
		return Result{}, err
	}
	canonical, err := compiledWithEnabledBuiltInTool(compiledJSON, name, compiledTool, sourceHadTool)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	if _, err := RuntimeContractFromCompiled(canonical, compilerVersion, hash); err != nil {
		return Result{}, err
	}
	var compiled Compiled
	if err := json.Unmarshal(canonical, &compiled); err != nil {
		return Result{}, fmt.Errorf("decode updated compiled agent config: %w", err)
	}
	return Result{
		Compiled:        compiled,
		CanonicalJSON:   canonical,
		Hash:            hash,
		Source:          updatedSource,
		SourceFormat:    format,
		CompilerVersion: compilerVersion,
	}, nil
}

func sourceWithEnabledBuiltInTool(
	format SourceFormat,
	source []byte,
	name string,
) (string, AgentConfigToolSource, bool, error) {
	semanticJSON, err := sourceJSON(format, source)
	if err != nil {
		return "", AgentConfigToolSource{}, false, err
	}
	var parsed AgentConfigSource
	if err := json.Unmarshal(semanticJSON, &parsed); err != nil {
		return "", AgentConfigToolSource{}, false, fmt.Errorf("decode stored agent config source: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(semanticJSON, &root); err != nil {
		return "", AgentConfigToolSource{}, false, fmt.Errorf("decode stored agent config source: %w", err)
	}
	if root == nil {
		return "", AgentConfigToolSource{}, false, errors.New("stored agent config source must be an object")
	}
	tools, err := rawJSONObject(root["tools"])
	if err != nil {
		return "", AgentConfigToolSource{}, false, fmt.Errorf("decode stored agent config tools: %w", err)
	}
	tool, hadTool := parsed.Tools[name]
	if hadTool {
		rawTool, err := rawJSONObject(tools[name])
		if err != nil {
			return "", AgentConfigToolSource{}, false, fmt.Errorf("decode stored tool %q: %w", name, err)
		}
		rawTool["enabled"] = json.RawMessage("true")
		tools[name], err = json.Marshal(rawTool)
		if err != nil {
			return "", AgentConfigToolSource{}, false, fmt.Errorf("encode stored tool %q: %w", name, err)
		}
	} else {
		tools[name] = json.RawMessage("{}")
	}
	root["tools"], err = json.Marshal(tools)
	if err != nil {
		return "", AgentConfigToolSource{}, false, fmt.Errorf("encode stored agent config tools: %w", err)
	}
	expected, err := json.Marshal(root)
	if err != nil {
		return "", AgentConfigToolSource{}, false, fmt.Errorf("encode stored agent config source: %w", err)
	}
	if format == SourceFormatJSON {
		var updated bytes.Buffer
		encoder := json.NewEncoder(&updated)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(root); err != nil {
			return "", AgentConfigToolSource{}, false, fmt.Errorf("encode stored agent config JSON: %w", err)
		}
		return updated.String(), tool, hadTool, nil
	}
	candidate, err := patchYAMLTool(source, name, hadTool)
	if err == nil {
		candidateJSON, candidateErr := sourceJSON(format, []byte(candidate))
		if candidateErr == nil && bytes.Equal(canonicalizeJSON(candidateJSON), canonicalizeJSON(expected)) {
			return candidate, tool, hadTool, nil
		}
	}
	fallback, err := yamlFromJSON(expected)
	if err != nil {
		return "", AgentConfigToolSource{}, false, err
	}
	return fallback, tool, hadTool, nil
}

func compiledWithEnabledBuiltInTool(
	compiledJSON json.RawMessage,
	name string,
	compiledTool ToolCompiled,
	sourceHadTool bool,
) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(compiledJSON, &root); err != nil {
		return nil, fmt.Errorf("decode compiled agent config: %w", err)
	}
	if root == nil {
		return nil, errors.New("compiled agent config must be an object")
	}
	tools, err := rawJSONObject(root["tools"])
	if err != nil {
		return nil, fmt.Errorf("decode compiled agent config tools: %w", err)
	}
	rawTool, compiledHadTool := tools[name]
	if compiledHadTool != sourceHadTool {
		return nil, fmt.Errorf("stored source and compiled agent config disagree about tool %q", name)
	}
	if compiledHadTool {
		tool, err := rawJSONObject(rawTool)
		if err != nil {
			return nil, fmt.Errorf("decode compiled tool %q: %w", name, err)
		}
		tool["enabled"] = json.RawMessage("true")
		tools[name], err = json.Marshal(tool)
		if err != nil {
			return nil, fmt.Errorf("encode compiled tool %q: %w", name, err)
		}
	} else {
		tools[name], err = json.Marshal(compiledTool)
		if err != nil {
			return nil, fmt.Errorf("encode compiled tool %q: %w", name, err)
		}
	}
	root["tools"], err = json.Marshal(tools)
	if err != nil {
		return nil, fmt.Errorf("encode compiled agent config tools: %w", err)
	}
	updated, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode compiled agent config: %w", err)
	}
	return canonicalizeJSON(updated), nil
}

func rawJSONObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return make(map[string]json.RawMessage), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		object = make(map[string]json.RawMessage)
	}
	return object, nil
}

func patchYAMLTool(source []byte, name string, hadTool bool) (string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return "", err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("agent config source has a trailing document")
		}
		return "", err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("agent config source must be a mapping")
	}
	root := document.Content[0]
	tools := yamlMappingValue(root, "tools")
	if tools == nil {
		tools = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setYAMLMappingValue(root, "tools", tools)
	}
	if tools.Kind != yaml.MappingNode {
		return "", errors.New("agent config tools cannot be patched safely")
	}
	tool := yamlMappingValue(tools, name)
	if !hadTool {
		setYAMLMappingValue(tools, name, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle})
	} else {
		if tool == nil || tool.Kind != yaml.MappingNode {
			return "", errors.New("agent config tool cannot be patched safely")
		}
		setYAMLMappingValue(tool, "enabled", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func yamlFromJSON(raw []byte) (string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return "", fmt.Errorf("decode canonical agent config source: %w", err)
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return "", fmt.Errorf("encode canonical agent config YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("encode canonical agent config YAML: %w", err)
	}
	return out.String(), nil
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func setYAMLMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			previous := mapping.Content[index+1]
			value.HeadComment = previous.HeadComment
			value.LineComment = previous.LineComment
			value.FootComment = previous.FootComment
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}
