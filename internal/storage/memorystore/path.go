package memorystore

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/skills"
)

const Root = "/memory"
const MaxPathBytes = 1024

func ValidatePath(value string) error {
	if len(value) == 0 || len(value) > MaxPathBytes || !utf8.ValidString(value) {
		return errors.New("memory path must contain 1–1024 UTF-8 bytes")
	}
	for _, part := range strings.Split(value, "/") {
		if len(part) > 255 || part == "" || part == "." || part == ".." {
			return errors.New("memory path contains an invalid component")
		}
	}
	for _, r := range value {
		if r == '\\' || r == '*' || r == '?' || unicode.IsControl(r) {
			return errors.New("memory path contains an unsupported character")
		}
	}
	return nil
}

func ParsePath(value string) (string, string, error) {
	rest, ok := strings.CutPrefix(value, Root+"/")
	if !ok {
		return "", "", errors.New("path must start with /memory/")
	}
	name, path, ok := strings.Cut(rest, "/")
	if !ok {
		return "", "", errors.New("memory path must identify a file within a store")
	}
	if err := skills.ValidateName(name); err != nil {
		return "", "", err
	}
	if err := ValidatePath(path); err != nil {
		return "", "", err
	}
	return name, path, nil
}
