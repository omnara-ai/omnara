package executionstore

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestMachineFileReadContentParts(t *testing.T) {
	var screenshot bytes.Buffer
	require.NoError(t, png.Encode(&screenshot, image.NewGray(image.Rect(0, 0, 1, 1))))
	process := ProcessRecord{MachineID: uuid.New()}
	digest := "sha256:" + strings.Repeat("a", 64)
	unreadable := "file must contain UTF-8 text without NUL bytes, or be a PNG, JPEG, GIF, or WebP image"
	for _, tc := range []struct {
		name        string
		input       string
		contentType string
		content     []byte
		unavailable bool
		wantOutcome ToolResultOutcome
		wantMedia   bool
		wantFields  map[string]any
	}{
		{
			name: "image", input: `{"path":"/tmp/shot.png","limit_lines":5}`, contentType: "image/png",
			content: screenshot.Bytes(), wantOutcome: ToolResultOutcomeSucceeded, wantMedia: true,
			wantFields: map[string]any{"path": "/tmp/shot.png", "content_type": "image/png", "content": nil},
		},
		{
			name: "image with mismatched type", input: `{"path":"shot.png"}`, contentType: "image/jpeg",
			content: screenshot.Bytes(), wantOutcome: ToolResultOutcomeFailed,
			wantFields: map[string]any{"error": unreadable},
		},
		{
			name: "binary", input: `{"path":"blob.bin"}`, contentType: "application/octet-stream",
			content: []byte{0, 1, 0xff}, wantOutcome: ToolResultOutcomeFailed,
			wantFields: map[string]any{"error": unreadable, "size_bytes": float64(3)},
		},
		{
			name: "text lines", input: `{"path":"a.txt","offset_line":2,"limit_lines":1}`, contentType: "text/plain",
			content: []byte("one\ntwo\nthree"), wantOutcome: ToolResultOutcomeSucceeded,
			wantFields: map[string]any{"content": "two\n", "next_offset_line": float64(3), "has_more": true},
		},
		{
			name: "text chars", input: `{"path":"a.txt","offset_char":1,"limit_chars":2}`, contentType: "text/plain",
			content: []byte("é😀z"), wantOutcome: ToolResultOutcomeSucceeded,
			wantFields: map[string]any{"content": "😀z", "offset_char": float64(1), "has_more": false},
		},
		{
			name: "digest match", input: `{"path":"a.txt","expected_digest":"` + digest + `"}`,
			contentType: "text/plain", content: []byte("same"), wantOutcome: ToolResultOutcomeSucceeded,
			wantFields: map[string]any{"content": "same", "has_more": false},
		},
		{
			name:        "digest mismatch",
			input:       `{"path":"a.txt","expected_digest":"sha256:` + strings.Repeat("b", 64) + `"}`,
			contentType: "text/plain", content: []byte("changed"), wantOutcome: ToolResultOutcomeFailed,
			wantFields: map[string]any{"error_code": "file_content_conflict", "current_digest": digest},
		},
		{
			name: "unavailable", input: `{"path":"a.txt"}`, unavailable: true, wantOutcome: ToolResultOutcomeFailed,
			wantFields: map[string]any{"error": "the uploaded file snapshot is unavailable; read the file again"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := machineFileRead{
				toolCallInput: json.RawMessage(tc.input), artifactID: uuid.New(), contentType: tc.contentType,
				digest: digest, content: tc.content, unavailable: tc.unavailable,
			}
			outcome, raw, err := machineFileReadContentParts(process, read)
			require.NoError(t, err)
			require.Equal(t, tc.wantOutcome, outcome)
			var parts []map[string]any
			require.NoError(t, json.Unmarshal(raw, &parts))
			if tc.wantMedia {
				require.Len(t, parts, 2)
				require.Equal(t, "media_ref", parts[1]["type"])
				require.Equal(t, read.artifactID.String(), parts[1]["artifact_id"])
				require.NotContains(t, parts[1], "exclude_from_model_context")
			} else {
				require.Len(t, parts, 1)
			}
			require.Equal(t, "structured_data", parts[0]["type"])
			value, ok := parts[0]["value"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, publicResourceID(publicid.KindMachine, process.MachineID), value["machine_id"])
			require.Equal(t,
				"/artifacts/"+publicResourceID(publicid.KindArtifact, read.artifactID), value["artifact_path"])
			for key, want := range tc.wantFields {
				require.Equal(t, want, value[key], key)
			}
		})
	}
}
