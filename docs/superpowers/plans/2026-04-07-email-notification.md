# Email Notification Target Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add SMTP email notifications with HTML templates matching the dashboard design, and unify notification subjects across all target types (email, webhook, ntfy).

**Architecture:** New `email.go` in root package handles SMTP dial/auth/send. New `email_template.html` embedded via `//go:embed`. Shared `NotificationSubject()` function replaces the existing `ntfyTitle()` and is used by all three target types. `NotificationTarget` gains `To` and `From` fields.

**Tech Stack:** Go stdlib (`net/smtp`, `crypto/tls`, `html/template`, `embed`), no external deps.

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `config.go` | Modify | Add `To`/`From` fields to `NotificationTarget`, update `Validate()` |
| `notify.go` | Modify | Add `NotificationSubject()`, update `SendNotification`/`SendTestNotification` to use it, add `case "email"` |
| `email.go` | Create | SMTP client (`sendEmail`) + template rendering |
| `email_template.html` | Create | Embedded HTML email template |
| `email_test.go` | Create | SMTP send test, template rendering test, subject generation tests |

---

### Task 1: Add To/From fields to NotificationTarget and update validation

**Files:**
- Modify: `config.go:70-76` (NotificationTarget struct)
- Modify: `config.go:229-256` (Validate)

- [ ] **Step 1: Add fields to NotificationTarget**

In `config.go`, update the struct:

```go
type NotificationTarget struct {
	Type          string    `json:"type"` // "webhook", "ntfy", or "email"
	URL           string    `json:"url"`
	Triggers      []Trigger `json:"triggers"`
	RepeatMinutes int       `json:"repeat_minutes,omitempty"`
	Secret        string    `json:"secret,omitempty"`
	To            []string  `json:"to,omitempty"`
	From          string    `json:"from,omitempty"`
}
```

- [ ] **Step 2: Update Validate() — allow "email" type**

In the type filter (line ~231), change:
```go
if t.Type != "webhook" && t.Type != "ntfy" {
```
to:
```go
if t.Type != "webhook" && t.Type != "ntfy" && t.Type != "email" {
```

- [ ] **Step 3: Update Validate() — allow smtp:// and smtps:// URL schemes for email targets**

In the URL scheme filter (line ~247), change:
```go
if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
```
to:
```go
validScheme := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
	strings.HasPrefix(lower, "smtp://") || strings.HasPrefix(lower, "smtps://")
if !validScheme {
```

- [ ] **Step 4: Add email-specific validation after the existing loops**

After the existing RepeatMinutes clamping loop (around line 280), add:

```go
// Validate email targets: require to, from, and smtp(s):// URL.
for i := range c.Notifications {
	t := &c.Notifications[i]
	if t.Type != "email" {
		continue
	}
	lower := strings.ToLower(t.URL)
	if !strings.HasPrefix(lower, "smtp://") && !strings.HasPrefix(lower, "smtps://") {
		if log != nil {
			LogMsg(log, LvlWRN, "email target requires smtp:// or smtps:// URL, ignored",
				fmt.Sprintf("url=%s", t.URL))
		}
		t.URL = "" // will be filtered by empty-URL check downstream
	}
	if t.From == "" {
		if log != nil {
			LogMsg(log, LvlWRN, "email target missing 'from' address, ignored",
				fmt.Sprintf("url=%s", t.URL))
		}
		t.URL = ""
	}
	if len(t.To) == 0 {
		if log != nil {
			LogMsg(log, LvlWRN, "email target missing 'to' addresses, ignored",
				fmt.Sprintf("url=%s", t.URL))
		}
		t.URL = ""
	}
}
```

- [ ] **Step 5: Build and lint**

Run: `go build ./... && just lint`
Expected: success

- [ ] **Step 6: Commit**

```bash
git add config.go
git commit -m "feat: add To/From fields to NotificationTarget, validate email targets"
```

---

### Task 2: NotificationSubject — shared subject generator

**Files:**
- Modify: `notify.go`
- Create: `email_test.go` (subject tests)

- [ ] **Step 1: Write subject tests**

Create `email_test.go`:

