//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
)

func TestMemoryConcurrentWritesAndReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newMemoryIntegrationStore(t, ctx, pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Admin", "admin")
	scope := memorystore.Scope{
		OrgID:     testOrgID,
		ProjectID: testProjectID,
		Principal: identitystore.PrincipalRecord{
			Type: identitystore.PrincipalTypeUser,
			ID:   admin.ID,
		},
	}
	resource, err := store.Memories().Create(ctx, scope, "engineering", "", false)
	if err != nil {
		t.Fatal(err)
	}
	input := memorystore.WriteInput{
		Scope:   scope,
		StoreID: resource.ID,
		Path:    "deployment/checklist.md",
		Content: []byte("initial"),
	}
	first, err := store.Memories().Write(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan string, 2)
	failures := make(chan error, 2)
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			update := input
			update.ExpectedDigest = &first
			update.Content = []byte(fmt.Sprint("edit ", i))
			r, e := store.Memories().Write(ctx, update)
			if e != nil {
				failures <- e
			} else {
				results <- r
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	if len(results) != 1 || len(failures) != 1 {
		t.Fatalf("successes=%d failures=%d; want one each", len(results), len(failures))
	}
	for e := range failures {
		if !errors.Is(e, storeerr.ErrConflict) {
			t.Fatal(e)
		}
	}
	latest := <-results
	if _, err = store.Memories().Write(ctx, input); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("stale retry: %v", err)
	}
	current, body, err := store.Memories().Read(ctx, scope, resource.ID, input.Path)
	if err != nil || current != latest {
		t.Fatalf("stale retry changed file: %+v %v", current, err)
	}
	input.Content = body
	replay, err := store.Memories().Write(ctx, input)
	if err != nil || replay != latest {
		t.Fatalf("identical retry: %+v %v", replay, err)
	}
	input.Content = []byte("different")
	if _, err = store.Memories().Write(ctx, input); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("unconditional replacement: %v", err)
	}
	input.Path = "deployment"
	if _, err = store.Memories().Write(ctx, input); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("directory collision: %v", err)
	}
	input.Path = "empty.md"
	input.Content = []byte{}
	_, err = store.Memories().Write(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	_, empty, err := store.Memories().Read(ctx, scope, resource.ID, input.Path)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty file: %q %v", empty, err)
	}
	readOnly := true
	if _, err = store.Memories().Update(ctx, scope, resource.ID, nil, &readOnly); err != nil {
		t.Fatal(err)
	}
	input.Path = "blocked.md"
	if _, err = store.Memories().Write(ctx, input); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("read-only write: %v", err)
	}
	if _, _, err = store.Memories().Read(ctx, scope, resource.ID, "empty.md"); err != nil {
		t.Fatal(err)
	}
	foreign := scope
	foreign.ProjectID = uuid.Must(uuid.NewV7())
	if _, _, err = store.Memories().Read(ctx, foreign, resource.ID, "empty.md"); err == nil {
		t.Fatal("cross-project read succeeded")
	}
	if err = store.Memories().Delete(ctx, scope, resource.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Memories().Read(ctx, scope, resource.ID, "empty.md"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("deleted store readable: %v", err)
	}
	replacement, err := store.Memories().Create(ctx, scope, "engineering", "", false)
	if err != nil || replacement.ID == resource.ID {
		t.Fatalf("store identity reused: %+v %v", replacement, err)
	}
}

