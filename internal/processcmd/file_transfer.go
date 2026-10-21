package processcmd

import (
	"errors"
	"io/fs"
	"strings"

	"github.com/google/uuid"
)

type FileTransferDirection string

const (
	FileTransferUpload   FileTransferDirection = "upload"
	FileTransferDownload FileTransferDirection = "download"
)

type FileTransfer struct {
	Direction FileTransferDirection `json:"direction"`
	LocalPath string                `json:"local_path"`
	Target    FileTarget            `json:"target"`
}

type FileTarget struct {
	Memory   *MemoryTarget   `json:"memory,omitempty"`
	Artifact *ArtifactTarget `json:"artifact,omitempty"`
}

type MemoryTarget struct {
	StoreID uuid.UUID `json:"store_id"`
	Path    string    `json:"path"`
	// ExpectedDigest is set only by processes created before uploads used memory read tracking.
	ExpectedDigest *string `json:"expected_digest,omitempty"`
	// Force replaces the file even if the agent has not read its current content.
	Force bool `json:"force,omitempty"`
}

type ArtifactTarget struct {
	ID uuid.UUID `json:"id,omitzero"`
	// View keeps an uploaded image in the model's context.
	View bool `json:"view,omitempty"`
}

func (t FileTransfer) validateLocal() error {
	if t.Direction != FileTransferUpload && t.Direction != FileTransferDownload {
		return errors.New("invalid file transfer direction")
	}
	if t.LocalPath == "" || strings.ContainsRune(t.LocalPath, 0) {
		return errors.New("file transfer local path must be non-empty and cannot contain NUL")
	}
	return nil
}

func (t FileTransfer) validateTarget() error {
	if (t.Target.Memory == nil) == (t.Target.Artifact == nil) {
		return errors.New("file transfer requires exactly one target")
	}
	if target := t.Target.Memory; target != nil {
		if target.StoreID == uuid.Nil || !fs.ValidPath(target.Path) ||
			target.Path == "." || strings.ContainsAny(target.Path, "\\\x00") {
			return errors.New("invalid memory transfer target")
		}
		if t.Direction == FileTransferDownload && (target.ExpectedDigest != nil || target.Force) {
			return errors.New("download cannot have an expected digest or force")
		}
	} else if (t.Direction == FileTransferDownload) != (t.Target.Artifact.ID != uuid.Nil) {
		return errors.New("artifact uploads create a new artifact; downloads require an artifact id")
	} else if t.Direction == FileTransferDownload && t.Target.Artifact.View {
		return errors.New("only artifact uploads can be viewed")
	}
	return nil
}

func (t FileTransfer) Label() string {
	return CommandLabel(string(t.Direction) + " " + t.LocalPath)
}
