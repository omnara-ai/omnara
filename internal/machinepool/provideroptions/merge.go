package provideroptions

import "encoding/json"

func Merge(
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	var merged map[string]json.RawMessage
	for _, overlay := range []map[string]json.RawMessage{
		defaultOptions,
		projectOptions,
		agentOptions,
	} {
		if overlay != nil && merged == nil {
			merged = map[string]json.RawMessage{}
		}
		for key, value := range overlay {
			merged[key] = append(json.RawMessage(nil), value...)
		}
	}
	return merged
}