```go
//go:build windows

package drainctl

import (
	"testing"
	"time"
)

func TestNotificationSubject(t *testing.T) {
	dur := 8100.0 // 2h 15m
	grace := 3600  // 1h
	sess := &SessionSummary{
		ActiveSessions: 17, TotalSessions: 17, MaxSessions: 20,
		UtilizationPct: 85,
	}
	result := &CheckResult{
		Host:                 "RDS01",
		Status:               "Alert",
		DrainModeLabel:       "Drain",
		GracePeriodSeconds:   grace,
		StateDurationSeconds: &dur,
		Sessions:             sess,
	}

	tests := []struct {
		trigger  Trigger
		changed  string
		contains string
	}{
		{TriggerDrainOn, `DOMAIN\admin`, "RDS01"},
		{TriggerDrainOn, `DOMAIN\admin`, "disabled"},
		{TriggerDrainOn, `DOMAIN\admin`, `DOMAIN\admin`},
		{TriggerDrainOff, `DOMAIN\admin`, "re-enabled"},
		{TriggerAlert, "", "2h 15m"},
		{TriggerAlert, "", "exceeds"},
		{TriggerGraceEntered, "", "grace period"},
		{TriggerHealthy, "", "healthy"},
		{TriggerSessionWarning, "", "85%"},
		{TriggerSessionWarning, "", "17/20"},
	}

	for _, tc := range tests {
		t.Run(string(tc.trigger), func(t *testing.T) {
			got := NotificationSubject(result, tc.trigger, tc.changed)
			if got == "" {
				t.Fatal("empty subject")
			}
			if !containsCI(got, tc.contains) {
				t.Errorf("subject %q does not contain %q", got, tc.contains)
			}
		})
	}
}

func containsCI(s, substr string) bool {
	return len(s) >= len(substr) && // quick check
		len(substr) > 0 &&
		stringContainsFold(s, substr)
}

func stringContainsFold(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test -run TestNotificationSubject -v`
Expected: FAIL — `NotificationSubject` not defined

- [ ] **Step 3: Implement NotificationSubject**

In `notify.go`, add after the `ntfyTitle` function:

```go
// NotificationSubject returns a natural-language subject line for the given
// trigger, suitable for email subjects, ntfy titles, and webhook payloads.
func NotificationSubject(result *CheckResult, trigger Trigger, changedBy string) string {
	host := result.Host
	if host == "" {
		host = "Unknown"
	}

	dur := ""
	if result.StateDurationSeconds != nil {
		dur = formatDuration(time.Duration(*result.StateDurationSeconds * float64(time.Second)))
	}

	grace := formatDuration(time.Duration(result.GracePeriodSeconds) * time.Second)

	by := ""
	if changedBy != "" {
		by = " by " + changedBy
	}

	switch trigger {
	case TriggerDrainOn:
		return fmt.Sprintf("%s \u2014 New remote connections disabled%s", host, by)
	case TriggerDrainOff:
		return fmt.Sprintf("%s \u2014 Remote connections re-enabled%s", host, by)
	case TriggerAlert:
		return fmt.Sprintf("%s \u2014 Remote connections disabled for %s (exceeds %s grace period)", host, dur, grace)
	case TriggerGraceEntered:
		remaining := ""
		if result.StateDurationSeconds != nil {
			rem := time.Duration(result.GracePeriodSeconds)*time.Second - time.Duration(*result.StateDurationSeconds*float64(time.Second))
			if rem > 0 {
				remaining = formatDuration(rem) + " remaining in "
			}
		}
		return fmt.Sprintf("%s \u2014 Remote connections disabled, %sgrace period", host, remaining)
	case TriggerHealthy:
		return fmt.Sprintf("%s \u2014 Remote connections enabled, server healthy", host)
	case TriggerSessionWarning:
		if result.Sessions != nil {
			return fmt.Sprintf("%s \u2014 Session utilization at %d%% (%d/%d sessions)",
				host, result.Sessions.UtilizationPct, result.Sessions.TotalSessions, result.Sessions.MaxSessions)
		}
		return fmt.Sprintf("%s \u2014 Session utilization warning", host)
	default:
		return fmt.Sprintf("%s \u2014 %s", host, trigger)
	}
}

// formatDuration returns a human-readable duration like "2h 15m" or "45m".
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 && m > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh", h)
	}
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return "0m"
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test -run TestNotificationSubject -v`
Expected: PASS

- [ ] **Step 5: Update ntfy to use NotificationSubject**

In `notify.go`, replace the ntfy title line (around line 120):
```go
			case "ntfy":
				title := ntfyTitle(trigger, result.Host)
```
with:
```go
			case "ntfy":
				title := NotificationSubject(result, trigger, changedBy)
```

- [ ] **Step 6: Update webhook to include subject**

In the payload map (around line 59), add after `"timestamp"`:
```go
		"subject": NotificationSubject(result, trigger, changedBy),
```

