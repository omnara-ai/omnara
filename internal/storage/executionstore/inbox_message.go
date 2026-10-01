package executionstore

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type InboxMessage struct {
	Scope                  integrationdefinition.Scope `json:"scope"`
	ContentBlocks          json.RawMessage             `json:"content_blocks"`
	Metadata               json.RawMessage             `json:"metadata,omitempty"`
	Actor                  *ActorParams                `json:"actor"`
	Origin                 *AgentInputOrigin           `json:"origin"`
	SemanticKey            string                      `json:"semantic_key"`
	DeliveryMode           AgentInputDeliveryMode      `json:"delivery_mode,omitempty"`
	CancelOpenInteractions bool                        `json:"cancel_open_interactions,omitempty"`
	Sibling                *InboxMessageSibling        `json:"sibling,omitempty"`
	Files                  []InboxPlannedFile          `json:"files,omitempty"`
}

func (m InboxMessage) RecipientContent(ids []uuid.UUID) (json.RawMessage, []InboxPlannedFile, error) {
	invalid := func() (json.RawMessage, []InboxPlannedFile, error) {
		return nil, nil, storeerr.InvalidRequest(errors.New("invalid frozen message artifact identities"))
	}
	if len(ids) != len(m.Files) {
		return invalid()
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(m.ContentBlocks, &blocks) != nil || len(blocks) == 0 {
		return invalid()
	}
	replacements := make(map[uuid.UUID]uuid.UUID, len(ids))
	used := make(map[uuid.UUID]bool, len(ids))
	files := make([]InboxPlannedFile, len(ids))
	for i, file := range m.Files {
		if file.ArtifactID == uuid.Nil || file.ProviderFileID == "" || len(file.ProviderFileID) > 512 ||
			replacements[file.ArtifactID] != uuid.Nil || ids[i].Version() != 7 ||
			ids[i].Variant() != uuid.RFC4122 || used[ids[i]] {
			return invalid()
		}
		replacements[file.ArtifactID], used[ids[i]] = ids[i], true
		file.ArtifactID = ids[i]
		if file.Expected != nil {
			expected := *file.Expected
			if expected.ID != m.Files[i].ArtifactID {
				return invalid()
			}
			if err := expected.Validate(); err != nil {
				return nil, nil, err
			}
			expected.ID = ids[i]
			file.Expected = &expected
		}
		files[i] = file
	}
	referenced := make(map[uuid.UUID]bool, len(ids))
	for _, block := range blocks {
		var kind string
		if json.Unmarshal(block["type"], &kind) != nil {
			return invalid()
		}
		switch kind {
		case "text":
			var text string
			if json.Unmarshal(block["text"], &text) != nil {
				return invalid()
			}
		case "media_ref":
			var old uuid.UUID
			if json.Unmarshal(block["artifact_id"], &old) != nil || replacements[old] == uuid.Nil {
				return invalid()
			}
			referenced[old] = true
			artifactID, err := json.Marshal(replacements[old])
			if err != nil {
				return nil, nil, err
			}
			block["artifact_id"] = artifactID
		default:
			return invalid()
		}
	}
	if len(referenced) != len(files) {
		return invalid()
	}
	content, err := json.Marshal(blocks)
	return content, files, err
}

func inboxMessage(receipt integrationstore.IntegrationInboxRecord) (InboxMessage, error) {
	var plan struct {
		Message *InboxMessage `json:"message"`
	}
	if json.Unmarshal(receipt.Plan, &plan) != nil || plan.Message == nil || plan.Message.Actor == nil ||
		plan.Message.Origin == nil || plan.Message.Origin.IntegrationID != receipt.IntegrationID {
		return InboxMessage{}, storeerr.InvalidRequest(errors.New("invalid frozen inbox message"))
	}
	return *plan.Message, nil
}

func (m InboxMessage) input(projectID, agentID uuid.UUID, content json.RawMessage) CreateAgentContentInputInput {
	return CreateAgentContentInputInput{
		ProjectID: projectID, AgentID: agentID, ContentBlocks: content, Metadata: m.Metadata,
		Actor: m.Actor, Origin: m.Origin, IdempotencyKey: m.SemanticKey,
		DeliveryMode: m.DeliveryMode, CancelOpenInteractions: m.CancelOpenInteractions,
	}
}

func (m InboxMessage) initialInput(content json.RawMessage) *LaunchInitialInput {
	return &LaunchInitialInput{
		ContentBlocks: content, Metadata: m.Metadata, Actor: m.Actor,
		Origin: &AgentInputOrigin{
			IntegrationID: m.Origin.IntegrationID, Address: m.Origin.Address, DisplayName: m.Origin.DisplayName,
		},
		SemanticEventKey: m.SemanticKey, DeliveryMode: m.DeliveryMode, CancelOpenInteractions: m.CancelOpenInteractions,
	}
}
