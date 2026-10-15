package omnarad

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/filesearch"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/stretchr/testify/require"
)

func runFileOperationForTest(
	t *testing.T, processID string, kind processcmd.ExecutionKind, operation processcmd.FileOperation,
) (map[string]any, daemonprotocol.FileOperationResult) {
	t.Helper()
	spec, err := json.Marshal(processcmd.ForFileOperation(kind, operation))
	require.NoError(t, err)
	var encoded bytes.Buffer
	require.NoError(t, runFileOperation(t.Context(), processID, bytes.NewReader(spec), &encoded))
	var result daemonprotocol.FileOperationResult
	require.NoError(t, json.Unmarshal(encoded.Bytes(), &result))
	require.NoError(t, result.Validate())
	var value map[string]any
	if result.Error == nil {
		require.NoError(t, json.Unmarshal(result.Value, &value))
	}
	return value, result
}

func intPointer(value int) *int { return &value }

func TestFileOperationReadPagesTextAndRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	require.NoError(t, os.WriteFile("notes.txt", []byte("one\ntwo\nthree\n"), 0o600))
	require.NoError(t, os.WriteFile("blob.bin", []byte{0, 1, 2}, 0o600))
	digest := fileDigest([]byte("one\ntwo\nthree\n"))

	value, _ := runFileOperationForTest(t, "", processcmd.KindFileRead, processcmd.FileOperation{
		Path: "notes.txt", Paging: fileread.Paging{OffsetLine: intPointer(2), LimitLines: intPointer(1)},
	})
	require.Equal(t, "two\n", value["content"])
	require.Equal(t, float64(3), value["next_offset_line"])
	require.Equal(t, digest, value["digest"])
	require.Equal(t, "text/plain; charset=utf-8", value["content_type"])

	value, _ = runFileOperationForTest(t, "", processcmd.KindFileRead, processcmd.FileOperation{
		Path: "~/notes.txt", Paging: fileread.Paging{OffsetChar: intPointer(4), LimitChars: intPointer(3)},
	})
	require.Equal(t, "two", value["content"])
	require.Equal(t, "~/notes.txt", value["path"])

	for _, test := range []struct {
		operation processcmd.FileOperation
		code      string
		message   string
	}{
		{processcmd.FileOperation{Path: "blob.bin"}, "file_operation_failed", "UTF-8 text without NUL bytes"},
		{processcmd.FileOperation{Path: "."}, "not_a_file", "path is a directory"},
		{processcmd.FileOperation{Path: "missing.txt"}, "file_operation_failed", "no such file"},
	} {
		_, result := runFileOperationForTest(t, "", processcmd.KindFileRead, test.operation)
		require.NotNil(t, result.Error, test.operation.Path)
		require.Equal(t, test.code, result.Error.Code)
		require.Contains(t, result.Error.Error(), test.message)
	}
}

func TestFileOperationReadUploadsImagesForViewing(t *testing.T) {
	var screenshot bytes.Buffer
	require.NoError(t, png.Encode(&screenshot, image.NewGray(image.Rect(0, 0, 2, 2))))
	processID := fileTransferTestPublicID(t, publicid.KindProcess)
	artifactPath := "/artifacts/" + fileTransferTestPublicID(t, publicid.KindArtifact)
	digest := fileDigest(screenshot.Bytes())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/v1/daemon/processes/"+processID+"/file" ||
			r.URL.Query().Get("filename") != "shot.png" || !bytes.Equal(body, screenshot.Bytes()) {
			t.Errorf("unexpected upload %s", r.URL)
		}
		_, _ = fmt.Fprintf(w, `{"path":%q,"digest":%q}`, artifactPath, digest)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon-home"), server.URL, "")
	path := filepath.Join(t.TempDir(), "shot.png")
	require.NoError(t, os.WriteFile(path, screenshot.Bytes(), 0o600))

	value, result := runFileOperationForTest(t, processID, processcmd.KindFileRead, processcmd.FileOperation{Path: path})
	require.Equal(t, artifactPath, result.Artifact)
	require.Equal(t, map[string]any{
		"path": path, "content_type": "image/png", "size_bytes": float64(screenshot.Len()), "digest": digest,
	}, value)
}

func TestFileOperationWriteReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "notes.txt")
	write := func(content string, appendText bool) (map[string]any, *daemonprotocol.FileTransferError) {
		value, result := runFileOperationForTest(t, "", processcmd.KindFileWrite, processcmd.FileOperation{
			Path: path, Content: &content, Append: appendText,
		})
		return value, result.Error
	}
	value, failure := write("one\n", false)
	require.Nil(t, failure, "parent directories are created")
	require.Equal(t, fileDigest([]byte("one\n")), value["digest"])
	require.NoError(t, os.Chmod(path, 0o640))

	_, failure = write("two\n", true)
	require.Nil(t, failure)
	_, failure = write("zero\n", false)
	require.Nil(t, failure)
	value, failure = write("three\n", true)
	require.Nil(t, failure)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "zero\nthree\n", string(content))
	require.Equal(t, fileDigest(content), value["digest"])
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "replacement keeps permissions")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary files remain")

	path = filepath.Join(dir, "log.txt")
	_, failure = write("first\n", true)
	require.Nil(t, failure, "append creates a missing file")
	path = dir
	_, failure = write("x", false)
	require.NotNil(t, failure, "directories are not replaced")
}

