//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
)

func TestUploadDaemonArtifactAuthorizationAndPersistence(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(
		pool,
		[]storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))},
	)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-artifact-upload")
	store := integrationStoreForHandler(t, handler)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	fixture := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-artifact-upload-success",
		"upload_artifact",
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		fixture.authority(),
		fixture.ProcessUUID,
	); err != nil {
		t.Fatalf("accept upload process: %v", err)
	} else if !found {
		t.Fatal("upload process offer was not found")
	}

	content := []byte("png bytes")
	response := requestDaemonArtifactUpload(
		t,
		handler,
		fixture,
		"screenshot.png",
		content,
		http.StatusCreated,
	)
	artifactIDValue, ok := response["artifact_id"].(string)
	if !ok {
		t.Fatalf("upload response = %+v", response)
	}
	replayResponse := requestDaemonArtifactUpload(
		t,
		handler,
		fixture,
		"screenshot.png",
		content,
		http.StatusCreated,
	)
	replayArtifactIDValue, ok := replayResponse["artifact_id"].(string)
	if !ok {
		t.Fatalf("replay upload response = %+v", replayResponse)
	}
	if replayArtifactIDValue != artifactIDValue {
		t.Fatalf("replay artifact id = %q, want %q", replayArtifactIDValue, artifactIDValue)
	}
	artifactID, err := publicid.Decode(publicid.KindArtifact, artifactIDValue)
	if err != nil {
		t.Fatalf("decode artifact id: %v", err)
	}
	stored, artifact, err := store.Artifacts().GetArtifactBlob(
		ctx,
		project.ProjectUUID,
		fixture.AgentUUID,
		artifactID,
	)
	if err != nil {
		t.Fatalf("load uploaded artifact: %v", err)
	}
	if !bytes.Equal(stored, content) || artifact.Filename != "screenshot.png" ||
		artifact.ContentType != "image/png" ||
		artifact.IdempotencyKey != "upload-artifact:"+fixture.ToolCallUUID.String() {
		t.Fatalf("stored artifact = %+v content=%q", artifact, stored)
	}

	otherProject := bootstrapPublicHTTPProject(t, handler, "daemon-artifact-upload-other-org")
	otherFixture := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		otherProject,
		now.Add(time.Second),
		"daemon-artifact-upload-other-org",
		"upload_artifact",
	)
	requestDaemonArtifactUploadForToolCall(
		t,
		handler,
		otherFixture.Token,
		fixture.ToolCallUUID,
		"other.png",
		[]byte("other"),
		http.StatusNotFound,
	)

	wrongTool := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now.Add(2*time.Second),
		"daemon-artifact-upload-wrong-tool",
		"run_command",
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		wrongTool.authority(),
		wrongTool.ProcessUUID,
	); err != nil {
		t.Fatalf("accept wrong-tool process: %v", err)
	} else if !found {
		t.Fatal("wrong-tool process offer was not found")
	}
	requestDaemonArtifactUpload(
		t,
		handler,
		wrongTool,
		"wrong.png",
		[]byte("wrong"),
		http.StatusNotFound,
	)

	ungranted := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now.Add(3*time.Second),
		"daemon-artifact-upload-ungranted",
		"upload_artifact",
	)
	requestDaemonArtifactUpload(
		t,
		handler,
		ungranted,
		"ungranted.png",
		[]byte("ungranted"),
		http.StatusNotFound,
	)

	terminal := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now.Add(4*time.Second),
		"daemon-artifact-upload-terminal",
		"upload_artifact",
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		terminal.authority(),
		terminal.ProcessUUID,
	); err != nil {
		t.Fatalf("accept terminal process: %v", err)
	} else if !found {
		t.Fatal("terminal process offer was not found")
	}
	applyDaemonReportForTest(t, ctx, store, project, terminal, daemonReportedEvent{
		Type:            "process_finished",
		ProcessID:       terminal.ProcessID,
		State:           "failed",
		StateReasonCode: "test_terminal",
	})
	requestDaemonArtifactUpload(
		t,
		handler,
		terminal,
		"terminal.png",
		[]byte("terminal"),
		http.StatusNotFound,
	)
}

