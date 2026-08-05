package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

const (
	SignatureHeader  = "X-Slack-Signature"
	TimestampHeader  = "X-Slack-Request-Timestamp"
	signatureVersion = "v0"
	signatureMaxSkew = 5 * time.Minute
)

func ValidSignature(header http.Header, body []byte, signingSecret string, now time.Time) bool {
	timestamp := header.Get(TimestampHeader)
	signature := header.Get(SignatureHeader)
	if timestamp == "" || signature == "" || signingSecret == "" {
		return false
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	signedAt := time.Unix(seconds, 0)
	if now.Sub(signedAt) > signatureMaxSkew || signedAt.Sub(now) > signatureMaxSkew {
		return false
	}
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte(signatureVersion + ":" + timestamp + ":"))
	_, _ = mac.Write(body)
	expected := signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