func TestMemoryAgentAttachmentsAndListing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newMemoryIntegrationStore(t, ctx, pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Listing Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	resource, err := store.Memories().Create(ctx, scope, "engineering", "Shared notes", false)
	if err != nil {
		t.Fatal(err)
	}
	publicID, err := publicid.Encode(publicid.KindMemoryStore, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := testAgentConfigYAML() + "\nmemory_stores:\n  - name: engineering\n    access: read_only\n"
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, source)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
		ResolveMemoryStoreName: func(string) (string, error) { return publicID, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.Execution().CreateAgentConfig(
		ctx,
		executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Source:                  source,
			SourceFormat:            "yaml",
			ConfiguredModelID:       configuredModel.ID,
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{
			ProjectID:       testProjectID,
			CurrentConfigID: config.ID,
		})
	if err != nil {
		t.Fatal(err)
	}
	agentScope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, AgentID: agent.ID}
	for _, path := range []string{
		"a.md",
		"deployment/b.md",
		"deployment/c.md",
	} {
		if _, err = store.Memories().Write(
			ctx,
			memorystore.WriteInput{
				Scope:   scope,
				StoreID: resource.ID,
				Path:    path,
				Content: []byte(path),
			}); err != nil {
			t.Fatal(err)
		}
	}
	artifact, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: testProjectID, AgentID: agent.ID, ContentType: "text/plain", Content: []byte("artifact"),
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: testProjectID, CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: testProjectID, AgentID: otherAgent.ID, ContentType: "text/plain", Content: []byte("other"),
	}); err != nil {
		t.Fatal(err)
	}
	artifactPath := "/artifacts/" + artifactID
	for _, pattern := range []string{"/artifacts/*", artifactPath} {
		result, listErr := store.ListFiles(ctx, testProjectID, agent.ID, pattern, 1)
		if listErr != nil || result.Truncated || len(result.Entries) != 1 || result.Entries[0].Path != artifactPath {
			t.Fatalf("artifact listing %s: %+v %v", pattern, result, listErr)
		}
		entry := result.Entries[0]
		if entry.Type != "file" || entry.SizeBytes == nil || *entry.SizeBytes != 8 || entry.Digest == "" {
			t.Fatalf("artifact metadata: %+v", entry)
		}
	}
	result, err := store.ListFiles(ctx, testProjectID, agent.ID, "/**", 100)
	if err != nil || result.Truncated {
		t.Fatalf("combined listing: %+v %v", result, err)
	}
	var paths []string
	for _, entry := range result.Entries {
		paths = append(paths, entry.Path)
	}
	wantPaths := []string{
		"/artifacts", artifactPath, "/memory", "/memory/engineering", "/memory/engineering/a.md",
		"/memory/engineering/deployment", "/memory/engineering/deployment/b.md", "/memory/engineering/deployment/c.md",
	}
	slices.Sort(paths)
	slices.Sort(wantPaths)
	if !slices.Equal(paths, wantPaths) {
		t.Fatalf("combined listing paths = %v, want %v", paths, wantPaths)
	}
	stores, err := store.ListFiles(ctx, testProjectID, agent.ID, "/memory/*", 1)
	if err != nil || len(stores.Entries) != 1 || stores.Entries[0].Access != "read_only" {
		t.Fatalf("store listing: %+v %v", stores, err)
	}
	first, err := store.ListFiles(ctx, testProjectID, agent.ID, "/memory/engineering/**/*.md", 2)
	if err != nil || !first.Truncated || len(first.Entries) != 2 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if _, _, err = store.Memories().Read(ctx, agentScope, resource.ID, "a.md"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Memories().Write(
		ctx,
		memorystore.WriteInput{
			Scope:   agentScope,
			StoreID: resource.ID,
			Path:    "denied.md",
			Content: []byte("no"),
		}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("read-only attachment wrote: %v", err)
	}
	if err = store.Memories().Delete(ctx, scope, resource.ID); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("deleted active attachment: %v", err)
	}
	if _, err = store.Memories().Resolve(ctx, testProjectID, "missing"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing store resolution: %v", err)
	}
}

func TestMemoryQuotasAndProjectDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newMemoryIntegrationStore(t, ctx, pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Quota Admin", "admin")
	scope := memorystore.Scope{
		OrgID: testOrgID, ProjectID: testProjectID,
		Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: admin.ID},
	}
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{
		"max_active_memory_stores_per_project": 1,
		"max_memories_per_store":               1,
	})
	resource, err := store.Memories().Create(ctx, scope, "team", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Memories().Create(ctx, scope, "extra", "", false); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("store quota: %v", err)
	}
	results := make(chan memorystore.WriteInput, 2)
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, name := range []string{"a.md", "b.md"} {
		wg.Go(func() {
			<-start
			input := memorystore.WriteInput{
				Scope: scope, StoreID: resource.ID, Path: name, Content: []byte("first"),
			}
			digest, err := store.Memories().Write(ctx, input)
			if err != nil {
				failures <- err
			} else {
				input.ExpectedDigest = &digest
				results <- input
			}
		})
	}
	close(start)
	wg.Wait()
	if len(results) != 1 || len(failures) != 1 {
		t.Fatalf("concurrent quota: %d successes, %d failures", len(results), len(failures))
	}
	if err := <-failures; !errors.Is(err, storeerr.ErrConflict) {
		t.Fatal(err)
	}
	input := <-results
	input.Content = []byte("updated")
	if _, err = store.Memories().Write(ctx, input); err != nil {
		t.Fatalf("update at quota: %v", err)
	}
	if _, err = store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, scope.Principal); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Memories().Resolve(ctx, testProjectID, "team"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("deleted project store remains accessible: %v", err)
	}
}

