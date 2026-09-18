// Package mailer sends transactional email. It supports two providers, chosen
// automatically from the environment, so deployments can pick whichever is
// easiest:
//
//	Resend (HTTP API — recommended, works cleanly from containers):
//	  RESEND_API_KEY   re_xxx
//	  EMAIL_FROM       "Lumintora <noreply@yourdomain.com>"  (or onboarding@resend.dev for testing)
//
//	SMTP (e.g. Gmail app password):
//	  SMTP_HOST  smtp.gmail.com
//	  SMTP_PORT  587
//	  SMTP_USER  you@gmail.com
//	  SMTP_PASS  app-password
//	  SMTP_FROM  you@gmail.com   (defaults to SMTP_USER)
//
// When neither is configured, Configured() is false and callers fall back to a
// dev path (e.g. returning a reset code in the API response).
package mailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Configured reports whether any email provider is set up.
func Configured() bool {
	return os.Getenv("RESEND_API_KEY") != "" || smtpConfigured()
}

func smtpConfigured() bool {
	return os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_USER") != "" && os.Getenv("SMTP_PASS") != ""
}

// Provider returns the active provider name for logging/diagnostics.
func Provider() string {
	switch {
	case os.Getenv("RESEND_API_KEY") != "":
		return "resend"
	case smtpConfigured():
		return "smtp"
	default:
		return "none"
	}
}

// Send delivers a plain-text email via whichever provider is configured. Returns
// an error if none is configured or the send fails; callers decide how to degrade.
func Send(to, subject, body string) error {
	if os.Getenv("RESEND_API_KEY") != "" {
		return sendResend(to, subject, body, "")
	}
	if smtpConfigured() {
		return sendSMTP(to, subject, body, "")
	}
	return fmt.Errorf("no email provider configured")
}

// SendHTML delivers an HTML email (with a plain-text fallback part).
func SendHTML(to, subject, text, html string) error {
	if os.Getenv("RESEND_API_KEY") != "" {
		return sendResend(to, subject, text, html)
	}
	if smtpConfigured() {
		return sendSMTP(to, subject, text, html)
	}
	return fmt.Errorf("no email provider configured")
}

func fromAddress() string {
	if v := os.Getenv("EMAIL_FROM"); v != "" {
		return v
	}
	if v := os.Getenv("SMTP_FROM"); v != "" {
		return v
	}
	if v := os.Getenv("SMTP_USER"); v != "" {
		return v
	}
	// Resend's shared testing sender — works with any API key, no domain setup.
	return "Lumintora <onboarding@resend.dev>"
}

// ── Resend (HTTPS API) ────────────────────────────────────────────────
func sendResend(to, subject, body, html string) error {
	fields := map[string]interface{}{
		"from":    fromAddress(),
		"to":      []string{to},
		"subject": subject,
		"text":    body,
	}
	if html != "" {
		fields["html"] = html
	}
	payload, _ := json.Marshal(fields)
	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("RESEND_API_KEY"))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("resend returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// ── SMTP ──────────────────────────────────────────────────────────────
func sendSMTP(to, subject, body, html string) error {
	host := os.Getenv("SMTP_HOST")
	port := envOr("SMTP_PORT", "587")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	from := fromAddress()

	var msg string
	if html != "" {
		// multipart/alternative: plain-text fallback + HTML.
		b := "lumintora_boundary_9f2a"
		msg = strings.Join([]string{
			"From: " + from,
			"To: " + to,
			"Subject: " + subject,
			"MIME-Version: 1.0",
			"Content-Type: multipart/alternative; boundary=\"" + b + "\"",
			"",
			"--" + b,
			"Content-Type: text/plain; charset=UTF-8",
			"",
			body,
			"",
			"--" + b,
			"Content-Type: text/html; charset=UTF-8",
			"",
			html,
			"",
			"--" + b + "--",
		}, "\r\n")
	} else {
		msg = strings.Join([]string{
			"From: " + from,
			"To: " + to,
			"Subject: " + subject,
			"MIME-Version: 1.0",
			"Content-Type: text/plain; charset=UTF-8",
			"",
			body,
		}, "\r\n")
	}

	auth := smtp.PlainAuth("", user, pass, host)
	return smtp.SendMail(host+":"+port, auth, user, []string{to}, []byte(msg))
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
