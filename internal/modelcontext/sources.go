package modelcontext

import (
	"context"
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
	ListActiveProcessesForContext(
		ctx context.Context,
		projectID, agentID storage.ID,
	) ([]executionstore.ActiveProcessRecord, error)
	ListExecutableAgentMachineBindings(
		ctx context.Context,
		projectID, agentID storage.ID,
	) ([]executionstore.AgentMachineBindingRecord, error)
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
		modelCallContextID := idString(event.ModelCallContextID)
		var usageAnchor *ProviderUsageAnchor
		if modelCallContextID != "" {
			usageAnchor = &ProviderUsageAnchor{
				Identity: ModelRequestIdentity{
					AgentConfigID:             idString(event.AgentConfigID),
					ConfiguredModelRevisionID: idString(event.ConfiguredModelRevisionID),
					RequestedModelSlug:        event.RequestedModelSlug,
					APIFormat:                 event.APIFormat,
					APIVariant:                event.APIVariant,
				},
				Usage: event.Usage,
			}
		}
		out = append(
			out,
			Message{
				ID:                 event.ID.String(),
				AgentInputID:       event.AgentInputID.String(),
				ModelCallContextID: modelCallContextID,
				Role:               role,
				Sequence:           event.Sequence,
				Content:            event.ContentParts,
				ProviderReplay:     event.ProviderReplay,
				ProviderReplaySource: modelenvelope.ProviderReplayIdentity{
					ModelProviderConfigID:      idString(event.ModelProviderConfigID),
					RequestedProviderModelSlug: event.RequestedModelSlug,
					APIFormat:                  event.APIFormat,
					APIVariant:                 event.APIVariant,
				},
				UsageAnchor: usageAnchor,
			},
		)
	}
	return out, nil
}

func idString(id storage.ID) string {
	if id == storage.NilID {
		return ""
	}
	return id.String()
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