func TestMemoryConfigAllowsReadWriteAttachmentToReadOnlyStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newMemoryIntegrationStore(t, ctx, pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Access Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	resource, err := store.Memories().Create(ctx, scope, "engineering", "", true)
	if err != nil {
		t.Fatal(err)
	}
	publicID, err := publicid.Encode(publicid.KindMemoryStore, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, testAgentConfigYAML())
	createConfig := func(access string) (executionstore.AgentConfigRecord, error) {
		source := testAgentConfigYAML() + "\nmemory_stores:\n  - name: engineering\n    access: " + access + "\n"
		compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
			ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
				return resolvedTestModelSelection(configuredModel), nil
			},
			ResolveMemoryStoreName: func(string) (string, error) { return publicID, nil },
		})
		if err != nil {
			return executionstore.AgentConfigRecord{}, err
		}
		return store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
			ProjectID: testProjectID,
			Source:    source, SourceFormat: "yaml", ConfiguredModelID: configuredModel.ID,
			CompiledDefinition: json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:    agentconfig.CompilerVersion, EffectiveDefinitionHash: compiled.Hash,
		})
	}
	if _, err := createConfig("read_write"); err != nil {
		t.Fatalf("read-write attachment to read-only store: %v", err)
	}
	if _, err := createConfig("read_only"); err != nil {
		t.Fatalf("read-only attachment: %v", err)
	}
	readOnly := false
	if _, err := store.Memories().Update(ctx, scope, resource.ID, nil, &readOnly); err != nil {
		t.Fatal(err)
	}
	config, err := createConfig("read_write")
	if err != nil {
		t.Fatalf("read-write attachment to writable store: %v", err)
	}
	if _, err := store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: scope, StoreID: resource.ID, Path: "reference.md", Content: []byte("reference"),
	}); err != nil {
		t.Fatal(err)
	}
	readOnly = true
	if _, err := store.Memories().Update(ctx, scope, resource.ID, nil, &readOnly); err != nil {
		t.Fatal(err)
	}
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: testProjectID, CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatalf("existing config became invalid: %v", err)
	}
	agentScope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, AgentID: agent.ID}
	if _, _, err := store.Memories().Read(ctx, agentScope, resource.ID, "reference.md"); err != nil {
		t.Fatalf("existing config cannot read: %v", err)
	}
	if _, err := store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: agentScope, StoreID: resource.ID, Path: "blocked.md", Content: []byte("blocked"),
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("existing config bypassed read-only store: %v", err)
	}
	var base agentconfig.Compiled
	if err := json.Unmarshal(config.CompiledDefinition, &base); err != nil {
		t.Fatal(err)
	}
	override := agentconfig.SubagentCompiled{Type: agentconfig.SubagentTypeSelf, InstructionAppend: "Extra instruction"}
	depth := agentconfig.SubagentDepth{Depth: 1}
	child, err := agentconfig.SubagentCompiledFrom(base, override, depth, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := agentconfig.EncodeCompiled(child)
	if err != nil {
		t.Fatal(err)
	}
	derived := executionstore.CreateAgentConfigInput{
		ProjectID:          testProjectID,
		ConfiguredModelID:  configuredModel.ID,
		CompiledDefinition: json.RawMessage(encoded.CanonicalJSON), CompilerVersion: agentconfig.CompilerVersion,
		EffectiveDefinitionHash: encoded.Hash,
	}
	if _, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: derived, AgentID: agent.ID, ExpectedCurrentConfigID: config.ID,
		ActorType: identitystore.PrincipalTypeUser, ActorID: admin.ID,
	}); err != nil {
		t.Fatalf("read-only store blocked instruction change: %v", err)
	}
	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: testProjectID, DerivedConfig: &derived, LaunchedBy: userPrincipal(admin.ID),
	}); err != nil {
		t.Fatalf("read-only store blocked inherited attachment: %v", err)
	}
}

