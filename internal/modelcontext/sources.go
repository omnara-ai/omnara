package modelcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
)

type ExecutionStore interface {
	CaptureAgentConfigForModelContext(
		ctx context.Context,
		projectID, agentID storage.ID,
	) (executionstore.AgentConfigSnapshotRecord, error)
	ListContextEvents(
		ctx context.Context,
		projectID, agentID storage.ID,
		afterSequence int64,
		watermark int64,
		limit int32,
	) ([]executionstore.ContextEventRecord, error)
	ListCompletedToolCallsAtWatermark(
		ctx context.Context,
		projectID, agentID storage.ID,
		afterSequence int64,
		watermark int64,
	) ([]executionstore.ToolCallRecord, error)
	ListMachinePoolSources(
		ctx context.Context,
		projectID, agentID, agentConfigID storage.ID,
	) ([]executionstore.MachinePoolSourceRecord, error)
	GetLatestApplicableContextCheckpoint(
		ctx context.Context,
		projectID, agentID storage.ID,
		maxEventSequence int64,
	) (executionstore.ContextCheckpointRecord, bool, error)
	ListAgentMCPConnections(
		ctx context.Context,
		projectID, agentID storage.ID,
	) ([]executionstore.MCPConnectionRecord, error)
}

type ArtifactStore interface {
	ListAgentArtifactsByIDs(
		ctx context.Context,
		projectID, agentID storage.ID,
		ids []storage.ID,
	) ([]artifactstore.ArtifactRecord, error)
	GetArtifactBlob(
		ctx context.Context,
		projectID, agentID, id storage.ID,
	) ([]byte, artifactstore.ArtifactRecord, error)
}

type IntegrationStore interface {
	ListIntegrationTargets(
		ctx context.Context,
		projectID, agentID storage.ID,
	) ([]integrationstore.IntegrationTargetSummary, error)
}

type Store interface {
	ArtifactStore
	IntegrationStore
	ExecutionStore
}

type composedStore struct {
	ArtifactStore
	IntegrationStore
	ExecutionStore
}

func NewStore(
	execution ExecutionStore,
	artifacts ArtifactStore,
	integrations IntegrationStore,
) Store {
	return composedStore{
		ArtifactStore:    artifacts,
		IntegrationStore: integrations,
		ExecutionStore:   execution,
	}
}

type SkillStore interface {
	GetSkillForDispatch(
		ctx context.Context,
		projectID storage.ID,
		publicSkillID string,
	) (skillstore.SkillRecord, error)
}

type TranscriptWindowInput struct {
	ProjectID     storage.ID
	AgentID       storage.ID
	Watermark     int64
	AfterSequence int64
}

func loadTranscriptWindow(
	ctx context.Context,
	store Store,
	input TranscriptWindowInput,
) ([]Message, error) {
	var records []executionstore.ContextEventRecord
	after := input.AfterSequence
	for {
		page, err := store.ListContextEvents(
			ctx,
			input.ProjectID,
			input.AgentID,
			after,
			input.Watermark,
			500,
		)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		records = append(records, page...)
		after = page[len(page)-1].Sequence
		if len(page) < 500 || after >= input.Watermark {
			break
		}
	}
	return contextEventsToMessages(records)
}

func contextEventsToMessages(records []executionstore.ContextEventRecord) ([]Message, error) {
	out := make([]Message, 0, len(records))
	for _, event := range records {
		role, err := contextEventRole(event)
		if err != nil {
			return nil, err
		}
		modelCallContextID := ""
		if event.ModelCallContextID != storage.NilID {
			modelCallContextID = event.ModelCallContextID.String()
		}
		modelProviderConfigID := ""
		if event.ModelProviderConfigID != storage.NilID {
			modelProviderConfigID = event.ModelProviderConfigID.String()
		}
		message := Message{
			ID: event.ID.String(), AgentInputID: event.AgentInputID.String(), ModelCallContextID: modelCallContextID,
			Role: role, Sequence: event.Sequence, Content: event.ContentParts, ProviderReplay: event.ProviderReplay,
			ProviderReplaySource: modelenvelope.ProviderReplayIdentity{
				ModelProviderConfigID: modelProviderConfigID, RequestedProviderModelSlug: event.RequestedModelSlug,
				APIFormat: event.APIFormat, APIVariant: event.APIVariant,
			},
		}
		if !event.HasOutputLimitFeedback {
			out = append(out, message)
			continue
		}
		assistant, feedback, err := splitOutputLimitFeedback(message.Content)
		if err != nil {
			return nil, fmt.Errorf("output limit feedback %s: %w", event.ID, err)
		}
		if len(assistant) > 0 {
			message.Content, err = json.Marshal(assistant)
			if err != nil {
				return nil, err
			}
			out = append(out, message)
		}
		feedbackContent, err := json.Marshal(feedback)
		if err != nil {
			return nil, err
		}
		// Harness feedback is separate from the partial assistant response. It has
		// no model-context identity or provider replay and cannot become a prefill.
		out = append(out, Message{
			ID: message.ID + "/output-limit-feedback", Role: modelprotocol.RoleUser,
			Sequence: message.Sequence, Content: feedbackContent,
		})
	}
	return out, nil
}

func contextEventRole(event executionstore.ContextEventRecord) (modelprotocol.MessageRole, error) {
	switch event.Role {
	case modelprotocol.RoleUser, modelprotocol.RoleAssistant:
		return event.Role, nil
	case "":
		return "", fmt.Errorf("context event %s is missing its model role", event.ID)
	default:
		return "", fmt.Errorf("context event %s has unsupported model role %q", event.ID, event.Role)
	}
}

func splitOutputLimitFeedback(content json.RawMessage) ([]json.RawMessage, []modelenvelope.ResponsePart, error) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, nil, err
	}
	var assistant []json.RawMessage
	var feedback []modelenvelope.ResponsePart
	for _, raw := range blocks {
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &part); err != nil {
			return nil, nil, err
		}
		if part.Type == "error" {
			feedback = append(feedback, modelenvelope.ResponsePart{Type: modelenvelope.ResponsePartTypeText, Text: part.Text})
		} else {
			assistant = append(assistant, raw)
		}
	}
	if len(feedback) == 0 {
		return nil, nil, errors.New("missing harness feedback")
	}
	return assistant, feedback, nil
}
