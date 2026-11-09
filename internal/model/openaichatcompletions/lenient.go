package openaichatcompletions

import "encoding/json"

type lenientCount struct {
	count int
	valid bool
}

func (n *lenientCount) UnmarshalJSON(data []byte) error {
	var value int
	valid := json.Unmarshal(data, &value) == nil && value >= 0
	if !valid {
		value = 0
	}
	*n = lenientCount{count: value, valid: valid}
	return nil
}

func (n lenientCount) MarshalJSON() ([]byte, error) {
	return json.Marshal(n.count)
}

func (n lenientCount) IsZero() bool {
	return !n.valid
}

type lenientString string

func (s *lenientString) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) != nil {
		value = ""
	}
	*s = lenientString(value)
	return nil
}
