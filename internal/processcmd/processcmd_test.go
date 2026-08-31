package processcmd

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExpandHomeRelativePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "~", want: home},
		{path: "~/", want: home},
		{path: "~/repo/../file", want: filepath.Join(home, "file")},
		{path: "relative/file", want: "relative/file"},
		{path: "~other/file", want: "~other/file"},
	} {
		got, err := ExpandHomeRelativePath(test.path)
		if err != nil {
			t.Fatalf("expand %q: %v", test.path, err)
		}
		if got != test.want {
			t.Fatalf("expand %q = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestResolveShellCommandDefaultUsesExecutionOS(t *testing.T) {
	cases := []struct {
		name string
		goos string
		want []string
	}{
		{name: "unix", goos: "linux", want: []string{"/bin/sh", "-c", "echo ok"}},
		{name: "darwin", goos: "darwin", want: []string{"/bin/sh", "-c", "echo ok"}},
		{name: "windows", goos: "windows", want: []string{"cmd.exe", "/d", "/s", "/c", "echo ok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveShellCommand("echo ok", ShellDefault, tc.goos)
			if err != nil {
				t.Fatalf("resolve shell command: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestResolveShellCommandExplicitSelectorIgnoresDefaultOS(t *testing.T) {
	got, err := ResolveShellCommand("echo ok", ShellSH, "windows")
	if err != nil {
		t.Fatalf("resolve explicit sh: %v", err)
	}
	want := []string{"/bin/sh", "-c", "echo ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestNormalizeIOMode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value IOMode
		want  IOMode
		ok    bool
	}{
		{want: IOModePipe, ok: true},
		{value: "pipe", want: IOModePipe, ok: true},
		{value: "pty", want: IOModePTY, ok: true},
		{value: "invalid"},
	} {
		got, err := NormalizeIOMode(test.value)
		if (err == nil) != test.ok || got != test.want {
			t.Fatalf(
				"NormalizeIOMode(%q) = %q, %v; want %q, success %t",
				test.value,
				got,
				err,
				test.want,
				test.ok,
			)
		}
	}
}

func TestCommandLabelTruncatesAtAUTF8Boundary(t *testing.T) {
	command := strings.Repeat("a", 159) + "界" + "tail"
	label := CommandLabel(command)
	if label != strings.Repeat("a", 159) {
		t.Fatalf("command label = %q", label)
	}
	if !utf8.ValidString(label) {
		t.Fatalf("command label is not valid UTF-8: %q", label)
	}
}
