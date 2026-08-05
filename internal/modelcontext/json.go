package modelcontext

import (
	"encoding/json"
	"fmt"
)

func marshalJSON(value any) (json.RawMessage, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal model context json: %w", err)
	}
	return body, nil
}
