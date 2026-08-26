package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
)

func (s strictOpenAPIServer) UploadDaemonArtifact(
	ctx context.Context,
	request openapi.UploadDaemonArtifactRequestObject,
) (openapi.UploadDaemonArtifactResponseObject, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	toolCallID, ok := parseOpenAPIPublicID(publicid.KindToolCall, request.ToolCallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	uploadScope, found, err := s.server.store.Execution().GetDaemonArtifactUploadScope(
		ctx,
		scope.OrgID,
		scope.MachineID,
		toolCallID,
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	if !found {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	filename := request.Params.Filename
	if filename == "" || !utf8.ValidString(filename) ||
		utf8.RuneCountInString(filename) > 255 || strings.Contains(filename, "\x00") {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid filename")
	}
	httpRequest, ok := openAPIHTTPRequest(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "artifact upload request is unavailable")
	}
	if httpRequest.ContentLength == 0 {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "artifact content is required")
	}
	if httpRequest.ContentLength > maxDaemonArtifactUploadBytes {
		return nil, apierror.FromCode(openapi.ErrorCodeRequestTooLarge, "artifact content exceeds the size limit")
	}
	content, err := io.ReadAll(request.Body)
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return nil, apierror.FromCode(openapi.ErrorCodeRequestTooLarge, "artifact content exceeds the size limit")
	}
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "read artifact content")
	}
	if len(content) == 0 {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "artifact content is required")
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
		MaxBytes:       maxDaemonArtifactUploadBytes,
		IdempotencyKey: "upload-artifact:" + toolCallID.String(),
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	artifactID, err := publicID(publicid.KindArtifact, artifact.ID)
	if err != nil {
		return nil, err
	}
	return openapi.UploadDaemonArtifact201JSONResponse{
		ArtifactId: artifactID,
	}, nil
}
