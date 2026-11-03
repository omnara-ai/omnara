package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedSchemasAreCurrent(t *testing.T) {
	require.NoError(t, generate("../..", true))
}

func TestCheckReportsStaleSchemaWithoutWriting(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, configSchemaPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	require.NoError(t, os.WriteFile(filename, []byte("stale config"), 0o644))

	require.ErrorContains(t, generate(root, true), configSchemaPath+" is stale")
	current, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, "stale config", string(current))

	require.NoError(t, generate(root, false))
	require.NoError(t, generate(root, true))
}