func TestUploadDaemonArtifactValidatesFilenameAndBody(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(
		pool,
		[]storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))},
	)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-artifact-validation")
	store := integrationStoreForHandler(t, handler)
	fixture := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC),
		"daemon-artifact-validation",
		"upload_artifact",
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		fixture.authority(),
		fixture.ProcessUUID,
	); err != nil {
		t.Fatalf("accept upload process: %v", err)
	} else if !found {
		t.Fatal("upload process offer was not found")
	}

	requestDaemonArtifactUpload(t, handler, fixture, "", []byte("content"), http.StatusBadRequest)
	requestDaemonArtifactUpload(
		t,
		handler,
		fixture,
		strings.Repeat("界", 255),
		[]byte("content"),
		http.StatusCreated,
	)
	requestDaemonArtifactUpload(
		t,
		handler,
		fixture,
		strings.Repeat("界", 256),
		[]byte("content"),
		http.StatusBadRequest,
	)
	requestDaemonArtifactUpload(
		t,
		handler,
		fixture,
		string([]byte{0xff}),
		[]byte("content"),
		http.StatusBadRequest,
	)
	requestDaemonArtifactUpload(t, handler, fixture, "empty.txt", nil, http.StatusBadRequest)
	requestDaemonArtifactUpload(
		t,
		handler,
		fixture,
		"large.bin",
		bytes.Repeat([]byte("x"), int(daemonprotocol.MaxArtifactUploadBytes)+1),
		http.StatusRequestEntityTooLarge,
	)
}

func TestDownloadDaemonArtifactAuthorizationAndContent(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(
		pool,
		[]storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))},
	)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-artifact-download")
	store := integrationStoreForHandler(t, handler)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	var artifactID string
	fixture := createDaemonProcessFixtureWithToolInputBuilder(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-artifact-download-success",
		"download_artifact",
		nil,
		func(agentID storage.ID) json.RawMessage {
			artifact, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
				ProjectID:   project.ProjectUUID,
				AgentID:     agentID,
				ContentType: "application/pdf",
				Filename:    "report.pdf",
				Content:     []byte("pdf bytes"),
			})
			if err != nil {
				t.Fatalf("create artifact: %v", err)
			}
			artifactID = testPublicID(t, publicid.KindArtifact, artifact.ID)
			input, err := json.Marshal(map[string]string{"artifact_id": artifactID, "path": "report.pdf"})
			if err != nil {
				t.Fatalf("marshal download tool input: %v", err)
			}
			return input
		},
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		fixture.authority(),
		fixture.ProcessUUID,
	); err != nil || !found {
		t.Fatalf("accept download process: found=%t err=%v", found, err)
	}

	recorder := requestDaemonArtifactDownload(
		t,
		handler,
		fixture.Token,
		fixture.ToolCallUUID,
		artifactID,
		http.StatusOK,
	)
	disposition, params, err := mime.ParseMediaType(recorder.Header().Get("Content-Disposition"))
	if recorder.Body.String() != "pdf bytes" ||
		recorder.Header().Get("Content-Type") != "application/pdf" ||
		err != nil || disposition != "attachment" || params["filename"] != "report.pdf" {
		t.Fatalf("download response headers=%v body=%q", recorder.Header(), recorder.Body.String())
	}

	otherArtifact, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   project.ProjectUUID,
		AgentID:     fixture.AgentUUID,
		ContentType: "text/plain",
		Filename:    "other.txt",
		Content:     []byte("other bytes"),
	})
	if err != nil {
		t.Fatalf("create other artifact: %v", err)
	}
	requestDaemonArtifactDownload(
		t,
		handler,
		fixture.Token,
		fixture.ToolCallUUID,
		testPublicID(t, publicid.KindArtifact, otherArtifact.ID),
		http.StatusNotFound,
	)

	otherFixture := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now.Add(time.Second),
		"daemon-artifact-download-other-agent",
		"download_artifact",
	)
	otherAgentArtifact, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   project.ProjectUUID,
		AgentID:     otherFixture.AgentUUID,
		ContentType: "text/plain",
		Filename:    "private.txt",
		Content:     []byte("private bytes"),
	})
	if err != nil {
		t.Fatalf("create other-agent artifact: %v", err)
	}
	otherAgentArtifactID := testPublicID(t, publicid.KindArtifact, otherAgentArtifact.ID)
	crossAgent := createDaemonProcessFixtureWithToolInputBuilder(
		t,
		ctx,
		pool,
		store,
		project,
		now.Add(2*time.Second),
		"daemon-artifact-download-cross-agent",
		"download_artifact",
		nil,
		func(storage.ID) json.RawMessage {
			input, err := json.Marshal(map[string]string{
				"artifact_id": otherAgentArtifactID,
				"path":        "private.txt",
			})
			if err != nil {
				t.Fatalf("marshal cross-agent tool input: %v", err)
			}
			return input
		},
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		crossAgent.authority(),
		crossAgent.ProcessUUID,
	); err != nil || !found {
		t.Fatalf("accept cross-agent process: found=%t err=%v", found, err)
	}
	requestDaemonArtifactDownload(
		t,
		handler,
		crossAgent.Token,
		crossAgent.ToolCallUUID,
		otherAgentArtifactID,
		http.StatusNotFound,
	)

	wrongTool := createDaemonProcessFixtureWithToolInputBuilder(
		t,
		ctx,
		pool,
		store,
		project,
		now.Add(3*time.Second),
		"daemon-artifact-download-wrong-tool",
		"run_command",
		nil,
		func(storage.ID) json.RawMessage {
			input, err := json.Marshal(map[string]string{"artifact_id": artifactID, "path": "report.pdf"})
			if err != nil {
				t.Fatalf("marshal wrong-tool input: %v", err)
			}
			return input
		},
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		wrongTool.authority(),
		wrongTool.ProcessUUID,
	); err != nil || !found {
		t.Fatalf("accept wrong-tool process: found=%t err=%v", found, err)
	}
	requestDaemonArtifactDownload(
		t,
		handler,
		wrongTool.Token,
		wrongTool.ToolCallUUID,
		artifactID,
		http.StatusNotFound,
	)
}

