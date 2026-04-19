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

// smtpDialTimeout bounds the TCP/TLS handshake; smtpOverallDeadline bounds
// the entire greeting + AUTH + DATA sequence past connect. Both are var (not
// const) so tests can swap them to millisecond scales without growing the
// suite by minutes.
var (
	smtpDialTimeout     = 10 * time.Second
	smtpOverallDeadline = 30 * time.Second
)

type emailData struct {
	Subject           string
	Preview           string // hidden preview text shown by email clients
	Host              string
	Mode              string
	Status            string
	Duration          string // drain-state duration (omitted for perf/session triggers)
	GracePeriod       string // grace period (only for drain-state triggers)
	ChangedBy         string // who changed drain state (omitted for perf/session triggers)
	Sessions          string // e.g. "45 of 50 (90%)"
	PerfSummary       string // e.g. "CPU 67% · Mem 92% · Delay 38ms"
	Timestamp         string
	Message           string
	Trigger           string
	StatusColor       string // badge background
	BorderColor       string // left accent border
	CardBg            string // card background tint
	BlockquoteBg      string // message blockquote background
	IsDrain           bool   // true for drain-state triggers, controls which detail rows render
	IsSpike           bool   // true for event_spike trigger — swaps detail rows for spike data
	SpikeChannel      string
	SpikeObserved     string
	SpikeExpected     string
	SpikeConfirmation string
	SpikeWindow       string
}

// sanitizeHeader strips CR, LF, and NUL from a string to prevent
// SMTP header injection.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(s)
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
	case TriggerEventSpike:
		switch drainStatus {
		case "warning":
			return label, emailColors{"#b87843", "#b87843", "#fdf6f0", "#faf0e6"}
		case "alert":
			return label, emailColors{"#9e2a3b", "#9e2a3b", "#fdf0f2", "#fae6ea"}
		default:
			return label, emailColors{"#7a5a5a", "#7a5a5a", "#f7f3f3", "#f0eaea"}
		}
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

	isSpike := trigger == TriggerEventSpike
	isDrain := !perfTriggers[trigger] && trigger != TriggerSessionWarning && !isSpike

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

	spikeChannel := ""
	spikeObserved := ""
	spikeExpected := ""
	spikeConfirmation := ""
	spikeWindow := ""
	preview := result.Message
	if isSpike && result.Spike != nil {
		s := result.Spike
		spikeChannel = s.Channel
		spikeObserved = fmt.Sprintf("%d", s.Observed)
		spikeExpected = fmt.Sprintf("%.1f", s.Expected)
		spikeConfirmation = fmt.Sprintf("%d of last 3 windows", s.ConfirmationCount)
		if !s.WindowStart.IsZero() && !s.WindowEnd.IsZero() {
			spikeWindow = fmt.Sprintf("%s \u2013 %s",
				s.WindowStart.Format("15:04:05"),
				s.WindowEnd.Format("15:04:05"))
		}
		preview = fmt.Sprintf("%s: %d events, expected ~%.1f. Tail probability %g.",
			s.Channel, s.Observed, s.Expected, s.TailProbability)
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
		Subject:           subject,
		Preview:           preview,
		Host:              result.Host,
		Mode:              humanModeLabel(result.DrainModeLabel),
		Status:            status,
		Duration:          dur,
		GracePeriod:       grace,
		ChangedBy:         cb,
		Sessions:          sessions,
		PerfSummary:       perfSummary,
		Timestamp:         result.Timestamp.Format("2006-01-02 15:04:05 MST"),
		Message:           result.Message,
		Trigger:           string(trigger),
		StatusColor:       colors.Badge,
		BorderColor:       colors.Border,
		CardBg:            colors.CardBg,
		BlockquoteBg:      colors.Blockquote,
		IsDrain:           isDrain,
		IsSpike:           isSpike,
		SpikeChannel:      spikeChannel,
		SpikeObserved:     spikeObserved,
		SpikeExpected:     spikeExpected,
		SpikeConfirmation: spikeConfirmation,
		SpikeWindow:       spikeWindow,
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
	msg.WriteString("From: " + sanitizeHeader(target.From) + "\r\n")
	msg.WriteString("To: " + sanitizeHeader(strings.Join(target.To, ", ")) + "\r\n")
	msg.WriteString("Subject: " + sanitizeHeader(subject) + "\r\n")
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
	d := net.Dialer{Timeout: smtpDialTimeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	// Arm the read deadline BEFORE smtp.NewClient — NewClient reads the
	// server greeting and will hang on a silent-accept server otherwise.
	if err := conn.SetDeadline(time.Now().Add(smtpOverallDeadline)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp set deadline: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp new client: %w", err)
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
	d := &net.Dialer{Timeout: smtpDialTimeout}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("smtps dial: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(smtpOverallDeadline)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtps set deadline: %w", err)
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
