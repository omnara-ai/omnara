package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	smtpDefaultTimeout  = 10 * time.Second
	sendGridHTTPTimeout = 10 * time.Second
)

var defaultSendGridHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: sendGridHTTPTimeout},
)

type Sender interface {
	SendInvite(ctx context.Context, to, orgName string) error
	SendEmailVerification(ctx context.Context, to, verifyURL string) error
	SendPasswordReset(ctx context.Context, to, resetURL string) error
	SendAccountExists(ctx context.Context, to, signInURL string) error
	SendPasswordChangedNotice(ctx context.Context, to string) error
}

type ConsoleSender struct{}

func (s ConsoleSender) SendInvite(ctx context.Context, to, orgName string) error {
	return s.log(ctx, to, "You have an Omnara invitation", inviteBody(orgName, ""))
}

func (s ConsoleSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	return s.log(ctx, to, "Verify your Omnara email", emailVerificationBody(verifyURL))
}

func (s ConsoleSender) SendPasswordReset(ctx context.Context, to, resetURL string) error {
	return s.log(ctx, to, "Reset your Omnara password", passwordResetBody(resetURL))
}

func (s ConsoleSender) SendAccountExists(ctx context.Context, to, signInURL string) error {
	return s.log(ctx, to, "Your Omnara account already exists", accountExistsBody(signInURL))
}

func (s ConsoleSender) SendPasswordChangedNotice(ctx context.Context, to string) error {
	return s.log(ctx, to, "Your Omnara password changed", passwordChangedBody())
}

func (s ConsoleSender) log(_ context.Context, to, subject, body string) error {
	fmt.Printf(
		"\n--- Omnara auth email ---\nTo: %s\nSubject: %s\n\n%s\n--- end Omnara auth email ---\n\n",
		singleLine(to),
		singleLine(subject),
		body,
	)
	return nil
}

func singleLine(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}

type SMTPSender struct {
	Addr       string
	Username   string
	Password   string
	From       string
	PublicURL  string
	RequireTLS bool
}

func (s SMTPSender) SendInvite(ctx context.Context, to, orgName string) error {
	return s.sendPlainText(ctx, to, "You have an Omnara invitation", inviteBody(orgName, s.PublicURL))
}

func (s SMTPSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	return s.sendPlainText(ctx, to, "Verify your Omnara email", emailVerificationBody(verifyURL))
}

func (s SMTPSender) SendPasswordReset(ctx context.Context, to, resetURL string) error {
	return s.sendPlainText(ctx, to, "Reset your Omnara password", passwordResetBody(resetURL))
}

func (s SMTPSender) SendAccountExists(ctx context.Context, to, signInURL string) error {
	return s.sendPlainText(ctx, to, "Your Omnara account already exists", accountExistsBody(signInURL))
}

func (s SMTPSender) SendPasswordChangedNotice(ctx context.Context, to string) error {
	return s.sendPlainText(ctx, to, "Your Omnara password changed", passwordChangedBody())
}

func (s SMTPSender) sendPlainText(ctx context.Context, to, subject, body string) error {
	if s.Addr == "" {
		return errors.New("smtp addr is required")
	}
	if s.From == "" {
		return errors.New("email from address is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	host := s.Addr
	if splitHost, _, err := net.SplitHostPort(s.Addr); err == nil {
		host = splitHost
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("parse email from address: %w", err)
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("parse email recipient: %w", err)
	}
	var auth smtp.Auth
	if s.Username != "" || s.Password != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, host)
	}
	message, err := plainTextMessage(from, recipient, subject, body, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := sendSMTPWithContext(
		ctx,
		s.Addr,
		host,
		auth,
		from.Address,
		[]string{recipient.Address},
		message,
		s.RequireTLS,
	); err != nil {
		return fmt.Errorf("send smtp email: %w", err)
	}
	return nil
}

func plainTextMessage(from, to *mail.Address, subject, body string, now time.Time) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("email subject must not contain newlines")
	}
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	var message bytes.Buffer
	message.WriteString("From: " + from.String() + "\r\n")
	message.WriteString("To: " + to.String() + "\r\n")
	message.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	message.WriteString("\r\n")
	message.WriteString(body)
	message.WriteString("\r\n")
	return message.Bytes(), nil
}

