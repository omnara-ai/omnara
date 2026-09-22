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
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (s strictOpenAPIServer) daemonTransferScope(
	ctx context.Context,
	toolID string,
	toolName string,
) (executionstore.DaemonFileProcessScope, error) {
	scope, err := machineDaemonScopeFromContext(ctx)
	if err != nil {
		return executionstore.DaemonFileProcessScope{}, *err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindToolCall, toolID)
	if !ok {
		return executionstore.DaemonFileProcessScope{}, storeerr.ErrNotFound
	}
	process, found, queryErr := s.server.store.Execution().GetDaemonFileProcessScope(
		ctx, scope.OrgID, scope.MachineID, id, toolName,
	)
	if queryErr != nil {
		return executionstore.DaemonFileProcessScope{}, fmt.Errorf("resolve file transfer: %w", queryErr)
	}
	if !found {
		return executionstore.DaemonFileProcessScope{}, storeerr.ErrNotFound
	}
	return process, nil
}

type daemonMemoryTarget struct {
	Scope   memorystore.Scope
	StoreID uuid.UUID
	Path    string
}

func (s strictOpenAPIServer) daemonMemoryScope(
	ctx context.Context,
	process executionstore.DaemonFileProcessScope,
) (daemonMemoryTarget, error) {
	name, path, parseErr := memorystore.ParsePath(process.Path)
	if parseErr != nil {
		return daemonMemoryTarget{}, storeerr.ErrNotFound
	}
	store, resolveErr := s.server.store.Memories().Resolve(ctx, process.ProjectID, name)
	if resolveErr != nil {
		return daemonMemoryTarget{}, fmt.Errorf("resolve memory transfer: %w", resolveErr)
	}
	return daemonMemoryTarget{
		Scope: memorystore.Scope{
			OrgID: process.OrgID, ProjectID: process.ProjectID, AgentID: process.AgentID,
		},
		StoreID: store.ID,
		Path:    path,
	}, nil
}

func (s strictOpenAPIServer) UploadDaemonFile(
	ctx context.Context,
	req openapi.UploadDaemonFileRequestObject,
) (openapi.UploadDaemonFileResponseObject, error) {
	process, err := s.daemonTransferScope(ctx, req.ToolCallID,
		toolcatalog.ToolNameUploadFile)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if process.Path == toolcatalog.ArtifactVFSRoot {
		filename := ""
		if req.Params.Filename != nil {
			filename = *req.Params.Filename
		}
		artifact, err := s.uploadDaemonArtifact(ctx, req.ToolCallID, filename, req.Body, process)
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
	target, err := s.daemonMemoryScope(ctx, process)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	body, err := readMemoryContent(req.Body)
	if err != nil {
		return nil, err
	}
	result, err := s.server.store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: target.Scope, StoreID: target.StoreID, Path: target.Path, Content: body,
		ExpectedDigest: process.ExpectedDigest,
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
	process, err := s.daemonTransferScope(ctx, req.ToolCallID,
		toolcatalog.ToolNameDownloadFile)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	artifactID := strings.TrimPrefix(process.Path, toolcatalog.ArtifactVFSRoot+"/")
	if strings.HasPrefix(process.Path, toolcatalog.ArtifactVFSRoot+"/") {
		id, ok := parseOpenAPIPublicID(publicid.KindArtifact, artifactID)
		if !ok {
			return nil, apierror.ProjectScoped(storeerr.ErrNotFound)
		}
		return s.downloadDaemonArtifact(ctx, process, id)
	}
	target, err := s.daemonMemoryScope(ctx, process)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	digest, body, err := s.server.store.Memories().Read(ctx, target.Scope, target.StoreID, target.Path)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	etag := `"` + digest + `"`
	return openapi.DownloadDaemonFile200AsteriskResponse{
		Body: bytes.NewReader(body), ContentType: "application/octet-stream", ContentLength: int64(len(body)),
		Headers: openapi.DownloadDaemonFile200ResponseHeaders{ETag: &etag, XOmnaraFileDigest: &digest},
	}, nil
}

func (s strictOpenAPIServer) uploadDaemonArtifact(
	ctx context.Context,
	publicToolCallID, filename string,
	body io.Reader,
	uploadScope executionstore.DaemonFileProcessScope,
) (artifactstore.ArtifactRecord, error) {
	toolCallID, err := publicid.Decode(publicid.KindToolCall, publicToolCallID)
	if err != nil {
		return artifactstore.ArtifactRecord{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
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
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if contentType == "" {
		contentType = http.DetectContentType(content)
	}
	artifact, err := s.server.store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:      uploadScope.ProjectID,
		AgentID:        uploadScope.AgentID,
		ContentType:    contentType,
		Filename:       filename,
		Content:        content,
		MaxBytes:       daemonprotocol.MaxFileTransferBytes,
		IdempotencyKey: executionstore.UploadArtifactIdempotencyKey(toolCallID),
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
