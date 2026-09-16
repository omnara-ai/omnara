//go:build integration

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestFileRetrievalWithoutMachine(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithMCP(
		t, ctx, "file-read", false,
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
	)
	artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   toolsTestProjectID,
		AgentID:     fixture.Agent.ID,
		ContentType: "text/plain",
		Content:     []byte("é😀first\nTARGET\nlast\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, "UPDATE artifacts SET size_bytes = NULL WHERE id = $1", artifact.ID); err != nil {
		t.Fatal(err)
	}
	publicID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/artifacts/" + publicID
	call := asyncToolContext{Executor: Executor{Store: fixture.Store}, Turn: fixture.turn()}
	call.Call = model.ToolCall{
		Name:  toolcatalog.ToolNameReadFile,
		Input: json.RawMessage(`{"path":"` + path + `","offset_line":2,"limit_lines":1}`),
	}
	result, err := runReadFileAsync(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if content := asyncCompletionContent(t, result); !strings.Contains(string(content), "TARGET") {
		t.Fatalf("read result = %s", content)
	}
	call.Call = model.ToolCall{
		Name:  toolcatalog.ToolNameReadFile,
		Input: json.RawMessage(`{"path":"` + path + `","offset_char":1,"limit_chars":1}`),
	}
	result, err = runReadFileAsync(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if content := asyncCompletionContent(t, result); !strings.Contains(string(content), "😀") {
		t.Fatalf("character read result = %s", content)
	}
	call.Call = model.ToolCall{
		Name:  toolcatalog.ToolNameSearchFiles,
		Input: json.RawMessage(`{"path":"` + path + `","pattern":"TARGET"}`),
	}
	result, err = runSearchFilesAsync(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if content := asyncCompletionContent(t, result); !strings.Contains(string(content), "TARGET") {
		t.Fatalf("search result = %s", content)
	}
	for _, contentType := range []string{
		"application/sql", "application/x-ruby", "application/octet-stream", "image/png",
	} {
		if _, err := fixture.Pool.Exec(
			ctx, "UPDATE artifacts SET content_type = $1 WHERE id = $2", contentType, artifact.ID,
		); err != nil {
			t.Fatal(err)
		}
		content, _, err := loadReadableArtifact(ctx, call, artifact.ID)
		if err != nil {
			t.Fatalf("read %s artifact: %v", contentType, err)
		}
		if string(content) != "é😀first\nTARGET\nlast\n" {
			t.Fatalf("%s artifact content = %q", contentType, content)
		}
	}
	call.Turn.AgentID = uuid.New()
	if _, _, err := loadReadableArtifact(ctx, call, artifact.ID); err == nil {
		t.Fatal("cross-agent access accepted")
	}
	call.Turn.AgentID = fixture.Agent.ID
	for _, content := range [][]byte{{'a', 0, 'b'}, {'a', 0xff, 'b'}} {
		invalidArtifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
			ProjectID:   toolsTestProjectID,
			AgentID:     fixture.Agent.ID,
			ContentType: "text/plain",
			Content:     content,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadReadableArtifact(ctx, call, invalidArtifact.ID); err == nil {
			t.Fatalf("non-text content accepted: %v", content)
		}
	}
}
