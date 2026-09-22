package storage

import (
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
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
		{"/memory/team/**/**/?.md", "/memory/team/é.md", true},
		{"/memory/team/**/**/?.md", "/memory/team/deep/nested/a.md", true},
		{"/memory/team/**/**/?.md", "/memory/team/deep/ab.md", false},
		{"/memory/team/**", "/memory/team/deep/a.md", true},
		{"/memory/team/**", "/memory/team", false},
		{"/**/**", "/memory/team/a.md", true},
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
	pattern := memorystore.Root + "/" + strings.Repeat("a", skills.MaxSkillNameChars) + "/" + strings.Repeat("b", memorystore.MaxPathBytes)
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
