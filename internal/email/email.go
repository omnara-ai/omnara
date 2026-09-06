package email

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
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

type ConsoleSender struct {
	PublicURL string
}

func (s ConsoleSender) SendInvite(ctx context.Context, to, orgName string) error {
	return s.log(ctx, to, inviteMessage(orgName, s.PublicURL))
}

func (s ConsoleSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	return s.log(ctx, to, emailVerificationMessage(verifyURL))
}

func (s ConsoleSender) SendPasswordReset(ctx context.Context, to, resetURL string) error {
	return s.log(ctx, to, passwordResetMessage(resetURL))
}

func (s ConsoleSender) SendAccountExists(ctx context.Context, to, signInURL string) error {
	return s.log(ctx, to, accountExistsMessage(signInURL))
}

func (s ConsoleSender) SendPasswordChangedNotice(ctx context.Context, to string) error {
	return s.log(ctx, to, passwordChangedMessage())
}

func (s ConsoleSender) log(_ context.Context, to string, email message) error {
	fmt.Printf(
		"\n--- Omnara auth email ---\nTo: %s\nSubject: %s\n\n%s\n--- end Omnara auth email ---\n\n",
		singleLine(to),
		singleLine(email.subject),
		email.text,
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
	return s.send(ctx, to, inviteMessage(orgName, s.PublicURL))
}

func (s SMTPSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	return s.send(ctx, to, emailVerificationMessage(verifyURL))
}

func (s SMTPSender) SendPasswordReset(ctx context.Context, to, resetURL string) error {
	return s.send(ctx, to, passwordResetMessage(resetURL))
}

func (s SMTPSender) SendAccountExists(ctx context.Context, to, signInURL string) error {
	return s.send(ctx, to, accountExistsMessage(signInURL))
}

func (s SMTPSender) SendPasswordChangedNotice(ctx context.Context, to string) error {
	return s.send(ctx, to, passwordChangedMessage())
}

func (s SMTPSender) send(ctx context.Context, to string, email message) error {
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
	html, err := email.renderHTML(s.PublicURL)
	if err != nil {
		return fmt.Errorf("render email HTML: %w", err)
	}
	data, err := multipartAlternativeMessage(from, recipient, email.subject, email.text, html, time.Now().UTC())
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
		data,
		s.RequireTLS,
	); err != nil {
		return fmt.Errorf("send smtp email: %w", err)
	}
	return nil
}

func multipartAlternativeMessage(
	from, to *mail.Address,
	subject, text, html string,
	now time.Time,
) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("email subject must not contain newlines")
	}
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	if err := writeEmailPart(writer, "text/plain; charset=utf-8", text); err != nil {
		return nil, err
	}
	if err := writeEmailPart(writer, "text/html; charset=utf-8", html); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close email multipart body: %w", err)
	}

	var message bytes.Buffer
	message.WriteString("From: " + from.String() + "\r\n")
	message.WriteString("To: " + to.String() + "\r\n")
	message.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: " + mime.FormatMediaType(
		"multipart/alternative",
		map[string]string{"boundary": writer.Boundary()},
	) + "\r\n")
	message.WriteString("\r\n")
	message.Write(multipartBody.Bytes())
	return message.Bytes(), nil
}

func writeEmailPart(writer *multipart.Writer, contentType, body string) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create %s email part: %w", contentType, err)
	}
	encoded := quotedprintable.NewWriter(part)
	if _, err := encoded.Write([]byte(body)); err != nil {
		return fmt.Errorf("write %s email part: %w", contentType, err)
	}
	if err := encoded.Close(); err != nil {
		return fmt.Errorf("close %s email part: %w", contentType, err)
	}
	return nil
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
	return s.send(ctx, to, inviteMessage(orgName, s.PublicURL), "invite")
}

func (s SendGridSender) SendEmailVerification(ctx context.Context, to, verifyURL string) error {
	return s.send(ctx, to, emailVerificationMessage(verifyURL), "email verification")
}

func (s SendGridSender) SendPasswordReset(ctx context.Context, to, resetURL string) error {
	return s.send(ctx, to, passwordResetMessage(resetURL), "password reset")
}

func (s SendGridSender) SendAccountExists(ctx context.Context, to, signInURL string) error {
	return s.send(ctx, to, accountExistsMessage(signInURL), "account exists")
}

func (s SendGridSender) SendPasswordChangedNotice(ctx context.Context, to string) error {
	return s.send(ctx, to, passwordChangedMessage(), "password changed notice")
}

