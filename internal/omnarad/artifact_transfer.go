package omnarad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func runUploadArtifactCommand(ctx context.Context, toolCallID, encodedPath string, stdout io.Writer) error {
	raw, err := uploadFile(ctx, toolCallID, encodedPath, "/artifact", false)
	if err != nil {
		return err
	}
	var result struct {
		ArtifactID string `json:"artifact_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode artifact upload response: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode artifact upload response: %w", err)
	}
	if _, err := publicid.Decode(publicid.KindArtifact, result.ArtifactID); err != nil {
		return errors.New("artifact upload response contains an invalid artifact id")
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return fmt.Errorf("write artifact upload result: %w", err)
	}
	return nil
}

func runDownloadArtifactCommand(
	ctx context.Context,
	toolCallID string,
	artifactID string,
	encodedPath string,
	stdout io.Writer,
) error {
	if _, err := publicid.Decode(publicid.KindArtifact, artifactID); err != nil {
		return errors.New("invalid artifact id")
	}
	return downloadFile(ctx, toolCallID, encodedPath, "/artifacts/"+url.PathEscape(artifactID)+"/content", false, stdout)
}
