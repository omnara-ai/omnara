package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedSchemasAreCurrent(t *testing.T) {
	require.NoError(t, generate("../..", true))
}

func TestGeneratePreservesHandwrittenOpenAPIAndCheckIsReadOnly(t *testing.T) {
	const before = "openapi: 3.0.3\n# Keep this comment.\ncomponents:\n  schemas:\n"
	const after = "    Handwritten:\n      type: string\n"
	spec := before + sectionStart + "    Stale: {}\n" + sectionEnd + after
	root := schemaFixture(t, spec)
	for _, path := range []string{configSchemaPath, openAPIPath} {
		t.Run(path, func(t *testing.T) {
			require.NoError(t, generate(root, false))
			// Change each output independently; checking must not repair either one.
			filename := filepath.Join(root, path)
			original, err := os.ReadFile(filename)
			require.NoError(t, err)
			stale := append([]byte("\n"), original...)
			if path == openAPIPath {
				stale = []byte(spec)
			}
			require.NoError(t, os.WriteFile(filename, stale, 0o644))
			require.ErrorContains(t, generate(root, true), path+" is stale")
			current, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, stale, current)
			require.NoError(t, generate(root, false))
		})
	}
	current, err := os.ReadFile(filepath.Join(root, openAPIPath))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(current), before+sectionStart))
	require.True(t, strings.HasSuffix(string(current), sectionEnd+after))
	require.NoError(t, generate(root, true))
}

func TestInvalidMarkersLeaveBothFilesUnchanged(t *testing.T) {
	for name, spec := range map[string]string{
		"missing":   "components:\n  schemas: {}\n",
		"unclosed":  sectionStart,
		"duplicate": sectionStart + sectionStart + sectionEnd,
		"reversed":  sectionEnd + sectionStart,
	} {
		t.Run(name, func(t *testing.T) {
			root := schemaFixture(t, spec)
			require.ErrorContains(t, generate(root, false), "marker")
			config, err := os.ReadFile(filepath.Join(root, configSchemaPath))
			require.NoError(t, err)
			require.Equal(t, "stale config", string(config))
			current, err := os.ReadFile(filepath.Join(root, openAPIPath))
			require.NoError(t, err)
			require.Equal(t, spec, string(current))
		})
	}
}

func schemaFixture(t *testing.T, spec string) string {
	t.Helper()
	root := t.TempDir()
	for path, contents := range map[string]string{configSchemaPath: "stale config", openAPIPath: spec} {
		filename := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, []byte(contents), 0o644))
	}
	return root
}
