package processcmd

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestExecutionSpec(t *testing.T) {
	for _, spec := range []ExecutionSpec{
		ForShell("printf 'hello'", "", ""),
		ForShell("python", ShellBash, IOModePTY),
		ForFileTransfer(FileTransfer{
			Direction: FileTransferUpload, LocalPath: "a file;$(false)",
			Target: FileTarget{Artifact: &ArtifactTarget{}},
		}),
		ForFileTransfer(FileTransfer{
			Direction: FileTransferDownload, LocalPath: "out",
			Target: FileTarget{Artifact: &ArtifactTarget{ID: uuid.New()}},
		}),
		ForFileTransfer(FileTransfer{
			Direction: FileTransferUpload, LocalPath: "notes",
			Target: FileTarget{Memory: &MemoryTarget{StoreID: uuid.New(), Path: "nested/notes.md"}},
		}),
		ForFileOperation(KindFileRead, FileOperation{Path: "~/shot.png"}),
		ForFileOperation(KindFileWrite, FileOperation{Path: "notes.md", Content: new(string), Append: true}),
		ForFileOperation(KindFileList, FileOperation{Path: "src/**/*.go", Limit: 50, After: "src/a.go"}),
		ForFileOperation(KindFileSearch, FileOperation{Path: "*.md", Args: []string{"-e", "x"}, Limit: 20}),
	} {
		require.NoError(t, spec.Validate())
		body, err := json.Marshal(spec)
		require.NoError(t, err)
		var decoded ExecutionSpec
		require.NoError(t, json.Unmarshal(body, &decoded))
		require.NoError(t, decoded.Validate())
		require.Equal(t, spec, decoded)
	}
}

func TestExecutionSpecRejectsInvalidOperations(t *testing.T) {
	for name, spec := range map[string]ExecutionSpec{
		"missing kind":          {},
		"missing shell":         {Kind: KindShell},
		"missing shell options": {Kind: KindShell, Shell: &ShellCommand{Command: "echo hi"}},
		"both kinds": {
			Kind: KindShell, Shell: &ShellCommand{Command: "echo hi"}, FileTransfer: &FileTransfer{},
		},
		"wrong payload":  {Kind: KindFileTransfer, Shell: &ShellCommand{Command: "echo hi"}},
		"empty command":  ForShell("", ShellDefault, IOModePipe),
		"invalid shell":  ForShell("echo hi", "unknown", IOModePipe),
		"invalid IO":     ForShell("echo hi", ShellDefault, "unknown"),
		"missing target": ForFileTransfer(FileTransfer{Direction: FileTransferUpload, LocalPath: "file"}),
		"both targets": ForFileTransfer(FileTransfer{
			Direction: FileTransferUpload, LocalPath: "file",
			Target: FileTarget{Memory: &MemoryTarget{}, Artifact: &ArtifactTarget{}},
		}),
		"artifact upload ID": ForFileTransfer(FileTransfer{
			Direction: FileTransferUpload, LocalPath: "file", Target: FileTarget{Artifact: &ArtifactTarget{ID: uuid.New()}},
		}),
		"artifact download without ID": ForFileTransfer(FileTransfer{
			Direction: FileTransferDownload, LocalPath: "file", Target: FileTarget{Artifact: &ArtifactTarget{}},
		}),
		"relative escape": ForFileTransfer(FileTransfer{
			Direction: FileTransferUpload, LocalPath: "file",
			Target: FileTarget{Memory: &MemoryTarget{StoreID: uuid.New(), Path: "../other/file"}},
		}),
		"missing store": ForFileTransfer(FileTransfer{
			Direction: FileTransferUpload, LocalPath: "file", Target: FileTarget{Memory: &MemoryTarget{Path: "file"}},
		}),
		"missing file operation": {Kind: KindFileRead},
		"file operation with shell": {
			Kind: KindFileList, File: &FileOperation{Path: "*"}, Shell: &ShellCommand{Command: "ls"},
		},
		"empty file path":     ForFileOperation(KindFileRead, FileOperation{}),
		"write without text":  ForFileOperation(KindFileWrite, FileOperation{Path: "file"}),
		"read with content":   ForFileOperation(KindFileRead, FileOperation{Path: "file", Content: new(string)}),
		"search without args": ForFileOperation(KindFileSearch, FileOperation{Path: "file"}),
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, spec.Validate()) })
	}
}
