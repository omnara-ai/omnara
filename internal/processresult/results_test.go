package processresult

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestCanonicalProcessReadResultValidatesSkippedOutput(t *testing.T) {
	t.Parallel()

	process := Process{
		ID:                  uuid.New(),
		State:               "running",
		DefaultOutputCursor: 4,
	}
	publicProcessID := publicID(publicid.KindProcess, process.ID)
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
			name:    "NUL symbols count as one raw byte",
			payload: json.RawMessage(`{"cursor":4,"max_bytes":3}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"a\u2400b","cursor":4,"next_cursor":7,"truncated":true}`,
		},
		{
			name:    "invalid UTF-8 symbols count as one raw byte",
			payload: json.RawMessage(`{"cursor":4,"max_bytes":3}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"\ufffd\ufffd\u2400","cursor":4,"next_cursor":7,"truncated":true}`,
		},
		{
			name:    "NUL symbols cannot hide output beyond the span",
			payload: json.RawMessage(`{"cursor":4,"max_bytes":3}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"a\u2400b\u2400","cursor":4,"next_cursor":7,"truncated":true}`,
			wantError: true,
		},
		{
			name:    "output beyond the span",
			payload: json.RawMessage(`{"cursor":4,"max_bytes":3}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"abcd","cursor":4,"next_cursor":7,"truncated":true}`,
			wantError: true,
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

			_, advance, err := Read(
				process,
				Action{Payload: test.payload},
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

func TestObservationNextCursorMeasuresReplacementSymbolsAsRawBytes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		output    string
		wantError bool
	}{
		{output: `\u2400\u2400\u2400`},
		{output: `\ufffda\ufffd`},
		{output: `\u2400\u2400\u2400\u2400`, wantError: true},
		{output: `\ufffd\ufffd\ufffd\ufffd`, wantError: true},
		{output: `abcd`, wantError: true},
	} {
		next, ok, err := ObservationNextCursor(json.RawMessage(
			`{"output":"` + test.output + `","cursor":0,"next_cursor":3}`,
		))
		if test.wantError {
			if err == nil {
				t.Fatalf("output %s: want span error", test.output)
			}
			continue
		}
		if err != nil || !ok || next != 3 {
			t.Fatalf("output %s: next=%d ok=%t err=%v", test.output, next, ok, err)
		}
	}
}
