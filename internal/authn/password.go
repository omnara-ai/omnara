package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordLength = 12
	MaxPasswordBytes  = 1024

	argon2MemoryKiB   uint32 = 19456
	argon2Iterations  uint32 = 2
	argon2Parallelism uint8  = 1
	argon2SaltBytes          = 16
	argon2KeyBytes    uint32 = 32
)

const passwordTimingEqualizerHash = "$argon2id$v=19$m=19456,t=2,p=1$c2ltdWxhdGVkLXB3LXNhbHQ$Gd3LkwlwmwgZDwDqBcK3dgKFANnGzYIFbSrHJup/2SQ"

var ErrInvalidPasswordHash = errors.New("invalid password hash")

type PasswordPolicyError struct {
	Reason string
}

func (e PasswordPolicyError) Error() string {
	return e.Reason
}

func ValidateNewPassword(password, normalizedEmail string) error {
	if len(password) > MaxPasswordBytes {
		return PasswordPolicyError{Reason: "password is too long"}
	}
	characters := 0
	hasLower := false
	hasUpper := false
	hasNumber := false
	hasSymbol := false
	for _, r := range password {
		characters++
		hasLower = hasLower || r >= 'a' && r <= 'z'
		hasUpper = hasUpper || r >= 'A' && r <= 'Z'
		hasNumber = hasNumber || r >= '0' && r <= '9'
		hasSymbol = hasSymbol ||
			r >= '!' && r <= '/' ||
			r >= ':' && r <= '@' ||
			r >= '[' && r <= '`' ||
			r >= '{' && r <= '~'
	}
	if characters < MinPasswordLength {
		return PasswordPolicyError{Reason: fmt.Sprintf("password must be at least %d characters", MinPasswordLength)}
	}
	if !hasLower || !hasUpper || !hasNumber || !hasSymbol {
		return PasswordPolicyError{
			Reason: "password must include at least one lowercase letter, one uppercase letter, one number, and one symbol",
		}
	}
	lower := strings.ToLower(password)
	normalizedEmail = strings.ToLower(strings.TrimSpace(normalizedEmail))
	if normalizedEmail != "" && strings.Contains(lower, normalizedEmail) {
		return PasswordPolicyError{Reason: "password must not include the email address"}
	}
	return nil
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2MemoryKiB, argon2Parallelism, argon2KeyBytes)
	encodedSalt := base64.RawStdEncoding.EncodeToString(salt)
	encodedKey := base64.RawStdEncoding.EncodeToString(key)
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2MemoryKiB,
		argon2Iterations,
		argon2Parallelism,
		encodedSalt,
		encodedKey,
	), nil
}

func VerifyPassword(password, encodedHash string) (bool, error) {
	params, salt, want, err := parsePasswordHash(encodedHash)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, params.iterations, params.memoryKiB, params.parallelism, uint32(len(want)))
	ok := subtle.ConstantTimeCompare(got, want) == 1
	return ok, nil
}

func PasswordHashNeedsRehash(encodedHash string) (bool, error) {
	params, salt, key, err := parsePasswordHash(encodedHash)
	if err != nil {
		return false, err
	}
	return params.memoryKiB != argon2MemoryKiB ||
		params.iterations != argon2Iterations ||
		params.parallelism != argon2Parallelism ||
		len(salt) != argon2SaltBytes ||
		len(key) != int(argon2KeyBytes), nil
}

func EqualizePasswordVerifyTiming(password string) {
	_, _ = VerifyPassword(password, passwordTimingEqualizerHash)
}

type passwordHashParams struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
}

func parsePasswordHash(encodedHash string) (passwordHashParams, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return passwordHashParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params, err := parseArgon2Params(parts[3])
	if err != nil {
		return passwordHashParams{}, nil, nil, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return passwordHashParams{}, nil, nil, ErrInvalidPasswordHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return passwordHashParams{}, nil, nil, ErrInvalidPasswordHash
	}
	return params, salt, key, nil
}

func parseArgon2Params(value string) (passwordHashParams, error) {
	params := passwordHashParams{}
	for _, part := range strings.Split(value, ",") {
		key, raw, ok := strings.Cut(part, "=")
		if !ok {
			return passwordHashParams{}, ErrInvalidPasswordHash
		}
		switch key {
		case "m":
			parsed, err := strconv.ParseUint(raw, 10, 32)
			if err != nil || parsed == 0 {
				return passwordHashParams{}, ErrInvalidPasswordHash
			}
			params.memoryKiB = uint32(parsed)
		case "t":
			parsed, err := strconv.ParseUint(raw, 10, 32)
			if err != nil || parsed == 0 {
				return passwordHashParams{}, ErrInvalidPasswordHash
			}
			params.iterations = uint32(parsed)
		case "p":
			parsed, err := strconv.ParseUint(raw, 10, 8)
			if err != nil || parsed == 0 {
				return passwordHashParams{}, ErrInvalidPasswordHash
			}
			params.parallelism = uint8(parsed)
		default:
			return passwordHashParams{}, ErrInvalidPasswordHash
		}
	}
	if params.memoryKiB == 0 || params.iterations == 0 || params.parallelism == 0 {
		return passwordHashParams{}, ErrInvalidPasswordHash
	}
	return params, nil
}