func sendSMTPWithContext(
	ctx context.Context,
	addr, host string,
	auth smtp.Auth,
	from string,
	to []string,
	msg []byte,
	requireTLS bool,
) error {
	dialer := net.Dialer{Timeout: smtpDefaultTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(smtpDefaultTimeout)
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = client.Close() }()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if requireTLS {
		return errors.New("smtp server does not support STARTTLS")
	}
	if auth != nil {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("smtp server does not support auth")
		}
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := client.Rcpt(addr); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(msg); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

type SendGridSender struct {
	APIKey     string
	From       string
	PublicURL  string
	HTTPClient *http.Client
}

func (s SendGridSender) SendInvite(ctx context.Context, to, orgName string) error {
	return s.sendPlainText(ctx, to, "You have an Omnara invitation", inviteBody(orgName, s.PublicURL), "invite")
}

func (s SendGridSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	return s.sendPlainText(ctx, to, "Verify your Omnara email", emailVerificationBody(verifyURL), "email verification")
}

func (s SendGridSender) SendPasswordReset(ctx context.Context, to, resetURL string) error {
	return s.sendPlainText(ctx, to, "Reset your Omnara password", passwordResetBody(resetURL), "password reset")
}

func (s SendGridSender) SendAccountExists(ctx context.Context, to, signInURL string) error {
	return s.sendPlainText(ctx, to, "Your Omnara account already exists", accountExistsBody(signInURL), "account exists")
}

func (s SendGridSender) SendPasswordChangedNotice(ctx context.Context, to string) error {
	return s.sendPlainText(ctx, to, "Your Omnara password changed", passwordChangedBody(), "password changed notice")
}

func (s SendGridSender) sendPlainText(ctx context.Context, to, subject, text, label string) error {
	if s.APIKey == "" {
		return errors.New("sendgrid api key is required")
	}
	if s.From == "" {
		return errors.New("email from address is required")
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("parse email from address: %w", err)
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("parse email recipient: %w", err)
	}
	client := s.HTTPClient
	if client == nil {
		client = defaultSendGridHTTPClient
	}
	requestClient := outboundhttp.CloneWithoutRedirects(client)
	body := map[string]any{
		"personalizations": []map[string]any{{
			"to": []map[string]string{sendGridAddress(recipient)},
		}},
		"from":    sendGridAddress(from),
		"subject": subject,
		"content": []map[string]string{{
			"type":  "text/plain",
			"value": text,
		}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode sendgrid request: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.sendgrid.com/v3/mail/send",
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("create sendgrid request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := requestClient.Do(req)
	if err != nil {
		return fmt.Errorf("submit sendgrid %s: %w", label, err)
	}
	closeErr := resp.Body.Close()
	if closeErr != nil {
		return fmt.Errorf("close sendgrid response body: %w", closeErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("submit sendgrid %s: status %d", label, resp.StatusCode)
	}
	return nil
}

func sendGridAddress(address *mail.Address) map[string]string {
	out := map[string]string{"email": address.Address}
	if address.Name != "" {
		out["name"] = address.Name
	}
	return out
}

func inviteBody(orgName, publicURL string) string {
	if orgName == "" {
		orgName = "an organization"
	}
	if publicURL == "" {
		return "You have a pending invitation to join " + orgName + " in Omnara. Sign in to Omnara to accept or decline it."
	}
	return "You have a pending invitation to join " + orgName + " in Omnara. Sign in at " +
		publicURL + " to accept or decline it."
}

func emailVerificationBody(verifyURL string) string {
	return "Verify your Omnara email address by opening this link:\n\n" + verifyURL + "\n\nIf you did not request this, you can ignore this email."
}

func passwordResetBody(resetURL string) string {
	return "Reset your Omnara password by opening this link:\n\n" + resetURL + "\n\nIf you did not request this, you can ignore this email."
}

func accountExistsBody(signInURL string) string {
	if signInURL == "" {
		return "An Omnara account already exists for this email address. Sign in or reset your password if you need access."
	}
	return "An Omnara account already exists for this email address. Sign in or reset your password here:\n\n" + signInURL
}

func passwordChangedBody() string {
	return "Your Omnara password was changed. If you did not make this change, reset your password and revoke active tokens immediately."
}
