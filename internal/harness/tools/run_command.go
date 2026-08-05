package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
)

type runCommandRequest struct {
	Command    string          `json:"command"`
	MachineRef json.RawMessage `json:"machine_ref,omitempty"`
	Shell      json.RawMessage `json:"shell,omitempty"`
	Cwd        json.RawMessage `json:"cwd,omitempty"`
	WaitMs     json.RawMessage `json:"wait_ms,omitempty"`
	IOMode     json.RawMessage `json:"io_mode,omitempty"`
}

type resolvedRunCommandRequest struct {
	Command    string
	MachineRef string
	Selector   processcmd.ShellSelector
	Cwd        string
	WaitMs     int
	IOMode     processcmd.IOMode
}

func resolveRunCommandRequest(raw json.RawMessage) (resolvedRunCommandRequest, error) {
	var input runCommandRequest
	if err := decodeSingleStrictJSON(raw, &input, "run_command request"); err != nil {
		return resolvedRunCommandRequest{}, fmt.Errorf("parse run_command request: %w", err)
	}
	machineRef := ""
	if len(input.MachineRef) > 0 {
		var rawMachineRef *string
		if err := json.Unmarshal(input.MachineRef, &rawMachineRef); err != nil {
			return resolvedRunCommandRequest{}, fmt.Errorf("parse machine_ref: %w", err)
		}
		if rawMachineRef == nil {
			return resolvedRunCommandRequest{}, errors.New("machine_ref cannot be null")
		}
		machineRef = strings.TrimSpace(*rawMachineRef)
	}
	selector := processcmd.ShellDefault
	if len(input.Shell) != 0 {
		var rawSelector *string
		if err := json.Unmarshal(input.Shell, &rawSelector); err != nil {
			return resolvedRunCommandRequest{}, fmt.Errorf("parse shell selector: %w", err)
		}
		if rawSelector == nil {
			return resolvedRunCommandRequest{}, errors.New("shell selector cannot be null")
		}
		selector = processcmd.ShellSelector(*rawSelector)
	}
	spec, err := processcmd.NormalizeShellCommand(input.Command, selector)
	if err != nil {
		return resolvedRunCommandRequest{}, err
	}
	cwd := ""
	if len(input.Cwd) > 0 {
		var rawCwd *string
		if err := json.Unmarshal(input.Cwd, &rawCwd); err != nil {
			return resolvedRunCommandRequest{}, fmt.Errorf("parse cwd: %w", err)
		}
		if rawCwd == nil {
			return resolvedRunCommandRequest{}, errors.New("cwd cannot be null")
		}
		cwd = strings.TrimSpace(*rawCwd)
		if strings.Contains(cwd, "\x00") {
			return resolvedRunCommandRequest{}, errors.New("cwd cannot contain NUL")
		}
	}
	waitMs := 0
	if len(input.WaitMs) != 0 {
		var rawWait *int
		if err := json.Unmarshal(input.WaitMs, &rawWait); err != nil {
			return resolvedRunCommandRequest{}, fmt.Errorf("parse wait_ms: %w", err)
		}
		if rawWait == nil {
			return resolvedRunCommandRequest{}, errors.New("wait_ms cannot be null")
		}
		waitMs = *rawWait
		if waitMs <= 0 {
			return resolvedRunCommandRequest{}, errors.New("wait_ms must be positive")
		}
		if waitMs > processaction.MaxWaitMilliseconds {
			return resolvedRunCommandRequest{}, fmt.Errorf(
				"wait_ms must be at most %d",
				processaction.MaxWaitMilliseconds,
			)
		}
	}
	ioMode := processcmd.IOModePipe
	if len(input.IOMode) > 0 {
		var rawMode *string
		if err := json.Unmarshal(input.IOMode, &rawMode); err != nil {
			return resolvedRunCommandRequest{}, fmt.Errorf("parse io_mode: %w", err)
		}
		if rawMode == nil {
			return resolvedRunCommandRequest{}, errors.New("io_mode cannot be null")
		}
		ioMode = processcmd.IOMode(strings.TrimSpace(*rawMode))
	}
	ioMode, err = processcmd.NormalizeIOMode(ioMode)
	if err != nil {
		return resolvedRunCommandRequest{}, err
	}
	return resolvedRunCommandRequest{
		Command:    spec.Command,
		MachineRef: machineRef,
		Selector:   spec.Shell,
		Cwd:        cwd,
		WaitMs:     waitMs,
		IOMode:     ioMode,
	}, nil
}
