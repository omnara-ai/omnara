//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
)

// 1x1 transparent PNG.
var testPNGBytes = mustBase64(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGBgAAAABQABh6FO1AAAAABJRU5ErkJggg==",
)

func mustBase64(value string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return decoded
}

func newMediaIntegrationHandler(
	t *testing.T,
	ctx context.Context,
) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	keyWrapper := integrationKeyWrapper()
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(keyWrapper),
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
	)
	handler := mustNewServer(t, store, WithSecretKeyWrapper(keyWrapper)).Handler()
	return newIntegrationHTTPHandler(handler, pool, store), pool
}

func TestSessionInputInlineMediaUploadAndDownload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, _ := newMediaIntegrationHandler(t, ctx)
	project := bootstrapPublicHTTPProject(t, handler, "media-upload")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"media-upload",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	inputsPath := project.ProjectPath + "/agents/" + agentPublicID + "/inputs"

	pngBase64 := base64.StdEncoding.EncodeToString(testPNGBytes)
	body := `{"content_blocks":[` +
		`{"type":"text","text":"what is in this image?"},` +
		`{"type":"media","media_type":"image/png","filename":"pixel.png","data":"` + pngBase64 + `"}` +
		`]}`
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		inputsPath,
		body,
		"idem-media-1",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)

	input := created["agent_input"].(map[string]any)
	blocks := input["content_blocks"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %+v", blocks)
	}
	mediaBlock := blocks[1].(map[string]any)
	if mediaBlock["type"] != "media_ref" {
		t.Fatalf("expected media_ref block, got %+v", mediaBlock)
	}
	artifactID, _ := mediaBlock["artifact_id"].(string)
	if !strings.HasPrefix(artifactID, "art_") {
		t.Fatalf("expected public artifact id, got %q", artifactID)
	}
	artifactPath := project.ProjectPath + "/agents/" + agentPublicID + "/artifacts/" + artifactID
	if _, hasData := mediaBlock["data"]; hasData {
		t.Fatalf("inline data must not survive ingestion: %+v", mediaBlock)
	}

	metadata := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		artifactPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if _, hasKind := metadata["kind"]; hasKind {
		t.Fatalf("artifact metadata must not expose kind: %+v", metadata)
	}
	if metadata["content_type"] != "image/png" ||
		metadata["filename"] != "pixel.png" {
		t.Fatalf("unexpected artifact metadata: %+v", metadata)
	}
	if metadata["size_bytes"].(float64) != float64(len(testPNGBytes)) {
		t.Fatalf("unexpected artifact size: %+v", metadata["size_bytes"])
	}

	req := httptest.NewRequest(http.MethodGet, artifactPath+"/content", nil)
	req.Header.Set("Authorization", "Bearer "+project.AdminToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("download content-type = %q", rec.Header().Get("Content-Type"))
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="pixel.png"` {
		t.Fatalf("download content-disposition = %q", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("download must include a digest ETag")
	}
	if rec.Body.String() != string(testPNGBytes) {
		t.Fatalf(
			"downloaded bytes differ: %d vs %d",
			rec.Body.Len(),
			len(testPNGBytes),
		)
	}

	// Replaying the same request must reuse the same artifacts and input.
	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		inputsPath,
		body,
		"idem-media-1",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	replayedInput := replayed["agent_input"].(map[string]any)
	if replayedInput["id"] != input["id"] {
		t.Fatalf(
			"replay created a new input: %v vs %v",
			replayedInput["id"],
			input["id"],
		)
	}
	replayedBlock := replayedInput["content_blocks"].([]any)[1].(map[string]any)
	if replayedBlock["artifact_id"] != artifactID {
		t.Fatalf(
			"replay minted a new artifact: %v vs %v",
			replayedBlock["artifact_id"],
			artifactID,
		)
	}
}

func TestSessionInputInlineMediaValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, _ := newMediaIntegrationHandler(t, ctx)
	project := bootstrapPublicHTTPProject(t, handler, "media-invalid")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"media-invalid",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	inputsPath := project.ProjectPath + "/agents/" + agentPublicID + "/inputs"

	unsupported := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		inputsPath,
		`{"content_blocks":[{"type":"media","media_type":"image/tiff","data":"aGk="}]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if unsupported["code"] != "validation_failed" {
		t.Fatalf("unexpected error: %+v", unsupported)
	}

	badBase64 := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		inputsPath,
		`{"content_blocks":[{"type":"media","media_type":"image/png","data":"not-base64!!"}]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if badBase64["code"] != "validation_failed" {
		t.Fatalf("unexpected error: %+v", badBase64)
	}

	empty := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		inputsPath,
		`{"content_blocks":[{"type":"media","media_type":"image/png","data":""}]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if empty["code"] != "validation_failed" {
		t.Fatalf("unexpected error: %+v", empty)
	}
}

func TestSessionInputRejectedAttachmentLeavesNoArtifacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, pool := newMediaIntegrationHandler(t, ctx)
	project := bootstrapPublicHTTPProject(t, handler, "media-orphan")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"media-orphan",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	inputsPath := project.ProjectPath + "/agents/" + agentPublicID + "/inputs"

	pngBase64 := base64.StdEncoding.EncodeToString(testPNGBytes)
	// A valid attachment followed by an invalid one must reject the request
	// before any artifact row is created.
	requestJSONWithHeaders(t, handler, http.MethodPost, inputsPath,
		`{"content_blocks":[`+
			`{"type":"media","media_type":"image/png","data":"`+pngBase64+`"},`+
			`{"type":"media","media_type":"image/tiff","data":"`+pngBase64+`"}`+
			`]}`,
		"", http.StatusBadRequest, authHeaders(project.AdminToken))

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artifacts`).Scan(&count); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected request left %d artifact rows", count)
	}
}

func TestSessionInputRejectsMediaRefBlocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, _ := newMediaIntegrationHandler(t, ctx)
	project := bootstrapPublicHTTPProject(t, handler, "media-refs")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"media-refs",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	inputsPath := project.ProjectPath + "/agents/" + agentPublicID + "/inputs"

	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		inputsPath,
		`{"content_blocks":[{"type":"media_ref","artifact_id":"art_00000000000000000000000000"}]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if response["code"] != "validation_failed" {
		t.Fatalf("unexpected error: %+v", response)
	}
}

func TestSessionInputAttachmentSizeLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, _ := newMediaIntegrationHandler(t, ctx)
	project := bootstrapPublicHTTPProject(t, handler, "media-size")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"media-size",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	inputsPath := project.ProjectPath + "/agents/" + agentPublicID + "/inputs"

	oversized := base64.StdEncoding.EncodeToString(
		make([]byte, maxAttachmentBytes+1),
	)
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		inputsPath,
		`{"content_blocks":[{"type":"media","media_type":"image/png","data":"`+oversized+`"}]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(response["error"].(string), "attachment exceeds") {
		t.Fatalf("unexpected error: %+v", response)
	}
}
