//go:build integration

package executionstore_test

import (
	"encoding/json"
	"sort"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func marshalInboxLaunchPlan(recipients map[string]executionstore.InboxLaunchRecipient) ([]byte, error) {
	var message *executionstore.InboxMessage
	keys := make([]string, 0, len(recipients))
	for key := range recipients {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		recipient := recipients[keys[0]]
		input := recipient.InitialInput
		message = &executionstore.InboxMessage{
			Scope: integrationdefinition.Scope{
				Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "123.456"},
			},
			ContentBlocks: input.ContentBlocks, Metadata: input.Metadata, Actor: input.Actor,
			SemanticKey: input.SemanticEventKey, DeliveryMode: input.DeliveryMode,
			CancelOpenInteractions: input.CancelOpenInteractions,
			Files:                  recipient.Files,
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
		Message    *executionstore.InboxMessage                   `json:"message"`
		Recipients map[string]executionstore.InboxLaunchRecipient `json:"recipients"`
	}{message, recipients})
}

func marshalInboxInputPlan(recipient executionstore.InboxInputRecipient) ([]byte, error) {
	input := recipient.Input
	message := executionstore.InboxMessage{
		Scope: recipient.Scope, ContentBlocks: input.ContentBlocks, Metadata: input.Metadata,
		Actor: input.Actor, Origin: input.Origin,
		SemanticKey: input.IdempotencyKey, DeliveryMode: input.DeliveryMode,
		CancelOpenInteractions: input.CancelOpenInteractions,
		Sibling:                recipient.Sibling, Files: recipient.Files,
	}
	for i := range message.Files {
		message.Files[i].ProviderFileID = "file"
	}
	return json.Marshal(struct {
		Message    executionstore.InboxMessage                   `json:"message"`
		Recipients map[string]executionstore.InboxInputRecipient `json:"recipients"`
	}{message, map[string]executionstore.InboxInputRecipient{"recipient": recipient}})
}
