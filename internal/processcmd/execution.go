package processcmd

import "errors"

type ExecutionKind string

const (
	KindShell        ExecutionKind = "shell"
	KindFileTransfer ExecutionKind = "file_transfer"
)

type ExecutionSpec struct {
	Kind         ExecutionKind  `json:"kind"`
	Shell        *ShellCommand  `json:"shell,omitempty"`
	FileTransfer *FileTransfer  `json:"file_transfer,omitempty"`
	File         *FileOperation `json:"file,omitempty"`
}

func ForShell(command string, shell ShellSelector, ioMode IOMode) ExecutionSpec {
	if shell == "" {
		shell = ShellDefault
	}
	if ioMode == "" {
		ioMode = IOModePipe
	}
	return ExecutionSpec{Kind: KindShell, Shell: &ShellCommand{Command: command, Shell: shell, IOMode: ioMode}}
}

func ForFileTransfer(transfer FileTransfer) ExecutionSpec {
	return ExecutionSpec{Kind: KindFileTransfer, FileTransfer: &transfer}
}

func (s ExecutionSpec) Validate() error {
	if err := s.ValidateLocal(); err != nil {
		return err
	}
	if s.FileTransfer != nil {
		return s.FileTransfer.validateTarget()
	}
	return nil
}

func (s ExecutionSpec) ValidateLocal() error {
	switch s.Kind {
	case KindShell:
		if s.Shell == nil || s.FileTransfer != nil || s.File != nil {
			return errors.New("shell execution requires only a shell payload")
		}
		if s.Shell.Shell == "" || s.Shell.IOMode == "" {
			return errors.New("shell execution requires a shell selector and IO mode")
		}
		if _, err := NormalizeShellCommand(s.Shell.Command, s.Shell.Shell); err != nil {
			return err
		}
		_, err := NormalizeIOMode(s.Shell.IOMode)
		return err
	case KindFileTransfer:
		if s.FileTransfer == nil || s.Shell != nil || s.File != nil {
			return errors.New("file transfer execution requires only a file transfer payload")
		}
		return s.FileTransfer.validateLocal()
	case KindFileRead, KindFileWrite, KindFileList, KindFileSearch:
		if s.File == nil || s.Shell != nil || s.FileTransfer != nil {
			return errors.New("file operation execution requires only a file operation payload")
		}
		return s.File.validate(s.Kind)
	default:
		return errors.New("invalid execution kind")
	}
}

// HasFileResult reports whether the process reports a structured file result
// on exit, which alone completes its tool call.
func (s ExecutionSpec) HasFileResult() bool {
	return s.FileTransfer != nil || s.File != nil
}

func (s ExecutionSpec) IOMode() IOMode {
	if s.Shell != nil {
		return s.Shell.IOMode
	}
	return IOModePipe
}

func (s ExecutionSpec) Label() string {
	if s.Shell != nil {
		return CommandLabel(s.Shell.Command)
	}
	if s.FileTransfer != nil {
		return s.FileTransfer.Label()
	}
	if s.File != nil {
		return s.File.label(s.Kind)
	}
	return ""
}
