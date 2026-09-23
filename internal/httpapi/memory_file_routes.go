package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
)

func (s strictOpenAPIServer) ListMemoryFiles(
	ctx context.Context, req openapi.ListMemoryFilesRequestObject,
) (openapi.ListMemoryFilesResponseObject, error) {
	scope, id, err := memoryStoreScope(ctx, req.MemoryStoreID)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(req.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	dir, after := "", ""
	if req.Params.Path != nil {
		dir = *req.Params.Path
	}
	cursorScope := shortHash(scope.OrgID.String() + "/" + scope.ProjectID.String() + "/" + id.String() + "/" + dir)
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		after, err = decodeMemoryDirectoryCursor(*req.Params.Cursor, cursorScope)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, errMalformedCursor.Error())
		}
	}
	page, err := s.server.store.Memories().ListDirectory(ctx, scope, id, dir, after, limit)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	var next *string
	if page.Next != "" {
		var payload bytes.Buffer
		encoder := json.NewEncoder(&payload)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(memoryDirectoryCursor{Version: 1, Scope: cursorScope, After: page.Next}); err != nil {
			return nil, err
		}
		token := base64.RawURLEncoding.EncodeToString(payload.Bytes())
		next = &token
	}
	out := openapi.MemoryFileList{Data: []openapi.MemoryFile{}, NextCursor: nullableFromPtr(next)}
	for _, entry := range page.Entries {
		var item openapi.MemoryFile
		if entry.Directory {
			err = item.FromMemoryDirectory(openapi.MemoryDirectory{
				Path: entry.Path, ModifiedAt: entry.ModifiedAt,
			})
		} else {
			err = item.FromMemoryRegularFile(openapi.MemoryRegularFile{
				Path: entry.Path, ModifiedAt: entry.ModifiedAt, SizeBytes: entry.Size,
			})
		}
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, item)
	}
	return openapi.ListMemoryFiles200JSONResponse(out), nil
}

type memoryDirectoryCursor struct {
	Version int    `json:"v"`
	Scope   string `json:"scope"`
	After   string `json:"after"`
}

func decodeMemoryDirectoryCursor(raw, scope string) (string, error) {
	if len(raw) > maxCursorTokenLength {
		return "", errInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) > maxCursorPayloadLength {
		return "", errInvalidCursor
	}
	var cursor memoryDirectoryCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return "", errInvalidCursor
	}
	if cursor.Version != 1 || cursor.Scope != scope || path.Base(cursor.After) != cursor.After ||
		memorystore.ValidatePath(cursor.After) != nil {
		return "", errInvalidCursor
	}
	return cursor.After, nil
}

func (s strictOpenAPIServer) DownloadMemoryFile(
	ctx context.Context, req openapi.DownloadMemoryFileRequestObject,
) (openapi.DownloadMemoryFileResponseObject, error) {
	scope, id, err := memoryStoreScope(ctx, req.MemoryStoreID)
	if err != nil {
		return nil, err
	}
	digest, content, err := s.server.store.Memories().Read(ctx, scope, id, req.Params.Path)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	etag, cache, nosniff := `"`+digest+`"`, "no-store", "nosniff"
	disposition := contentDisposition(path.Base(req.Params.Path))
	return openapi.DownloadMemoryFile200ApplicationoctetStreamResponse{
		Body: bytes.NewReader(content), ContentLength: int64(len(content)),
		Headers: openapi.DownloadMemoryFile200ResponseHeaders{
			ETag: &etag, XOmnaraFileDigest: &digest, ContentDisposition: &disposition,
			CacheControl: &cache, XContentTypeOptions: &nosniff,
		},
	}, nil
}

func (s strictOpenAPIServer) WriteMemoryFile(
	ctx context.Context, req openapi.WriteMemoryFileRequestObject,
) (openapi.WriteMemoryFileResponseObject, error) {
	scope, id, err := memoryStoreScope(ctx, req.MemoryStoreID)
	if err != nil {
		return nil, err
	}
	body, err := readMemoryContent(req.Body)
	if err != nil {
		return nil, err
	}
	result, err := s.server.store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: scope, StoreID: id, Path: req.Params.Path, Content: body, ExpectedDigest: req.Params.ExpectedDigest,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.WriteMemoryFile200JSONResponse{
		Path: req.Params.Path, Digest: result.Digest,
	}, nil
}

func (s strictOpenAPIServer) DeleteMemoryFile(
	ctx context.Context, req openapi.DeleteMemoryFileRequestObject,
) (openapi.DeleteMemoryFileResponseObject, error) {
	scope, id, err := memoryStoreScope(ctx, req.MemoryStoreID)
	if err != nil {
		return nil, err
	}
	if err := s.server.store.Memories().DeleteFile(
		ctx, scope, id, req.Params.Path, req.Params.ExpectedDigest,
	); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteMemoryFile204Response{}, nil
}

func readMemoryContent(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(reader, daemonprotocol.MaxFileTransferBytes+1))
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) || len(body) > daemonprotocol.MaxFileTransferBytes {
		return nil, apierror.FromCode(openapi.ErrorCodeRequestTooLarge, "memory content exceeds the size limit")
	}
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "cannot read memory content").WithCause(err)
	}
	return body, nil
}
