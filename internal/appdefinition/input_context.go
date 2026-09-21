package appdefinition

import "encoding/json"

// AppendInputContext identifies the verified source alongside ordinary content.
// A receive subscription can differ from the app tools' assigned conversation;
// this source does not change their destination. Hidden blocks remain visible
// to the model while the console renders the original message separately.
func AppendInputContext(
	appName string, scope Scope, content json.RawMessage,
) (json.RawMessage, error) {
	context, err := json.Marshal(struct {
		App     string `json:"app"`
		Address Scope  `json:"source_conversation"`
	}{appName, scope})
	if err != nil {
		return nil, err
	}
	block, err := json.Marshal(map[string]any{
		"type": "text", "text": "Incoming app conversation: " + string(context),
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
