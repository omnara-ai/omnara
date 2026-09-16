package memorystore

import (
	"strings"
	"testing"
)

func TestPathsRejectAmbiguity(t *testing.T) {
	for _, path := range []string{"", "/a", "a/", "a//b", "a/../b", "a/./b", "a\\b", "a\x00b"} {
		if ValidatePath(path) == nil {
			t.Errorf("accepted %q", path)
		}
	}
	if err := ValidatePath("deployment/gotchas.md"); err != nil {
		t.Fatal(err)
	}
}

func TestPathComponentByteLimit(t *testing.T) {
	if err := ValidatePath(strings.Repeat("a", 255) + "/" + strings.Repeat("文", 85)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{strings.Repeat("a", 256), strings.Repeat("文", 86), "a/" + strings.Repeat("b", 256)} {
		if err := ValidatePath(name); err == nil {
			t.Fatalf("accepted oversized component: %q", name)
		}
	}
}
