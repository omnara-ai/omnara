package integrationdb

import (
	"strings"
	"testing"
)

func TestValidateTestDatabaseURL(t *testing.T) {
	valid := []string{
		"postgres://omnara:omnara@localhost:55432/omnara?sslmode=disable",
		"postgres://omnara:secret@127.0.0.1:55432/omnara",
		"postgres://omnara:secret@127.0.0.1:56432/omnara",
	}
	for _, databaseURL := range valid {
		if err := validateTestDatabaseURL(databaseURL); err != nil {
			t.Fatalf("expected %q to be accepted: %v", databaseURL, err)
		}
	}

	invalid := []string{
		"postgres://omnara:omnara@localhost:5432/omnara",
		"postgres://omnara:omnara@localhost/omnara",
		"postgres://omnara:omnara@db.internal:55432/omnara",
		"postgres://app:omnara@localhost:55432/omnara",
		"postgres://omnara:omnara@localhost:55432/prod",
		"mysql://omnara:omnara@localhost:55432/omnara",
		"://bad",
	}
	for _, databaseURL := range invalid {
		if err := validateTestDatabaseURL(databaseURL); err == nil {
			t.Fatalf("expected %q to be rejected", databaseURL)
		}
	}
}

func TestRedactDatabaseURL(t *testing.T) {
	redacted := redactDatabaseURL("postgres://omnara:secret@localhost:55432/omnara?sslmode=disable")
	if strings.Contains(redacted, "secret") || !strings.Contains(redacted, "@localhost:55432/omnara") {
		t.Fatalf("unexpected redaction: %s", redacted)
	}
	if got := redactDatabaseURL("not-a-url"); got != "not-a-url" {
		t.Fatalf("expected URL without credentials to pass through, got %q", got)
	}
}

func TestSanitizeRunID(t *testing.T) {
	if got := sanitizeRunID("ABC123"); got != "abc123" {
		t.Fatalf("sanitizeRunID() = %q, want %q", got, "abc123")
	}
	if got := sanitizeRunID("0123456789abcdef"); got != "0123456789abcdef" {
		t.Fatalf("sanitizeRunID() = %q, want %q", got, "0123456789abcdef")
	}
	if got := sanitizeRunID("0123456789abcdef0"); got != "" {
		t.Fatalf("sanitizeRunID() = %q, want empty string", got)
	}
	if got := sanitizeRunID("not-hex"); got != "" {
		t.Fatalf("sanitizeRunID() = %q, want empty string", got)
	}
}

func TestGeneratedDatabaseHasRunIDAndPID(t *testing.T) {
	newDatabaseName := generatedDatabasePrefix + "18be01bc68724a00_abc123_456_template"
	if !generatedDatabaseHasRunIDAndPID(newDatabaseName, "abc123", 456) {
		t.Fatalf("expected %s to match run id and pid", newDatabaseName)
	}
	if generatedDatabaseHasRunIDAndPID(newDatabaseName, "18be01bc68724a00", 456) {
		t.Fatalf("expected %s not to match started-at as run id", newDatabaseName)
	}
	if generatedDatabaseHasRunIDAndPID(newDatabaseName, "abc123", 789) {
		t.Fatalf("expected %s not to match another pid", newDatabaseName)
	}

	legacyDatabaseName := generatedDatabasePrefix + "abc123_456_template"
	if generatedDatabaseHasRunIDAndPID(legacyDatabaseName, "abc123", 456) {
		t.Fatalf("expected %s not to match legacy run id and pid shape", legacyDatabaseName)
	}
}
