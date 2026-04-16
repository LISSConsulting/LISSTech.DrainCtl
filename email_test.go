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

	target := NotificationTarget{
		Type: "email",
		URL:  "smtp://127.0.0.1:1025",
		To:   []string{"test@example.com"},
		From: "drainctl@example.com",
	}

	// Realistic base data shared across scenarios
	drainDur := 7200.0
	perfDur := 1847.0
	connBlocked := false
	connOpen := true

	drainResult := &CheckResult{
		Host:                 "MDS-LDC1-RDS5",
		Status:               "Alert",
		DrainModeLabel:       "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS",
		DrainModeValue:       1,
		GracePeriodSeconds:   3600,
		StateDurationSeconds: &drainDur,
		ConnectionsAllowed:   &connBlocked,
		Timestamp:            time.Now(),
		Version:              Version,
		Transition:           true,
		TransitionFrom:       "ALLOW_ALL_CONNECTIONS",
		Sessions:             &SessionSummary{ActiveSessions: 8, DisconnectedSessions: 12, TotalSessions: 20, MaxSessions: 50, UtilizationPct: 40},
	}

	perfResult := &CheckResult{
		Host:                 "CST-ISLAB-DC1",
		Status:               "Healthy",
		DrainModeLabel:       "ALLOW_ALL_CONNECTIONS",
		GracePeriodSeconds:   3600,
		StateDurationSeconds: &perfDur,
		ConnectionsAllowed:   &connOpen,
		Timestamp:            time.Now(),
		Version:              Version,
		Sessions:             &SessionSummary{ActiveSessions: 42, DisconnectedSessions: 3, TotalSessions: 45, MaxSessions: 50, UtilizationPct: 90},
		Performance:          &PerfSnapshot{CPUPct: 67.3, CPUP95: 82.1, MemTotalMB: 16384, MemAvailMB: 1310, InputDelayP95: 38, InputDelayP50: 12, DiskQueue: 0.8, TCPRetrans: 2.1},
	}

	scenarios := []struct {
		name      string
		trigger   Trigger
		result    *CheckResult
		changedBy string
		message   string
		status    string // override result.Status for this scenario
	}{
		{"drain-on", TriggerDrainOn, drainResult, `CONTOSO\admin`, "New remote connections have been disabled.", "Grace"},
		{"drain-off", TriggerDrainOff, drainResult, `CONTOSO\admin`, "Remote connections re-enabled.", "Healthy"},
		{"grace", TriggerGraceEntered, drainResult, `CONTOSO\admin`, "Drain mode active, within grace period (30m remaining).", "Grace"},
		{"alert", TriggerAlert, drainResult, `CONTOSO\admin`, "Drain mode active for 2h, exceeding grace period of 1h. New connections are blocked.", "Alert"},
		{"healthy", TriggerHealthy, drainResult, "", "All connections allowed.", "Healthy"},
		{"session-warning", TriggerSessionWarning, perfResult, "", "Session utilization at 90% (45/50 sessions).", "Healthy"},
		{"cpu-warning", TriggerCPUWarning, perfResult, "", "CPU at 67% (threshold: 70%)", "Healthy"},
		{"cpu-critical", TriggerCPUCritical, perfResult, "", "CPU at 67% (critical threshold: 85%)", "Healthy"},
		{"memory-warning", TriggerMemoryWarning, perfResult, "", "Memory at 92% (threshold: 80%)", "Healthy"},
		{"memory-critical", TriggerMemoryCritical, perfResult, "", "Memory at 92% (critical threshold: 90%)", "Healthy"},
		{"delay-warning", TriggerInputDelayWarning, perfResult, "", "Input delay P95 38ms (threshold: 50ms)", "Healthy"},
		{"delay-critical", TriggerInputDelayCritical, perfResult, "", "Input delay P95 38ms (critical threshold: 100ms)", "Healthy"},
	}

	sent := 0
	for _, sc := range scenarios {
		r := *sc.result // shallow copy
		r.Message = sc.message
		r.Status = sc.status
		subject := NotificationSubject(&r, sc.trigger, sc.changedBy)
		if err := sendEmail(target, &r, sc.trigger, sc.changedBy); err != nil {
			t.Logf("[%s] mailpit send failed: %v", sc.name, err)
		} else {
			sent++
		}

		// Write last one as .eml for Outlook testing
		html, err := renderEmailHTML(&r, subject, sc.trigger, sc.changedBy)
		if err != nil {
			t.Fatalf("[%s] render: %v", sc.name, err)
		}
		_ = html
	}
	t.Logf("sent %d/%d emails to Mailpit — check http://127.0.0.1:8025", sent, len(scenarios))
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
