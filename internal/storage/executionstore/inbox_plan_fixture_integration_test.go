//go:build integration

package executionstore_test

import (
	"encoding/json"
	"sort"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func marshalInboxLaunchPlan(slots map[string]executionstore.InboxLaunchSlot) ([]byte, error) {
	var message *executionstore.InboxMessage
	keys := make([]string, 0, len(slots))
	for key := range slots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		slot := slots[keys[0]]
		input := slot.InitialInput
		message = &executionstore.InboxMessage{
			Scope: integrationdefinition.Scope{
				Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "123.456"},
			},
			ContentBlocks: input.ContentBlocks, Metadata: input.Metadata, Actor: input.Actor,
			SemanticKey: input.SemanticEventKey, DeliveryMode: input.DeliveryMode,
			CancelOpenInteractions: input.CancelOpenInteractions,
			Files:                  slot.Files,
		}
		if input.Origin != nil {
			message.Origin = &executionstore.AgentInputOrigin{
				IntegrationID: input.Origin.IntegrationID, Address: input.Origin.Address, DisplayName: input.Origin.DisplayName,
			}
		}
		for i := range message.Files {
			message.Files[i].ProviderFileID = "file"
		}
	}
	return json.Marshal(struct {
		Message    *executionstore.InboxMessage              `json:"message"`
		Recipients map[string]executionstore.InboxLaunchSlot `json:"recipients"`
	}{message, slots})
}

func marshalInboxInputPlan(slot executionstore.InboxInputSlot) ([]byte, error) {
	input := slot.Input
	message := executionstore.InboxMessage{
		Scope: slot.Scope, ContentBlocks: input.ContentBlocks, Metadata: input.Metadata,
		Actor: input.Actor, Origin: input.Origin,
		SemanticKey: input.IdempotencyKey, DeliveryMode: input.DeliveryMode,
		CancelOpenInteractions: input.CancelOpenInteractions,
		Sibling:                slot.Sibling, Files: slot.Files,
	}
	for i := range message.Files {
		message.Files[i].ProviderFileID = "file"
	}
	return json.Marshal(struct {
		Message    executionstore.InboxMessage              `json:"message"`
		Recipients map[string]executionstore.InboxInputSlot `json:"recipients"`
	}{message, map[string]executionstore.InboxInputSlot{"recipient": slot}})
}