func TestListFilesScopedFilesystem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newMemoryIntegrationStore(t, ctx, pool)
	listStore := store
	admin := createSecretTestUser(t, ctx, store, "File Listing Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	stores := make(map[string]memorystore.Record)
	source := testAgentConfigYAML() + "\nmemory_stores:\n"
	for _, name := range []string{"z", "a-b", "a", "unattached"} {
		record, err := store.Memories().Create(ctx, scope, name, "Notes for "+name, false)
		if err != nil {
			t.Fatal(err)
		}
		stores[name] = record
		if name != "unattached" {
			source += "  - name: " + name + "\n    access: read_only\n"
		}
	}
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, source)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
		ResolveMemoryStoreName: func(name string) (string, error) {
			return publicid.Encode(publicid.KindMemoryStore, stores[name].ID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID: testProjectID,
		Source:    source, SourceFormat: "yaml", ConfiguredModelID: configuredModel.ID,
		CompiledDefinition: json.RawMessage(compiled.CanonicalJSON), CompilerVersion: agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: testProjectID, CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string][]string{
		"a": {"root.md", "dir/deep/note.md", "dir/a.txt", "a-b.txt", "a/z.md", "a b.txt", "a.b.txt", "a0.txt",
			"weird[1]%_文.md", "folder.md/child.txt", "prefix/\U0001f600.md"},
		"a-b": {"next.md"}, "z": {"last.md", "last.txt"}, "unattached": {"hidden.md"},
	}
	all := []FileEntry{{Path: "/artifacts", Type: "directory"}, {Path: "/memory", Type: "directory"}}
	for _, name := range []string{"a", "a-b", "z", "unattached"} {
		for _, path := range fixtures[name] {
			if _, err := store.Memories().Write(ctx, memorystore.WriteInput{
				Scope: scope, StoreID: stores[name].ID, Path: path, Content: []byte("test"),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if name == "unattached" {
			continue
		}
		if name == "a" {
			root, err := store.memoryFS.OpenStore(memoryFilesystemRef(t, scope, stores[name]))
			if err != nil {
				t.Fatal(err)
			}
			err = root.Mkdir("empty", 0700)
			_ = root.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		root := "/memory/" + name
		all = append(all, FileEntry{Path: root, Type: "directory"})
		children := make(map[string]string)
		if name == "a" {
			children["empty"] = "directory"
		}
		for _, path := range fixtures[name] {
			children[path] = "file"
			parts := strings.Split(path, "/")
			for n := 1; n < len(parts); n++ {
				children[strings.Join(parts[:n], "/")] = "directory"
			}
		}
		var paths []string
		for path := range children {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		for _, path := range paths {
			all = append(all, FileEntry{Path: root + "/" + path, Type: children[path]})
		}
	}
	lock, err := store.memoryFS.Lock(ctx, memoryFilesystemRef(t, scope, stores["a"]))
	if err != nil {
		t.Fatal(err)
	}
	listCtx, cancel := context.WithTimeout(ctx, time.Second)
	result, listErr := listStore.ListFiles(listCtx, testProjectID, agent.ID, "/memory/a/root.md", 1)
	cancel()
	_ = lock.Close()
	if listErr != nil || len(result.Entries) != 1 {
		t.Fatalf("listing waited for writer lock: %+v %v", result, listErr)
	}
	for _, test := range []struct {
		pattern, want string
		truncated     bool
	}{
		{"/memory/?", "/memory/a", true},
		{"/memory/*z", "/memory/z", false},
		{"/memory/*/last.md", "/memory/z/last.md", false},
	} {
		result, err := listStore.ListFiles(ctx, testProjectID, agent.ID, test.pattern, 1)
		if err != nil || len(result.Entries) != 1 ||
			result.Entries[0].Path != test.want || result.Truncated != test.truncated {
			t.Fatalf("limited listing %s: %+v %v", test.pattern, result, err)
		}
	}
	rowLimit := int32(1)
	rows, err := dbsqlc.New(pool).ListAttachedMemoryStores(ctx, dbsqlc.ListAttachedMemoryStoresParams{
		ProjectID: testProjectID, StoreIds: []uuid.UUID{stores["a"].ID, stores["a-b"].ID, stores["z"].ID},
		RootPattern: "^/memory/[az]$", RowLimit: &rowLimit,
	})
	if err != nil || len(rows) != 1 || rows[0].Name != "a" {
		t.Fatalf("store query limit: %+v %v", rows, err)
	}
	otherProjectID := testID("memory_listing_other_project")
	if _, err := pool.Exec(ctx, `
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Other Project', 'memory-listing-other-project', statement_timestamp(), statement_timestamp())
`, otherProjectID, testOrgID); err != nil {
		t.Fatal(err)
	}
	otherScope := scope
	otherScope.ProjectID = otherProjectID
	foreign, err := store.Memories().Create(ctx, otherScope, "a", "Other project's notes", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: otherScope, StoreID: foreign.ID, Path: "cross-project.md",
	}); err != nil {
		t.Fatal(err)
	}
	agentScope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, AgentID: agent.ID}
	for _, record := range []memorystore.Record{stores["unattached"], foreign} {
		path := "hidden.md"
		if record.ID == foreign.ID {
			path = "cross-project.md"
		}
		if _, _, err := store.Memories().Read(ctx, agentScope, record.ID, path); !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("unauthorized store read: %v", err)
		}
		if _, err := store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: agentScope, StoreID: record.ID, Path: "blocked.md", Content: []byte("blocked"),
		}); !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("unauthorized store write: %v", err)
		}
	}
	if _, err := listStore.ListFiles(
		ctx, otherProjectID, agent.ID, "/memory/**", 100,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("cross-project agent listing: %v", err)
	}
	for _, test := range []struct {
		pattern string
		paths   []string
	}{
		{"/memory/*/*.md", []string{
			"/memory/a/folder.md", "/memory/a/root.md", "/memory/a/weird[1]%_文.md",
			"/memory/a-b/next.md", "/memory/z/last.md",
		}},
		{"/memory/*/**/*.txt", []string{
			"/memory/a/a b.txt", "/memory/a/a-b.txt", "/memory/a/a.b.txt", "/memory/a/a0.txt",
			"/memory/a/dir/a.txt", "/memory/a/folder.md/child.txt", "/memory/z/last.txt",
		}},
	} {
		result, err := listStore.ListFiles(ctx, testProjectID, agent.ID, test.pattern, 100)
		if err != nil || result.Truncated {
			t.Fatalf("multi-store listing %s: %+v %v", test.pattern, result, err)
		}
		var paths []string
		for _, entry := range result.Entries {
			paths = append(paths, entry.Path)
		}
		slices.Sort(paths)
		slices.Sort(test.paths)
		if !slices.Equal(paths, test.paths) {
			t.Fatalf("multi-store listing %s: got %v, want %v", test.pattern, paths, test.paths)
		}
	}
	for _, pattern := range []string{
		"/*", "/memory", "/memory/*", "/memory/a", "/memory/a/*", "/memory/a/**", "/memory/a/**/*.md",
		"/memory/a/dir", "/memory/a/dir/*", "/memory/a/dir/**", "/memory/a/dir/deep/note.md",
		"/memory/a/dir/missing", "/memory/a/dir*/*.md", "/memory/a*/*", "/memory/a*/**",
		"/**/a/**/*.md", "/**/**/*.md", "/**/*/*.md", "/memory/?", "/memory/文*",
		"/memory/a/weird[1]%_?.md", "/memory/a/prefix/\U0001f600*", "/mem*/a/**/*.md", "/**/note.md", "/**",
		"/memory/missing/**", "/memory/unattached", "/memory/unattached/**", "/memory/unattached/hidden.md",
		"/memory/a/**/*.pdf", "/memory/*/**/*.md", "/memory/*/cross-project.md",
		"/memory/a/shared/*", "/memory/a/shared/**", "/memory/a/shared/one/t*/**", "/memory/a/shared/**/*.md",
	} {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			matcher, compileErr := CompileFilePattern(pattern)
			if compileErr != nil {
				t.Fatal(compileErr)
			}
			var want []FileEntry
			for _, entry := range all {
				if matcher.MatchString(entry.Path) {
					want = append(want, entry)
				}
			}
			result, listErr := listStore.ListFiles(ctx, testProjectID, agent.ID, pattern, 100)
			if result.Truncated {
				t.Fatal("unexpected truncation")
			}
			got := result.Entries
			slices.SortFunc(got, func(a, b FileEntry) int { return strings.Compare(a.Path, b.Path) })
			slices.SortFunc(want, func(a, b FileEntry) int { return strings.Compare(a.Path, b.Path) })
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d entries, want %d", len(got), len(want))
			}
			for i, entry := range got {
				if entry.Path != want[i].Path || entry.Type != want[i].Type {
					t.Fatalf("entry %d: got %+v, want %+v", i, entry, want[i])
				}
				if entry.Type == "file" && (entry.Digest != "" || entry.SizeBytes == nil) {
					t.Fatalf("missing file metadata: %+v", entry)
				}
			}
		})
	}
}

