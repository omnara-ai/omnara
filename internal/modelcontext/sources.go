package modelcontext

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
)

type ExecutionStore interface {
	ListInteractionDestinations(
		ctx context.Context,
		projectID, agentID uuid.UUID,
	) (executionstore.InteractionDestinations, error)
	IsOutputLimitBoundary(ctx context.Context, projectID, agentID uuid.UUID, sequence int64) (bool, error)
	CaptureAgentConfigForModelContext(
		ctx context.Context,
		projectID, agentID uuid.UUID,
	) (executionstore.AgentConfigSnapshotRecord, error)
	ListContextEvents(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		afterSequence int64,
		watermark int64,
		limit int32,
	) ([]executionstore.ContextEventRecord, error)
	ListCompletedToolCallsAtWatermark(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		afterSequence int64,
		watermark int64,
	) ([]executionstore.ToolCallRecord, error)
	ListMachinePoolSources(
		ctx context.Context,
		projectID, agentID, agentConfigID uuid.UUID,
	) ([]executionstore.MachinePoolSourceRecord, error)
	GetLatestApplicableContextCheckpoint(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		maxEventSequence int64,
	) (executionstore.ContextCheckpointRecord, bool, error)
	ListAgentMCPConnections(
		ctx context.Context,
		projectID, agentID uuid.UUID,
	) ([]executionstore.MCPConnectionRecord, error)
}

type ArtifactStore interface {
	ListAgentArtifactsByIDs(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		ids []uuid.UUID,
	) ([]artifactstore.ArtifactRecord, error)
	GetArtifactBlob(
		ctx context.Context,
		projectID, agentID, id uuid.UUID,
	) ([]byte, artifactstore.ArtifactRecord, error)
}

type Store interface {
	ArtifactStore
	ExecutionStore
}

type composedStore struct {
	ArtifactStore
	ExecutionStore
}

func NewStore(
	execution ExecutionStore,
	artifacts ArtifactStore,
) Store {
	return composedStore{
		ArtifactStore:  artifacts,
		ExecutionStore: execution,
	}
}

type SkillStore interface {
	GetSkillForDispatch(
		ctx context.Context,
		projectID uuid.UUID,
		publicSkillID string,
	) (skillstore.SkillRecord, error)
}

type TranscriptWindowInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
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
		if event.ModelCallContextID != uuid.Nil {
			modelCallContextID = event.ModelCallContextID.String()
		}
		modelProviderConfigID := ""
		if event.ModelProviderConfigID != uuid.Nil {
			modelProviderConfigID = event.ModelProviderConfigID.String()
		}
		message := Message{
			ID: event.ID.String(), AgentInputID: event.AgentInputID.String(), ModelCallContextID: modelCallContextID,
			Role: role, Sequence: event.Sequence, Content: event.ContentParts, ProviderReplay: event.ProviderReplay,
			StopReason: event.StopReason,
			ProviderReplaySource: modelenvelope.ProviderReplayIdentity{
				ModelProviderConfigID: modelProviderConfigID, RequestedProviderModelSlug: event.RequestedModelSlug,
				APIFormat: event.APIFormat, APIVariant: event.APIVariant,
			},
		}
		out = append(out, message)
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
