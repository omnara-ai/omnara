package email

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSendGridSenderSendInviteBuildsRequest(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "marker")

	var seenRequest *http.Request
	sender := SendGridSender{
		APIKey:    "sg-secret",
		From:      "Omnara <noreply@example.com>",
		PublicURL: "https://app.example.com",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenRequest = req
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})},
	}

	if err := sender.SendInvite(ctx, "User <user@example.com>", "Acme"); err != nil {
		t.Fatalf("send invite: %v", err)
	}
	if seenRequest == nil {
		t.Fatal("expected HTTP request")
	}
	if seenRequest.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", seenRequest.Method)
	}
	if got := seenRequest.URL.String(); got != "https://api.sendgrid.com/v3/mail/send" {
		t.Fatalf("url = %s", got)
	}
	if got := seenRequest.Header.Get("Authorization"); got != "Bearer sg-secret" {
		t.Fatalf("authorization header = %q", got)
	}
	if got := seenRequest.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type header = %q", got)
	}
	if got := seenRequest.Context().Value(contextKey("request")); got != "marker" {
		t.Fatalf("request context value = %v", got)
	}

	var body map[string]any
	if err := json.NewDecoder(seenRequest.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	assertSendGridInviteBody(t, body, "user@example.com", "noreply@example.com", "Acme", "https://app.example.com/invitations")
	personalization := firstMapInSlice(t, body["personalizations"])
	toEntry := firstMapInSlice(t, personalization["to"])
	if got := toEntry["name"]; got != "User" {
		t.Fatalf("to name = %v, want User", got)
	}
	fromEntry, ok := body["from"].(map[string]any)
	if !ok {
		t.Fatalf("from = %#v, want object", body["from"])
	}
	if got := fromEntry["name"]; got != "Omnara" {
		t.Fatalf("from name = %v, want Omnara", got)
	}
}

func TestSendGridSenderSendInviteUsesDefaultInviteText(t *testing.T) {
	var body map[string]any
	sender := SendGridSender{
		APIKey: "sg-secret",
		From:   "noreply@example.com",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
	}

	if err := sender.SendInvite(context.Background(), "user@example.com", ""); err != nil {
		t.Fatalf("send invite: %v", err)
	}

	content := firstMapInSlice(t, body["content"])
	value, _ := content["value"].(string)
	if !strings.Contains(value, "join an organization in Omnara") {
		t.Fatalf("invite body = %q, want default org text", value)
	}
	if strings.Contains(value, "Sign in at ") {
		t.Fatalf("invite body = %q, should omit empty public URL", value)
	}
}

func TestInviteBodyNormalizesInvitationsURL(t *testing.T) {
	value := inviteBody("Acme", "https://app.example.com/")
	if !strings.Contains(value, "https://app.example.com/invitations") {
		t.Fatalf("invite body = %q, want invitations URL", value)
	}
	if strings.Contains(value, "example.com//invitations") {
		t.Fatalf("invite body = %q, contains double slash", value)
	}
}

