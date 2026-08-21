package machinedaemon

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCanonicalProcessCwd(t *testing.T) {
	home := t.TempDir()
	relative, err := filepath.Abs("relative")
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := filepath.Abs("~other")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name           string
		cwd            string
		want           string
		wantHomeLookup bool
	}{
		{name: "home", cwd: "~", want: home, wantHomeLookup: true},
		{name: "home slash", cwd: "~/", want: home, wantHomeLookup: true},
		{name: "home relative", cwd: "~/repo", want: filepath.Join(home, "repo"), wantHomeLookup: true},
		{name: "home parent", cwd: "~/repo/../other", want: filepath.Join(home, "other"), wantHomeLookup: true},
		{name: "absolute", cwd: home, want: home},
		{name: "relative", cwd: "relative", want: relative},
		{name: "other user", cwd: "~other", want: otherUser},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			homeLookups := 0
			got, err := canonicalProcessCwd(test.cwd, func() (string, error) {
				homeLookups++
				return home, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("canonical cwd = %q, want %q", got, test.want)
			}
			if (homeLookups > 0) != test.wantHomeLookup {
				t.Fatalf("home lookups = %d, want lookup %t", homeLookups, test.wantHomeLookup)
			}
		})
	}
}

func TestCanonicalProcessCwdRequiresHome(t *testing.T) {
	homeErr := errors.New("missing home")
	_, err := canonicalProcessCwd("~/repo", func() (string, error) {
		return "", homeErr
	})
	if !errors.Is(err, homeErr) {
		t.Fatalf("canonical cwd error = %v, want %v", err, homeErr)
	}
	if _, err := canonicalProcessCwd("~/repo", func() (string, error) {
		return "", nil
	}); err == nil {
		t.Fatal("expected empty home error")
	}
}
