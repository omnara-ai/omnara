//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestMemoryFileManagementAPI(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, context.Background())
	handler := newIntegrationServerWithStoreOptions(pool, []storage.Option{memoryFileOption(t)})
	project := bootstrapPublicHTTPProject(t, handler, "memory-files")
	other := bootstrapPublicHTTPProject(t, handler, "memory-files-other")
	stores := project.ProjectPath + "/memory-stores"
	record := requestJSONWithHeaders(
		t, handler, http.MethodPost, stores, `{"name":"notes"}`, "", http.StatusCreated, authHeaders(project.AdminToken),
	)
	id, ok := record["id"].(string)
	require.True(t, ok)
	base := stores + "/" + id
	request := func(method, endpoint string, body []byte, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, endpoint, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if method == http.MethodPut {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, status, rec.Code, rec.Body.String())
		return rec
	}
	file := func(name string) string { return base + "/file?path=" + url.QueryEscape(name) }
	empty := request(http.MethodGet, base+"/files", nil,
		project.AdminToken, http.StatusOK)
	require.JSONEq(t, `{"data":[],"next_cursor":null}`, empty.Body.String())
	for _, body := range [][]byte{{}, {0, 255, 1, 2}, []byte("hello\n")} {
		name := "folder/file.bin"
		written := request(http.MethodPut, file(name), body,
			project.AdminToken, http.StatusOK)
		var result map[string]string
		require.NoError(t, json.Unmarshal(written.Body.Bytes(), &result))
		require.Equal(t, "/memory/notes/"+name, result["path"])
		require.Equal(t, blobstore.ContentDigest(body), result["digest"])
		listed := requestJSONWithHeaders(t, handler, http.MethodGet, base+"/files?path=folder",
			"", "", http.StatusOK, authHeaders(project.AdminToken))
		entries, ok := listed["data"].([]any)
		require.True(t, ok)
		require.Len(t, entries, 1)
		entry, ok := entries[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "file", entry["type"])
		require.Equal(t, float64(len(body)), entry["size_bytes"])
		rec := request(http.MethodGet, file(name), nil,
			project.AdminToken, http.StatusOK)
		require.True(t, bytes.Equal(body, rec.Body.Bytes()))
		digest := rec.Header().Get("X-Omnara-File-Digest")
		require.NotEmpty(t, digest)
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
		request(http.MethodGet, file(name), nil, other.AdminToken, http.StatusNotFound)
		conflict := request(http.MethodPut, file(name), []byte("changed"),
			project.AdminToken, http.StatusConflict)
		var conflictBody map[string]string
		require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &conflictBody))
		require.Equal(t, "file_content_conflict", conflictBody["code"])
		require.Equal(t, digest, conflictBody["current_digest"])
		request(http.MethodDelete, file(name), nil,
			project.AdminToken, http.StatusBadRequest)
		request(
			http.MethodPut, file(name)+"&expected_digest="+url.QueryEscape(digest), []byte("changed"),
			project.AdminToken, http.StatusOK,
		)
		conflict = request(
			http.MethodPut, file(name)+"&expected_digest="+url.QueryEscape(digest), []byte("stale replacement"),
			project.AdminToken, http.StatusConflict,
		)
		require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &conflictBody))
		require.Equal(t, "file_content_conflict", conflictBody["code"])
		require.Equal(t, blobstore.ContentDigest([]byte("changed")), conflictBody["current_digest"])
		conflict = request(
			http.MethodDelete, file(name)+"&expected_digest="+url.QueryEscape(digest), nil,
			project.AdminToken, http.StatusConflict,
		)
		require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &conflictBody))
		require.Equal(t, "file_content_conflict", conflictBody["code"])
		require.Equal(t, blobstore.ContentDigest([]byte("changed")), conflictBody["current_digest"])
		rec = request(http.MethodGet, file(name), nil,
			project.AdminToken, http.StatusOK)
		require.Equal(t, "changed", rec.Body.String())
		digest = rec.Header().Get("X-Omnara-File-Digest")
		request(
			http.MethodDelete, file(name)+"&expected_digest="+url.QueryEscape(digest), nil,
			project.AdminToken, http.StatusNoContent,
		)
		request(http.MethodGet, file(name), nil,
			project.AdminToken, http.StatusNotFound)
		missing := request(
			http.MethodPut, file(name)+"&expected_digest="+url.QueryEscape(digest), body,
			project.AdminToken, http.StatusConflict,
		)
		var missingBody map[string]string
		require.NoError(t, json.Unmarshal(missing.Body.Bytes(), &missingBody))
		require.Equal(t, "file_content_conflict", missingBody["code"])
		require.NotContains(t, missingBody, "current_digest")
		request(http.MethodPut, file(name), body, project.AdminToken, http.StatusOK)
		request(
			http.MethodDelete, file(name)+"&expected_digest="+url.QueryEscape(blobstore.ContentDigest(body)), nil,
			project.AdminToken, http.StatusNoContent,
		)
		empty = request(http.MethodGet, base+"/files", nil,
			project.AdminToken, http.StatusOK)
		require.JSONEq(t, `{"data":[],"next_cursor":null}`, empty.Body.String())
	}
	for _, name := range []string{"a.txt", "b.txt", "nested/c.txt"} {
		request(http.MethodPut, file(name), []byte("content"),
			project.AdminToken, http.StatusOK)
	}
	conflict := request(http.MethodPut, file("nested"), []byte("content"),
		project.AdminToken, http.StatusConflict)
	var conflictBody map[string]string
	require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &conflictBody))
	require.Equal(t, "conflict", conflictBody["code"])
	require.NotContains(t, conflictBody, "current_digest")
	first := request(http.MethodGet, base+"/files?limit=1", nil,
		project.AdminToken, http.StatusOK)
	var page struct {
		Data []struct {
			Path string `json:"path"`
		}
		Next *string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &page))
	require.Equal(t, "a.txt", page.Data[0].Path)
	require.NotNil(t, page.Next)
	require.NotEqual(t, "a.txt", *page.Next)
	for _, endpoint := range []string{
		base + "/files?cursor=a.txt",
		base + "/files?path=nested&cursor=" + url.QueryEscape(*page.Next),
	} {
		request(http.MethodGet, endpoint, nil, project.AdminToken, http.StatusBadRequest)
	}
	secondStore := requestJSONWithHeaders(
		t, handler, http.MethodPost, stores, `{"name":"other"}`, "", http.StatusCreated, authHeaders(project.AdminToken),
	)
	secondID, ok := secondStore["id"].(string)
	require.True(t, ok)
	request(http.MethodGet, stores+"/"+secondID+"/files?cursor="+url.QueryEscape(*page.Next), nil,
		project.AdminToken, http.StatusBadRequest)
	second := request(
		http.MethodGet, base+"/files?limit=1&cursor="+url.QueryEscape(*page.Next), nil,
		project.AdminToken, http.StatusOK,
	)
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &page))
	require.Equal(t, "b.txt", page.Data[0].Path)
	third := requestJSONWithHeaders(t, handler, http.MethodGet, base+"/files?limit=1&cursor="+url.QueryEscape(*page.Next),
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	entries, ok := third["data"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 1)
	directory, ok := entries[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "directory", directory["type"])
	require.Equal(t, "nested", directory["path"])
	require.NotContains(t, directory, "size_bytes")
	nested := request(http.MethodGet, base+"/files?path=nested", nil,
		project.AdminToken, http.StatusOK)
	require.NoError(t, json.Unmarshal(nested.Body.Bytes(), &page))
	require.Len(t, page.Data, 1)
	require.Equal(t, "nested/c.txt", page.Data[0].Path)
	require.Nil(t, page.Next)
	longName := strings.Repeat("<", 255)
	request(http.MethodPut, file(longName), []byte("content"), project.AdminToken, http.StatusOK)
	first = request(http.MethodGet, base+"/files?limit=1", nil, project.AdminToken, http.StatusOK)
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &page))
	require.Equal(t, longName, page.Data[0].Path)
	require.NotNil(t, page.Next)
	second = request(http.MethodGet, base+"/files?limit=1&cursor="+url.QueryEscape(*page.Next), nil,
		project.AdminToken, http.StatusOK)
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &page))
	require.Equal(t, "a.txt", page.Data[0].Path)
	request(http.MethodGet, base+"/files?path=missing", nil,
		project.AdminToken, http.StatusNotFound)
	request(http.MethodGet, base+"/files", nil, other.AdminToken, http.StatusNotFound)
	request(http.MethodGet, base+"/files?path=..", nil,
		project.AdminToken, http.StatusBadRequest)
	request(http.MethodPut, file("../escape"), []byte("x"),
		project.AdminToken, http.StatusBadRequest)
	request(
		http.MethodPut, file("large"), bytes.Repeat([]byte("x"), daemonprotocol.MaxFileTransferBytes),
		project.AdminToken, http.StatusOK,
	)
	request(
		http.MethodPut, file("too-large"), bytes.Repeat([]byte("x"), daemonprotocol.MaxFileTransferBytes+1),
		project.AdminToken, http.StatusRequestEntityTooLarge,
	)
	rec := request(http.MethodGet, file("a.txt"), nil,
		project.AdminToken, http.StatusOK)
	digest := rec.Header().Get("X-Omnara-File-Digest")
	requestJSONWithHeaders(
		t, handler, http.MethodPatch, base, `{"read_only":true}`, "", http.StatusOK, authHeaders(project.AdminToken),
	)
	request(http.MethodPut, file("new.txt"), []byte("x"),
		project.AdminToken, http.StatusOK)
	request(http.MethodPut, file("a.txt"), []byte("updated"),
		project.AdminToken, http.StatusConflict)
	request(
		http.MethodPut, file("a.txt")+"&expected_digest="+url.QueryEscape(digest), []byte("updated"),
		project.AdminToken, http.StatusOK,
	)
	request(
		http.MethodDelete, file("a.txt")+"&expected_digest="+url.QueryEscape(digest), nil,
		project.AdminToken, http.StatusConflict,
	)
	rec = request(http.MethodGet, file("a.txt"), nil,
		project.AdminToken, http.StatusOK)
	digest = rec.Header().Get("X-Omnara-File-Digest")
	request(
		http.MethodDelete, file("a.txt")+"&expected_digest="+url.QueryEscape(digest), nil,
		project.AdminToken, http.StatusNoContent,
	)
	filtered := request(http.MethodGet, stores+"?name=no*", nil,
		project.AdminToken, http.StatusOK)
	require.Contains(t, filtered.Body.String(), `"notes"`)
	filtered = request(http.MethodGet, stores+"?name=missing", nil,
		project.AdminToken, http.StatusOK)
	require.NotContains(t, filtered.Body.String(), `"notes"`)
}