func TestFileOperationListGlobsWithPagination(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, name := range []string{"a.go", "b.txt", "src/c.go", "src/d/e.go", "src/f.go"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, os.WriteFile(name, []byte(name), 0o600))
	}
	list := func(pattern string, limit int) []string {
		var paths []string
		after := ""
		for range 10 {
			value, result := runFileOperationForTest(t, "", processcmd.KindFileList, processcmd.FileOperation{
				Path: pattern, Limit: limit, After: after, CursorScope: "scope",
			})
			require.Nil(t, result.Error)
			entries, ok := value["entries"].([]any)
			require.True(t, ok)
			for _, entry := range entries {
				fields, ok := entry.(map[string]any)
				require.True(t, ok)
				path, ok := fields["path"].(string)
				require.True(t, ok)
				paths = append(paths, path)
			}
			next, ok := value["next_cursor"].(string)
			if !ok {
				return paths
			}
			cursor, ok := listing.DecodeFileCursor(next)
			require.True(t, ok)
			require.Equal(t, "scope", cursor.Scope)
			after = cursor.Key
		}
		t.Fatal("listing did not finish")
		return nil
	}
	require.Equal(t, []string{"a.go", "src/c.go", "src/d/e.go", "src/f.go"}, list("**/*.go", 2))
	all := []string{"a.go", "b.txt", "src", "src/c.go", "src/d", "src/d/e.go", "src/f.go"}
	require.Equal(t, all, list("**", 100))
	require.Equal(t, all, list("**", 1), "pages resume after directories")

	value, _ := runFileOperationForTest(t, "", processcmd.KindFileList, processcmd.FileOperation{
		Path: dir + "/src/*", Limit: 10,
	})
	require.Equal(t, []any{
		map[string]any{"path": dir + "/src/c.go", "type": "file", "size_bytes": float64(len("src/c.go"))},
		map[string]any{"path": dir + "/src/d", "type": "directory"},
		map[string]any{"path": dir + "/src/f.go", "type": "file", "size_bytes": float64(len("src/f.go"))},
	}, value["entries"])
	require.Nil(t, value["next_cursor"])

	value, _ = runFileOperationForTest(t, "", processcmd.KindFileList, processcmd.FileOperation{Path: "src", Limit: 10})
	require.Equal(t, []any{map[string]any{"path": "src", "type": "directory"}}, value["entries"])
}

func TestFileOperationSearchModesAndTruncation(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile("a.txt", []byte("before\nTODO one\nafter\n"), 0o600))
	require.NoError(t, os.WriteFile("b.txt", []byte("TODO two\nTODO three\n"), 0o600))
	require.NoError(t, os.WriteFile("c.bin", []byte("TODO\x00binary"), 0o600))
	searchFiles := func(path string, limit int, args ...string) filesearch.Result {
		value, result := runFileOperationForTest(t, "", processcmd.KindFileSearch, processcmd.FileOperation{
			Path: path, Args: args, Limit: limit,
		})
		require.Nil(t, result.Error)
		body, err := json.Marshal(value)
		require.NoError(t, err)
		var decoded filesearch.Result
		require.NoError(t, json.Unmarshal(body, &decoded))
		return decoded
	}

	result := searchFiles("*", 20, "-C", "1", "-e", "TODO")
	require.Equal(t, 3, result.MatchCount)
	require.False(t, result.Truncated, "binary files are skipped silently")
	require.Equal(t, filesearch.Line{Path: "a.txt", LineNumber: 1, EndLine: 1, Text: "before"}, result.Lines[0])

	result = searchFiles("*.txt", 20, "-c", "-e", "TODO")
	two := uint64(2)
	one := uint64(1)
	require.Equal(t, []filesearch.File{{Path: "a.txt", Count: &one}, {Path: "b.txt", Count: &two}}, result.Files)

	result = searchFiles("*.txt", 1, "-l", "-e", "TODO")
	require.Equal(t, []filesearch.File{{Path: "a.txt"}}, result.Files)
	require.True(t, result.Truncated)
	require.Equal(t, "search result limit reached; narrow the search", result.IncompleteReason)

	result = searchFiles("c.bin", 20, "-e", "TODO")
	require.Zero(t, result.MatchCount)
	require.Equal(t, "binary data detected; results may be incomplete", result.IncompleteReason)

	_, failed := runFileOperationForTest(t, "", processcmd.KindFileSearch, processcmd.FileOperation{
		Path: "missing.txt", Args: []string{"-e", "x"}, Limit: 1,
	})
	require.NotNil(t, failed.Error)
}
