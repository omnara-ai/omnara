//go:build integration

package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestSearchMemoryScopesAndLimits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("memory search confinement requires Linux")
	}
	setupFileExec(t)
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "memory-search", toolFixtureOptions{withMemory: true})
	scope := memorystore.Scope{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Principal: toolsTestUserPrincipal(fixture.User.ID),
	}
	engineering, err := fixture.Store.Memories().Resolve(ctx, scope.ProjectID, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := fixture.Store.Memories().Create(ctx, scope, "secondary", "", false)
	if err != nil {
		t.Fatal(err)
	}
	private, err := fixture.Store.Memories().Create(ctx, scope, "private", "", false)
	if err != nil {
		t.Fatal(err)
	}
	source := fixture.AgentConfig.Source + "  - name: secondary\n    access: read_only\n"
	for i := range searchStoreBatchSize + 1 {
		name := fmt.Sprintf("batch-%02d", i)
		store, err := fixture.Store.Memories().Create(ctx, scope, name, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: store.ID, Path: "n.txt", Content: []byte("TARGET\n"),
		}); err != nil {
			t.Fatal(err)
		}
		source += "  - name: " + name + "\n    access: read_only\n"
	}
	compiled := compileToolsAgentYAMLResolved(t, ctx, fixture.Store, fixture.User.ID, source)
	config, err := fixture.Store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID: scope.ProjectID, Source: source, SourceFormat: "yaml",
		ConfiguredModelID: parseConfiguredModelID(t, compiled), CompiledDefinition: compiled.CanonicalJSON,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.Store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: scope.ProjectID, CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	write := func(storeID uuid.UUID, path, content string) {
		t.Helper()
		_, err := fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: storeID, Path: path, Content: []byte(content),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	write(engineering.ID, "a.md", "TARGET\nTARGET\nTARGET\n")
	write(engineering.ID, "a/note.md", "TARGET\nTARGET\nTARGET\n")
	for i := range 10 {
		write(engineering.ID, fmt.Sprintf("z%02d.md", i), "TARGET\n")
	}
	write(engineering.ID, "binary.md", "TARGET\n\x00binary")
	write(engineering.ID, "empty.md", "")
	write(secondary.ID, "notes.md", "TARGET\nTARGET\n")
	write(private.ID, "secret.md", "TARGET SECRET\n")
	call := asyncToolContext{Executor: Executor{Store: fixture.Store}, Turn: fixture.turn()}
	call.Turn.AgentID = agent.ID
	search := func(path string, args []string, limit, offset int) (searchResult, error) {
		t.Helper()
		request := map[string]any{"path": path, "args": args, "limit": limit}
		if offset > 0 {
			request["offset_line"] = offset
		}
		input, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		call.Call = model.ToolCall{Name: toolcatalog.ToolNameSearchFiles, Input: input}
		result, err := runSearchFilesAsync(ctx, call)
		if err != nil {
			return searchResult{}, err
		}
		var parts []struct {
			Value searchResult `json:"value"`
		}
		if err := json.Unmarshal(asyncCompletionContent(t, result), &parts); err != nil {
			t.Fatal(err)
		}
		return parts[0].Value, nil
	}
	for _, path := range []string{"/memory/engineering/missing.md", "/memory/engineering/missing/notes.md"} {
		if _, err := search(path, []string{"-e", "TARGET"}, 20, 0); !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("missing memory %s: %v", path, err)
		}
	}
	result, err := search("/memory/**/*.md", []string{"-e", "TARGET"}, 2, 0)
	if err != nil || result.MatchCount != 2 || !result.Truncated || result.IncompleteReason == "" {
		t.Fatalf("missing bounded results: %+v, %v", result, err)
	}
	result, err = search("/memory/**/*.md", []string{"-e", "TARGET"}, 100, 0)
	if err != nil || result.Truncated {
		t.Fatalf("broad search: %+v, %v", result, err)
	}
	paths := make(map[string]int)
	for _, match := range result.Lines {
		paths[match.Path]++
		if strings.Contains(match.Path, "private") {
			t.Fatalf("searched unauthorized content: %+v", match)
		}
	}
	wantPaths := map[string]int{
		"/memory/engineering/a/note.md": 3,
		"/memory/engineering/a.md":      3,
		"/memory/secondary/notes.md":    2,
	}
	for i := range 10 {
		wantPaths[fmt.Sprintf("/memory/engineering/z%02d.md", i)] = 1
	}
	if !maps.Equal(paths, wantPaths) {
		t.Fatalf("wrong search results: %v", paths)
	}
	for _, mode := range []string{"-l", "-c"} {
		result, err := search("/memory/**/*.md", []string{mode, "-e", "TARGET"}, 100, 0)
		if err != nil || len(result.Files) != 13 || result.MatchCount != 13 {
			t.Fatalf("%s: %+v, %v", mode, result, err)
		}
	}
	for _, mode := range []string{"", "-l", "-c"} {
		args := []string{"-e", "TARGET"}
		if mode != "" {
			args = append(args, mode)
		}
		result, err := search("/memory/batch-*/*.txt", args, 100, 0)
		if err != nil || result.Truncated || result.MatchCount != searchStoreBatchSize+1 {
			t.Fatalf("cross-batch %s search: %+v, %v", mode, result, err)
		}
	}
	var lastStorePath string
	if err := fixture.Store.Memories().VisitSearchStores(ctx, scope.ProjectID, agent.ID,
		fmt.Sprintf("/memory/batch-%02d/n.txt", searchStoreBatchSize), func(store memorystore.SearchStore) error {
			lastStorePath = store.Root.Name()
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(lastStorePath, lastStorePath+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lastStorePath+"-moved", lastStorePath); err != nil {
		t.Fatal(err)
	}
	result, err = search("/memory/batch-*/*.txt", []string{"-e", "TARGET"}, 1, 0)
	if err != nil || result.MatchCount != 1 || !result.Truncated {
		t.Fatalf("opened later batch after reaching the limit: %+v, %v", result, err)
	}
	if _, err := search("/memory/batch-*/*.txt", []string{"-e", "TARGET"}, 100, 0); err == nil {
		t.Fatal("searched a store replaced by a symlink")
	}
	if _, err := fixture.Pool.Exec(
		ctx, "UPDATE agents SET current_config_id = $1 WHERE id = $2", fixture.AgentConfig.ID, agent.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := search("/memory/secondary/notes.md", []string{"-e", "TARGET"}, 1, 0); err == nil {
		t.Fatal("search bypassed revoked attachment")
	}
	if _, err := search("/memory/private/secret.md", []string{"-e", "TARGET"}, 1, 0); err == nil {
		t.Fatal("exact search bypassed attachment")
	}
	var physicalPath string
	err = fixture.Store.Memories().VisitSearchStores(
		ctx, scope.ProjectID, agent.ID, "/memory/engineering/a.md",
		func(store memorystore.SearchStore) error {
			physicalPath = filepath.Join(store.Root.Name(), "a.md")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(physicalPath), "link.md")
	if err := os.Symlink(filepath.Join("..", "private", "secret.md"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := search("/memory/engineering/link.md", []string{"-e", "SECRET"}, 1, 0); err == nil {
		t.Fatal("searched a symlink outside the store")
	}
	longPath := strings.Repeat(strings.Repeat("&", 250)+"/", 3) + strings.Repeat("&", 250) + ".md"
	write(engineering.ID, longPath, "TARGET\nTARGET\n")
	result, err = search("/memory/engineering/"+longPath, []string{"-C", "5", "-e", "TARGET"}, 1, 0)
	if err != nil || result.MatchCount != 1 || !result.Truncated {
		t.Fatalf("long-path result: %+v, %v", result, err)
	}
	result, err = search("/memory/engineering/"+longPath, []string{"-C", "5", "-e", "TARGET"}, 100, 2)
	if err != nil || result.MatchCount != 1 || result.Lines[0].LineNumber != 2 || result.Truncated {
		t.Fatalf("long-path offset: %+v, %v", result, err)
	}
	write(engineering.ID, "a:b.md", "TARGET\nTARGET\n")
	result, err = search("/memory/engineering/a:b.md", []string{"-c", "-e", "TARGET"}, 1, 0)
	if err != nil || len(result.Files) != 1 || result.Files[0].Path != "/memory/engineering/a:b.md" ||
		result.Files[0].Count == nil || *result.Files[0].Count != 2 {
		t.Fatalf("count path containing colon: %+v, %v", result, err)
	}
	write(engineering.ID, "é.md", "TARGET\n")
	result, err = search("/memory/engineering/?.md", []string{"-l", "-e", "TARGET"}, 100, 0)
	if err != nil || len(result.Files) != 2 || !slices.ContainsFunc(result.Files, func(file searchFileResult) bool {
		return file.Path == "/memory/engineering/é.md"
	}) {
		t.Fatalf("Unicode glob: %+v, %v", result, err)
	}
	call.Turn.ProjectID = uuid.New()
	if _, err := search("/memory/engineering/a.md", []string{"-e", "TARGET"}, 1, 0); err == nil {
		t.Fatal("cross-project search succeeded")
	}
}
