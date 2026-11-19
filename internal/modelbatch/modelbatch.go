// Package modelbatch holds what the worker and the API share about provider
// batch callbacks: the callback URL, its capability token, and the OpenRouter
// webhook signature.
package modelbatch

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// OpenRouterCallbackPathPattern is the API route pattern OpenRouter posts
	// batch events to.
	OpenRouterCallbackPathPattern = openRouterCallbackPath + "{batch_request_id}/{token}"

	openRouterCallbackPath = "/api/model-batches/openrouter/"

	// SignatureHeader carries `t=<unix seconds>,v1=<hex HMAC-SHA256>`.
	SignatureHeader = "X-OpenRouter-Signature"

	signatureTolerance = 5 * time.Minute
)

var ErrInvalidSignature = errors.New("invalid OpenRouter webhook signature")

// NewCallbackToken returns a random capability token and the hash to store.
func NewCallbackToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(raw)
	return token, HashCallbackToken(token), nil
}

func HashCallbackToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// OpenRouterCallbackURL builds the callback URL for one batch request, or ""
// when no public origin is configured. OpenRouter only accepts public HTTPS
// callback URLs.
func OpenRouterCallbackURL(publicURL string, id uuid.UUID, token string) string {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if !strings.HasPrefix(publicURL, "https://") || id == uuid.Nil || token == "" {
		return ""
	}
	return publicURL + openRouterCallbackPath + id.String() + "/" + token
}

// VerifyOpenRouterSignature checks the webhook signature over the raw body.
func VerifyOpenRouterSignature(secret, header string, body []byte, now time.Time) error {
	if secret == "" {
		return errors.New("webhook signing secret is required")
	}
	var timestamp, signature string
	for _, part := range strings.Split(header, ",") {
		key, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signature = value
		}
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || signature == "" {
		return ErrInvalidSignature
	}
	if age := now.Sub(time.Unix(seconds, 0)); age > signatureTolerance || age < -signatureTolerance {
		return ErrInvalidSignature
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + ","))
	mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}
