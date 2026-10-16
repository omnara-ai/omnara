package daemonprotocol

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/stretchr/testify/require"
)

func TestEncodeForDaemonWithholdsFileOperationsFromOldDaemons(t *testing.T) {
	offer := func(spec processcmd.ExecutionSpec) Message {
		return Message{Type: MessageProcessOffer, ProcessID: "prc_test", ProcessOffer: &ProcessOffer{
			ProcessID: "prc_test", ExecutionSpec: spec, Cwd: "/work", TimeoutSeconds: 30,
		}}
	}
	read := processcmd.ForFileOperation(processcmd.KindFileRead, processcmd.FileOperation{Path: "notes.txt"})
	for _, test := range []struct {
		version string
		spec    processcmd.ExecutionSpec
		blocked bool
	}{
		{version: "0.2.5", spec: read, blocked: true},
		{version: "", spec: read, blocked: true},
		{version: "0.2.5", spec: processcmd.ForShell("ls", "", "")},
		{version: processcmd.FileOperationDaemonVersion, spec: read},
		{version: "1.0.0", spec: read},
		{version: daemonversion.Development, spec: read},
	} {
		data, err := encodeForDaemon(test.version, offer(test.spec))
		require.NoError(t, err)
		var sent Message
		require.NoError(t, json.Unmarshal(data, &sent))
		if test.blocked {
			require.Equal(t, ProcessOffer{
				ProcessID: "prc_test", PreparationError: UnsupportedFileOperationsMessage(test.version),
			}, *sent.ProcessOffer, test.version)
			require.NotContains(t, string(data), `"file"`)
			continue
		}
		require.Equal(t, test.spec, sent.ProcessOffer.ExecutionSpec, test.version)
	}
}

func TestFileOperationResultValidate(t *testing.T) {
	for _, test := range []struct {
		result FileOperationResult
		valid  bool
	}{
		{FileOperationResult{Value: json.RawMessage(`{"path":"a"}`)}, true},
		{FileOperationResult{Value: json.RawMessage(`{"path":"a"}`), Artifact: "/artifacts/art_x"}, true},
		{FileOperationResult{Error: &FileTransferError{Code: "c", Message: "m"}}, true},
		{FileOperationResult{}, false},
		{FileOperationResult{Value: json.RawMessage(`[]`)}, false},
		{FileOperationResult{Error: &FileTransferError{Code: "c"}}, false},
		{FileOperationResult{Error: &FileTransferError{Code: "c", Message: "m"}, Value: json.RawMessage(`{}`)}, false},
	} {
		require.Equal(t, test.valid, test.result.Validate() == nil, "%+v", test.result)
	}
}