func TestListFilesArtifactNamesAndTruncation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	agentID := mustCreateAgent(t, ctx, store)
	otherAgentID := mustCreateAgent(t, ctx, store)
	filenames := []string{
		"report.pdf", "report.pdf", "report.pdf", "", "folder/name.pdf", "folder\\name.pdf",
		"line\nbreak.pdf", "note.TXT", "résumé文.md",
	}
	var artifactPaths []string
	for i, filename := range filenames {
		id := uuid.MustParse(fmt.Sprintf(
			"%02x000000-0000-7000-8000-000000000001", []byte{0, 1, 208, 224, 240, 241, 242, 243, 244}[i],
		))
		publicID, err := publicid.Encode(publicid.KindArtifact, id)
		if err != nil {
			t.Fatal(err)
		}
		artifactPaths = append(artifactPaths, "/artifacts/"+publicID)
		_, err = pool.Exec(ctx, `INSERT INTO artifacts(id,agent_id,content_type,filename,digest,size_bytes,created_at)
VALUES($1,$2,'application/octet-stream',nullif($3,''),'test-digest',4,statement_timestamp())`, id, agentID, filename)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO artifacts(agent_id,content_type,filename,created_at)
VALUES($1,'application/pdf','report.pdf',statement_timestamp())`, otherAgentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		pattern string
		matches []int
	}{
		{"/artifacts/*", []int{0, 1, 2, 3, 4, 5, 6, 7, 8}},
		{"/artifacts/*.pdf", []int{0, 1, 2, 4, 5, 6}},
		{"/artifacts/report.pdf", []int{0, 1, 2}},
		{"/artifacts/name.pdf", []int{4, 5}},
		{"/artifacts/*.txt", nil},
		{"/artifacts/*.TXT", []int{7}},
		{"/artifacts/résumé?.md", []int{8}},
		{"/artifacts/missing*", nil},
		{"/artifacts/**", []int{0, 1, 2, 3, 4, 5, 6, 7, 8}},
		{artifactPaths[2], []int{2}},
	} {
		t.Run(test.pattern, func(t *testing.T) {
			t.Parallel()
			entries, listErr := collectFileListing(ctx, store, agentID, test.pattern, 100)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(entries) != len(test.matches) {
				t.Fatalf("got %d entries, want %d", len(entries), len(test.matches))
			}
			for i, n := range test.matches {
				entry := entries[i]
				if entry.Path != artifactPaths[n] || entry.Filename != filenames[n] || entry.Type != "file" ||
					entry.Digest != "test-digest" || entry.SizeBytes == nil || *entry.SizeBytes != 4 {
					t.Fatalf("entry %d: %+v", i, entry)
				}
			}
		})
	}
	combined, err := collectFileListing(ctx, store, agentID, "/**", 100)
	if err != nil {
		t.Fatal(err)
	}
	var gotPaths []string
	for _, entry := range combined {
		gotPaths = append(gotPaths, entry.Path)
	}
	wantPaths := append([]string{"/artifacts"}, artifactPaths...)
	wantPaths = append(wantPaths, "/memory")
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("combined listing: %v, want %v", gotPaths, wantPaths)
	}
	first, err := store.ListFiles(ctx, testProjectID, agentID, "/artifacts/*", 1)
	if err != nil || !first.Truncated || len(first.Entries) != 1 {
		t.Fatalf("truncated artifacts: %+v %v", first, err)
	}
	other, err := store.ListFiles(ctx, testProjectID, otherAgentID, "/artifacts/*", 100)
	if err != nil || len(other.Entries) != 1 || other.Entries[0].Path == first.Entries[0].Path {
		t.Fatalf("artifact scope: %+v %v", other, err)
	}

}

func TestFilePatternSQLMatchesGo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	for _, test := range []struct {
		pattern, path string
		match         bool
	}{
		{"/memory/team/**/*.md", "/memory/team/a.md", true},
		{"/memory/team/**/*.md", "/memory/team/deep/a.md", true},
		{"/memory/team/**/**/?.md", "/memory/team/é.md", true},
		{"/memory/team/*[x].md", "/memory/team/a[x].md", true},
		{"/memory/team/*[x].md", "/memory/team/ax.md", false},
		{"/memory/team/a%_?.md", "/memory/team/a%_文.md", true},
		{"/memory/team/*", "/memory/team/deep/a.md", false},
		{"/**", "/artifacts/report\nfinal.pdf", true},
		{"/artifacts/*", "/artifacts/report\nfinal.pdf", true},
		{"/memory/team/**", "/memory/team", false},
		{"/memory/team/a+b.(md)", "/memory/team/a+b.(md)", true},
		{"/memory/team/a{2}^$|.md", "/memory/team/a{2}^$|.md", true},
	} {
		matcher, err := CompileFilePattern(test.pattern)
		if err != nil {
			t.Fatal(err)
		}
		var sqlMatch bool
		err = pool.QueryRow(ctx, `SELECT $1::text COLLATE "C" ~ $2::text`, test.path, matcher.String()).Scan(&sqlMatch)
		if err != nil {
			t.Fatal(err)
		}
		if sqlMatch != test.match || matcher.MatchString(test.path) != test.match {
			t.Fatalf(
				"%q matching %q: SQL %v, Go %v, want %v",
				test.pattern, test.path, sqlMatch, matcher.MatchString(test.path), test.match,
			)
		}
	}
}

func newMemoryIntegrationStore(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *Store {
	t.Helper()
	files, err := memorystore.OpenFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	return newIntegrationStore(pool, WithMemoryFilesystem(files), WithBlobStore(integrationblob.MustOpen(t, ctx)))
}

func collectFileListing(
	ctx context.Context,
	store *Store,
	agentID uuid.UUID,
	pattern string,
	limit int,
) ([]FileEntry, error) {
	result, err := store.ListFiles(ctx, testProjectID, agentID, pattern, limit)
	if err == nil && result.Truncated {
		err = errors.New("unexpected truncation")
	}
	return result.Entries, err
}

func TestMemoryWaitingUploadRechecksPolicyAndDeletion(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"read_only", "store_delete", "project_delete", "org_delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			dir := t.TempDir()
			files, err := memorystore.OpenFilesystem(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = files.Close() }()
			store := newIntegrationStore(pool, WithMemoryFilesystem(files))
			admin := createSecretTestUser(t, ctx, store, "Memory Race Admin", "admin")
			scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
			resource, err := store.Memories().Create(ctx, scope, "race", "", false)
			if err != nil {
				t.Fatal(err)
			}
			first, err := store.Memories().Write(ctx, memorystore.WriteInput{
				Scope: scope, StoreID: resource.ID, Path: "file", Content: []byte("first"),
			})
			if err != nil {
				t.Fatal(err)
			}
			lock, err := files.Lock(ctx, memoryFilesystemRef(t, scope, resource))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()
			written := make(chan error, 1)
			go func() {
				_, err := store.Memories().Write(ctx, memorystore.WriteInput{
					Scope: scope, StoreID: resource.ID, Path: "file",
					Content: []byte("second"), ExpectedDigest: &first,
				})
				written <- err
			}()
			org := mustPublicID(t, publicid.KindOrganization, scope.OrgID)
			project := mustPublicID(t, publicid.KindProject, scope.ProjectID)
			contentPath := filepath.Join(dir, org, project, resource.Name)
			staging := filepath.Join(dir, ".staging", org, project, resource.ID.String())
			for {
				entries, err := os.ReadDir(staging)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) > 0 {
					break
				}
				select {
				case err := <-written:
					t.Fatalf("upload did not wait: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			deleted := make(chan error, 1)
			switch operation {
			case "read_only", "store_delete":
				waiting, stop := context.WithTimeout(ctx, 60*time.Millisecond)
				readOnly := true
				if operation == "read_only" {
					_, err = store.Memories().Update(waiting, scope, resource.ID, nil, &readOnly)
				} else {
					err = store.Memories().Delete(waiting, scope, resource.ID)
				}
				stop()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("management bypassed filesystem lock: %v", err)
				}
				if operation == "read_only" {
					_, err = pool.Exec(ctx, `UPDATE memory_stores SET read_only=true WHERE id=$1`, resource.ID)
				} else {
					_, err = pool.Exec(ctx, `UPDATE memory_stores SET deleted_at=statement_timestamp() WHERE id=$1`, resource.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
			default:
				go func() {
					var err error
					if operation == "project_delete" {
						_, err = store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, scope.Principal)
					} else {
						_, err = store.Organizations().DeleteOrganization(ctx, testOrgID, scope.Principal)
					}
					deleted <- err
				}()
				select {
				case err := <-deleted:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("scope deletion waited for a store lock")
				}
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-written:
				want := storeerr.ErrNotFound
				if operation == "read_only" {
					want = storeerr.ErrConflict
				}
				if !errors.Is(err, want) {
					t.Fatalf("waiting upload: got %v want %v", err, want)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if operation == "project_delete" || operation == "org_delete" {
				for _, area := range []string{
					contentPath,
					staging,
				} {
					if _, err := os.Stat(area); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("%s survived deletion: %v", area, err)
					}
				}
			} else {
				body, err := os.ReadFile(filepath.Join(contentPath, "file"))
				if err != nil || string(body) != "first" {
					t.Fatalf("waiting upload changed content: %q %v", body, err)
				}
			}
		})
	}
}

func TestMemoryStoreDeletionPreservesProfileManagement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newMemoryIntegrationStore(t, ctx, pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Profile Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	resource, err := store.Memories().Create(ctx, scope, "engineering", "Shared notes", false)
	if err != nil {
		t.Fatal(err)
	}
	publicID, err := publicid.Encode(publicid.KindMemoryStore, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := testAgentConfigYAML() + "\nmemory_stores:\n  - name: engineering\n    access: read_only\n"
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, source)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
		ResolveMemoryStoreName: func(string) (string, error) { return publicID, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.Execution().CreateAgentConfig(
		ctx,
		executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Source:                  source,
			SourceFormat:            "yaml",
			ConfiguredModelID:       configuredModel.ID,
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: testProjectID, Name: "memory-profile", CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Execution().GetAgentProfile(ctx, testProjectID, profile.ID); err != nil {
		t.Fatalf("before delete: %v", err)
	}
	if err := store.Memories().Delete(ctx, scope, resource.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: testProjectID, ProfileID: profile.ID, AgentConfigID: config.ID, LaunchedBy: userPrincipal(admin.ID),
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Errorf("launch with deleted store must remain blocked, got: %v", err)
	}
	if _, err := store.Execution().GetAgentProfile(ctx, testProjectID, profile.ID); err != nil {
		t.Errorf("existing profile GET after deleting referenced store: %v", err)
	}
	if _, err := store.Execution().RenameAgentProfile(ctx, executionstore.RenameAgentProfileInput{
		ProjectID: testProjectID, ProfileID: profile.ID, Name: "renamed-memory-profile",
	}); err != nil {
		t.Errorf("existing profile rename after deleting referenced store: %v", err)
	}
}
