package cronschedule

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsTimezonePrefixes(t *testing.T) {
	for _, expression := range []string{
		"TZ=UTC 0 9 * * *",
		"CRON_TZ=America/New_York 0 9 * * *",
		"  TZ=UTC 0 9 * * *",
	} {
		if err := Validate(expression, "UTC"); err == nil {
			t.Fatalf("expected %q to be rejected", expression)
		}
	}
	if err := Validate("0 9 * * *", "America/New_York"); err != nil {
		t.Fatalf("valid expression rejected: %v", err)
	}
}

func TestNextRejectsTimezonePrefixes(t *testing.T) {
	after := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	if _, err := Next("CRON_TZ=UTC 0 9 * * *", "UTC", after); err == nil {
		t.Fatal("expected timezone prefix to be rejected")
	}
	next, err := Next("0 9 * * *", "UTC", after)
	if err != nil {
		t.Fatalf("valid expression rejected: %v", err)
	}
	want := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next fire = %v, want %v", next, want)
	}
}

func TestRenderMessage(t *testing.T) {
	data := MessageData("sample", time.Time{}, nil)
	rendered, err := RenderMessage("Hello {{ .trigger.name }}", data)
	if err != nil {
		t.Fatalf("render valid template: %v", err)
	}
	if rendered != "Hello sample" {
		t.Fatalf("rendered = %q, want %q", rendered, "Hello sample")
	}
}

func TestRenderMessageRejectsOversizedSource(t *testing.T) {
	data := MessageData("sample", time.Time{}, nil)
	if _, err := RenderMessage(strings.Repeat("a", MaxMessageTemplateBytes+1), data); err == nil {
		t.Fatal("expected oversized template source to be rejected")
	}
}

func TestRenderMessageRejectsOversizedOutput(t *testing.T) {
	data := MessageData("sample", time.Time{}, nil)
	if _, err := RenderMessage(`{{ printf "%70000s" "" }}`, data); err == nil {
		t.Fatal("expected oversized rendered output to be rejected")
	}
	if _, err := RenderMessage(
		strings.Repeat(`{{ printf "%1000s" "" }}`, 70),
		data,
	); err == nil {
		t.Fatal("expected cumulative rendered output limit to be enforced")
	}
}

func TestRenderMessageRejectsMissingKeys(t *testing.T) {
	if _, err := RenderMessage("{{ .missing }}", MessageData("sample", time.Time{}, nil)); err == nil {
		t.Fatal("expected missing key to be rejected")
	}
}
