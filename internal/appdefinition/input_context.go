package appdefinition

import "encoding/json"

// AppendInputContext records the verified reply address alongside ordinary content.
// Flexible tools need typed provider IDs and the saved app name, rather than a
// display label or an opaque attribution target. Hidden blocks remain visible
// to the model while the console can render the original message separately.
func AppendInputContext(
	appName string, scope Scope, content json.RawMessage,
) (json.RawMessage, error) {
	context, err := json.Marshal(struct {
		App     string `json:"app"`
		Address Scope  `json:"reply_address"`
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