- [ ] **Step 7: Build and lint**

Run: `go build ./... && just lint`

- [ ] **Step 8: Commit**

```bash
git add notify.go email_test.go
git commit -m "feat: shared NotificationSubject for email, webhook, and ntfy"
```

---

### Task 3: HTML email template

**Files:**
- Create: `email_template.html`
- Modify: `email_test.go` (add template rendering test)

- [ ] **Step 1: Create the HTML template**

Create `email_template.html` with inline styles matching the dashboard design language. The template receives an `emailData` struct.

Key design tokens (from landing page):
- Accent: `#a3475b` (rose gold)
- Background: `#fdf8f6`
- Card: `#fffaf8`
- Surface: `#f5ebe8`
- Font: `Segoe UI, -apple-system, Helvetica Neue, Arial, sans-serif`

Status badge colors:
- Healthy: `#2d6a4f`
- Grace: `#b5651d`
- Alert: `#c1292e`
- Offline/Unknown: `#6c757d`

The template should include:
- Rose gold left border on the card
- Status badge with color
- Table rows: Server, Mode, Status, Duration, Changed By, Timestamp
- Footer: "Sent by LISSTech DrainCtl — LISS Technologies"

- [ ] **Step 2: Add template rendering test to email_test.go**

```go
func TestEmailTemplateRenders(t *testing.T) {
	dur := 300.0
	result := &CheckResult{
		Host:                 "RDS01",
		Status:               "Alert",
		DrainModeLabel:       "Drain Active",
		GracePeriodSeconds:   3600,
		StateDurationSeconds: &dur,
		Timestamp:            time.Now(),
		Message:              "Drain mode active for 5m.",
	}
	subject := "RDS01 — test subject"
	html, err := renderEmailHTML(result, subject, TriggerAlert, "DOMAIN\\admin")
	if err != nil {
		t.Fatal(err)
	}
	if html == "" {
		t.Fatal("empty HTML")
	}
	for _, want := range []string{"RDS01", "Alert", "Drain Active", "#a3475b", "LISS Technologies"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}
```

Add `"strings"` to the test imports.

- [ ] **Step 3: Run test, verify it fails**

Run: `go test -run TestEmailTemplateRenders -v`
Expected: FAIL — `renderEmailHTML` not defined

- [ ] **Step 4: Implement renderEmailHTML in email.go**

Create `email.go` with the template rendering function (SMTP send comes in next task):

```go
//go:build windows

package drainctl

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"time"
)

//go:embed email_template.html
var emailTemplateRaw string

var emailTmpl = template.Must(template.New("email").Parse(emailTemplateRaw))

type emailData struct {
	Subject   string
	Host      string
	Mode      string
	Status    string
	Duration  string
	ChangedBy string
	Timestamp string
	Message   string
	Trigger   string
	// Badge color
	StatusColor string
}

func renderEmailHTML(result *CheckResult, subject string, trigger Trigger, changedBy string) (string, error) {
	dur := ""
	if result.StateDurationSeconds != nil {
		dur = formatDuration(time.Duration(*result.StateDurationSeconds * float64(time.Second)))
	}

	statusColor := "#6c757d"
	switch result.Status {
	case "Healthy":
		statusColor = "#2d6a4f"
	case "Grace":
		statusColor = "#b5651d"
	case "Alert":
		statusColor = "#c1292e"
	}

	cb := changedBy
	if cb == "" {
		cb = "—"
	}

	data := emailData{
		Subject:     subject,
		Host:        result.Host,
		Mode:        result.DrainModeLabel,
		Status:      result.Status,
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
```

- [ ] **Step 5: Create email_template.html**

Write the HTML template file with inline styles. The template uses `{{.Field}}` syntax.

- [ ] **Step 6: Run tests**

Run: `go test -run TestEmailTemplateRenders -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add email.go email_template.html email_test.go
git commit -m "feat: HTML email template with dashboard design language"
```

---

### Task 4: SMTP client

**Files:**
- Modify: `email.go` (add sendEmail)
- Modify: `email_test.go` (add SMTP send test)

- [ ] **Step 1: Write SMTP send test**

Add to `email_test.go`:

```go
func TestSendEmailSMTP(t *testing.T) {
	// Start a minimal SMTP test server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	var received bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Minimal SMTP conversation
		fmt.Fprintf(conn, "220 test SMTP\r\n")
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "EHLO") {
				fmt.Fprintf(conn, "250-hello\r\n250 OK\r\n")
			} else if strings.HasPrefix(line, "MAIL FROM") {
				fmt.Fprintf(conn, "250 OK\r\n")
			} else if strings.HasPrefix(line, "RCPT TO") {
				fmt.Fprintf(conn, "250 OK\r\n")
			} else if line == "DATA" {
				fmt.Fprintf(conn, "354 Go ahead\r\n")
				for scanner.Scan() {
					dl := scanner.Text()
					if dl == "." {
						break
					}
					received.WriteString(dl + "\n")
				}
				fmt.Fprintf(conn, "250 OK\r\n")
			} else if strings.HasPrefix(line, "QUIT") {
				fmt.Fprintf(conn, "221 Bye\r\n")
				return
			} else {
				fmt.Fprintf(conn, "250 OK\r\n")
			}
		}
	}()

	target := NotificationTarget{
		Type: "email",
		URL:  "smtp://" + addr,
		To:   []string{"test@example.com"},
		From: "drainctl@example.com",
	}
	result := &CheckResult{
		Host:           "SRV01",
		Status:         "Alert",
		DrainModeLabel: "Drain",
		Timestamp:      time.Now(),
		Message:        "Test alert.",
	}

	err = sendEmail(target, result, TriggerAlert, "", DiscardLogger())
	if err != nil {
		t.Fatalf("sendEmail: %v", err)
	}

	<-done
	body := received.String()
	if !strings.Contains(body, "SRV01") {
		t.Error("email body missing host")
	}
	if !strings.Contains(body, "text/html") {
		t.Error("email missing Content-Type html")
	}
}
```

Add `"bufio"`, `"bytes"`, `"net"` to imports.

- [ ] **Step 2: Run test, verify it fails**

Run: `go test -run TestSendEmailSMTP -v`
Expected: FAIL — `sendEmail` not defined

- [ ] **Step 3: Implement sendEmail in email.go**

Add to `email.go`:

```go
import (
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/smtp"
	"net/url"
	"strings"
)

// sendEmail sends an HTML notification email via SMTP.
func sendEmail(target NotificationTarget, result *CheckResult, trigger Trigger, changedBy string, log LogFunc) error {
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
	defer c.Close()

	if err := c.Hello("drainctl"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}

	// STARTTLS if supported.
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	// Auth if credentials provided.
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
	defer c.Close()

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
```

- [ ] **Step 4: Run tests**

Run: `go test -run TestSendEmailSMTP -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add email.go email_test.go
git commit -m "feat: SMTP client with STARTTLS and implicit TLS support"
```

---

### Task 5: Wire email into SendNotification

**Files:**
- Modify: `notify.go`

- [ ] **Step 1: Add email case to SendNotification**

In the `switch target.Type` block (around line 111), add before `default`:

```go
		case "email":
			if err := sendEmail(target, result, trigger, changedBy, log); err != nil {
				LogMsg(log, LvlWRN, "email notification failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
			} else {
				log(LvlINF, "notify=email", fmt.Sprintf("event=%s to=%v", trigger, target.To))
			}
```

- [ ] **Step 2: Add email case to SendTestNotification**

In the `switch target.Type` block (around line 190), add before the closing `}`:

```go
		case "email":
			testResult := &CheckResult{
				Host:           host,
				Status:         "Test",
				DrainModeLabel: "N/A",
				Timestamp:      time.Now(),
				Message:        "This is a test notification from DrainCtl.",
				Version:        Version,
			}
			if err := sendEmail(target, testResult, "test", "", log); err != nil {
				LogMsg(log, LvlERR, "email test failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
				errs = append(errs, fmt.Errorf("email %s: %w", target.URL, err))
			} else {
				log(LvlOK, "notify=email", fmt.Sprintf("test=sent to=%v", target.To))
			}
```

- [ ] **Step 3: Build and lint**

Run: `go build ./... && just lint`

- [ ] **Step 4: Run all notification tests**

Run: `go test -run "TestNotification|TestSendEmail" -v`

- [ ] **Step 5: Commit**

```bash
git add notify.go
git commit -m "feat: wire email target into SendNotification and SendTestNotification"
```

---

### Task 6: Update docs and guide

**Files:**
- Modify: `docs/guide.html` — add email notification section
- Modify: `README.md` — mention email in notification targets

- [ ] **Step 1: Update guide**

Add an email configuration example to the notifications section of the guide.

- [ ] **Step 2: Update README**

Add email to the notification target types list.

- [ ] **Step 3: Commit**

```bash
git add docs/guide.html README.md
git commit -m "docs: add email notification target to guide and README"
```
