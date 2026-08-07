package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
)

func TestArtifactRefTextUsesPublicIDAndRetrievalGuidance(t *testing.T) {
	rawID := storage.ID{0x01, 0x98, 0xc8, 0xb0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x42}
	publicID, err := publicid.Encode(publicid.KindArtifact, rawID)
	if err != nil {
		t.Fatal(err)
	}
	text := ArtifactRefText(map[string]json.RawMessage{
		"artifact_id":  json.RawMessage(`"` + rawID.String() + `"`),
		"content_type": json.RawMessage(`"text/plain; charset=utf-8"`),
		"size_bytes":   json.RawMessage(`1234`),
		"line_count":   json.RawMessage(`42`),
	})
	if !strings.Contains(text, publicID) || !strings.Contains(text, "read_artifact") ||
		strings.Contains(text, rawID.String()) {
		t.Fatalf("artifact ref text = %q", text)
	}
}

func TestArtifactRefTextUsesDownloadGuidanceForBinary(t *testing.T) {
	rawID := storage.ID{0x01, 0x98, 0xc8, 0xb0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x43}
	text := ArtifactRefText(map[string]json.RawMessage{
		"artifact_id":  json.RawMessage(`"` + rawID.String() + `"`),
		"content_type": json.RawMessage(`"application/octet-stream"`),
		"size_bytes":   json.RawMessage(`1234`),
		"line_count":   json.RawMessage(`1`),
	})
	if !strings.Contains(text, "artifacts API") || strings.Contains(text, "read_artifact") {
		t.Fatalf("binary artifact ref text = %q", text)
	}
}
