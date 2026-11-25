package cronschedule

import (
	"slices"
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

func TestNextFiresFixedTimesOncePerLocalDayAcrossDSTChanges(t *testing.T) {
	utc := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
	}
	tests := []struct {
		name       string
		expression string
		timezone   string
		from, to   time.Time
		want       []time.Time
	}{
		{
			name:       "fall back runs a fixed time in the repeated hour once",
			expression: "30 1 * * *",
			timezone:   "America/New_York",
			from:       utc(time.October, 31, 12, 0),
			to:         utc(time.November, 2, 12, 0),
			want:       []time.Time{utc(time.November, 1, 5, 30), utc(time.November, 2, 6, 30)},
		},
		{
			name:       "fall back in Europe runs a fixed time in the repeated hour once",
			expression: "30 2 * * *",
			timezone:   "Europe/Berlin",
			from:       utc(time.October, 24, 12, 0),
			to:         utc(time.October, 26, 12, 0),
			want:       []time.Time{utc(time.October, 25, 0, 30), utc(time.October, 26, 1, 30)},
		},
		{
			name:       "fall back keeps interval schedules running through the repeated hour",
			expression: "*/30 * * * *",
			timezone:   "America/New_York",
			from:       utc(time.November, 1, 4, 10),
			to:         utc(time.November, 1, 7, 10),
			want: []time.Time{
				utc(time.November, 1, 4, 30), utc(time.November, 1, 5, 0), utc(time.November, 1, 5, 30),
				utc(time.November, 1, 6, 0), utc(time.November, 1, 6, 30), utc(time.November, 1, 7, 0),
			},
		},
		{
			name:       "spring forward runs a skipped fixed time when the clocks change",
			expression: "30 2 * * *",
			timezone:   "America/New_York",
			from:       utc(time.March, 7, 12, 0),
			to:         utc(time.March, 9, 12, 0),
			want:       []time.Time{utc(time.March, 8, 7, 0), utc(time.March, 9, 6, 30)},
		},
		{
			name:       "spring forward at midnight still runs a daily midnight schedule",
			expression: "0 0 * * *",
			timezone:   "America/Santiago",
			from:       utc(time.September, 4, 12, 0),
			to:         utc(time.September, 7, 12, 0),
			want: []time.Time{
				utc(time.September, 5, 4, 0), utc(time.September, 6, 4, 0), utc(time.September, 7, 3, 0),
			},
		},
		{
			name:       "spring forward leaves fixed times outside the skipped hour alone",
			expression: "30 3 * * *",
			timezone:   "America/New_York",
			from:       utc(time.March, 7, 12, 0),
			to:         utc(time.March, 9, 12, 0),
			want:       []time.Time{utc(time.March, 8, 7, 30), utc(time.March, 9, 7, 30)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Mirror the store: each completed firing schedules the next run from
			// the time it completed, shortly after the run was due.
			var fired []time.Time
			after := test.from
			for {
				next, err := Next(test.expression, test.timezone, after)
				if err != nil {
					t.Fatal(err)
				}
				if next.After(test.to) {
					break
				}
				fired = append(fired, next)
				after = next.Add(time.Minute)
			}
			if !slices.EqualFunc(fired, test.want, time.Time.Equal) {
				t.Fatalf("fired at %v, want %v", fired, test.want)
			}
		})
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

func TestRenderMessageRejectsDatabaseUnsafeOutput(t *testing.T) {
	for _, test := range []struct{ name, template, trigger string }{
		{name: "literal NUL", template: "before\x00after", trigger: "sample"},
		{name: "escaped NUL", template: `{{ "\x00" }}`, trigger: "sample"},
		{name: "printf NUL", template: `{{ printf "%c" 0 }}`, trigger: "sample"},
		{name: "escaped non-UTF-8", template: `{{ "\xff" }}`, trigger: "sample"},
		{name: "NUL from data", template: `Run {{ .trigger.name }}`, trigger: "before\x00after"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := MessageData(test.trigger, time.Time{}, nil)
			if _, err := RenderMessage(test.template, data); err == nil {
				t.Fatal("expected database-unsafe rendered text to be rejected")
			}
		})
	}
}

func TestValidateMessageTemplateRejectsUnrenderedNUL(t *testing.T) {
	if err := ValidateMessageTemplate("{{/* before\x00after */}}ok"); err == nil {
		t.Fatal("expected NUL in the persisted template source to be rejected")
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