func (s SendGridSender) send(ctx context.Context, to string, email message, label string) error {
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
	html, err := email.renderHTML(s.PublicURL)
	if err != nil {
		return fmt.Errorf("render email HTML: %w", err)
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
		"subject": email.subject,
		"content": []map[string]string{
			{"type": "text/plain", "value": email.text},
			{"type": "text/html", "value": html},
		},
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

type message struct {
	subject  string
	text     string
	template emailTemplateData
}

type emailTemplateData struct {
	Preheader    string
	Heading      string
	Paragraphs   []string
	ActionLabel  string
	ActionURL    string
	Note         string
	LogoWhiteURL string
	LogoBlackURL string
	MSOFontStyle template.HTML
}

//go:embed branded_email.html.tmpl
var brandedEmailHTML string

var brandedEmailTemplate = template.Must(template.New("branded-email").Parse(brandedEmailHTML))

const msoFontStyle template.HTML = `<!--[if mso]>
<style type="text/css">
  body, table, td, h1, p, span {
    font-family:'Segoe UI',Arial,sans-serif !important;
  }
  .email-shell { width:800px !important; }
  .email-content { width:600px !important; }
</style>
<![endif]-->`

func (m message) renderHTML(publicURL string) (string, error) {
	m.template.MSOFontStyle = msoFontStyle
	baseURL := strings.TrimRight(publicURL, "/")
	if baseURL != "" {
		m.template.LogoWhiteURL = baseURL + "/omnara-logo-white.png"
		m.template.LogoBlackURL = baseURL + "/omnara-logo-black.png"
	}
	var rendered strings.Builder
	if err := brandedEmailTemplate.Execute(&rendered, m.template); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func inviteMessage(orgName, publicURL string) message {
	if orgName == "" {
		orgName = "an organization"
	}
	actionURL := ""
	text := "You have a pending invitation to join " + orgName + " in Omnara. Sign in to Omnara to accept or decline it."
	if publicURL != "" {
		actionURL = strings.TrimRight(publicURL, "/") + "/invitations"
		text = "You have a pending invitation to join " + orgName + " in Omnara. Sign in at " +
			actionURL + " to accept or decline it."
	}
	return message{
		subject: "You have an Omnara invitation",
		text:    text,
		template: emailTemplateData{
			Preheader: "You have a pending invitation to join " + orgName + " in Omnara.",
			Heading:   "You're invited to join " + orgName,
			Paragraphs: []string{
				"You have a pending invitation to join " + orgName + " in Omnara. Review it to accept or decline.",
			},
			ActionLabel: "Review invitation",
			ActionURL:   actionURL,
		},
	}
}

func emailVerificationMessage(verifyURL string) message {
	return message{
		subject: "Verify your Omnara email",
		text: "Verify your Omnara email address by opening this link:\n\n" + verifyURL +
			"\n\nIf you did not request this, you can ignore this email.",
		template: emailTemplateData{
			Preheader: "Verify your email address to finish setting up your Omnara account.",
			Heading:   "Verify your email",
			Paragraphs: []string{
				"Confirm your email address to finish setting up your Omnara account.",
			},
			ActionLabel: "Verify email",
			ActionURL:   verifyURL,
			Note:        "If you didn't request this, you can safely ignore this email.",
		},
	}
}

func passwordResetMessage(resetURL string) message {
	return message{
		subject: "Reset your Omnara password",
		text: "Reset your Omnara password by opening this link:\n\n" + resetURL +
			"\n\nIf you did not request this, you can ignore this email.",
		template: emailTemplateData{
			Preheader: "Reset your Omnara password.",
			Heading:   "Reset your password",
			Paragraphs: []string{
				"We received a request to reset your Omnara password.",
			},
			ActionLabel: "Reset password",
			ActionURL:   resetURL,
			Note:        "If you didn't request this, you can safely ignore this email.",
		},
	}
}

func accountExistsMessage(signInURL string) message {
	text := "An Omnara account already exists for this email address. Sign in or reset your password if you need access."
	if signInURL != "" {
		text = "An Omnara account already exists for this email address. Sign in or reset your password here:\n\n" + signInURL
	}
	return message{
		subject: "Your Omnara account already exists",
		text:    text,
		template: emailTemplateData{
			Preheader: "An Omnara account already exists for this email address.",
			Heading:   "Your Omnara account already exists",
			Paragraphs: []string{
				"An account already exists for this email address. Sign in or reset your password if you need access.",
			},
			ActionLabel: "Sign in to Omnara",
			ActionURL:   signInURL,
		},
	}
}

func passwordChangedMessage() message {
	return message{
		subject: "Your Omnara password changed",
		text: "Your Omnara password was changed. If you did not make this change, " +
			"reset your password and revoke active tokens immediately.",
		template: emailTemplateData{
			Preheader: "Your Omnara password was changed.",
			Heading:   "Your password was changed",
			Paragraphs: []string{
				"Your Omnara password was changed.",
				"If you did not make this change, reset your password and revoke active tokens immediately.",
			},
		},
	}
}
