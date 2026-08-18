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

func TestRenderMessageBoundsPrintf(t *testing.T) {
	data := MessageData("sample", time.Time{}, nil)
	rendered, err := RenderMessage(`{{ printf "%6s!" .trigger.name }}`, data)
	if err != nil {
		t.Fatalf("render bounded printf: %v", err)
	}
	if rendered != "sample!" {
		t.Fatalf("rendered = %q, want %q", rendered, "sample!")
	}
	rejected := []string{
		`{{ printf "%1000000000s" "" }}`,
		`{{ printf "%.1000000000f" 1.0 }}`,
		`{{ printf "%01000000000d" 1 }}`,
		`{{ printf "%*s" 1000000000 "" }}`,
		`{{ printf "%[2]*[1]s" "" 1000 }}`,
		`{{ printf "%.[2]*[1]f" 1.0 1000 }}`,
		`{{ printf "%[1]999999d" 1 }}`,
		`{{ printf "%999999[1]d" 1 }}`,
		`{{ printf "%[1]s" "sample" }}`,
		`{{ printf "%[1]s%[1]s" "sample" }}`,
	}
	for _, messageTemplate := range rejected {
		if _, err := RenderMessage(messageTemplate, data); err == nil {
			t.Fatalf("expected printf bomb to be rejected: %s", messageTemplate)
		}
	}
}

func TestRenderMessageAllowsLiteralPrintfStarsAndBrackets(t *testing.T) {
	rendered, err := RenderMessage(
		`{{ printf "[%s] 2 * 3 = %d %%[1]*" .trigger.name 6 }}`,
		MessageData("sample", time.Time{}, nil),
	)
	if err != nil {
		t.Fatalf("render literals in printf format: %v", err)
	}
	if rendered != "[sample] 2 * 3 = 6 %[1]*" {
		t.Fatalf("rendered = %q", rendered)
	}
}

func TestRenderMessageBoundsCumulativePrintfWork(t *testing.T) {
	format := strings.Repeat("%1024s", maxRenderedMessageBytes/1024+1)
	if err := validatePrintfFormat(format); err == nil {
		t.Fatal("expected cumulative printf widths to be rejected")
	}

	precisionFormat := strings.Repeat("%.1024f", maxRenderedMessageBytes/1024+1)
	if err := validatePrintfFormat(precisionFormat); err == nil {
		t.Fatal("expected cumulative printf precisions to be rejected")
	}
}

func TestBoundedFormatFuncRejectsAmplifiedArgumentsBeforeFormatting(t *testing.T) {
	called := false
	bounded := boundedFormatFunc(func(...any) string {
		called = true
		return ""
	})
	_, err := bounded(strings.Repeat("a", maxRenderedMessageBytes), "b")
	if err == nil {
		t.Fatal("expected amplified formatting arguments to be rejected")
	}
	if called {
		t.Fatal("formatter was called before its arguments were bounded")
	}
}

func TestRenderMessageBoundsBuiltinEscapers(t *testing.T) {
	data := MessageData("sample", time.Time{}, nil)
	for _, function := range []string{"html", "js", "urlquery"} {
		messageTemplate := `{{ $a := printf "%1000s" "" }}{{ ` + function + ` ` +
			strings.Repeat("$a ", 70) + `}}`
		if _, err := RenderMessage(messageTemplate, data); err == nil {
			t.Fatalf("expected amplified %s output to be rejected", function)
		}
	}
}

func TestRenderMessageRejectsTemplateInvocations(t *testing.T) {
	messageTemplate := `{{ define "loop" }}{{ template "loop" }}{{ end }}` +
		`{{ template "loop" }}`
	if _, err := RenderMessage(
		messageTemplate,
		MessageData("sample", time.Time{}, nil),
	); err == nil || !strings.Contains(err.Error(), "template invocations are not supported") {
		t.Fatal("expected recursive template invocation to be rejected")
	}

	if _, err := RenderMessage(
		`{{ block "message" . }}hello{{ end }}`,
		MessageData("sample", time.Time{}, nil),
	); err == nil || !strings.Contains(err.Error(), "template invocations are not supported") {
		t.Fatal("expected block invocation to be rejected")
	}
}

func TestRenderMessageBoundsFormattedValues(t *testing.T) {
	data := MessageData("sample", time.Time{}, nil)
	doubling := `{{ $a := "aaaaaaaaaaaaaaaa" }}` +
		strings.Repeat(`{{ $a = printf "%s%s%s%s" $a $a $a $a }}`, 10) +
		`{{ len $a }}`
	if _, err := RenderMessage(doubling, data); err == nil {
		t.Fatal("expected amplified printf output to be rejected")
	}
	if _, err := RenderMessage(
		`{{ $a := printf "%1000s" "" }}{{ print $a $a `+strings.Repeat("$a ", 70)+`}}`,
		data,
	); err == nil {
		t.Fatal("expected amplified print output to be rejected")
	}
}

func TestRenderMessageRejectsMissingKeys(t *testing.T) {
	if _, err := RenderMessage("{{ .missing }}", MessageData("sample", time.Time{}, nil)); err == nil {
		t.Fatal("expected missing key to be rejected")
	}
}
