//go:build integration

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestWriteMemoryWithoutMachine(t *testing.T) {
	setupFileExec(t)
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "memory-write", toolFixtureOptions{withMemory: true})
	memories := fixture.Store.Memories()
	scope := memorystore.Scope{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Principal: toolsTestUserPrincipal(fixture.User.ID),
	}
	store, err := memories.Resolve(ctx, scope.ProjectID, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ReplaceAll(fixture.AgentConfig.Source, "access: read_only", "access: read_write")
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
	call := asyncToolContext{Executor: Executor{Store: fixture.Store}, Turn: fixture.turn()}
	call.Turn.AgentID = agent.ID
	write := func(request map[string]any) (string, error) {
		input, err := json.Marshal(request)
		if err != nil {
			return "", err
		}
		local := call
		local.Call = model.ToolCall{Name: toolcatalog.ToolNameWriteFile, Input: input}
		result, err := runWriteFileAsync(ctx, local)
		if err != nil {
			return "", err
		}
		var parts []struct {
			Value map[string]string `json:"value"`
		}
		if err := json.Unmarshal(asyncCompletionContent(t, result), &parts); err != nil {
			return "", err
		}
		if len(parts) != 1 || len(parts[0].Value) != 2 || parts[0].Value["path"] != request["path"] {
			return "", fmt.Errorf("unexpected write result: %+v", parts)
		}
		return parts[0].Value["digest"], nil
	}
	path := "/memory/engineering/nested/note.txt"
	check := func(want string) {
		t.Helper()
		digest, content, err := memories.Read(ctx, scope, store.ID, "nested/note.txt")
		if err != nil || string(content) != want || digest != blobstore.ContentDigest(content) {
			t.Fatalf("file = %q, %s, %v; want %q", content, digest, err, want)
		}
	}
	first, err := write(map[string]any{"path": path, "content": "é😀", "append": true})
	if err != nil || first != blobstore.ContentDigest([]byte("é😀")) {
		t.Fatalf("create: %s, %v", first, err)
	}
	request := map[string]any{"path": path, "content": " tail", "append": true, "expected_digest": first}
	second, err := write(request)
	if err != nil {
		t.Fatal(err)
	}
	check("é😀 tail")
	if _, err := write(request); !errors.Is(err, storeerr.ErrConflict) ||
		!strings.Contains(err.Error(), "memory changed;") {
		t.Fatalf("append replay: %v", err)
	}
	for _, test := range []struct {
		input   map[string]any
		message string
	}{
		{map[string]any{"path": path, "content": "stale", "expected_digest": first}, "memory changed;"},
		{map[string]any{"path": path, "script": "d", "expected_digest": first}, "memory changed;"},
		{map[string]any{"path": path, "content": "unguarded"}, "expected_digest is required"},
		{map[string]any{"path": path, "content": "tail", "append": true}, "expected_digest is required"},
		{map[string]any{"path": path, "script": "d"}, "expected_digest is required"},
		{
			map[string]any{"path": "/memory/engineering/missing.txt", "content": "x", "expected_digest": first},
			"memory file does not exist",
		},
	} {
		if _, err := write(test.input); !errors.Is(err, storeerr.ErrConflict) ||
			!strings.Contains(err.Error(), test.message) {
			t.Fatalf("write %v: %v; want conflict containing %q", test.input, err, test.message)
		}
	}
	check("é😀 tail")
	if digest, err := write(map[string]any{"path": path, "content": "é😀 tail"}); err != nil || digest != second {
		t.Fatalf("identical replacement: %s, %v", digest, err)
	}
	results := make(chan error, 2)
	for _, suffix := range []string{"A", "B"} {
		go func() {
			_, err := write(map[string]any{"path": path, "content": suffix, "append": true, "expected_digest": second})
			results <- err
		}()
	}
	wins, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			wins++
		case errors.Is(err, storeerr.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("parallel appends: %d wins, %d conflicts", wins, conflicts)
	}
	digest, content, err := memories.Read(ctx, scope, store.ID, "nested/note.txt")
	if err != nil || (string(content) != "é😀 tailA" && string(content) != "é😀 tailB") {
		t.Fatalf("parallel append content: %q, %v", content, err)
	}
	if runtime.GOOS == "linux" {
		digest, err = write(map[string]any{"path": path, "script": "s/tail[AB]/done/", "expected_digest": digest})
		if err != nil {
			t.Fatal(err)
		}
		check("é😀 done")
		for _, script := range []string{"s/[//", "r /etc/passwd", "s/done/secret/e", "s/done/\\x00/", "s/done/\\xFF/", "Q1"} {
			if _, err := write(map[string]any{"path": path, "script": script, "expected_digest": digest}); err == nil {
				t.Fatalf("invalid script accepted: %q", script)
			}
			check("é😀 done")
		}
	}
	if _, err := write(map[string]any{"path": "/memory/engineering/empty.txt", "content": ""}); err != nil {
		t.Fatal(err)
	}
	private, err := memories.Create(ctx, scope, "private", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write(map[string]any{
		"path": "/memory/" + private.Name + "/note.txt", "content": "x",
	}); !storeerr.IsNotFound(err) {
		t.Fatalf("unattached store write: %v", err)
	}
	call.Turn.AgentID = fixture.Agent.ID
	if _, err := write(map[string]any{
		"path": path, "content": "x", "expected_digest": digest,
	}); !errors.Is(err, storeerr.ErrConflict) || !strings.Contains(err.Error(), "attachment is read-only") {
		t.Fatalf("read-only attachment write: %v", err)
	}
	call.Turn.AgentID = agent.ID
	readOnly := true
	if _, err := memories.Update(ctx, scope, store.ID, nil, &readOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := write(map[string]any{
		"path": path, "content": "x", "expected_digest": digest,
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("read-only store write: %v", err)
	}
}

func TestEditFileTextLimits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file scripts require Linux confinement")
	}
	setupFileExec(t)
	if err := CheckFileToolSupport(t.Context()); err != nil {
		t.Fatalf("file tool support: %v", err)
	}
	for _, test := range []struct {
		name, script, input, want string
		failure                   string
	}{
		{name: "extended regex", script: "s/a+/é/g", input: "aaa\n", want: "é\n"},
		{name: "empty input", script: "s/a/b/"},
		{name: "delete", script: "d", input: "abc\n"},
		{name: "syntax error", script: "s/a/b", input: "a", failure: "char 5: unterminated"},
		{name: "empty diagnostic", script: "Q1", input: "a", failure: "script execution failed: exit status 1"},
		{name: "sandbox read", script: "r /etc/passwd", input: "x", failure: "sandbox"},
		{name: "sandbox write", script: "w /tmp/forbidden", input: "x", failure: "sandbox"},
		{name: "sandbox execute", script: "e id", input: "x", failure: "sandbox"},
		{name: "memory limit", script: ":a;G;h;ba", input: "x\n", failure: "memory exhausted"},
		{name: "output limit", script: ":a;p;ba", input: strings.Repeat("x", 1024), failure: "at most 10 MiB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			got, err := editFileText(ctx, []byte(test.input), test.script)
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) || got != nil {
					t.Fatalf("output = %q, error = %v", got, err)
				}
				if test.name == "syntax error" && strings.Contains(err.Error(), "\n") {
					t.Fatalf("unexpected newline in syntax diagnostic: %v", err)
				}
			} else if err != nil || string(got) != test.want {
				t.Fatalf("output = %q, error = %v, want %q", got, err, test.want)
			}
		})
	}
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "parent deadline", cause: context.DeadlineExceeded},
		{name: "tool deadline", cause: errors.New("simplify the script and retry")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeoutCause(t.Context(), 100*time.Millisecond, test.cause)
			defer cancel()
			output, err := editFileText(ctx, []byte("x"), ":a;ba")
			if output != nil || !errors.Is(err, test.cause) || !strings.Contains(err.Error(), "script execution timed out:") {
				t.Fatalf("loop output = %q, error = %v", output, err)
			}
			if !errors.Is(test.cause, context.DeadlineExceeded) &&
				(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
				t.Fatalf("tool timeout reported as interrupted: %v", err)
			}
		})
	}
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	large := bytes.Repeat([]byte("x"), daemonprotocol.MaxFileTransferBytes)
	output, err := editFileText(ctx, large, "s/x/y/g")
	if err != nil || !bytes.Equal(output, bytes.Repeat([]byte("y"), len(large))) {
		t.Fatalf("maximum file transform: %v", err)
	}
}
