package boxd

import (
	"strings"
	"testing"
)

func TestBootPayloadExportsEnvBeforeBootScript(t *testing.T) {
	text := string(bootPayload(map[string]string{
		"ZETA":  "last",
		"ALPHA": "it's $HOME `x`",
		"_OK1":  "",
	}, "boot-script"))
	exports := "export ALPHA='it'\\''s $HOME `x`'\nexport ZETA='last'\nexport _OK1=''\n"
	if text != "#!/bin/sh\n"+exports+"\nboot-script\n" {
		t.Fatalf("payload = %q", text)
	}
}

func TestBootPayloadSkipsNamesTheShellCannotExport(t *testing.T) {
	text := string(bootPayload(map[string]string{
		"GOOD":    "kept",
		"1BAD":    "dropped",
		"BAD-KEY": "dropped",
		"BAD KEY": "dropped",
		"":        "dropped",
	}, ""))
	if !strings.Contains(text, "export GOOD='kept'\n") || strings.Contains(text, "dropped") {
		t.Fatalf("payload exports = %q", text[:min(len(text), 200)])
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
