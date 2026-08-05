package compaction

import (
	"encoding/json"
	"fmt"
	"time"
)

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r Runner) modelRetryDelay(delay time.Duration) time.Duration {
	if r.ModelRetryDelay == nil {
		return delay
	}
	delay = r.ModelRetryDelay(delay)
	if delay < 0 {
		return 0
	}
	return delay
}

func marshalJSON(value any) (json.RawMessage, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal compaction json: %w", err)
	}
	return body, nil
}
