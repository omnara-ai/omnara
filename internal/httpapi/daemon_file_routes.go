package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (s strictOpenAPIServer) daemonTransferScope(
	ctx context.Context,
	processID string,
	direction processcmd.FileTransferDirection,
) (executionstore.DaemonFileProcessScope, error) {
	scope, err := machineDaemonScopeFromContext(ctx)
	if err != nil {
		return executionstore.DaemonFileProcessScope{}, *err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProcess, processID)
	if !ok {
		return executionstore.DaemonFileProcessScope{}, storeerr.ErrNotFound
	}
	process, found, queryErr := s.server.store.Execution().GetDaemonFileProcessScope(
		ctx, scope.OrgID, scope.MachineID, id, direction,
	)
	if queryErr != nil {
		return executionstore.DaemonFileProcessScope{}, fmt.Errorf("resolve file transfer: %w", queryErr)
	}
	if !found {
		return executionstore.DaemonFileProcessScope{}, storeerr.ErrNotFound
	}
	return process, nil
}

func (s strictOpenAPIServer) UploadDaemonFile(
	ctx context.Context,
	req openapi.UploadDaemonFileRequestObject,
) (openapi.UploadDaemonFileResponseObject, error) {
	process, err := s.daemonTransferScope(ctx, req.ProcessID,
		processcmd.FileTransferUpload)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if process.Transfer.Target.Artifact != nil {
		filename := ""
		if req.Params.Filename != nil {
			filename = *req.Params.Filename
		}
		artifact, err := s.uploadDaemonArtifact(ctx, filename, req.Body, process)
		if err != nil {
			return nil, err
		}
		artifactID, err := publicID(publicid.KindArtifact, artifact.ID)
		if err != nil {
			return nil, err
		}
		return openapi.UploadDaemonFile201JSONResponse{
			Path: toolcatalog.ArtifactVFSRoot + "/" + artifactID, Digest: artifact.Digest,
		}, nil
	}
	target := process.Transfer.Target.Memory
	body, err := readMemoryContent(req.Body)
	if err != nil {
		return nil, err
	}
	result, err := s.server.store.Memories().Write(ctx, memorystore.WriteInput{
		Scope:   memorystore.Scope{OrgID: process.OrgID, ProjectID: process.ProjectID, AgentID: process.AgentID},
		StoreID: target.StoreID, Path: target.Path, Content: body, ExpectedDigest: target.ExpectedDigest,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.UploadDaemonFile201JSONResponse{Path: result.Path, Digest: result.Digest}, nil
}

func (s strictOpenAPIServer) DownloadDaemonFile(
	ctx context.Context,
	req openapi.DownloadDaemonFileRequestObject,
) (openapi.DownloadDaemonFileResponseObject, error) {
	process, err := s.daemonTransferScope(ctx, req.ProcessID,
		processcmd.FileTransferDownload)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if target := process.Transfer.Target.Artifact; target != nil {
		return s.downloadDaemonArtifact(ctx, process, target.ID)
	}

	target := process.Transfer.Target.Memory
	digest, body, err := s.server.store.Memories().Read(
		ctx, memorystore.Scope{OrgID: process.OrgID, ProjectID: process.ProjectID, AgentID: process.AgentID},
		target.StoreID, target.Path,
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	etag := `"` + digest + `"`
	return openapi.DownloadDaemonFile200AsteriskResponse{
		Body: bytes.NewReader(body), ContentType: "application/octet-stream", ContentLength: int64(len(body)),
		Headers: openapi.DownloadDaemonFile200ResponseHeaders{ETag: &etag, XOmnaraFileDigest: &digest},
	}, nil
}

func uploadedArtifactContentType(filename string, content []byte) string {
	detected := http.DetectContentType(content)
	if kind, _ := modelcontext.AttachmentKindForMediaType(detected); kind == modelcontext.AttachmentKindImage {
		return detected
	}
	if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); contentType != "" {
		return contentType
	}
	return detected
}

func (s strictOpenAPIServer) uploadDaemonArtifact(
	ctx context.Context,
	filename string,
	body io.Reader,
	uploadScope executionstore.DaemonFileProcessScope,
) (artifactstore.ArtifactRecord, error) {
	if filename == "" || !utf8.ValidString(filename) ||
		utf8.RuneCountInString(filename) > 255 || strings.Contains(filename, "\x00") {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest, "invalid filename",
		)
	}
	httpRequest, ok := openAPIHTTPRequest(ctx)
	if !ok {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable, "artifact upload request is unavailable",
		)
	}
	if httpRequest.ContentLength == 0 {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest, "artifact content is required",
		)
	}
	if httpRequest.ContentLength > daemonprotocol.MaxFileTransferBytes {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(
			openapi.ErrorCodeRequestTooLarge, "artifact content exceeds the size limit",
		)
	}
	content, err := io.ReadAll(body)
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(
			openapi.ErrorCodeRequestTooLarge, "artifact content exceeds the size limit",
		)
	}
	if err != nil {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest, "read artifact content",
		)
	}
	if len(content) == 0 {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest, "artifact content is required",
		)
	}
	artifact, err := s.server.store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:      uploadScope.ProjectID,
		AgentID:        uploadScope.AgentID,
		ContentType:    uploadedArtifactContentType(filename, content),
		Filename:       filename,
		Content:        content,
		MaxBytes:       daemonprotocol.MaxFileTransferBytes,
		IdempotencyKey: executionstore.UploadArtifactIdempotencyKey(uploadScope.ToolCallID),
	})
	if err != nil {
		return artifactstore.ArtifactRecord{}, apierror.OrgScoped(err)
	}
	return artifact, nil
}

func (s strictOpenAPIServer) downloadDaemonArtifact(
	ctx context.Context,
	downloadScope executionstore.DaemonFileProcessScope,
	artifactID uuid.UUID,
) (artifactContentResponse, error) {
	content, artifact, err := s.server.store.Artifacts().GetArtifactBlob(
		ctx,
		downloadScope.ProjectID,
		downloadScope.AgentID,
		artifactID,
	)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return artifactContentResponse{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		return artifactContentResponse{}, apierror.OrgScoped(err)
	}
	return artifactContentResponse{content: content, artifact: artifact}, nil
}
