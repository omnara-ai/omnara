//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestStorageListPagination(t *testing.T) {
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
		Entries    []listing.FileEntry `json:"entries"`
		HasMore    bool                `json:"has_more"`
		NextOffset *int                `json:"next_offset"`
	}
	read := func(input storageListRequest) (page, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return page{}, err
		}
		if err := validateRegisteredToolInput(toolcatalog.ToolNameStorageList, raw); err != nil {
			return page{}, err
		}
		local := call
		local.Call = model.ToolCall{Name: toolcatalog.ToolNameStorageList, Input: raw}
		result, err := runStorageList(ctx, local)
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
		if parts[0].Value.HasMore != (parts[0].Value.NextOffset != nil) {
			t.Fatalf("next_offset must be present exactly when has_more: %+v", parts[0].Value)
		}
		return parts[0].Value, nil
	}
	for _, pattern := range []string{"/artifacts", "/memory", "/artifacts/*", "/artifacts/tool-result", "/memory/**"} {
		var expected []listing.FileEntry
		for offset := 0; ; {
			baseline, err := fixture.Store.ListFiles(ctx, toolsTestProjectID, fixture.Agent.ID, pattern, offset, 100)
			if err != nil {
				t.Fatal(err)
			}
			expected = append(expected, baseline.Entries...)
			if !baseline.HasMore {
				break
			}
			if len(baseline.Entries) == 0 {
				t.Fatal("store pagination did not advance")
			}
			offset += len(baseline.Entries)
		}
		if pattern == "/artifacts/tool-result" && len(expected) != 101 {
			t.Fatalf("lost duplicate filenames: %d", len(expected))
		}
		if (pattern == "/artifacts" || pattern == "/memory") &&
			(len(expected) != 1 || expected[0].Path != pattern || expected[0].Type != listing.FileTypeDirectory) {
			t.Fatalf("root listing %s: %+v", pattern, expected)
		}
		for _, limit := range []int{1, 7, 100} {
			request := storageListRequest{Pattern: pattern, Limit: limit}
			var got []listing.FileEntry
			for pages := 0; ; pages++ {
				if pages > len(expected)+1 {
					t.Fatal("pagination failed to finish")
				}
				result, err := read(request)
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Entries) > limit {
					t.Fatalf("%s limit %d: page returned %d entries", pattern, limit, len(result.Entries))
				}
				got = append(got, result.Entries...)
				if !result.HasMore {
					break
				}
				if len(result.Entries) == 0 || *result.NextOffset != request.Offset+len(result.Entries) {
					t.Fatalf("offset did not advance: request=%d entries=%d next=%d",
						request.Offset, len(result.Entries), *result.NextOffset)
				}
				request.Offset = *result.NextOffset
			}
			if !slices.EqualFunc(got, expected, func(a, b listing.FileEntry) bool { return a.Path == b.Path }) {
				t.Fatalf("%s limit %d: got %d entries, want %d", pattern, limit, len(got), len(expected))
			}
			// has_more is exact: a page that ends on the last entry reports no more.
			if len(expected) > 0 && len(expected)%limit == 0 {
				last, err := read(storageListRequest{Pattern: pattern, Limit: limit, Offset: len(expected) - limit})
				if err != nil || len(last.Entries) != limit || last.HasMore {
					t.Fatalf("%s limit %d: exact final page %+v, %v", pattern, limit, last, err)
				}
			}
		}
		for _, offset := range []int{len(expected), len(expected) + 5, toolcatalog.StorageListMaxOffset} {
			past, err := read(storageListRequest{Pattern: pattern, Offset: offset})
			if err != nil || len(past.Entries) != 0 || past.HasMore {
				t.Fatalf("%s offset %d past the end: %+v, %v", pattern, offset, past, err)
			}
		}
	}
	for _, pattern := range []string{"/*", "/**/review-note.txt", "/mem*/*", "/unknown/*"} {
		_, err := fixture.Store.ListFiles(ctx, toolsTestProjectID, fixture.Agent.ID, pattern, 0, 100)
		if !errors.Is(err, storeerr.ErrInvalidRequest) || !strings.Contains(err.Error(), "/memory or /artifacts") {
			t.Fatalf("invalid root %s: %v", pattern, err)
		}
	}
	for _, offset := range []int{-1, toolcatalog.StorageListMaxOffset + 1} {
		_, err := fixture.Store.ListFiles(ctx, toolsTestProjectID, fixture.Agent.ID, "/artifacts/*", offset, 100)
		if !errors.Is(err, storeerr.ErrInvalidRequest) || !strings.Contains(err.Error(), "offset must be between") {
			t.Fatalf("invalid offset %d: %v", offset, err)
		}
		if _, err := read(storageListRequest{Pattern: "/artifacts/*", Offset: offset}); err == nil {
			t.Fatalf("tool accepted offset %d", offset)
		}
	}
	first, err := read(storageListRequest{Pattern: "/artifacts/*", Limit: 100})
	if err != nil || len(first.Entries) != 100 || !first.HasMore || *first.NextOffset != 100 {
		t.Fatalf("first page: %d entries, has_more=%t, %v", len(first.Entries), first.HasMore, err)
	}
	exact, err := read(storageListRequest{Pattern: "/artifacts/*", Offset: 1, Limit: 100})
	if err != nil || len(exact.Entries) != 100 || exact.HasMore {
		t.Fatalf("exact final page: %d entries, has_more=%t, %v", len(exact.Entries), exact.HasMore, err)
	}
	single, err := read(storageListRequest{Pattern: first.Entries[0].Path})
	if err != nil || len(single.Entries) != 1 || single.Entries[0].Path != first.Entries[0].Path || single.HasMore {
		t.Fatalf("exact artifact listing: %+v, %v", single, err)
	}
	if single, err = read(storageListRequest{Pattern: first.Entries[0].Path, Offset: 1}); err != nil ||
		len(single.Entries) != 0 || single.HasMore {
		t.Fatalf("exact artifact listing past its only entry: %+v, %v", single, err)
	}
	artifactID, err := publicid.Decode(publicid.KindArtifact, strings.TrimPrefix(first.Entries[0].Path, "/artifacts/"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, "DELETE FROM artifacts WHERE id = $1", artifactID); err != nil {
		t.Fatal(err)
	}
	// Offsets index the current listing, so removing an earlier entry shifts later ones down.
	continued, err := read(storageListRequest{Pattern: "/artifacts/*", Offset: 1, Limit: 100})
	if err != nil || len(continued.Entries) != 99 || continued.HasMore ||
		continued.Entries[0].Path != first.Entries[2].Path {
		t.Fatalf("listing after deleting an earlier entry: %d entries, %v", len(continued.Entries), err)
	}
	memoryPage, err := read(storageListRequest{Pattern: "/memory/**", Limit: 1})
	if err != nil || !memoryPage.HasMore {
		t.Fatalf("first memory page: %+v, %v", memoryPage, err)
	}
	if err := fixture.Store.Memories().Delete(ctx, scope, store.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := read(storageListRequest{Pattern: "/memory/**", Offset: *memoryPage.NextOffset})
	if err != nil || len(removed.Entries) != 0 || removed.HasMore {
		t.Fatalf("deleted store remained visible: %+v, %v", removed, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fixture.Store.ListFiles(
		canceled, toolsTestProjectID, fixture.Agent.ID, "/memory/**", 0, 1,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled listing succeeded: %v", err)
	}
}
