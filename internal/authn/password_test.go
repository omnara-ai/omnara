package authn

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("verify password: %v", err)
	}
	if !ok {
		t.Fatal("expected password to verify")
	}
	ok, err = VerifyPassword("wrong horse battery staple", hash)
	if err != nil {
		t.Fatalf("verify wrong password: %v", err)
	}
	if ok {
		t.Fatal("wrong password verified")
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash = %q, want argon2id PHC params", hash)
	}
	if parts := strings.Split(hash, "$"); len(parts) != 6 || parts[4] == "" || parts[5] == "" {
		t.Fatalf("hash = %q, want PHC salt and key parts", hash)
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	ok, err := VerifyPassword("password", "$argon2id$v=19$m=bad,t=2,p=1$salt$key")
	if !errors.Is(err, ErrInvalidPasswordHash) {
		t.Fatalf("error = %v, want ErrInvalidPasswordHash", err)
	}
	if ok {
		t.Fatal("malformed hash verified")
	}
}

func TestPasswordHashNeedsRehash(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	needsRehash, err := PasswordHashNeedsRehash(hash)
	if err != nil {
		t.Fatalf("needs rehash: %v", err)
	}
	if needsRehash {
		t.Fatal("current hash should not need rehash")
	}

	oldHash := testPasswordHash("correct horse battery staple", 4096, 1, 1, 32)
	needsRehash, err = PasswordHashNeedsRehash(oldHash)
	if err != nil {
		t.Fatalf("old hash needs rehash: %v", err)
	}
	if !needsRehash {
		t.Fatal("old params should need rehash")
	}

	shortSaltHash := testPasswordHashWithSalt(
		"correct horse battery staple",
		[]byte("short-salt"),
		argon2MemoryKiB,
		argon2Iterations,
		argon2Parallelism,
		argon2KeyBytes,
	)
	needsRehash, err = PasswordHashNeedsRehash(shortSaltHash)
	if err != nil {
		t.Fatalf("short salt hash needs rehash: %v", err)
	}
	if !needsRehash {
		t.Fatal("short salt should need rehash")
	}

	_, err = PasswordHashNeedsRehash("$argon2id$v=19$m=bad,t=2,p=1$salt$key")
	if !errors.Is(err, ErrInvalidPasswordHash) {
		t.Fatalf("malformed error = %v, want ErrInvalidPasswordHash", err)
	}
}

func TestValidateNewPassword(t *testing.T) {
	const classError = "password must include at least one lowercase letter, one uppercase letter, " +
		"one number, and one symbol"
	tests := []struct {
		name     string
		password string
		email    string
		wantErr  string
	}{
		{name: "valid", password: "Correct Horse Battery 1!", email: "owner@example.com"},
		{name: "exact minimum", password: "Abcdefghij1!"},
		{name: "unicode characters allowed", password: "Aåbcdefghij1!"},
		{name: "unicode does not satisfy required classes", password: "Åbcdefghij١€", wantErr: classError},
		{name: "too short", password: "Abcdef1!", wantErr: "password must be at least 12 characters"},
		{name: "too long", password: string(make([]byte, MaxPasswordBytes+1)), wantErr: "password is too long"},
		{
			name:     "missing lowercase",
			password: "ABCDEFGHIJ1!",
			wantErr:  classError,
		},
		{
			name:     "missing uppercase",
			password: "abcdefghij1!",
			wantErr:  classError,
		},
		{
			name:     "missing number",
			password: "Abcdefghijk!",
			wantErr:  classError,
		},
		{
			name:     "missing symbol",
			password: "Abcdefghij12",
			wantErr:  classError,
		},
		{
			name:     "whitespace is not a symbol",
			password: "Abcdefghij1 ",
			wantErr:  classError,
		},
		{
			name:     "blank",
			password: "            ",
			wantErr:  classError,
		},
		{
			name:     "contains email",
			password: "Owner@Example.com1A!",
			email:    "owner@example.com",
			wantErr:  "password must not include the email address",
		},
		{
			name:     "email local part substring allowed",
			password: "Andes Mountain 1!",
			email:    "an@corp.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNewPassword(tt.password, tt.email)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("error = nil, want %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("error = %q, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestEqualizePasswordVerifyTiming(t *testing.T) {
	EqualizePasswordVerifyTiming("anything")
}

func testPasswordHash(password string, memoryKiB, iterations uint32, parallelism uint8, keyBytes uint32) string {
	return testPasswordHashWithSalt(password, []byte("test-password-s"), memoryKiB, iterations, parallelism, keyBytes)
}

func testPasswordHashWithSalt(
	password string,
	salt []byte,
	memoryKiB, iterations uint32,
	parallelism uint8,
	keyBytes uint32,
) string {
	key := argon2.IDKey([]byte(password), salt, iterations, memoryKiB, parallelism, keyBytes)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memoryKiB,
		iterations,
		parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}
