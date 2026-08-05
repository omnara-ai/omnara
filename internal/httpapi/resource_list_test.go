package httpapi

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

func TestResourceNameGlobToLike(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		input, want string
		wantErr     bool
	}{
		"exact":            {input: "worker", want: "worker"},
		"wildcards":        {input: "work?r*", want: "work_r%"},
		"like literals":    {input: `a%_b`, want: `a\%\_b`},
		"escaped wildcard": {input: `work\*`, want: "work*"},
		"only wildcards":   {input: "*?", wantErr: true},
		"bad escape":       {input: `a\x`, wantErr: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := resourceNameGlobToLike(test.input)
			if test.wantErr {
				if !errors.Is(err, errInvalidListQuery) {
					t.Fatalf("expected invalid query, got %v", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestResourceListCursorBindsScopeAndFilters(t *testing.T) {
	t.Parallel()
	sorts := sortSet("name", "created_at")
	sortValue := "name"
	base := resourceListQueryInput{
		ListKind: "agents", Scope: "org/project-a", IDKind: publicid.KindAgent,
		AllowedSorts: sorts, Sort: &sortValue, Extra: []string{"active"},
	}
	options, err := parseResourceListQuery(base)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.MustParse("018f0f4d-48ac-7d7d-8d91-111111111111")
	cursor, err := encodeResourceListNextCursor(
		true, listing.Cursor{Set: true, Key: "alpha", ID: id},
		options, base.ListKind, base.Scope, base.IDKind, base.Extra,
	)
	if err != nil {
		t.Fatal(err)
	}
	base.Cursor = cursor
	decoded, err := parseResourceListQuery(base)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.After.Set || decoded.After.Key != "alpha" || decoded.After.ID != id {
		t.Fatalf("unexpected decoded cursor: %#v", decoded.After)
	}
	base.Scope = "org/project-b"
	if _, err := parseResourceListQuery(base); !errors.Is(err, errMalformedCursor) {
		t.Fatalf("expected scope mismatch, got %v", err)
	}
	base.Scope = "org/project-a"
	base.Extra = []string{"archived"}
	if _, err := parseResourceListQuery(base); !errors.Is(err, errMalformedCursor) {
		t.Fatalf("expected filter mismatch, got %v", err)
	}
}
