package boxd

import (
	"strings"
	"testing"
)

func TestBootPayloadExportsEnvBeforeBootScript(t *testing.T) {
	payload, err := bootPayload(map[string]string{
		"ZETA":  "last",
		"ALPHA": "it's $HOME `x`",
		"_OK1":  "",
	})
	if err != nil {
		t.Fatalf("build boot payload: %v", err)
	}
	text := string(payload)
	exports := "export ALPHA='it'\\''s $HOME `x`'\nexport ZETA='last'\nexport _OK1=''\n"
	if !strings.HasPrefix(text, "#!/bin/sh\n"+exports) {
		t.Fatalf("payload exports = %q", text[:min(len(text), 200)])
	}
	if !strings.Contains(text, "start --no-service") {
		t.Fatalf("payload is missing the managed boot script:\n%s", text)
	}
	if strings.Index(text, exports) > strings.Index(text, "set -eu") {
		t.Fatal("env exports must precede the boot script")
	}
}

func TestBootPayloadRejectsInvalidEnv(t *testing.T) {
	for _, env := range []map[string]string{
		{"1BAD": "value"},
		{"BAD-KEY": "value"},
		{"BAD KEY": "value"},
		{"": "value"},
		{"NUL": "a\x00b"},
	} {
		if _, err := bootPayload(env); err == nil {
			t.Fatalf("expected env %v to be rejected", env)
		}
	}
}

func TestLauncherCommandDetachesBootScript(t *testing.T) {
	command := launcherCommand()
	for _, want := range []string{
		`setsid /bin/sh -c 'echo $$ >"$1/pid" && exec /bin/sh "$1/boot"'`,
		`cat >"$b/boot.tmp"`,
		"already running",
		"</dev/null",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("launcher command missing %q:\n%s", want, command)
		}
	}
	if strings.HasSuffix(command, "\n") {
		t.Fatal("launcher command must be trimmed")
	}
}
