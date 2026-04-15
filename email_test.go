//go:build windows

package drainctl

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

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
	subject := "RDS01 \u2014 test subject"
	html, err := renderEmailHTML(result, subject, TriggerAlert, `DOMAIN\admin`)
	if err != nil {
		t.Fatal(err)
	}
	if html == "" {
		t.Fatal("empty HTML")
	}
	for _, want := range []string{"RDS01", "Alert", "Drain Active", "#9e2a3b", "LISS Technologies"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

func TestEmailWriteEML(t *testing.T) {
	if os.Getenv("WRITE_EML") == "" {
		t.Skip("set WRITE_EML=1 to generate test .eml file")
	}

	dur := 300.0
	result := &CheckResult{
		Host:                 "RDS01",
		Status:               "Alert",
		DrainModeLabel:       "Drain Active",
		GracePeriodSeconds:   3600,
		StateDurationSeconds: &dur,
		Timestamp:            time.Now(),
		Message:              "Drain mode active for 5m. Sessions are being redirected to RDS02.",
	}
	subject := "RDS01 — Alert: drain active"
	html, err := renderEmailHTML(result, subject, TriggerAlert, `CONTOSO\admin`)
	if err != nil {
		t.Fatal(err)
	}

	var eml bytes.Buffer
	eml.WriteString("From: drainctl@example.com\r\n")
	eml.WriteString("To: test@example.com\r\n")
	eml.WriteString("Subject: " + subject + "\r\n")
	eml.WriteString("MIME-Version: 1.0\r\n")
	eml.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	eml.WriteString("X-Mailer: DrainCtl/" + Version + "\r\n")
	eml.WriteString("\r\n")
	eml.WriteString(html)

	path := "test_email.eml"
	if err := os.WriteFile(path, eml.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes) — double-click to open in Outlook", path, eml.Len())

	// Also send to Mailpit if reachable.
	target := NotificationTarget{
		Type: "email",
		URL:  "smtp://127.0.0.1:1025",
		To:   []string{"test@example.com"},
		From: "drainctl@example.com",
	}
	if err := sendEmail(target, result, TriggerAlert, `CONTOSO\admin`); err != nil {
		t.Logf("mailpit send failed (not running?): %v", err)
	} else {
		t.Log("sent to Mailpit — check http://127.0.0.1:8025")
	}
}

func TestSendEmailSMTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	var received bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = fmt.Fprintf(conn, "220 test SMTP\r\n")
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO") {
				_, _ = fmt.Fprintf(conn, "250-hello\r\n250 OK\r\n")
			} else if strings.HasPrefix(line, "MAIL FROM") {
				_, _ = fmt.Fprintf(conn, "250 OK\r\n")
			} else if strings.HasPrefix(line, "RCPT TO") {
				_, _ = fmt.Fprintf(conn, "250 OK\r\n")
			} else if line == "DATA" {
				_, _ = fmt.Fprintf(conn, "354 Go ahead\r\n")
				for scanner.Scan() {
					dl := scanner.Text()
					if dl == "." {
						break
					}
					received.WriteString(dl + "\n")
				}
				_, _ = fmt.Fprintf(conn, "250 OK\r\n")
			} else if strings.HasPrefix(line, "QUIT") {
				_, _ = fmt.Fprintf(conn, "221 Bye\r\n")
				return
			} else {
				_, _ = fmt.Fprintf(conn, "250 OK\r\n")
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

	err = sendEmail(target, result, TriggerAlert, "")
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

func TestNotificationSubject(t *testing.T) {
	dur := 8100.0 // 2h 15m
	grace := 3600 // 1h
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
		t.Run(string(tc.trigger)+"_"+tc.contains, func(t *testing.T) {
			got := NotificationSubject(result, tc.trigger, tc.changed)
			if got == "" {
				t.Fatal("empty subject")
			}
			if !strings.Contains(strings.ToLower(got), strings.ToLower(tc.contains)) {
				t.Errorf("subject %q does not contain %q", got, tc.contains)
			}
		})
	}
}
