package daemonprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/processcmd"
)

// MaxFileOperationResultBytes keeps a file operation's terminal report within one daemon message.
const MaxFileOperationResultBytes = 256 * 1024

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

func UnsupportedFileOperationsMessage(daemonVersion string) string {
	return fmt.Sprintf(
		"this machine's omnarad %s does not support file tools on machines; "+
			"update omnarad to %s or later, or use run_command",
		daemonVersion, processcmd.FileOperationDaemonVersion,
	)
}

func withoutUnsupportedFileOperation(daemonVersion string, offer *ProcessOffer) *ProcessOffer {
	if offer == nil || offer.ExecutionSpec.File == nil ||
		processcmd.SupportsFileOperations(daemonVersion) {
		return offer
	}
	return &ProcessOffer{
		ProcessID:        offer.ProcessID,
		PreparationError: UnsupportedFileOperationsMessage(daemonVersion),
	}
}
