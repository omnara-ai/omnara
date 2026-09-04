package openaichatcompletions

import "encoding/json"

type lenientString string

func (s *lenientString) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) != nil {
		value = ""
	}
	*s = lenientString(value)
	return nil
}
