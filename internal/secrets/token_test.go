package secrets

import (
	"context"
	"strings"
	"testing"
)

func sealedTokenTestWrapper(t *testing.T) KeyWrapper {
	t.Helper()
	wrapper, err := NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new local key wrapper: %v", err)
	}
	return wrapper
}

func TestSealTokenRoundTrip(t *testing.T) {
	ctx := context.Background()
	wrapper := sealedTokenTestWrapper(t)
	plaintext := []byte(`{"code_verifier":"abc","expires_at":"2026-01-01T00:00:00Z"}`)

	token, err := SealToken(ctx, wrapper, "mcp-oauth-state", plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.ContainsAny(token, "+/=") {
		t.Fatalf("token %q is not URL-safe", token)
	}
	opened, err := OpenToken(ctx, wrapper, "mcp-oauth-state", token)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != string(plaintext) {
		t.Fatalf("opened = %q, want original plaintext", opened)
	}
}

func TestOpenTokenRejectsWrongPurpose(t *testing.T) {
	ctx := context.Background()
	wrapper := sealedTokenTestWrapper(t)
	token, err := SealToken(ctx, wrapper, "purpose-a", []byte("payload"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := OpenToken(ctx, wrapper, "purpose-b", token); err == nil {
		t.Fatal("open with wrong purpose succeeded, want error")
	}
}

func TestOpenTokenRejectsTampering(t *testing.T) {
	ctx := context.Background()
	wrapper := sealedTokenTestWrapper(t)
	token, err := SealToken(ctx, wrapper, "purpose", []byte("payload"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, bad := range []string{
		"",
		"not-base64!*",
		token[:len(token)-2],
		token[:len(token)-4] + "AAAA",
		strings.Repeat("A", len(token)),
	} {
		if bad == token {
			continue
		}
		if _, err := OpenToken(ctx, wrapper, "purpose", bad); err == nil {
			t.Fatalf("open of corrupted token %q succeeded, want error", bad)
		}
	}
}

func TestOpenTokenRejectsWrongKey(t *testing.T) {
	ctx := context.Background()
	wrapper := sealedTokenTestWrapper(t)
	otherWrapper, err := NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("fedcba9876543210fedcba9876543210")},
	)
	if err != nil {
		t.Fatalf("new local key wrapper: %v", err)
	}
	token, err := SealToken(ctx, wrapper, "purpose", []byte("payload"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := OpenToken(ctx, otherWrapper, "purpose", token); err == nil {
		t.Fatal("open with different key succeeded, want error")
	}
}
