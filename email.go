//go:build windows

package drainctl

import (
	"bytes"
	"crypto/tls"
	_ "embed"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"text/template"
	"time"
)

//go:embed email_template.html
var emailTemplateRaw string

var emailTmpl = template.Must(template.New("email").Parse(emailTemplateRaw))

type emailData struct {
	Subject      string
	Host         string
	Mode         string
	Status       string
	Duration     string // drain-state duration (omitted for perf/session triggers)
	GracePeriod  string // grace period (only for drain-state triggers)
	ChangedBy    string // who changed drain state (omitted for perf/session triggers)
	Sessions     string // e.g. "45 of 50 (90%)"
	PerfSummary  string // e.g. "CPU 67% · Mem 92% · Delay 38ms"
	Timestamp    string
	Message      string
	Trigger      string
	StatusColor  string // badge background
	BorderColor  string // left accent border
	CardBg       string // card background tint
	BlockquoteBg string // message blockquote background
	IsDrain      bool   // true for drain-state triggers, controls which detail rows render
}

// humanModeLabel converts raw Windows registry drain mode constants
// to concise human-readable labels for notifications.
func humanModeLabel(raw string) string {
	switch raw {
	case "ALLOW_ALL_CONNECTIONS":
		return "Accepting Connections"
	case "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS":
		return "Blocking New Connections"
	case "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART":
		return "Blocking Until Restart"
	default:
		if raw == "" {
			return "Unknown"
		}
		return raw
	}
}

// TriggerStatus returns a human-readable status label for a trigger.
// For perf and session triggers it names the alert ("CPU Warning", "Memory Critical")
// so consumers don't show a contradictory "Healthy" on a perf notification.
// For drain-state triggers it returns the drain status as-is.
func TriggerStatus(drainStatus string, trigger Trigger) string {
	switch trigger {
	case TriggerCPUWarning:
		return "CPU Warning"
	case TriggerCPUCritical:
		return "CPU Critical"
	case TriggerMemoryWarning:
		return "Memory Warning"
	case TriggerMemoryCritical:
		return "Memory Critical"
	case TriggerInputDelayWarning:
		return "Input Delay Warning"
	case TriggerInputDelayCritical:
		return "Input Delay Critical"
	case TriggerSessionWarning:
		return "Session Warning"
	default:
		return drainStatus
	}
}

// emailColors holds the status-driven palette for the email template.
type emailColors struct {
	Badge      string // badge background
	Border     string // left accent border
	CardBg     string // card background tint
	Blockquote string // message blockquote background
}

// emailStatus returns the status label and colour palette for the email template.
// Colours match the dashboard neobrutal palette.
func emailStatus(drainStatus string, trigger Trigger) (label string, colors emailColors) {
	label = TriggerStatus(drainStatus, trigger)

	// Neobrutal palette: green (#5d8a6e), amber (#b87843), red (#9e2a3b), blue (#4a90d9), muted (#7a5a5a).
	switch trigger {
	case TriggerCPUWarning, TriggerMemoryWarning, TriggerInputDelayWarning, TriggerSessionWarning:
		return label, emailColors{"#b87843", "#b87843", "#fdf6f0", "#faf0e6"}
	case TriggerCPUCritical, TriggerMemoryCritical, TriggerInputDelayCritical:
		return label, emailColors{"#9e2a3b", "#9e2a3b", "#fdf0f2", "#fae6ea"}
	}
	switch drainStatus {
	case "Healthy":
		return label, emailColors{"#5d8a6e", "#5d8a6e", "#f0f7f3", "#e6f0ea"}
	case "Grace":
		return label, emailColors{"#b87843", "#b87843", "#fdf6f0", "#faf0e6"}
	case "Alert":
		return label, emailColors{"#9e2a3b", "#9e2a3b", "#fdf0f2", "#fae6ea"}
	case "Test":
		return label, emailColors{"#4a90d9", "#4a90d9", "#f0f5fd", "#e6eefa"}
	default:
		return label, emailColors{"#7a5a5a", "#7a5a5a", "#f7f3f3", "#f0eaea"}
	}
}

func renderEmailHTML(result *CheckResult, subject string, trigger Trigger, changedBy string) (string, error) {
	status, colors := emailStatus(result.Status, trigger)

	isDrain := !perfTriggers[trigger] && trigger != TriggerSessionWarning

	dur := ""
	grace := ""
	cb := "\u2014" // em dash
	if isDrain {
		if result.StateDurationSeconds != nil {
			dur = formatDuration(time.Duration(*result.StateDurationSeconds * float64(time.Second)))
		}
		if result.GracePeriodSeconds > 0 {
			grace = formatDuration(time.Duration(result.GracePeriodSeconds) * time.Second)
		}
		if changedBy != "" {
			cb = changedBy
		}
	}

	// Session summary
	sessions := ""
	if result.Sessions != nil {
		s := result.Sessions
		if s.MaxSessions > 0 {
			sessions = fmt.Sprintf("%d of %d (%d%%)", s.TotalSessions, s.MaxSessions, s.UtilizationPct)
		} else {
			sessions = fmt.Sprintf("%d active", s.TotalSessions)
		}
	}

	// Performance one-liner
	perfSummary := ""
	if result.Performance != nil {
		p := result.Performance
		parts := []string{}
		parts = append(parts, fmt.Sprintf("CPU %.0f%%", p.CPUPct))
		if p.MemTotalMB > 0 {
			memPct := (1 - p.MemAvailMB/p.MemTotalMB) * 100
			parts = append(parts, fmt.Sprintf("Mem %.0f%%", memPct))
		}
		if p.InputDelayP95 > 0 {
			parts = append(parts, fmt.Sprintf("Delay %.0fms", p.InputDelayP95))
		}
		perfSummary = strings.Join(parts, " · ")
	}

	data := emailData{
		Subject:      subject,
		Host:         result.Host,
		Mode:         humanModeLabel(result.DrainModeLabel),
		Status:       status,
		Duration:     dur,
		GracePeriod:  grace,
		ChangedBy:    cb,
		Sessions:     sessions,
		PerfSummary:  perfSummary,
		Timestamp:    result.Timestamp.Format("2006-01-02 15:04:05 MST"),
		Message:      result.Message,
		Trigger:      string(trigger),
		StatusColor:  colors.Badge,
		BorderColor:  colors.Border,
		CardBg:       colors.CardBg,
		BlockquoteBg: colors.Blockquote,
		IsDrain:      isDrain,
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
