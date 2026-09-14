package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"gopkg.in/yaml.v3"
)

func SubagentSourceFrom(base AgentConfigSource, subagent SubagentCompiled, depth SubagentDepth) AgentConfigSource {
	child := base
	child.Tools = maps.Clone(base.Tools)
	child.MaxDepth = depth.MaxDepth
	if subagent.InstructionAppend != "" {
		child.Instruction = strings.TrimSpace(base.Instruction) + "\n\n" + subagent.InstructionAppend
	}
	if !depth.CanSpawn() {
		child.Subagents = nil
		child.MaxSubagents = nil
		delete(child.Tools, toolcatalog.ToolNameSpawnAgent)
		if len(child.Tools) == 0 {
			child.Tools = nil
		}
	}
	child.Model = subagent.Model.ApplyTo(base.Model)
	return child
}

func EncodeSourceYAML(source AgentConfigSource) (string, error) {
	encodedJSON, err := json.Marshal(source)
	if err != nil {
		return "", fmt.Errorf("marshal agent config source: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(encodedJSON, &root); err != nil {
		return "", fmt.Errorf("decode agent config source: %w", err)
	}
	blockStyle(&root)
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return "", fmt.Errorf("encode agent config source yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("encode agent config source yaml: %w", err)
	}
	return out.String(), nil
}

func blockStyle(node *yaml.Node) {
	switch node.Kind {
	case yaml.ScalarNode:
		node.Style = 0
		if strings.Contains(node.Value, "\n") {
			node.Style = yaml.LiteralStyle
		}
	default:
		node.Style = 0
	}
	for _, child := range node.Content {
		blockStyle(child)
	}
}
