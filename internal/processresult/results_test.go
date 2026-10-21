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
			name:    "NUL output",
			payload: json.RawMessage(`{"cursor":4,"max_bytes":3}`),
			observation: `{"process_id":"` + publicProcessID +
				`","output":"a\u0000b","cursor":4,"next_cursor":7,"truncated":true}`,
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

func TestProcessResultsExposeOutputRangeAsByteOffsets(t *testing.T) {
	t.Parallel()

	process := Process{ID: uuid.New(), State: "running"}
	publicProcessID := publicID(publicid.KindProcess, process.ID)
	observation := json.RawMessage(`{"process_id":"` + publicProcessID +
		`","output":"ab","cursor":0,"next_cursor":2,"truncated":false}`)

	read, _, err := Read(process, Action{Payload: json.RawMessage(`{}`)}, observation)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	assertPublicOutputRange(t, read, 0, 2)
	reread, _, err := Read(process, Action{Payload: json.RawMessage(`{}`)}, read)
	if err != nil {
		t.Fatalf("reread published result: %v", err)
	}
	assertPublicOutputRange(t, reread, 0, 2)
	for _, raw := range []json.RawMessage{observation, read} {
		matches, err := ReadMatches(raw, read)
		if err != nil || !matches {
			t.Fatalf("read matches %s = %t, err = %v", raw, matches, err)
		}
	}
	mismatched, err := ReadMatches(
		json.RawMessage(`{"process_id":"`+publicProcessID+
			`","output":"ab","cursor":0,"next_cursor":3,"truncated":false}`),
		read,
	)
	if err != nil || mismatched {
		t.Fatalf("mismatched read matches = %t, err = %v", mismatched, err)
	}

	started, err := Started(process, observation)
	if err != nil {
		t.Fatalf("started result: %v", err)
	}
	assertPublicOutputRange(t, started, 0, 2)
}

func assertPublicOutputRange(t *testing.T, raw json.RawMessage, offset, next float64) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if body["offset_byte"] != offset || body["next_offset_byte"] != next {
		t.Fatalf("result output range = %s", raw)
	}
	if _, ok := body["cursor"]; ok {
		t.Fatalf("result exposes daemon cursor: %s", raw)
	}
	if _, ok := body["next_cursor"]; ok {
		t.Fatalf("result exposes daemon next_cursor: %s", raw)
	}
}
