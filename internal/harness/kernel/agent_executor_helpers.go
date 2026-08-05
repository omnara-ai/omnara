package kernel

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func toolSpecSet(specs []modelcontext.ToolSpec) map[string]modelcontext.ToolSpec {
	byName := make(map[string]modelcontext.ToolSpec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	return byName
}

func executableToolSet(specs []modelcontext.ToolSpec) map[string]tools.ToolSpec {
	executable := make(map[string]tools.ToolSpec, len(specs))
	for _, spec := range specs {
		executable[spec.Name] = tools.ToolSpec{
			Type:        spec.Type,
			Permission:  spec.Permission,
			InputSchema: spec.InputSchema,
		}
	}
	return executable
}

func toToolTurn(input ModelWorkExecution) tools.Turn {
	return tools.Turn{
		OrgID:         input.OrgID,
		ProjectID:     input.ProjectID,
		AgentID:       input.AgentID,
		RuntimeLockID: input.RuntimeLockID,
	}
}

func toolWorkTurn(input ToolWorkExecution, orgID storage.ID, specs []modelcontext.ToolSpec) tools.Turn {
	return tools.Turn{
		OrgID:              orgID,
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		SourceEventID:      input.SourceEventID,
		RuntimeLockID:      input.RuntimeLockID,
		ModelCallContextID: input.ModelCallContextID,
		Tools:              executableToolSet(specs),
	}
}

func modelToolCallFromRecord(record executionstore.ToolCallRecord) model.ToolCall {
	return model.ToolCall{
		ID:    record.ProviderCallID,
		Name:  record.Name,
		Input: record.Input,
	}
}

func (e AgentExecutor) configuredToolExecutor() tools.Executor {
	executor := e.ToolExecutor
	if executor.Store == nil {
		executor.Store = e.Store
	}
	if executor.MCP == nil {
		executor.MCP = e.MCP
	}
	if executor.MCPAuthHTTPClient == nil {
		executor.MCPAuthHTTPClient = e.MCPAuthHTTPClient
	}
	if executor.Now == nil {
		executor.Now = e.now
	}
	if executor.MCPInitializationBackoff == nil {
		executor.MCPInitializationBackoff = e.MCPInitializationBackoff
	}
	return executor
}

func (e AgentExecutor) contextBuilder() modelcontext.Builder {
	builder := e.ContextBuilder
	if builder.Store == nil && e.Store != nil {
		builder.Store = modelcontext.NewStore(
			e.Store.Execution(),
			e.Store.Artifacts(),
			e.Store.Integrations(),
		)
	}
	if builder.Skills == nil && e.Store != nil {
		if skills := e.Store.Skills(); skills != nil {
			builder.Skills = skills
		}
	}
	return builder
}

func (e AgentExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e AgentExecutor) modelRetryDelay(delay time.Duration) time.Duration {
	if e.ModelRetryDelay == nil {
		return delay
	}
	delay = e.ModelRetryDelay(delay)
	if delay < 0 {
		return 0
	}
	return delay
}

func marshalJSON(value any) (json.RawMessage, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal agent kernel json: %w", err)
	}
	return body, nil
}
