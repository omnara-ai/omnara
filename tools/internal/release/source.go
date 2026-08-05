package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	outputNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Output struct {
	Name  string
	Value string
}

func ValidateCommit(commit string) error {
	if !commitPattern.MatchString(commit) {
		return errors.New("release commit is not a full Git commit")
	}
	return nil
}

func ResolveSourceCommit(ctx context.Context, directory string, mainRef string) (string, error) {
	commit, err := gitOutput(ctx, directory, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	if err := ValidateCommit(commit); err != nil {
		return "", err
	}
	if err := runGit(ctx, directory, "merge-base", "--is-ancestor", commit, mainRef); err != nil {
		return "", fmt.Errorf("release commit must belong to main: %w", err)
	}
	return commit, nil
}

func AppendGitHubOutputs(path string, outputs ...Output) error {
	if path == "" {
		return errors.New("GITHUB_OUTPUT is required")
	}
	for _, output := range outputs {
		if !outputNamePattern.MatchString(output.Name) {
			return fmt.Errorf("invalid GitHub output name %q", output.Name)
		}
		if strings.ContainsAny(output.Value, "\r\n") {
			return fmt.Errorf("GitHub output %s contains a newline", output.Name)
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open GitHub output: %w", err)
	}
	for _, output := range outputs {
		if _, err := fmt.Fprintf(file, "%s=%s\n", output.Name, output.Value); err != nil {
			_ = file.Close()
			return fmt.Errorf("write GitHub output: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close GitHub output: %w", err)
	}
	return nil
}

func runGit(ctx context.Context, directory string, args ...string) error {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return nil
}

func gitOutput(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
