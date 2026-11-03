package processcmd

import (
	"errors"

	"github.com/omnara-ai/omnara/internal/processaction"
)

type ExecutionKind string

const (
	KindShell        ExecutionKind = "shell"
	KindFileTransfer ExecutionKind = "file_transfer"
)

type ExecutionSpec struct {
	Kind         ExecutionKind `json:"kind"`
	Shell        *ShellCommand `json:"shell,omitempty"`
	FileTransfer *FileTransfer `json:"file_transfer,omitempty"`
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
		if s.Shell == nil || s.FileTransfer != nil {
			return errors.New("shell execution requires only a shell payload")
		}
		if s.Shell.Shell == "" || s.Shell.IOMode == "" {
			return errors.New("shell execution requires a shell selector and IO mode")
		}
		if _, err := NormalizeShellCommand(s.Shell.Command, s.Shell.Shell); err != nil {
			return err
		}
		mode, err := NormalizeIOMode(s.Shell.IOMode)
		if err != nil {
			return err
		}
		if s.Shell.NoStdin && mode != IOModePipe {
			return errors.New("only pipe commands can run without stdin")
		}
		return nil
	case KindFileTransfer:
		if s.FileTransfer == nil || s.Shell != nil {
			return errors.New("file transfer execution requires only a file transfer payload")
		}
		return s.FileTransfer.validateLocal()
	default:
		return errors.New("invalid execution kind")
	}
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
	return ""
}

func (s ExecutionSpec) AgentOutputWait() (processaction.OutputWait, bool) {
	if s.Kind != KindShell || s.Shell == nil {
		return processaction.OutputWait{}, false
	}
	wait := processaction.OutputWait{WaitForExit: true, HeadBytes: processaction.AgentHeadBytes}
	if s.Shell.IOMode == IOModePTY || !s.Shell.NoStdin {
		wait.QuietMS = processaction.InteractiveQuietMilliseconds
	}
	return wait, true
}
