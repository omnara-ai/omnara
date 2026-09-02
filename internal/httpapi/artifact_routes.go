package httpapi

import (
	"context"
	"mime"
	"net/http"
	"strconv"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) GetArtifact(
	ctx context.Context,
	request openapi.GetArtifactRequestObject,
) (openapi.GetArtifactResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getArtifact(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) getArtifact(
	ctx context.Context,
	request openapi.GetArtifactRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.GetArtifactResponseObject, error) {
	artifactID, ok := parseOpenAPIPublicID(publicid.KindArtifact, request.ArtifactID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	artifact, err := s.server.store.Artifacts().GetArtifact(ctx, project.ID, agent.ID, artifactID)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, err.Error())
	}
	response, err := publicArtifactResponseFromRecord(project.OrgID, artifact)
	if err != nil {
		return nil, err
	}
	return openapi.GetArtifact200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetArtifactContent(
	ctx context.Context,
	request openapi.GetArtifactContentRequestObject,
) (openapi.GetArtifactContentResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getArtifactContent(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) getArtifactContent(
	ctx context.Context,
	request openapi.GetArtifactContentRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.GetArtifactContentResponseObject, error) {
	artifactID, ok := parseOpenAPIPublicID(publicid.KindArtifact, request.ArtifactID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	content, artifact, err := s.server.store.Artifacts().GetArtifactBlob(ctx, project.ID, agent.ID, artifactID)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		return nil, apierror.ProjectScoped(err)
	}
	return artifactContentResponse{content: content, artifact: artifact}, nil
}

type artifactContentResponse struct {
	content  []byte
	artifact artifactstore.ArtifactRecord
}

func (response artifactContentResponse) VisitGetArtifactContentResponse(w http.ResponseWriter) error {
	return response.write(w)
}

func (response artifactContentResponse) VisitDownloadDaemonArtifactResponse(w http.ResponseWriter) error {
	return response.write(w)
}

func (response artifactContentResponse) write(w http.ResponseWriter) error {
	contentType := response.artifact.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(response.content)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", contentDisposition(response.artifact.Filename))
	if response.artifact.Digest != "" {
		w.Header().Set("ETag", `"`+response.artifact.Digest+`"`)
	}
	// Artifact content is immutable: the digest never changes for an id.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(response.content)
	return err
}

func contentDisposition(filename string) string {
	if filename == "" || filename == "." || filename == ".." {
		return "attachment"
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}