func TestConsoleSenderInviteIncludesInvitationsURL(t *testing.T) {
	sender := ConsoleSender{PublicURL: "https://app.example.com"}
	var sendErr error

	output := captureStdout(t, func() {
		sendErr = sender.SendInvite(context.Background(), "user@example.com", "Acme")
	})
	if sendErr != nil {
		t.Fatalf("send invite: %v", sendErr)
	}

	if !strings.Contains(output, "https://app.example.com/invitations") {
		t.Fatalf("console invite = %q, want invitations URL", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = original
		_ = reader.Close()
		_ = writer.Close()
	}()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(output)
}

func TestSendGridSenderAuthEmailBodies(t *testing.T) {
	tests := []struct {
		name     string
		send     func(context.Context, SendGridSender) error
		subject  string
		contains []string
	}{
		{
			name: "verification",
			send: func(ctx context.Context, sender SendGridSender) error {
				return sender.SendEmailVerification(ctx, "user@example.com", "https://app.example.com/verify-email?token=verify")
			},
			subject:  "Verify your Omnara email",
			contains: []string{"https://app.example.com/verify-email?token=verify", "ignore this email"},
		},
		{
			name: "reset",
			send: func(ctx context.Context, sender SendGridSender) error {
				return sender.SendPasswordReset(ctx, "user@example.com", "https://app.example.com/reset-password?token=reset")
			},
			subject:  "Reset your Omnara password",
			contains: []string{"https://app.example.com/reset-password?token=reset", "ignore this email"},
		},
		{
			name: "account exists",
			send: func(ctx context.Context, sender SendGridSender) error {
				return sender.SendAccountExists(ctx, "user@example.com", "https://app.example.com/login")
			},
			subject:  "Your Omnara account already exists",
			contains: []string{"already exists", "https://app.example.com/login"},
		},
		{
			name: "password changed",
			send: func(ctx context.Context, sender SendGridSender) error {
				return sender.SendPasswordChangedNotice(ctx, "user@example.com")
			},
			subject:  "Your Omnara password changed",
			contains: []string{"password was changed", "revoke active tokens"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			sender := SendGridSender{
				APIKey: "sg-secret",
				From:   "noreply@example.com",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatalf("decode body: %v", err)
					}
					return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
				})},
			}
			if err := tt.send(context.Background(), sender); err != nil {
				t.Fatalf("send: %v", err)
			}
			assertSendGridTextBody(t, body, "user@example.com", "noreply@example.com", tt.subject, tt.contains...)
		})
	}
}

func TestSendGridSenderSendInviteRequiresConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sender SendGridSender
		want   string
	}{
		{name: "api key", sender: SendGridSender{From: "noreply@example.com"}, want: "sendgrid api key is required"},
		{name: "from", sender: SendGridSender{APIKey: "sg-secret"}, want: "email from address is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sender.SendInvite(context.Background(), "user@example.com", "Acme")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSendGridSenderSendInviteReportsHTTPFailures(t *testing.T) {
	sender := SendGridSender{
		APIKey: "sg-secret",
		From:   "noreply@example.com",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
	}

	err := sender.SendInvite(context.Background(), "user@example.com", "Acme")
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("error = %v, want status 502", err)
	}
}

func TestSendGridSenderDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	sender := SendGridSender{
		APIKey: "sg-secret",
		From:   "noreply@example.com",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": {"https://169.254.169.254/latest/meta-data/"}},
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})},
	}

	if err := sender.SendInvite(context.Background(), "user@example.com", "Acme"); err == nil {
		t.Fatal("expected redirect response to fail")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestSendGridSenderValidatesAddresses(t *testing.T) {
	sender := SendGridSender{APIKey: "sg-secret", From: "noreply@example.com"}
	if err := sender.SendInvite(
		context.Background(),
		"bad <",
		"Acme",
	); err == nil ||
		!strings.Contains(err.Error(), "parse email recipient") {
		t.Fatalf("recipient error = %v, want parse email recipient", err)
	}
	sender.From = "bad <"
	if err := sender.SendInvite(
		context.Background(),
		"user@example.com",
		"Acme",
	); err == nil ||
		!strings.Contains(err.Error(), "parse email from address") {
		t.Fatalf("from error = %v, want parse email from address", err)
	}
}

func TestSendGridSenderSendInviteWrapsClientError(t *testing.T) {
	clientErr := errors.New("network down")
	sender := SendGridSender{
		APIKey: "sg-secret",
		From:   "noreply@example.com",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, clientErr
		})},
	}

	err := sender.SendInvite(context.Background(), "user@example.com", "Acme")
	if !errors.Is(err, clientErr) {
		t.Fatalf("error = %v, want wrapped client error", err)
	}
}

