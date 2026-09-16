package omnarad

import (
	"context"
	"errors"
	"io"
	"net/url"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func runUploadArtifactCommand(
	ctx context.Context,
	toolCallID string,
	encodedPath string,
	stdout io.Writer,
) error {
	return runFileTransfer(ctx, fileTransferRequest{
		direction: "upload", toolCallID: toolCallID, encodedPath: encodedPath, endpointSuffix: "/artifact",
	}, stdout)
}

func runDownloadArtifactCommand(
	ctx context.Context,
	toolCallID string,
	artifactID string,
	encodedPath string,
) error {
	if _, err := publicid.Decode(publicid.KindArtifact, artifactID); err != nil {
		return errors.New("invalid artifact id")
	}
	return runFileTransfer(ctx, fileTransferRequest{
		direction: "download", toolCallID: toolCallID, encodedPath: encodedPath,
		endpointSuffix: "/artifacts/" + url.PathEscape(artifactID) + "/content",
	}, io.Discard)
}
