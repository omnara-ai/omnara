//go:build integration

package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	if content := asyncCompletionContent(t, result); !strings.Contains(string(content), "TARGET") ||
		!strings.Contains(string(content), artifact.Digest) {
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
		Input: json.RawMessage(`{"path":"` + path + `","args":["-e","TARGET"]}`),
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
		content, _, err := loadArtifactContent(ctx, call, artifact.ID)
		if err != nil {
			t.Fatalf("read %s artifact: %v", contentType, err)
		}
		if string(content) != "é😀first\nTARGET\nlast\n" {
			t.Fatalf("%s artifact content = %q", contentType, content)
		}
	}
	call.Turn.AgentID = uuid.New()
	if _, _, err := loadArtifactContent(ctx, call, artifact.ID); err == nil {
		t.Fatal("cross-agent access accepted")
	}
	call.Turn.AgentID = fixture.Agent.ID
	for _, test := range []struct {
		content, snippet string
		binary           bool
	}{
		{"TARGET\n\x00binary", "TARGET", true},
		{"TARGET\xff\n", "TARGET�", false},
	} {
		invalidArtifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
			ProjectID:   toolsTestProjectID,
			AgentID:     fixture.Agent.ID,
			ContentType: "text/plain",
			Content:     []byte(test.content),
		})
		if err != nil {
			t.Fatal(err)
		}
		id, err := publicid.Encode(publicid.KindArtifact, invalidArtifact.ID)
		if err != nil {
			t.Fatal(err)
		}
		path := "/artifacts/" + id
		call.Call = model.ToolCall{
			Name:  toolcatalog.ToolNameReadFile,
			Input: json.RawMessage(`{"path":"` + path + `"}`),
		}
		if _, err := runReadFileAsync(ctx, call); err == nil ||
			!strings.Contains(err.Error(), "UTF-8 text without NUL bytes") {
			t.Fatalf("non-text read: %v", err)
		}
		call.Call = model.ToolCall{
			Name:  toolcatalog.ToolNameSearchFiles,
			Input: json.RawMessage(`{"path":"` + path + `","args":["-e","TARGET"]}`),
		}
		result, err := runSearchFilesAsync(ctx, call)
		if err != nil {
			t.Fatal(err)
		}
		var parts []struct {
			Value searchResult `json:"value"`
		}
		if err := json.Unmarshal(asyncCompletionContent(t, result), &parts); err != nil {
			t.Fatal(err)
		}
		if len(parts) != 1 || parts[0].Value.MatchCount != 1 || len(parts[0].Value.Lines) != 1 ||
			parts[0].Value.Lines[0].Path != path || parts[0].Value.Lines[0].Text != test.snippet ||
			parts[0].Value.Truncated != test.binary {
			t.Fatalf("incorrect native artifact search: %+v", parts)
		}
	}
}

func TestReadMemoryWithoutMachine(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "memory-read", toolFixtureOptions{withMemory: true})
	scope := memorystore.Scope{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Principal: toolsTestUserPrincipal(fixture.User.ID),
	}
	store, err := fixture.Store.Memories().Resolve(ctx, toolsTestProjectID, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	call := asyncToolContext{Executor: Executor{Store: fixture.Store}, Turn: fixture.turn()}
	for index, test := range []struct {
		content []byte
		invalid bool
	}{
		{content: []byte("é😀first\nTARGET\nlast\n")},
		{content: nil},
		{content: []byte{0xff}, invalid: true},
		{content: []byte{'a', 0, 'b'}, invalid: true},
	} {
		file := fmt.Sprintf("nested/%d.txt", index)
		written, err := fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: store.ID, Path: file, Content: test.content,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, paging := range []string{`"offset_line":2,"limit_lines":1`, `"offset_char":1,"limit_chars":1`} {
			call.Call = model.ToolCall{
				Name:  toolcatalog.ToolNameReadFile,
				Input: json.RawMessage(`{"path":"/memory/engineering/` + file + `",` + paging + `}`),
			}
			result, err := runReadFileAsync(ctx, call)
			if test.invalid {
				if err == nil {
					t.Fatal("non-text memory accepted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			var parts []struct {
				Value struct {
					Path      string `json:"path"`
					Content   string `json:"content"`
					Digest    string `json:"digest"`
					SizeBytes int    `json:"size_bytes"`
				} `json:"value"`
			}
			if err := json.Unmarshal(asyncCompletionContent(t, result), &parts); err != nil {
				t.Fatal(err)
			}
			if len(parts) != 1 {
				t.Fatalf("unexpected result: %+v", parts)
			}
			value := parts[0].Value
			want := ""
			if len(test.content) > 0 {
				want = "TARGET\n"
				if strings.Contains(paging, "offset_char") {
					want = "😀"
				}
			}
			if value.Path != "/memory/engineering/"+file || value.Content != want ||
				value.Digest != written.Digest || value.Digest != blobstore.ContentDigest(test.content) ||
				value.SizeBytes != len(test.content) {
				t.Fatalf("incorrect memory page: %+v", value)
			}
		}
	}
	private, err := fixture.Store.Memories().Create(ctx, scope, "private", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: scope, StoreID: private.ID, Path: "secret.txt", Content: []byte("secret"),
	}); err != nil {
		t.Fatal(err)
	}
	call.Call.Input = json.RawMessage(`{"path":"/memory/private/secret.txt"}`)
	if _, err := runReadFileAsync(ctx, call); !storeerr.IsNotFound(err) {
		t.Fatalf("unattached store read: %v", err)
	}
	call.Call.Input = json.RawMessage(`{"path":"/memory/engineering/nested/0.txt"}`)
	call.Turn.ProjectID = uuid.New()
	if _, err := runReadFileAsync(ctx, call); !storeerr.IsNotFound(err) {
		t.Fatalf("cross-project store read: %v", err)
	}
}
