package log

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestScrubLogStringRemovesURLParameters(t *testing.T) {
	input := "GET https://user:pass@example.com/v1/messages?token=secret&include=body#section and /agents?code=abc."
	got := ScrubLogString(input)
	for _, forbidden := range []string{"token=secret", "include=body", "code=abc", "user:pass@"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("ScrubLogString() leaked %q in %q", forbidden, got)
		}
	}
	for _, want := range []string{"https://example.com/v1/messages#section", "/agents."} {
		if !strings.Contains(got, want) {
			t.Fatalf("ScrubLogString() = %q, want %q", got, want)
		}
	}
}

func TestScrubLogStringRedactsSensitiveAssignments(t *testing.T) {
	input := `provider failed token=tok_123 password="hunter2" code=${code} ` +
		`Authorization: Bearer tok_456 Bearer tok_789 {"api_key":"sk-test","message":"keep"}`
	got := ScrubLogString(input)
	for _, forbidden := range []string{"tok_123", "hunter2", "${code}", "tok_456", "tok_789", "sk-test"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("ScrubLogString() leaked %q in %q", forbidden, got)
		}
	}
	if strings.Count(got, redactedLogValue) != 6 {
		t.Fatalf("ScrubLogString() = %q, want six redactions", got)
	}
	for _, want := range []string{"Authorization: Bearer " + redactedLogValue, "Bearer " + redactedLogValue} {
		if !strings.Contains(got, want) {
			t.Fatalf("ScrubLogString() = %q, want %q", got, want)
		}
	}
	if !strings.Contains(got, `"message":"keep"`) {
		t.Fatalf("ScrubLogString() removed unrelated content: %q", got)
	}
}

func TestJSONHandlerScrubsDirectLogValues(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, jsonHandlerOptions(true)))
	logger.ErrorContext(
		context.Background(),
		"request failed",
		"error",
		errors.New("post https://example.com/callback?code=abc token=tok_123"),
		"url",
		"https://example.com/path?password=secret",
	)

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"error", "url"} {
		got, _ := record[key].(string)
		for _, forbidden := range []string{"code=abc", "tok_123", "password=secret"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%s leaked %q in %q", key, forbidden, got)
			}
		}
	}
	if record["message"] != "request failed" {
		t.Fatalf("message key normalization failed: %+v", record)
	}
}

func TestNewJSONHandlerScrubsDirectLogValues(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewJSONHandler(&buf, nil))
	logger.ErrorContext(
		context.Background(),
		"request failed",
		"error",
		errors.New("post https://example.com/callback?code=abc token=tok_123"),
	)

	raw := buf.String()
	for _, forbidden := range []string{"code=abc", "tok_123"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("json log output leaked %q in %s", forbidden, raw)
		}
	}
}

func TestEventDoneScrubsAttachedStringField(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, jsonHandlerOptions(true)))
	ctx := WithLogger(context.Background(), logger)
	event := NewEvent(ctx, "provider.callback")
	event.Attach(
		Fields{"detail": "callback https://example.com/oauth/callback?code=abc123&state=xyz failed token=tok_secret"},
	)
	event.Done(ctx)

	raw := buf.String()
	for _, forbidden := range []string{"code=abc123", "state=xyz", "tok_secret"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("json log output leaked %q in %s", forbidden, raw)
		}
	}

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v\n%s", err, raw)
	}
	detail, _ := record["detail"].(string)
	if !strings.Contains(detail, "https://example.com/oauth/callback") || !strings.Contains(detail, redactedLogValue) {
		t.Fatalf("detail was not scrubbed as expected: %q", detail)
	}
	if record["message"] != "provider.callback" || record["event.name"] != "provider.callback" {
		t.Fatalf("event log identity missing: %+v", record)
	}
}
