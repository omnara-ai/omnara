package integrationdefinition

import "encoding/json"

func AppendInputContext(
	integrationName string, scope Scope, content json.RawMessage,
) (json.RawMessage, error) {
	// Hidden blocks reach the model while the console renders the original message separately.
	context, err := json.Marshal(struct {
		Integration string `json:"integration"`
		Address     Scope  `json:"source_conversation"`
	}{integrationName, scope})
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
