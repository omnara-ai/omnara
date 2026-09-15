package httpapi

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func publicAgentResponseFromRecord(record executionstore.AgentRecord) (openapi.Agent, error) {
	id, err := publicID(publicid.KindAgent, record.ID)
	if err != nil {
		return openapi.Agent{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.Agent{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.Agent{}, err
	}
	agentProfileID, err := idOrEmpty(publicid.KindAgentProfile, record.AgentProfileID)
	if err != nil {
		return openapi.Agent{}, err
	}
	currentConfigID, err := idOrEmpty(publicid.KindAgentConfig, record.CurrentConfigID)
	if err != nil {
		return openapi.Agent{}, err
	}
	parentAgentID, err := idOrEmpty(publicid.KindAgent, record.ParentAgentID)
	if err != nil {
		return openapi.Agent{}, err
	}
	response := openapi.Agent{
		Id:         id,
		OrgId:      orgID,
		ProjectId:  projectID,
		State:      openapi.AgentState(record.State),
		Name:       record.Name,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
		ArchivedAt: record.ArchivedAt,
	}
	if record.IntegrationTarget.Provider != "" &&
		record.IntegrationTarget.ProviderRef != "" &&
		record.IntegrationTarget.ProviderRefKind != "" {
		target := openapi.IntegrationTarget{
			Provider:        record.IntegrationTarget.Provider,
			ProviderRef:     record.IntegrationTarget.ProviderRef,
			ProviderRefKind: record.IntegrationTarget.ProviderRefKind,
			DisplayName:     record.IntegrationTarget.DisplayName,
		}
		if providerURI := integrationTargetProviderURI(record.IntegrationTarget); providerURI != "" {
			target.ProviderUri = &providerURI
		}
		response.IntegrationTarget = &target
	}
	if currentConfigID != "" {
		response.CurrentConfigId = &currentConfigID
	}
	if agentProfileID != "" {
		response.AgentProfileId = &agentProfileID
	}
	if parentAgentID != "" {
		response.ParentAgentId = &parentAgentID
	}
	if record.SubagentKey != "" {
		response.SubagentKey = &record.SubagentKey
	}
	if record.Activity != nil {
		state := openapi.AgentActivityState(record.Activity.State)
		if !state.Valid() {
			return openapi.Agent{}, fmt.Errorf("invalid agent activity state %q", record.Activity.State)
		}
		response.Activity = &openapi.AgentActivity{State: state, LastActivityAt: record.Activity.LastActivityAt}
	}
	if record.Model.ProviderConfig != "" && record.Model.Name != "" {
		response.Model = &openapi.AgentModel{
			ProviderConfig: record.Model.ProviderConfig,
			Name:           record.Model.Name,
		}
	}
	return response, nil
}

func integrationTargetProviderURI(target executionstore.IntegrationTargetDisplay) string {
	switch target.Provider {
	case integrationstore.IntegrationProviderSlack:
		return slack.ConversationURI(target.ProviderTenantID, target.ProviderRef)
	default:
		return ""
	}
}

func publicArtifactResponseFromRecord(
	orgIDValue storage.ID,
	record artifactstore.ArtifactRecord,
) (openapi.Artifact, error) {
	id, err := publicID(publicid.KindArtifact, record.ID)
	if err != nil {
		return openapi.Artifact{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, orgIDValue)
	if err != nil {
		return openapi.Artifact{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.Artifact{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.Artifact{}, err
	}
	response := openapi.Artifact{
		Id:        id,
		OrgId:     orgID,
		ProjectId: projectID,
		AgentId:   agentID,
		SizeBytes: record.SizeBytes,
		CreatedAt: record.CreatedAt,
	}
	if record.ContentType != "" {
		response.ContentType = &record.ContentType
	}
	if record.Filename != "" {
		response.Filename = &record.Filename
	}
	if record.Digest != "" {
		response.Digest = &record.Digest
	}
	return response, nil
}

func publicToolCallFromRecord(
	record executionstore.ToolCallRecord,
) (openapi.ToolCall, error) {
	toolCallID, err := publicID(publicid.KindToolCall, record.ID)
	if err != nil {
		return openapi.ToolCall{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.ToolCall{}, err
	}
	turnID, err := publicID(publicid.KindAgentTurn, record.TurnID)
	if err != nil {
		return openapi.ToolCall{}, err
	}
	input, err := publicevents.ToolInput(record.Input, "tool call input")
	if err != nil {
		return openapi.ToolCall{}, err
	}
	response := openapi.ToolCall{
		Id:             toolCallID,
		AgentId:        agentID,
		TurnId:         turnID,
		ProviderCallId: record.ProviderCallID,
		Name:           record.Name,
		Input:          input,
		Type:           openapi.ToolCallType(record.Type),
		State:          openapi.ToolCallState(record.State),
		CreatedAt:      record.CreatedAt,
	}
	if record.Outcome != "" {
		outcome, err := publicevents.ToolCallOutcome(record.Outcome)
		if err != nil {
			return openapi.ToolCall{}, err
		}
		response.Outcome = &outcome
	}
	response.CompletedAt = record.CompletedAt
	return response, nil
}

func publicToolResultFromRecord(
	toolCall executionstore.ToolCallRecord,
	event executionstore.TypedAgentEventRecord,
	contentBlocks json.RawMessage,
) (openapi.ToolResult, error) {
	id, err := publicID(publicid.KindAgentEvent, event.Event.ID)
	if err != nil {
		return openapi.ToolResult{}, err
	}
	agentID, err := publicID(publicid.KindAgent, event.Event.AgentID)
	if err != nil {
		return openapi.ToolResult{}, err
	}
	toolCallID, err := publicID(publicid.KindToolCall, toolCall.ID)
	if err != nil {
		return openapi.ToolResult{}, err
	}
	blocks, err := publicevents.ToolResultContentBlocks(contentBlocks)
	if err != nil {
		return openapi.ToolResult{}, err
	}
	outcome, err := publicevents.ToolCallOutcome(toolCall.Outcome)
	if err != nil {
		return openapi.ToolResult{}, err
	}
	return openapi.ToolResult{
		EventId:       id,
		AgentId:       agentID,
		ToolCallId:    toolCallID,
		Outcome:       outcome,
		ContentBlocks: blocks,
		CreatedAt:     event.Event.At,
	}, nil
}

func publicAgentInputResponseFromRecord(record executionstore.AgentInputRecord) (openapi.AgentInput, error) {
	return publicAgentInputResponseFromRecordWithContent(record, record.ContentBlocks)
}

func publicAgentInputResponseFromRecordWithContent(
	record executionstore.AgentInputRecord,
	contentBlocks json.RawMessage,
) (openapi.AgentInput, error) {
	id, err := publicID(publicid.KindAgentInput, record.ID)
	if err != nil {
		return openapi.AgentInput{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.AgentInput{}, err
	}
	inputKind := openapi.AgentInputKind(record.InputKind)
	if !inputKind.Valid() {
		return openapi.AgentInput{}, fmt.Errorf("invalid agent input kind %q", record.InputKind)
	}
	response := openapi.AgentInput{
		Id:           id,
		AgentId:      agentID,
		State:        record.State,
		DeliveryMode: openapi.AgentInputDeliveryMode(record.DeliveryMode),
		InputKind:    inputKind,
		QueuedAt:     record.QueuedAt,
	}
	if record.ActorID != storage.NilID {
		actorID, err := publicID(publicid.KindActor, record.ActorID)
		if err != nil {
			return openapi.AgentInput{}, err
		}
		response.ActorId = &actorID
	}
	if inputKind == openapi.AgentInputKindContent && record.InputIdempotencyKey != "" {
		response.InputIdempotencyKey = &record.InputIdempotencyKey
	}
	if len(contentBlocks) != 0 {
		blocks, err := publicevents.AgentInputContentBlocks(contentBlocks)
		if err != nil {
			return openapi.AgentInput{}, err
		}
		response.ContentBlocks = &blocks
	}
	return response, nil
}

func publicAgentInputResponsesFromRecords(records []executionstore.AgentInputRecord) ([]openapi.AgentInput, error) {
	out := make([]openapi.AgentInput, 0, len(records))
	for _, record := range records {
		response, err := publicAgentInputResponseFromRecord(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	return out, nil
}

func jsonOrFallback(raw json.RawMessage, fallback json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" || !json.Valid(raw) {
		return fallback
	}
	return raw
}

func jsonMapOrFallback(raw json.RawMessage, fallback json.RawMessage) (map[string]interface{}, error) {
	var out map[string]interface{}
	if err := json.Unmarshal(jsonOrFallback(raw, fallback), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	return out, nil
}
