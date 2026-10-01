//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestListFilesPagination(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "file-list", toolFixtureOptions{withMemory: true})
	_, err := fixture.Pool.Exec(ctx, `
INSERT INTO artifacts(agent_id, content_type, filename, created_at)
SELECT $1, 'text/plain', 'tool-result', statement_timestamp() FROM generate_series(1, 101)`, fixture.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := memorystore.Scope{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Principal: toolsTestUserPrincipal(fixture.User.ID),
	}
	store, err := fixture.Store.Memories().Resolve(ctx, scope.ProjectID, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a/x.md", "a-b.md"} {
		if _, err := fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: store.ID, Path: name, Content: []byte("note"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	call := asyncToolContext{Executor: Executor{Store: fixture.Store}, Turn: fixture.turn()}
	type page struct {
		Entries []listing.FileEntry `json:"entries"`
		Next    *string             `json:"next_cursor"`
	}
	read := func(input listFilesRequest, turn Turn) (page, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return page{}, err
		}
		if err := validateRegisteredToolInput(toolcatalog.ToolNameListFiles, raw); err != nil {
			return page{}, err
		}
		local := call
		local.Turn = turn
		local.Call = model.ToolCall{Name: toolcatalog.ToolNameListFiles, Input: raw}
		result, err := runListFiles(ctx, local)
		if err != nil {
			return page{}, err
		}
		var parts []struct {
			Value page `json:"value"`
		}
		if err := json.Unmarshal(asyncCompletionContent(t, result), &parts); err != nil {
			return page{}, err
		}
		if len(parts) != 1 {
			t.Fatalf("unexpected tool result: %+v", parts)
		}
		return parts[0].Value, nil
	}
	for _, pattern := range []string{"/*", "/**", "/artifacts/tool-result", "/memory/**"} {
		baseline, err := fixture.Store.ListFiles(ctx, toolsTestProjectID, fixture.Agent.ID, pattern, 100, listing.Cursor{})
		if err != nil {
			t.Fatal(err)
		}
		expected := append([]listing.FileEntry{}, baseline.Entries...)
		for baseline.Next.Set {
			baseline, err = fixture.Store.ListFiles(ctx, toolsTestProjectID, fixture.Agent.ID, pattern, 100, baseline.Next)
			if err != nil {
				t.Fatal(err)
			}
			expected = append(expected, baseline.Entries...)
		}
		if pattern == "/artifacts/tool-result" && len(expected) != 101 {
			t.Fatalf("lost duplicate filenames: %d", len(expected))
		}
		for _, limit := range []int{1, 7, 100} {
			request := listFilesRequest{Pattern: pattern, Limit: limit}
			var got []listing.FileEntry
			for pages := 0; ; pages++ {
				if pages > len(expected)+1 {
					t.Fatal("pagination failed to finish")
				}
				result, err := read(request, call.Turn)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, result.Entries...)
				if result.Next == nil {
					break
				}
				if len(result.Entries) == 0 || *result.Next == request.Cursor {
					t.Fatal("cursor did not advance")
				}
				request.Cursor = *result.Next
			}
			if !slices.EqualFunc(got, expected, func(a, b listing.FileEntry) bool { return a.Path == b.Path }) {
				t.Fatalf("%s limit %d: got %d entries, want %d", pattern, limit, len(got), len(expected))
			}
		}
	}
	first, err := read(listFilesRequest{Pattern: "/artifacts/*", Limit: 1}, call.Turn)
	if err != nil || first.Next == nil {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	request := listFilesRequest{Pattern: "/artifacts/*", Limit: 100, Cursor: *first.Next}
	for _, dimension := range []string{"pattern", "agent", "project"} {
		changed, turn := request, call.Turn
		switch dimension {
		case "pattern":
			changed.Pattern = "/**"
		case "agent":
			turn.AgentID = uuid.New()
		case "project":
			turn.ProjectID = uuid.New()
		}
		if _, err := read(changed, turn); !errors.Is(err, errInvalidFileListCursor) {
			t.Fatalf("cursor accepted another %s: %v", dimension, err)
		}
	}
	cursor, err := decodeFileListCursor(*first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, "DELETE FROM artifacts WHERE id = $1", cursor.ID); err != nil {
		t.Fatal(err)
	}
	continued, err := read(request, call.Turn)
	if err != nil || len(continued.Entries) != 100 {
		t.Fatalf("deleted cursor anchor: %d entries, %v", len(continued.Entries), err)
	}
	memoryPage, err := read(listFilesRequest{Pattern: "/memory/**", Limit: 1}, call.Turn)
	if err != nil || memoryPage.Next == nil {
		t.Fatalf("first memory page: %+v, %v", memoryPage, err)
	}
	if err := fixture.Store.Memories().Delete(ctx, scope, store.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := read(listFilesRequest{Pattern: "/memory/**", Cursor: *memoryPage.Next}, call.Turn)
	if err != nil || len(removed.Entries) != 0 || removed.Next != nil {
		t.Fatalf("deleted store remained visible: %+v, %v", removed, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fixture.Store.ListFiles(
		canceled, toolsTestProjectID, fixture.Agent.ID, "/**", 1, listing.Cursor{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled listing succeeded: %v", err)
	}
}
