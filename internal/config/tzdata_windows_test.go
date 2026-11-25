package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsNamedTimeZonesWithoutGoInstallation(t *testing.T) {
	if os.Getenv("OMNARA_TEST_EMBEDDED_TZDATA") == "1" {
		location, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			month  time.Month
			offset int
		}{
			{time.January, -5 * 60 * 60},
			{time.July, -4 * 60 * 60},
		} {
			_, offset := time.Date(2026, tc.month, 15, 12, 0, 0, 0, location).Zone()
			if offset != tc.offset {
				t.Fatalf("%s UTC offset = %d, want %d", tc.month, offset, tc.offset)
			}
		}
		return
	}

	// A fresh process avoids time's cached ZONEINFO. An empty GOROOT directory
	// also prevents falling back to the Go installation used to build this test.
	t.Setenv("OMNARA_TEST_EMBEDDED_TZDATA", "1")
	t.Setenv("GOROOT", t.TempDir())
	t.Setenv("ZONEINFO", filepath.Join(t.TempDir(), "absent-zoneinfo.zip"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsNamedTimeZonesWithoutGoInstallation$")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("named time zones without installed data: %v\n%s", err, output)
	}
}
