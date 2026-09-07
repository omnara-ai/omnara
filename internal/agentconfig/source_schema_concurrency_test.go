package agentconfig

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceSchemaValidatorConcurrentFirstUse(t *testing.T) {
	t.Parallel()
	// A fresh schema keeps earlier ParseSource calls from warming its regex caches.
	validator, err := newSourceSchemaValidator()
	require.NoError(t, err)
	validSource := `{
		"instruction": "Help the user.",
		"model": {"provider_config": "provider", "name": "model"},
		"machine_sources": [{
			"machine_pool_name": "pool",
			"secret_env_overlay": {"TOKEN": "sec_aaaaaaaaaaaaaaaaaaaaaaaaaa"}
		}],
		"tools": {"custom_tool": {
			"type": "custom", "description": "Do a thing.", "input_schema": {"type": "object"}
		}},
		"mcp": {"demo": {
			"url": "https://example.com/mcp",
			"auth": {"type": "bearer", "secret_id": "sec_aaaaaaaaaaaaaaaaaaaaaaaaaa"}
		}},
		"skills": ["skl_aaaaaaaaaaaaaaaaaaaaaaaaaa"]
	}`
	sources := [][]byte{
		[]byte(validSource),
		[]byte(strings.ReplaceAll(validSource, "aaaaaaaaaaaaaaaaaaaaaaaaaa", "invalid")),
	}
	wantPaths := []string{
		"/machine_sources/0/secret_env_overlay/TOKEN",
		"/mcp/demo/auth/secret_id",
		"/skills/0",
	}
	results := make([]error, 24)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range results {
		workers.Go(func() {
			<-start
			results[i] = validator.validate(sources[i%len(sources)], nil)
		})
	}
	close(start)
	workers.Wait()

	for i, err := range results {
		if i%len(sources) == 0 {
			if err != nil {
				t.Errorf("valid source in worker %d: %v", i, err)
			}
			continue
		}
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) {
			t.Errorf("invalid source in worker %d: got %v, want ValidationError", i, err)
			continue
		}
		paths := make([]string, 0, len(validationErr.Issues))
		for _, issue := range validationErr.Issues {
			paths = append(paths, issue.Path)
			if issue.Message == "" {
				t.Errorf("worker %d: missing message for issue %q", i, issue.Path)
			}
		}
		if !slices.Equal(paths, wantPaths) {
			t.Errorf("worker %d: issue paths = %q, want %q; error: %v", i, paths, wantPaths, err)
		}
	}
}
