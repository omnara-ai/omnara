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
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
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

func TestDaemonArtifactUploadScopeRejectsWrongMachine(t *testing.T) {
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
	_, found, err := store.Execution().GetDaemonArtifactUploadScope(
		ctx,
		fixture.OrgUUID,
		storage.ID{1},
		fixture.ToolCallUUID,
	)
	if err != nil {
		t.Fatalf("load wrong-machine scope: %v", err)
	}
	if found {
		t.Fatal("wrong machine was authorized for artifact upload")
	}
}
