package processcmd

import (
	"errors"
	"strings"
)

type FileTransfer struct {
	Direction string `json:"direction"`
	LocalPath string `json:"local_path"`
}

func (t FileTransfer) Validate() error {
	if t.Direction != "upload" && t.Direction != "download" {
		return errors.New("invalid file transfer direction")
	}
	if t.LocalPath == "" || strings.ContainsRune(t.LocalPath, 0) {
		return errors.New("file transfer local path must be non-empty and cannot contain NUL")
	}
	return nil
}

func (t FileTransfer) Label() string {
	return CommandLabel(t.Direction + " " + t.LocalPath)
}
