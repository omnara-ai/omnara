package executionstore

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestCanonicalProcessReadResultValidatesSkippedOutput(t *testing.T) {
	t.Parallel()

	process := ProcessRecord{
		ID:                  uuid.New(),
		State:               ProcessStateRunning,
		DefaultOutputCursor: 4,
	}
	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
	tests := []struct {
		name        string
		payload     json.RawMessage
		observation string
		wantError   bool
		wantAdvance int64
	}{
		{
			name:    "implicit skip without truncation",
			payload: json.RawMessage(`{}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"cd","cursor":6,"next_cursor":8,"truncated":false}`,
			wantError: true,
		},
		{
			name:    "explicit skip without truncation",
			payload: json.RawMessage(`{"cursor":4}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"cd","cursor":6,"next_cursor":8,"truncated":false}`,
			wantError: true,
		},
		{
			name:    "bounded implicit read at requested cursor",
			payload: json.RawMessage(`{"max_bytes":2}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"ab","cursor":4,"next_cursor":6,"truncated":true}`,
			wantAdvance: 6,
		},
		{
			name:    "explicit read after retained prefix was dropped",
			payload: json.RawMessage(`{"cursor":4}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"cd","cursor":6,"next_cursor":8,"truncated":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, advance, err := canonicalProcessReadResult(
				process,
				ProcessActionRecord{Payload: test.payload},
				json.RawMessage(test.observation),
			)
			if (err != nil) != test.wantError {
				t.Fatalf(
					"canonical read error = %v, want error %t",
					err,
					test.wantError,
				)
			}
			if test.wantAdvance == 0 {
				if advance != nil {
					t.Fatalf("cursor advance = %d, want none", *advance)
				}
			} else if advance == nil || *advance != test.wantAdvance {
				t.Fatalf("cursor advance = %v, want %d", advance, test.wantAdvance)
			}
		})
	}
}
