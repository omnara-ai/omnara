// Package bearertoken generates and validates long-lived bearer credentials.
package bearertoken

import (
	"crypto/rand"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

type Kind string

const (
	KindPersonalAccess Kind = "pat"
	KindOrganization   Kind = "org"
	KindDaemon         Kind = "daemon"
)

const (
	alphabet       = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	secretLength   = 43
	checksumLength = 6

	personalAccessPrefix = "omnara_pat_v1_"
	organizationPrefix   = "omnara_org_v1_"
	daemonPrefix         = "omnara_daemon_v1_"

	checksumSeparator = '_'
	base62Radix       = uint32(len(alphabet))
	// Rejecting 248–255 prevents modulo bias across the 62-character alphabet.
	unbiasedByteLimit = byte(256 - (256 % len(alphabet)))
)

var ErrInvalid = errors.New("invalid bearer token")

// Generate returns a token with at least 256 bits of secret entropy.
func Generate(kind Kind) (string, error) {
	secret, err := generateSecret(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate %s bearer token: %w", kind, err)
	}
	return format(kind, secret)
}

func format(kind Kind, secret string) (string, error) {
	prefix, ok := prefixForKind(kind)
	if !ok {
		return "", fmt.Errorf("%w: unsupported kind %q", ErrInvalid, kind)
	}
	if err := validateBase62(secret, secretLength, "secret"); err != nil {
		return "", err
	}
	body := prefix + secret
	return body + string(checksumSeparator) + encodeChecksum(crc32.ChecksumIEEE([]byte(body))), nil
}

// Parse validates a token and returns its credential kind.
func Parse(token string) (Kind, error) {
	kind, prefix, ok := kindAndPrefix(token)
	if !ok {
		return "", fmt.Errorf("%w: unsupported kind or version", ErrInvalid)
	}
	wantLength := len(prefix) + secretLength + 1 + checksumLength
	if len(token) != wantLength {
		return "", fmt.Errorf("%w: incorrect length", ErrInvalid)
	}
	separator := len(prefix) + secretLength
	if token[separator] != checksumSeparator {
		return "", fmt.Errorf("%w: missing checksum separator", ErrInvalid)
	}
	if err := validateBase62(token[len(prefix):separator], secretLength, "secret"); err != nil {
		return "", err
	}
	presented := token[separator+1:]
	if err := validateBase62(presented, checksumLength, "checksum"); err != nil {
		return "", err
	}
	want := encodeChecksum(crc32.ChecksumIEEE([]byte(token[:separator])))
	if presented != want {
		return "", fmt.Errorf("%w: checksum mismatch", ErrInvalid)
	}
	return kind, nil
}

func Validate(token string, want Kind) error {
	kind, err := Parse(token)
	if err != nil {
		return err
	}
	if kind != want {
		return fmt.Errorf("%w: got kind %q, want %q", ErrInvalid, kind, want)
	}
	return nil
}

func prefixForKind(kind Kind) (string, bool) {
	switch kind {
	case KindPersonalAccess:
		return personalAccessPrefix, true
	case KindOrganization:
		return organizationPrefix, true
	case KindDaemon:
		return daemonPrefix, true
	default:
		return "", false
	}
}

func kindAndPrefix(token string) (Kind, string, bool) {
	switch {
	case strings.HasPrefix(token, personalAccessPrefix):
		return KindPersonalAccess, personalAccessPrefix, true
	case strings.HasPrefix(token, organizationPrefix):
		return KindOrganization, organizationPrefix, true
	case strings.HasPrefix(token, daemonPrefix):
		return KindDaemon, daemonPrefix, true
	default:
		return "", "", false
	}
}

func validateBase62(value string, length int, part string) error {
	if len(value) != length {
		return fmt.Errorf("%w: %s must contain %d Base62 characters", ErrInvalid, part, length)
	}
	for i := range len(value) {
		character := value[i]
		if !((character >= '0' && character <= '9') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z')) {
			return fmt.Errorf("%w: %s contains a non-Base62 character", ErrInvalid, part)
		}
	}
	return nil
}

func generateSecret(source io.Reader) (string, error) {
	secret := make([]byte, 0, secretLength)
	var candidates [64]byte
	for len(secret) < secretLength {
		n, err := source.Read(candidates[:])
		for _, candidate := range candidates[:n] {
			if candidate >= unbiasedByteLimit {
				continue
			}
			secret = append(secret, alphabet[int(candidate)%len(alphabet)])
			if len(secret) == secretLength {
				break
			}
		}
		if len(secret) == secretLength {
			break
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", io.ErrNoProgress
		}
	}
	return string(secret), nil
}

func encodeChecksum(value uint32) string {
	encoded := [checksumLength]byte{}
	for i := checksumLength - 1; i >= 0; i-- {
		encoded[i] = alphabet[value%base62Radix]
		value /= base62Radix
	}
	return string(encoded[:])
}
