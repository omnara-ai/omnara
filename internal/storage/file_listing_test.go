package storage

import (
	"strings"
	"testing"
)

func TestCompileFilePatternDirectoryDepth(t *testing.T) {
	for _, test := range []struct {
		pattern, path string
		match         bool
	}{
		{"/*", "/memory", true}, {"/*", "/memory/team", false},
		{"/memory/*", "/memory/team", true}, {"/memory/*", "/memory/team/a.md", false},
		{"/memory/team/**/*.md", "/memory/team/a.md", true},
		{"/memory/team/**/*.md", "/memory/team/deep/a.md", true},
		{"/memory/team/**/*.md", "/memory/team/deep/a.txt", false},
		{"/memory/team/*", "/memory/team", false},
		{"/memory/team", "/memory/team", true},
	} {
		t.Run(test.pattern+test.path, func(t *testing.T) {
			glob, err := CompileFilePattern(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if glob.MatchString(test.path) != test.match {
				t.Fatalf("%q matching %q: want %v", test.pattern, test.path, test.match)
			}
		})
	}
}

func TestFilePatternMaximumMemoryPath(t *testing.T) {
	pattern := "/memory/" + strings.Repeat("a", 64) + "/" + strings.Repeat("b", 1024)
	matcher, err := CompileFilePattern(pattern)
	if err != nil || !matcher.MatchString(pattern) {
		t.Fatalf("maximum memory path: %v", err)
	}
	if _, err = CompileFilePattern(pattern + "b"); err == nil {
		t.Fatal("accepted an oversized pattern")
	}
}

func TestFilePatternRejectsTraversal(t *testing.T) {
	for _, pattern := range []string{
		"../memory/team/*", "/memory/team/../other/*", "/memory/team/**/../../other/*.md",
		"/memory/team/./note.md", "/memory/team//note.md", "/memory/team/..\\x/*",
	} {
		if _, err := CompileFilePattern(pattern); err == nil {
			t.Errorf("accepted %q", pattern)
		}
	}
}
