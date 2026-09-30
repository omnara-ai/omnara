package integrationdefinition

import "encoding/json"

type InputContext struct {
	SenderID         string `json:"sender_id,omitempty"`
	SenderName       string `json:"sender_name,omitempty"`
	Mentioned        bool   `json:"bot_mentioned"`
	ConversationName string `json:"conversation_name,omitempty"`
}

func AppendInputContext(
	integrationName string, scope Scope, content json.RawMessage,
) (json.RawMessage, error) {
	return appendInputContext(integrationName, scope, content, nil)
}

func AppendEventInputContext(
	integrationName string, scope Scope, content json.RawMessage, details InputContext,
) (json.RawMessage, error) {
	return appendInputContext(integrationName, scope, content, &details)
}

func appendInputContext(
	integrationName string, scope Scope, content json.RawMessage, details *InputContext,
) (json.RawMessage, error) {
	// Hidden blocks reach the model while the console renders the original message separately.
	guidance := ""
	if details != nil {
		guidance = "The bot_mentioned field indicates whether this message addressed your integration bot. " +
			"Conversations can include multiple participants; not every message is directed at you."
	}
	context, err := json.Marshal(struct {
		Integration string        `json:"integration"`
		Address     Scope         `json:"source_conversation"`
		Event       *InputContext `json:"event,omitempty"`
		Guidance    string        `json:"guidance,omitempty"`
	}{integrationName, scope, details, guidance})
	if err != nil {
		return nil, err
	}
	block, err := json.Marshal(map[string]any{
		"type": "text", "text": "Incoming integration conversation: " + string(context),
		"metadata": map[string]string{"omnara_hidden": "true"},
	})
	if err != nil {
		return nil, err
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, err
	}
	return json.Marshal(append(blocks, block))
}
