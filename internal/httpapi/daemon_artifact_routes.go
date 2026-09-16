package httpapi

import (
	"context"
	"net/http"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (s strictOpenAPIServer) UploadDaemonArtifact(
	ctx context.Context,
	request openapi.UploadDaemonArtifactRequestObject,
) (openapi.UploadDaemonArtifactResponseObject, error) {
	uploadScope, err := s.daemonTransferScope(ctx, request.ToolCallID,
		toolcatalog.ToolNameUploadFile)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	if uploadScope.Path != toolcatalog.ArtifactVFSRoot {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	artifactID, err := s.uploadDaemonArtifact(ctx, request.ToolCallID, request.Params.Filename, request.Body, uploadScope)
	if err != nil {
		return nil, err
	}
	return openapi.UploadDaemonArtifact201JSONResponse{ArtifactId: artifactID}, nil
}

func (s strictOpenAPIServer) DownloadDaemonArtifact(
	ctx context.Context,
	request openapi.DownloadDaemonArtifactRequestObject,
) (openapi.DownloadDaemonArtifactResponseObject, error) {
	artifactID, ok := parseOpenAPIPublicID(publicid.KindArtifact, request.ArtifactID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	downloadScope, err := s.daemonTransferScope(ctx, request.ToolCallID,
		toolcatalog.ToolNameDownloadFile)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	if downloadScope.Path != toolcatalog.ArtifactVFSRoot+"/"+request.ArtifactID {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return s.downloadDaemonArtifact(ctx, downloadScope, artifactID)
}

func (response artifactContentResponse) VisitDownloadDaemonArtifactResponse(w http.ResponseWriter) error {
	return response.write(w)
}