func requestDaemonArtifactDownload(
	t *testing.T,
	handler http.Handler,
	token string,
	toolCallID storage.ID,
	artifactID string,
	wantStatus int,
) *httptest.ResponseRecorder {
	t.Helper()
	publicToolCallID := testPublicID(t, publicid.KindToolCall, toolCallID)
	path := "/api/v1/daemon/tool-calls/" + publicToolCallID +
		"/artifacts/" + artifactID + "/content"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != wantStatus {
		t.Fatalf("artifact download status = %d, want %d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	return recorder
}

func requestDaemonArtifactUpload(
	t *testing.T,
	handler http.Handler,
	fixture daemonProcessFixture,
	filename string,
	body []byte,
	wantStatus int,
) map[string]any {
	t.Helper()
	return requestDaemonArtifactUploadForToolCall(
		t,
		handler,
		fixture.Token,
		fixture.ToolCallUUID,
		filename,
		body,
		wantStatus,
	)
}

func requestDaemonArtifactUploadForToolCall(
	t *testing.T,
	handler http.Handler,
	token string,
	toolCallID storage.ID,
	filename string,
	body []byte,
	wantStatus int,
) map[string]any {
	t.Helper()
	publicToolCallID := testPublicID(t, publicid.KindToolCall, toolCallID)
	path := "/api/v1/daemon/tool-calls/" + publicToolCallID +
		"/artifact?filename=" + url.QueryEscape(filename)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("artifact upload status = %d, want %d body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode artifact upload response: %v body=%s", err, rec.Body.String())
	}
	if wantStatus == http.StatusCreated && len(response) != 1 {
		t.Fatalf("artifact upload response has extra fields: %+v", response)
	}
	return response
}

func TestDaemonArtifactProcessScopeRejectsWrongMachine(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-artifact-wrong-machine")
	store := integrationStoreForHandler(t, handler)
	fixture := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC),
		"daemon-artifact-wrong-machine",
		"upload_artifact",
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		fixture.authority(),
		fixture.ProcessUUID,
	); err != nil || !found {
		t.Fatalf("accept upload process: found=%t err=%v", found, err)
	}
	_, found, err := store.Execution().GetDaemonArtifactProcessScope(
		ctx,
		fixture.OrgUUID,
		storage.ID{1},
		fixture.ToolCallUUID,
		"upload_artifact",
	)
	if err != nil {
		t.Fatalf("load wrong-machine scope: %v", err)
	}
	if found {
		t.Fatal("wrong machine was authorized for artifact upload")
	}
}
