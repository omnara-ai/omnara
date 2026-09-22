//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
)

func TestMemoryStoreManagementAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(pool, []storage.Option{memoryFileOption(t)})
	project := bootstrapPublicHTTPProject(t, handler, "memory-api")
	path := project.ProjectPath + "/memory-stores"
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"name":"engineering"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("missing id: %+v", created)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"name":"engineering"}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	got := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		`{"read_only":true,"description":"Reference"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if got["read_only"] != true || got["description"] != "Reference" {
		t.Fatalf("update: %+v", got)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"?limit=1",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		path+"/"+id,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"/"+id,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"name":"bad/name"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
}

func TestDaemonMemoryTransferAuthorization(t *testing.T) {
	t.Run("empty", func(t *testing.T) { testDaemonMemoryTransfer(t, nil) })
	t.Run("binary-at-limit", func(t *testing.T) {
		testDaemonMemoryTransfer(t, bytes.Repeat([]byte{0xff, 0x00}, daemonprotocol.MaxFileTransferBytes/2))
	})
}

func testDaemonMemoryTransfer(t *testing.T, content []byte) {
	t.Helper()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(pool, []storage.Option{
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
		memoryFileOption(t),
	})
	project := bootstrapPublicHTTPProject(t, handler, "daemon-memory")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/memory-stores",
		`{"name":"engineering"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	store := integrationStoreForHandler(t, handler)
	makeFixture := func(name, tool string, expectedDigest *string) daemonProcessFixture {
		fixture := createDaemonProcessFixtureWithToolInputBuilder(
			t, ctx, pool, store, project, time.Now(), name, tool, nil, func(uuid.UUID) json.RawMessage {
				if tool == "upload_file" {
					input, err := json.Marshal(struct {
						Path           string  `json:"path"`
						Source         string  `json:"source"`
						ExpectedDigest *string `json:"expected_digest,omitempty"`
					}{Path: "/memory/engineering/empty.md", Source: "note.md", ExpectedDigest: expectedDigest})
					if err != nil {
						t.Fatal(err)
					}
					return input
				}
				return json.RawMessage(`{"path":"/memory/engineering/empty.md","destination":"note.md"}`)
			})
		var source string
		if err := pool.QueryRow(
			ctx,
			`SELECT c.source FROM agents a JOIN agent_configs c ON c.id=a.current_config_id WHERE a.id=$1`,
			fixture.AgentUUID,
		).Scan(&source); err != nil {
			t.Fatal(err)
		}
		source += "\nmemory_stores:\n  - name: engineering\n    access: read_write\n"
		config := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			project.ProjectPath+"/agent-configs",
			`{"source_format":"yaml","source":`+quotedJSONString(source)+`}`,
			"",
			http.StatusCreated,
			authHeaders(project.AdminToken),
		)
		configPublicID, ok := config["id"].(string)
		if !ok {
			t.Fatal("missing config id")
		}
		configID, err := publicid.Decode(publicid.KindAgentConfig, configPublicID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(
			ctx,
			`UPDATE agents SET current_config_id=$1 WHERE id=$2`,
			configID,
			fixture.AgentUUID,
		); err != nil {
			t.Fatal(err)
		}
		if _, found, err := acceptDaemonProcessOfferForTest(
			ctx,
			store,
			fixture.authority(),
			fixture.ProcessUUID,
		); err != nil || !found {
			t.Fatalf("accept: %v %v", found, err)
		}
		return fixture
	}
	upload := makeFixture("memory-upload", "upload_file", nil)
	call := func(f daemonProcessFixture, method string, content []byte, status int) map[string]any {
		id, err := publicid.Encode(publicid.KindToolCall, f.ToolCallUUID)
		if err != nil {
			t.Fatal(err)
		}
		var payload []byte
		if method == http.MethodPost {
			payload = content
		}
		req := httptest.NewRequest(method, "/api/v1/daemon/tool-calls/"+id+"/file", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+f.Token)
		req.Header.Set("Content-Type", "application/octet-stream")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s status %d: %s", method, rec.Code, rec.Body.String())
		}
		if method == http.MethodGet && status == http.StatusOK {
			if !bytes.Equal(rec.Body.Bytes(), content) {
				t.Fatal("download bytes differ from upload")
			}
			return map[string]any{"digest": rec.Header().Get("X-Omnara-File-Digest")}
		}
		var body map[string]any
		if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	uploaded := call(upload, http.MethodPost, content, http.StatusCreated)
	if uploaded["path"] != "/memory/engineering/empty.md" {
		t.Fatalf("incorrect upload path: %v", uploaded)
	}
	if uploaded["digest"] != fmt.Sprintf("sha256:%x", sha256.Sum256(content)) {
		t.Fatalf("incorrect upload digest: %v", uploaded)
	}
	if len(content) == daemonprotocol.MaxFileTransferBytes {
		call(upload, http.MethodPost, append(content, 0), http.StatusRequestEntityTooLarge)
	}
	replay := call(upload, http.MethodPost, content, http.StatusCreated)
	if replay["path"] != uploaded["path"] || replay["digest"] != uploaded["digest"] {
		t.Fatal("upload replay changed the path or digest")
	}
	call(upload, http.MethodGet, nil, http.StatusNotFound)
	download := makeFixture("memory-download", "download_file", nil)
	read := call(download, http.MethodGet, content, http.StatusOK)
	if read["digest"] != uploaded["digest"] {
		t.Fatalf("download content or digest mismatch")
	}
	call(download, http.MethodPost, content, http.StatusNotFound)
	replacementContent := []byte("updated content")
	call(upload, http.MethodPost, replacementContent, http.StatusConflict)
	digest := uploaded["digest"].(string)
	replacement := makeFixture("memory-replacement", "upload_file", &digest)
	replaced := call(replacement, http.MethodPost, replacementContent, http.StatusCreated)
	if replaced["digest"] != fmt.Sprintf("sha256:%x", sha256.Sum256(replacementContent)) {
		t.Fatalf("incorrect replacement digest: %v", replaced)
	}
	staleContent := []byte("stale replacement")
	call(replacement, http.MethodPost, staleContent, http.StatusConflict)
	if _, err := pool.Exec(ctx, `ALTER TABLE memory_stores RENAME TO unavailable_memory_stores`); err != nil {
		t.Fatal(err)
	}
	call(upload, http.MethodPost, staleContent, http.StatusInternalServerError)
	call(download, http.MethodGet, nil, http.StatusInternalServerError)
}

func TestMemoryStorePagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(pool, []storage.Option{memoryFileOption(t)})
	project := bootstrapPublicHTTPProject(t, handler, "memory-pagination")
	path := project.ProjectPath + "/memory-stores"
	headers := authHeaders(project.AdminToken)
	for _, name := range []string{"zulu", "alpha"} {
		requestJSONWithHeaders(
			t, handler, http.MethodPost, path, `{"name":"`+name+`"}`, "", http.StatusCreated, headers,
		)
	}
	first := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=1", "", "", http.StatusOK, headers)
	cursor, ok := first["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("missing continuation: %+v", first)
	}
	second := requestJSONWithHeaders(
		t, handler, http.MethodGet, path+"?limit=1&cursor="+cursor, "", "", http.StatusOK, headers,
	)
	for i, page := range []map[string]any{first, second} {
		if _, ok := page["has_more"]; ok {
			t.Fatalf("unexpected has_more field: %+v", page)
		}
		data, ok := page["data"].([]any)
		if !ok || len(data) != 1 {
			t.Fatalf("invalid page: %+v", page)
		}
		store, ok := data[0].(map[string]any)
		if !ok || store["name"] != []string{"alpha", "zulu"}[i] {
			t.Fatalf("unexpected store: %+v", data[0])
		}
	}
	if next, ok := second["next_cursor"]; !ok || next != nil {
		t.Fatalf("unexpected continuation: %+v", second)
	}
	other := bootstrapPublicHTTPProject(t, handler, "memory-pagination-other")
	requestJSONWithHeaders(
		t, handler, http.MethodGet, other.ProjectPath+"/memory-stores?cursor="+cursor,
		"", "", http.StatusBadRequest, authHeaders(other.AdminToken),
	)
	for _, invalidCursor := range []string{"invalid", strings.Repeat("x", 1025)} {
		requestJSONWithHeaders(
			t, handler, http.MethodGet, path+"?cursor="+invalidCursor, "", "", http.StatusBadRequest, headers,
		)
	}
}

func memoryFileOption(t *testing.T) storage.Option {
	t.Helper()
	files, err := memorystore.OpenFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	return storage.WithMemoryFilesystem(files)
}
