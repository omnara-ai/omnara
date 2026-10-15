package daemonprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/processcmd"
)

// MaxFileOperationResultBytes bounds the result omnarad reports for a file
// operation so the terminal report always fits in one daemon message.
const MaxFileOperationResultBytes = 256 * 1024

// FileOperationResult is the outcome of a processcmd.FileOperation. Value is
// the tool result object. Artifact is set when a read uploaded an image for
// the model to view.
type FileOperationResult struct {
	Value    json.RawMessage    `json:"value,omitempty"`
	Artifact string             `json:"artifact,omitempty"`
	Error    *FileTransferError `json:"error,omitempty"`
}

func (r FileOperationResult) Validate() error {
	if r.Error != nil {
		if r.Value != nil || r.Artifact != "" || r.Error.Code == "" || r.Error.Message == "" {
			return errors.New("invalid file operation error")
		}
		return nil
	}
	if !bytes.HasPrefix(bytes.TrimSpace(r.Value), []byte("{")) || !json.Valid(r.Value) {
		return errors.New("file operation result must contain a value object")
	}
	return nil
}

// UnsupportedFileOperationsMessage explains how to use file tools on a machine
// whose omnarad predates processcmd.FileOperationDaemonVersion.
func UnsupportedFileOperationsMessage(daemonVersion string) string {
	return fmt.Sprintf(
		"this machine's omnarad %s does not support file tools on machines; "+
			"update omnarad to %s or later, or use run_command",
		daemonVersion, processcmd.FileOperationDaemonVersion,
	)
}

// withoutUnsupportedFileOperation replaces a file operation offer for a daemon
// that cannot parse it with a preparation error, so the tool call fails cleanly.
func withoutUnsupportedFileOperation(daemonVersion string, offer *ProcessOffer) *ProcessOffer {
	if offer == nil || !processcmd.IsFileOperation(offer.ExecutionSpec.Kind) ||
		processcmd.SupportsFileOperations(daemonVersion) {
		return offer
	}
	return &ProcessOffer{
		ProcessID:        offer.ProcessID,
		PreparationError: UnsupportedFileOperationsMessage(daemonVersion),
	}
}
