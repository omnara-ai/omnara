package modelcontext

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/tooloutput"
)

// ArtifactRefText is the provider-neutral textual rendering of a durable
// artifact reference. Only its public ID is exposed to the model.
func ArtifactRefText(part map[string]json.RawMessage) string {
	var rawID string
	if json.Unmarshal(part["artifact_id"], &rawID) != nil {
		return ""
	}
	id, err := storage.ParseID(rawID)
	if err != nil || id == storage.NilID {
		return ""
	}
	artifactID, err := publicid.Encode(publicid.KindArtifact, id)
	if err != nil {
		return ""
	}
	var contentType string
	var sizeBytes int64
	var lineCount int
	_ = json.Unmarshal(part["content_type"], &contentType)
	_ = json.Unmarshal(part["size_bytes"], &sizeBytes)
	_ = json.Unmarshal(part["line_count"], &lineCount)
	usage := "use read_artifact or search_artifact with this ID"
	if !tooloutput.IsTextReadableContentType(contentType) {
		usage = "binary content; use the artifacts API to download it"
	}
	return fmt.Sprintf(
		"[artifact %s: %s, %d bytes, %d lines; full content stored; %s]",
		artifactID,
		contentType,
		sizeBytes,
		lineCount,
		usage,
	)
}
