package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
)

const maxNotifyMatchBytes = 500

type runCommandRequest struct {
	Command   string          `json:"command"`
	MachineID json.RawMessage `json:"machine_id,omitempty"`
	Shell     json.RawMessage `json:"shell,omitempty"`
	Cwd       json.RawMessage `json:"cwd,omitempty"`
	IOMode    json.RawMessage `json:"io_mode,omitempty"`
	Detach    *bool           `json:"detach,omitempty"`
	// NotifyMatch is a regular expression for output lines to report.
	NotifyMatch *string `json:"notify_match,omitempty"`
}

type resolvedRunCommandRequest struct {
	Command   string
	MachineID uuid.UUID
	Selector  processcmd.ShellSelector
	Cwd       string
	WaitMs    int
	IOMode    processcmd.IOMode
	Detach    bool
	// NotifyMatch is a regular expression for output lines to report.
	NotifyMatch string
}

func resolveRunCommandRequest(raw json.RawMessage) (resolvedRunCommandRequest, error) {
	var input runCommandRequest
	if err := decodeSingleStrictJSON(raw, &input, "run_command request"); err != nil {
		return resolvedRunCommandRequest{}, fmt.Errorf("parse run_command request: %w", err)
	}
	machineID, err := resolveOptionalMachineID(input.MachineID)
	if err != nil {
		return resolvedRunCommandRequest{}, err
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
	notifyMatch := ""
	if input.NotifyMatch != nil {
		notifyMatch = *input.NotifyMatch
		if notifyMatch == "" || len(notifyMatch) > maxNotifyMatchBytes {
			return resolvedRunCommandRequest{}, fmt.Errorf(
				"notify_match must be 1 to %d bytes", maxNotifyMatchBytes,
			)
		}
		if _, err := regexp.Compile(notifyMatch); err != nil {
			return resolvedRunCommandRequest{}, fmt.Errorf("notify_match is not a valid regular expression: %w", err)
		}
	}
	return resolvedRunCommandRequest{
		Command:     spec.Command,
		MachineID:   machineID,
		Selector:    spec.Shell,
		Cwd:         cwd,
		WaitMs:      processaction.CommandWaitMilliseconds,
		IOMode:      ioMode,
		Detach:      input.Detach != nil && *input.Detach,
		NotifyMatch: notifyMatch,
	}, nil
}
