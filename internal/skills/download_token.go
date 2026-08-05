package skills

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// DownloadTokenTTL bounds how long a machine may use one skill offer to
// download the offered revision.
const DownloadTokenTTL = 5 * time.Minute

var (
	ErrDownloadTokenExpired = errors.New("skill download token expired")
	ErrInvalidDownloadToken = errors.New("invalid skill download token")
)

// MintDownloadToken returns a short-lived capability bound to one machine,
// skill, and revision.
func MintDownloadToken(
	signingKey []byte,
	skillPublicID, revisionPublicID, machinePublicID string,
	now time.Time,
) (string, int64, error) {
	if len(signingKey) == 0 {
		return "", 0, errors.New("skill download signing key is not configured")
	}
	if skillPublicID == "" || revisionPublicID == "" || machinePublicID == "" {
		return "", 0, errors.New("skill, revision, and machine public ids are required")
	}
	expiresAt := now.Add(DownloadTokenTTL).Unix()
	return SignDownload(signingKey, skillPublicID, revisionPublicID, machinePublicID, expiresAt), expiresAt, nil
}

// SignDownload returns the hex HMAC-SHA256 for a machine's offered skill
// revision. The version prefix permits future token format changes.
func SignDownload(
	key []byte,
	skillPublicID, revisionPublicID, machinePublicID string,
	expiresAt int64,
) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(
		mac,
		"v1\n%s\n%s\n%s\n%d",
		skillPublicID,
		revisionPublicID,
		machinePublicID,
		expiresAt,
	)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyDownloadToken(
	key []byte,
	token, skillPublicID, revisionPublicID, machinePublicID string,
	expiresAt int64,
	now time.Time,
) error {
	if len(key) == 0 {
		return errors.New("skill download signing key is not configured")
	}
	if expiresAt <= now.Unix() {
		return ErrDownloadTokenExpired
	}
	expected := SignDownload(key, skillPublicID, revisionPublicID, machinePublicID, expiresAt)
	if !hmac.Equal([]byte(token), []byte(expected)) {
		return ErrInvalidDownloadToken
	}
	return nil
}
