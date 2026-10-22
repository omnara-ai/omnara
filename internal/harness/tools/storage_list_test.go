package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestListFilesOffsetValidation(t *testing.T) {
	for _, test := range []struct {
		input string
		valid bool
	}{
		{`{"pattern":"/memory/**"}`, true},
		{`{"pattern":"/memory/**","offset":0}`, true},
		{`{"pattern":"/memory/**","offset":50,"limit":25}`, true},
		{fmt.Sprintf(`{"pattern":"/artifacts/*","offset":%d}`, toolcatalog.StorageListMaxOffset), true},
		{fmt.Sprintf(`{"pattern":"/artifacts/*","offset":%d}`, toolcatalog.StorageListMaxOffset+1), false},
		{`{"pattern":"/memory/**","offset":-1}`, false},
		{`{"pattern":"/memory/**","offset":1.5}`, false},
		{`{"pattern":"/memory/**","offset":"1"}`, false},
		{`{"pattern":"/memory/**","offset":null}`, false},
		{`{"pattern":"/memory/**","cursor":"e30"}`, false},
	} {
		t.Run(test.input, func(t *testing.T) {
			err := validateRegisteredToolInput(toolcatalog.ToolNameStorageList, json.RawMessage(test.input))
			if test.valid && err != nil {
				t.Fatalf("valid input rejected: %v", err)
			} else if !test.valid && err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	for _, offset := range []int{-1, toolcatalog.StorageListMaxOffset + 1} {
		raw, err := json.Marshal(storageListRequest{Pattern: "/memory/**", Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if err := validateStorageList(raw); err == nil ||
			!strings.Contains(err.Error(), "offset must be between 0 and") {
			t.Fatalf("offset %d: %v", offset, err)
		}
	}
}

func TestStorageListLiteralRootValidation(t *testing.T) {
	for _, pattern := range []string{
		"/memory", "/artifacts", "/memory/*", "/memory/**", "/memory/*-archive/**/*.md",
		"/artifacts/*.pdf", "/artifacts/art_*", "/*", "/**", "/**/review-note.txt", "/mem*/*", "/unknown/*",
	} {
		raw, err := json.Marshal(storageListRequest{Pattern: pattern})
		if err != nil {
			t.Fatal(err)
		}
		err = validateRegisteredToolInput(toolcatalog.ToolNameStorageList, raw)
		valid := pattern == "/memory" || pattern == "/artifacts" ||
			strings.HasPrefix(pattern, "/memory/") || strings.HasPrefix(pattern, "/artifacts/")
		if valid && err != nil {
			t.Errorf("valid pattern %q rejected: %v", pattern, err)
		} else if !valid && (err == nil || !strings.Contains(err.Error(), "/memory or /artifacts")) {
			t.Errorf("pattern %q: expected allowed roots in error, got %v", pattern, err)
		}
	}
}
