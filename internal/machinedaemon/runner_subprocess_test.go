package machinedaemon

import (
	"path/filepath"
	"testing"
)

func TestCanonicalProcessCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	relative, err := filepath.Abs("relative")
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := filepath.Abs("~other")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{name: "home", cwd: "~", want: home},
		{name: "home slash", cwd: "~/", want: home},
		{name: "home relative", cwd: "~/repo", want: filepath.Join(home, "repo")},
		{name: "home parent", cwd: "~/repo/../other", want: filepath.Join(home, "other")},
		{name: "absolute", cwd: home, want: home},
		{name: "relative", cwd: "relative", want: relative},
		{name: "other user", cwd: "~other", want: otherUser},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalProcessCwd(test.cwd)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("canonical cwd = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCanonicalProcessCwdRequiresHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := canonicalProcessCwd("~/repo"); err == nil {
		t.Fatal("expected empty home error")
	}
}
