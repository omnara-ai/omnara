package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

const (
	SignatureHeader   = "X-Hub-Signature-256"
	EventHeader       = "X-Github-Event"
	DeliveryHeader    = "X-Github-Delivery"
	EventBodyMaxBytes = 2 * 1024 * 1024
)

func ValidSignature(header http.Header, body []byte, secret string) bool {
	values := header.Values(SignatureHeader)
	if secret == "" || len(body) > EventBodyMaxBytes || len(values) != 1 ||
		len(values[0]) != len("sha256=")+sha256.Size*2 || !strings.HasPrefix(values[0], "sha256=") {
		return false
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(values[0], "sha256="))
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(signature, mac.Sum(nil))
}

type Installation struct {
	ID      int64 `json:"id"`
	AppID   int64 `json:"app_id"`
	Account User  `json:"account"`
}

type Issue struct {
	ID          int64  `json:"id"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	User        User   `json:"user"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

type Webhook struct {
	EventType              string         `json:"-"`
	DeliveryID             string         `json:"-"`
	HookID                 int64          `json:"-"`
	InstallationTargetID   int64          `json:"-"`
	InstallationTargetType string         `json:"-"`
	Action                 string         `json:"action"`
	Sender                 User           `json:"sender"`
	Installation           Installation   `json:"installation"`
	Repository             Repository     `json:"repository"`
	PullRequest            *PullRequest   `json:"pull_request"`
	Issue                  *Issue         `json:"issue"`
	Comment                *ReviewComment `json:"comment"`
	Before                 string         `json:"before"`
	After                  string         `json:"after"`
}

func DecodeWebhook(header http.Header, raw []byte, secret string) (Webhook, error) {
	if len(raw) > EventBodyMaxBytes {
		return Webhook{}, errors.New("github webhook exceeds the byte limit")
	}
	if !ValidSignature(header, raw, secret) {
		return Webhook{}, errors.New("invalid github webhook signature")
	}
	var event Webhook
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal(raw, &event) != nil {
		return Webhook{}, errors.New("invalid github webhook JSON")
	}
	for _, field := range []struct {
		name string
		out  *string
	}{
		{EventHeader, &event.EventType},
		{DeliveryHeader, &event.DeliveryID},
		{"X-Github-Hook-Installation-Target-Type", &event.InstallationTargetType},
	} {
		values := header.Values(field.name)
		if len(values) == 0 && field.name == "X-Github-Hook-Installation-Target-Type" {
			continue
		}
		if len(values) != 1 || !validHeaderValue(values[0]) {
			return Webhook{}, errors.New("invalid github webhook headers")
		}
		*field.out = values[0]
	}
	for _, field := range []struct {
		name string
		out  *int64
	}{
		{"X-Github-Hook-Id", &event.HookID},
		{"X-Github-Hook-Installation-Target-Id", &event.InstallationTargetID},
	} {
		values := header.Values(field.name)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 {
			return Webhook{}, errors.New("invalid github webhook headers")
		}
		value, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || value <= 0 {
			return Webhook{}, errors.New("invalid github webhook headers")
		}
		*field.out = value
	}
	return event, nil
}

func validHeaderValue(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
