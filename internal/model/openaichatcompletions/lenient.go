package openaichatcompletions

import "encoding/json"

type lenientInt int

func (n *lenientInt) UnmarshalJSON(data []byte) error {
	var value int
	if json.Unmarshal(data, &value) != nil {
		value = 0
	}
	*n = lenientInt(value)
	return nil
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

func (n *lenientInt) value() int {
	if n == nil {
		return 0
	}
	return int(*n)
}