func TestPlainTextMessageBuildsMIMEMessage(t *testing.T) {
	from, err := mail.ParseAddress("Omnara <noreply@example.com>")
	if err != nil {
		t.Fatalf("parse from: %v", err)
	}
	to, err := mail.ParseAddress("User <user@example.com>")
	if err != nil {
		t.Fatalf("parse to: %v", err)
	}
	message, err := plainTextMessage(
		from,
		to,
		"Verify your Omnara email",
		"line one\nline two",
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("plain text message: %v", err)
	}
	got := string(message)
	for _, want := range []string{
		"From: \"Omnara\" <noreply@example.com>\r\n",
		"To: \"User\" <user@example.com>\r\n",
		"Date: Fri, 02 Jan 2026 03:04:05 +0000\r\n",
		"Subject: Verify your Omnara email\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\nline one\r\nline two\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("message = %q, want %q", got, want)
		}
	}
}

func TestPlainTextMessageRejectsSubjectNewline(t *testing.T) {
	from, _ := mail.ParseAddress("noreply@example.com")
	to, _ := mail.ParseAddress("user@example.com")
	if _, err := plainTextMessage(from, to, "bad\r\nsubject", "body", time.Now()); err == nil {
		t.Fatal("expected subject newline error")
	}
}

func TestSMTPSenderRequiresSTARTTLSWhenConfigured(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "220 smtp.example.com ESMTP\r\n")
		buf := make([]byte, 1024)
		if _, err := conn.Read(buf); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "250-smtp.example.com\r\n250 AUTH PLAIN\r\n")
	}()
	sender := SMTPSender{Addr: listener.Addr().String(), From: "noreply@example.com", RequireTLS: true}
	err = sender.SendEmailVerification(context.Background(), "user@example.com", "https://app.example.com/verify")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("error = %v, want STARTTLS requirement", err)
	}
	<-done
}

func assertSendGridInviteBody(t *testing.T, body map[string]any, to, from, orgName, inviteURL string) {
	t.Helper()

	personalization := firstMapInSlice(t, body["personalizations"])
	toEntry := firstMapInSlice(t, personalization["to"])
	if got := toEntry["email"]; got != to {
		t.Fatalf("to email = %v, want %s", got, to)
	}

	fromEntry, ok := body["from"].(map[string]any)
	if !ok {
		t.Fatalf("from = %#v, want object", body["from"])
	}
	if got := fromEntry["email"]; got != from {
		t.Fatalf("from email = %v, want %s", got, from)
	}
	if got := body["subject"]; got != "You have an Omnara invitation" {
		t.Fatalf("subject = %v", got)
	}

	content := firstMapInSlice(t, body["content"])
	if got := content["type"]; got != "text/plain" {
		t.Fatalf("content type = %v, want text/plain", got)
	}
	value, _ := content["value"].(string)
	for _, want := range []string{orgName, inviteURL, "accept or decline"} {
		if !strings.Contains(value, want) {
			t.Fatalf("invite body = %q, want to contain %q", value, want)
		}
	}
}

func assertSendGridTextBody(t *testing.T, body map[string]any, to, from, subject string, contains ...string) {
	t.Helper()

	personalization := firstMapInSlice(t, body["personalizations"])
	toEntry := firstMapInSlice(t, personalization["to"])
	if got := toEntry["email"]; got != to {
		t.Fatalf("to email = %v, want %s", got, to)
	}
	fromEntry, ok := body["from"].(map[string]any)
	if !ok {
		t.Fatalf("from = %#v, want object", body["from"])
	}
	if got := fromEntry["email"]; got != from {
		t.Fatalf("from email = %v, want %s", got, from)
	}
	if got := body["subject"]; got != subject {
		t.Fatalf("subject = %v, want %s", got, subject)
	}
	content := firstMapInSlice(t, body["content"])
	value, _ := content["value"].(string)
	for _, want := range contains {
		if !strings.Contains(value, want) {
			t.Fatalf("body = %q, want %q", value, want)
		}
	}
}

func firstMapInSlice(t *testing.T, value any) map[string]any {
	t.Helper()

	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("value = %#v, want non-empty array", value)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first item = %#v, want object", items[0])
	}
	return item
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
