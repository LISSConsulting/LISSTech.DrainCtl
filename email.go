//go:build windows

package drainctl

import (
	"bytes"
	"crypto/tls"
	_ "embed"
	"fmt"
	"html/template"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

//go:embed email_template.html
var emailTemplateRaw string

var emailTmpl = template.Must(template.New("email").Parse(emailTemplateRaw))

type emailData struct {
	Subject     string
	Host        string
	Mode        string
	Status      string
	Duration    string
	ChangedBy   string
	Timestamp   string
	Message     string
	Trigger     string
	StatusColor string
}

// emailStatus returns the status label and badge colour for the email template.
// For drain-state triggers the label is the drain status ("Healthy"/"Grace"/"Alert").
// For perf and session triggers the label names the alert so the badge doesn't
// show a contradictory green "Healthy" on a memory-critical notification.
func emailStatus(drainStatus string, trigger Trigger) (label, color string) {
	// Colours: green (#2d6a4f), amber (#b5651d), red (#c1292e), blue (#4a90d9), grey (#6c757d).
	switch trigger {
	case TriggerCPUWarning:
		return "CPU Warning", "#b5651d"
	case TriggerCPUCritical:
		return "CPU Critical", "#c1292e"
	case TriggerMemoryWarning:
		return "Memory Warning", "#b5651d"
	case TriggerMemoryCritical:
		return "Memory Critical", "#c1292e"
	case TriggerInputDelayWarning:
		return "Input Delay Warning", "#b5651d"
	case TriggerInputDelayCritical:
		return "Input Delay Critical", "#c1292e"
	case TriggerSessionWarning:
		return "Session Warning", "#b5651d"
	}

	// Drain-state triggers — use the drain status as-is.
	switch drainStatus {
	case "Healthy":
		return drainStatus, "#2d6a4f"
	case "Grace":
		return drainStatus, "#b5651d"
	case "Alert":
		return drainStatus, "#c1292e"
	case "Test":
		return drainStatus, "#4a90d9"
	default:
		return drainStatus, "#6c757d"
	}
}

func renderEmailHTML(result *CheckResult, subject string, trigger Trigger, changedBy string) (string, error) {
	dur := ""
	if result.StateDurationSeconds != nil {
		dur = formatDuration(time.Duration(*result.StateDurationSeconds * float64(time.Second)))
	}

	status, statusColor := emailStatus(result.Status, trigger)

	cb := changedBy
	if cb == "" {
		cb = "\u2014" // em dash
	}

	data := emailData{
		Subject:     subject,
		Host:        result.Host,
		Mode:        result.DrainModeLabel,
		Status:      status,
		Duration:    dur,
		ChangedBy:   cb,
		Timestamp:   result.Timestamp.Format("2006-01-02 15:04:05 MST"),
		Message:     result.Message,
		Trigger:     string(trigger),
		StatusColor: statusColor,
	}

	var buf bytes.Buffer
	if err := emailTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render email template: %w", err)
	}
	return buf.String(), nil
}

// sendEmail sends an HTML notification email via SMTP.
func sendEmail(target NotificationTarget, result *CheckResult, trigger Trigger, changedBy string) error {
	subject := NotificationSubject(result, trigger, changedBy)

	html, err := renderEmailHTML(result, subject, trigger, changedBy)
	if err != nil {
		return err
	}

	u, err := url.Parse(target.URL)
	if err != nil {
		return fmt.Errorf("parse SMTP URL: %w", err)
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "smtps" {
			port = "465"
		} else {
			port = "587"
		}
	}
	addr := net.JoinHostPort(host, port)

	// Build MIME message.
	var msg bytes.Buffer
	msg.WriteString("From: " + target.From + "\r\n")
	msg.WriteString("To: " + strings.Join(target.To, ", ") + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	msg.WriteString("X-Mailer: DrainCtl/" + Version + "\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(html)

	if u.Scheme == "smtps" {
		return sendSMTPS(addr, host, target, msg.Bytes())
	}
	return sendSMTPStartTLS(addr, host, target, msg.Bytes())
}

func sendSMTPStartTLS(addr, host string, target NotificationTarget, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Hello("drainctl"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	if target.Secret != "" {
		auth := smtp.PlainAuth("", target.From, target.Secret, host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	return smtpSend(c, target.From, target.To, msg)
}

func sendSMTPS(addr, host string, target NotificationTarget, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("smtps dial: %w", err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer func() { _ = c.Close() }()

	if target.Secret != "" {
		auth := smtp.PlainAuth("", target.From, target.Secret, host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	return smtpSend(c, target.From, target.To, msg)
}

func smtpSend(c *smtp.Client, from string, to []string, msg []byte) error {
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt to %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}
