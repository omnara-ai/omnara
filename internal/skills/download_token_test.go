package skills

import (
	"errors"
	"testing"
	"time"
)

func TestDownloadTokenIsBoundToMachineSkillAndRevision(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	token, expiresAt, err := MintDownloadToken(key, "skl_one", "skr_one", "mch_one", now)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if err := VerifyDownloadToken(
		key,
		token,
		"skl_one",
		"skr_one",
		"mch_one",
		expiresAt,
		now,
	); err != nil {
		t.Fatalf("verify token: %v", err)
	}
	for name, values := range map[string][3]string{
		"skill":    {"skl_two", "skr_one", "mch_one"},
		"revision": {"skl_one", "skr_two", "mch_one"},
		"machine":  {"skl_one", "skr_one", "mch_two"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyDownloadToken(
				key,
				token,
				values[0],
				values[1],
				values[2],
				expiresAt,
				now,
			); !errors.Is(err, ErrInvalidDownloadToken) {
				t.Fatalf("verify changed binding error = %v, want invalid token", err)
			}
		})
	}
	if err := VerifyDownloadToken(
		key,
		token,
		"skl_one",
		"skr_one",
		"mch_one",
		expiresAt,
		now.Add(DownloadTokenTTL+time.Second),
	); !errors.Is(err, ErrDownloadTokenExpired) {
		t.Fatalf("verify expired token error = %v, want expired", err)
	}
}
