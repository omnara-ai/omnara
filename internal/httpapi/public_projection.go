package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
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
	turnID, err := publicID(publicid.KindAgentTurn, record.TurnID)
	if err != nil {
		return openapi.ToolCall{}, err
	}
	input, err := publicToolInput(record.Input, "tool call input")
	if err != nil {
		return openapi.ToolCall{}, err
	}
	response := openapi.ToolCall{
		Id:             toolCallID,
		TurnId:         turnID,
		ProviderCallId: record.ProviderCallID,
		Name:           record.Name,
		Input:          input,
		Type:           openapi.ToolCallType(record.Type),
		State:          openapi.ToolCallState(record.State),
		CreatedAt:      record.CreatedAt,
	}
	if record.Outcome != "" {
		outcome, err := publicToolCallOutcome(record.Outcome)
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
	blocks, err := publicToolResultContentBlocks(contentBlocks)
	if err != nil {
		return openapi.ToolResult{}, err
	}
	outcome, err := publicToolCallOutcome(toolCall.Outcome)
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
	if len(contentBlocks) != 0 {
		blocks, err := publicAgentInputContentBlocks(contentBlocks)
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

func publicEventResponseFromReadRecord(record executionstore.AgentEventReadRecord) (openapi.AgentEvent, error) {
	id, err := publicID(publicid.KindAgentEvent, record.ID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	turnID, err := publicID(publicid.KindAgentTurn, record.TurnID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	switch record.EventKind {
	case "agent_input":
		return publicAgentInputEvent(record, id, orgID, projectID, agentID, turnID)
	case "model_output":
		return publicModelOutputEvent(record, id, orgID, projectID, agentID, turnID)
	case "tool_result":
		return publicToolResultEvent(record, id, orgID, projectID, agentID, turnID)
	case "context_checkpoint":
		return publicContextCheckpointEvent(record, id, orgID, projectID, agentID, turnID)
	default:
		return openapi.AgentEvent{}, fmt.Errorf("unsupported agent event kind %q", record.EventKind)
	}
}

func publicContextCheckpointEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	if record.IsOpeningEvent {
		return openapi.AgentEvent{}, errors.New("context checkpoint cannot open a turn")
	}
	checkpointID, err := publicID(
		publicid.KindContextCheckpoint,
		record.ContextCheckpointID,
	)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.ContextCheckpointEvent{
		Id:                             id,
		OrgId:                          orgID,
		ProjectId:                      projectID,
		AgentId:                        agentID,
		TurnId:                         turnID,
		TurnSequence:                   record.TurnSequence,
		IsOpeningEvent:                 openapi.ContextCheckpointEventIsOpeningEventFalse,
		Sequence:                       record.Sequence,
		ContextCheckpointId:            checkpointID,
		SummarizedThroughEventSequence: record.SummarizedThroughEventSequence,
		Summary:                        record.CheckpointSummary,
		CreatedAt:                      record.CreatedAt,
	}
	var response openapi.AgentEvent
	if err := response.FromContextCheckpointEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func publicAgentInputEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	agentInputID, err := publicID(publicid.KindAgentInput, record.AgentInputID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	inputKind := openapi.AgentInputKind(record.InputKind)
	if !inputKind.Valid() {
		return openapi.AgentEvent{}, fmt.Errorf("invalid agent input kind %q", record.InputKind)
	}
	blocks, err := publicAgentInputContentBlocks(record.ContentBlocks)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.AgentInputEvent{
		Id:             id,
		OrgId:          orgID,
		ProjectId:      projectID,
		AgentId:        agentID,
		TurnId:         turnID,
		TurnSequence:   record.TurnSequence,
		IsOpeningEvent: record.IsOpeningEvent,
		Sequence:       record.Sequence,
		AgentInputId:   agentInputID,
		InputKind:      inputKind,
		ContentBlocks:  blocks,
		CreatedAt:      record.CreatedAt,
	}
	if record.ActorID != storage.NilID {
		actorID, err := publicID(publicid.KindActor, record.ActorID)
		if err != nil {
			return openapi.AgentEvent{}, err
		}
		event.ActorId = &actorID
	}
	if record.InputIdempotencyKey != "" {
		event.InputIdempotencyKey = &record.InputIdempotencyKey
	}
	if record.ControlType != "" {
		controlType := openapi.AgentControlType(record.ControlType)
		if !controlType.Valid() {
			return openapi.AgentEvent{}, fmt.Errorf("invalid agent control type %q", record.ControlType)
		}
		event.ControlType = &controlType
	}
	if record.TargetInteractionID != storage.NilID {
		interactionID, err := publicID(publicid.KindAgentInteraction, record.TargetInteractionID)
		if err != nil {
			return openapi.AgentEvent{}, err
		}
		event.InteractionId = &interactionID
	}
	if record.AgentConfigID != storage.NilID {
		agentConfigID, err := publicID(publicid.KindAgentConfig, record.AgentConfigID)
		if err != nil {
			return openapi.AgentEvent{}, err
		}
		event.AgentConfigId = &agentConfigID
	}
	var response openapi.AgentEvent
	if err := response.FromAgentInputEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func publicModelOutputEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	if record.IsOpeningEvent {
		return openapi.AgentEvent{}, errors.New("model output cannot open a turn")
	}
	modelCallContextID, err := publicID(
		publicid.KindModelCallContext,
		record.ModelCallContextID,
	)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	stopReason := openapi.ModelOutputStopReason(record.ModelStopReason)
	if !stopReason.Valid() {
		return openapi.AgentEvent{}, fmt.Errorf(
			"invalid model stop reason %q",
			record.ModelStopReason,
		)
	}
	blocks, err := publicModelOutputContentBlocks(record.ContentBlocks)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.ModelOutputEvent{
		Id:                 id,
		OrgId:              orgID,
		ProjectId:          projectID,
		AgentId:            agentID,
		TurnId:             turnID,
		TurnSequence:       record.TurnSequence,
		IsOpeningEvent:     openapi.ModelOutputEventIsOpeningEventFalse,
		Sequence:           record.Sequence,
		ModelCallContextId: modelCallContextID,
		StopReason:         stopReason,
		ContentBlocks:      blocks,
		CreatedAt:          record.CreatedAt,
	}
	var response openapi.AgentEvent
	if err := response.FromModelOutputEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func publicToolResultEvent(
	record executionstore.AgentEventReadRecord,
	id, orgID, projectID, agentID, turnID string,
) (openapi.AgentEvent, error) {
	if record.IsOpeningEvent {
		return openapi.AgentEvent{}, errors.New("tool result cannot open a turn")
	}
	toolCallID, err := publicID(publicid.KindToolCall, record.ToolCallID)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	outcome, err := publicToolCallOutcome(record.ToolOutcome)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	blocks, err := publicToolResultContentBlocks(record.ContentBlocks)
	if err != nil {
		return openapi.AgentEvent{}, err
	}
	event := openapi.ToolResultEvent{
		Id:             id,
		OrgId:          orgID,
		ProjectId:      projectID,
		AgentId:        agentID,
		TurnId:         turnID,
		TurnSequence:   record.TurnSequence,
		IsOpeningEvent: openapi.ToolResultEventIsOpeningEventFalse,
		Sequence:       record.Sequence,
		ToolCallId:     toolCallID,
		Outcome:        outcome,
		ContentBlocks:  blocks,
		CreatedAt:      record.CreatedAt,
	}
	var response openapi.AgentEvent
	if err := response.FromToolResultEvent(event); err != nil {
		return openapi.AgentEvent{}, err
	}
	return response, nil
}

func publicToolCallOutcome(
	outcome executionstore.ToolResultOutcome,
) (openapi.ToolCallOutcome, error) {
	value := openapi.ToolCallOutcome(outcome)
	if !value.Valid() {
		return "", fmt.Errorf("invalid terminal tool outcome %q", outcome)
	}
	return value, nil
}

func publicTurnResponseFromReadRecord(record executionstore.AgentTurnReadRecord) (openapi.AgentTurn, error) {
	id, err := publicID(publicid.KindAgentTurn, record.ID)
	if err != nil {
		return openapi.AgentTurn{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.AgentTurn{}, err
	}
	openingEvents, err := publicEventResponsesFromReadRecords(record.OpeningEvents)
	if err != nil {
		return openapi.AgentTurn{}, err
	}
	response := openapi.AgentTurn{
		Id:            id,
		AgentId:       agentID,
		TurnSequence:  record.TurnSequence,
		EventCount:    record.EventCount,
		OpeningEvents: openingEvents,
		StartedAt:     record.StartedAt,
		UpdatedAt:     record.UpdatedAt,
	}
	if record.LatestEvent.ID != storage.NilID {
		latest, err := publicEventResponseFromReadRecord(record.LatestEvent)
		if err != nil {
			return openapi.AgentTurn{}, err
		}
		response.LatestEvent = &latest
	}
	if record.LatestSemanticEvent.ID != storage.NilID {
		latestSemantic, err := publicEventResponseFromReadRecord(record.LatestSemanticEvent)
		if err != nil {
			return openapi.AgentTurn{}, err
		}
		response.LatestSemanticEvent = &latestSemantic
	}
	return response, nil
}

func publicTurnResponsesFromReadRecords(records []executionstore.AgentTurnReadRecord) ([]openapi.AgentTurn, error) {
	out := make([]openapi.AgentTurn, 0, len(records))
	for _, record := range records {
		response, err := publicTurnResponseFromReadRecord(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	return out, nil
}

func publicEventResponsesFromReadRecords(records []executionstore.AgentEventReadRecord) ([]openapi.AgentEvent, error) {
	out := make([]openapi.AgentEvent, 0, len(records))
	for _, record := range records {
		response, err := publicEventResponseFromReadRecord(record)
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
