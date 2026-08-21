package processcmd

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type ShellSelector string

const (
	ShellDefault    ShellSelector = "default"
	ShellSH         ShellSelector = "sh"
	ShellBash       ShellSelector = "bash"
	ShellZSH        ShellSelector = "zsh"
	ShellPowerShell ShellSelector = "powershell"
	ShellPWSH       ShellSelector = "pwsh"
	ShellCMD        ShellSelector = "cmd"
)

type IOMode string

const (
	IOModePipe IOMode = "pipe"
	IOModePTY  IOMode = "pty"
)

type ShellCommand struct {
	Command string
	Shell   ShellSelector
}

func NormalizeShellCommand(command string, shell ShellSelector) (ShellCommand, error) {
	if strings.TrimSpace(command) == "" {
		return ShellCommand{}, errors.New("command is required")
	}
	if strings.Contains(command, "\x00") {
		return ShellCommand{}, errors.New("command cannot contain NUL bytes")
	}
	if shell == "" {
		shell = ShellDefault
	}
	rawShell := string(shell)
	if strings.TrimSpace(rawShell) == "" {
		return ShellCommand{}, errors.New("shell selector cannot be blank")
	}
	if strings.TrimSpace(rawShell) != rawShell {
		return ShellCommand{}, errors.New("shell selector cannot contain leading or trailing whitespace")
	}
	if strings.Contains(rawShell, "\x00") {
		return ShellCommand{}, errors.New("shell selector cannot contain NUL bytes")
	}
	if !shell.Valid() {
		return ShellCommand{}, fmt.Errorf("unsupported shell selector %q", shell)
	}
	return ShellCommand{Command: command, Shell: shell}, nil
}

func (selector ShellSelector) Valid() bool {
	switch selector {
	case ShellDefault,
		ShellSH,
		ShellBash,
		ShellZSH,
		ShellPWSH,
		ShellPowerShell,
		ShellCMD:
		return true
	default:
		return false
	}
}

func NormalizeIOMode(value IOMode) (IOMode, error) {
	if value == "" {
		return IOModePipe, nil
	}
	switch value {
	case IOModePipe, IOModePTY:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported io_mode %q", value)
	}
}

func IsHomeRelativeCwd(cwd string) bool {
	return cwd == "~" || strings.HasPrefix(cwd, "~/")
}

func ResolveShellCommand(command string, shell ShellSelector, goos string) ([]string, error) {
	spec, err := NormalizeShellCommand(command, shell)
	if err != nil {
		return nil, err
	}
	template, err := ShellArgvTemplate(spec.Shell, goos)
	if err != nil {
		return nil, err
	}
	argv := make([]string, len(template))
	for i, item := range template {
		if item == "${command}" {
			argv[i] = spec.Command
			continue
		}
		argv[i] = item
	}
	return argv, nil
}

func ShellArgvTemplate(shell ShellSelector, goos string) ([]string, error) {
	if shell == "" {
		shell = ShellDefault
	}
	switch shell {
	case ShellDefault:
		if goos == "windows" {
			return []string{"cmd.exe", "/d", "/s", "/c", "${command}"}, nil
		}
		return []string{"/bin/sh", "-c", "${command}"}, nil
	case ShellSH:
		return []string{"/bin/sh", "-c", "${command}"}, nil
	case ShellBash:
		return []string{"bash", "-c", "${command}"}, nil
	case ShellZSH:
		return []string{"zsh", "-c", "${command}"}, nil
	case ShellPWSH:
		return []string{"pwsh", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "${command}"}, nil
	case ShellPowerShell:
		return []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "${command}"}, nil
	case ShellCMD:
		return []string{"cmd.exe", "/d", "/s", "/c", "${command}"}, nil
	default:
		return nil, fmt.Errorf("unsupported shell selector %q", shell)
	}
}

func CommandLabel(command string) string {
	const maxBytes = 160
	if len(command) <= maxBytes {
		return command
	}
	end := maxBytes
	for !utf8.RuneStart(command[end]) {
		end--
	}
	return command[:end]
}
