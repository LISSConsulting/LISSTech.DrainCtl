//go:build windows

package drainctl

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
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

func TestEmailTemplate_EscapesHTML(t *testing.T) {
	dur := 60.0
	result := &CheckResult{
		Host:                 "RDS01",
		Status:               "Alert",
		DrainModeLabel:       "Drain Active",
		GracePeriodSeconds:   300,
		StateDurationSeconds: &dur,
		Timestamp:            time.Now(),
		Message:              "<script>alert(1)</script>",
	}

	html, err := renderEmailHTML(result, "subject", TriggerAlert, `DOMAIN\admin`)
	if err != nil {
		t.Fatalf("renderEmailHTML: %v", err)
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("escaped script tag missing from HTML: %s", html)
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatalf("raw script tag present in HTML: %s", html)
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

// TestEventSpikePayload_EmailTemplate verifies the rendered MJML → HTML output
// for an event_spike payload: subject emoji matches per-target severity, preview
// text carries the channel and observed/expected values, and the card body
// lists Observed-vs-Expected and Confirmation-Window rows. Corresponds to
// TestEventSpikePayload_EmailTemplate in contracts/event_spike-payload.md.
func TestEventSpikePayload_EmailTemplate(t *testing.T) {
	base, spike := newSpikeResult()
	const warnEmoji = "\u26A0"      // ⚠ (optionally followed by VS-16 U+FE0F)
	const alertEmoji = "\U0001F6A8" // 🚨
	const infoEmoji = "\u2139"      // ℹ

	cases := []struct {
		name      string
		severity  string
		wantEmoji string
		skipEmoji string
	}{
		{"warning", "warning", warnEmoji, alertEmoji},
		{"alert", "alert", alertEmoji, warnEmoji},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := *base
			r.Status = tc.severity
			subject := NotificationSubject(&r, TriggerEventSpike, "")

			if !strings.Contains(subject, tc.wantEmoji) {
				t.Errorf("subject %q missing expected severity emoji %q", subject, tc.wantEmoji)
			}
			if strings.Contains(subject, tc.skipEmoji) {
				t.Errorf("subject %q unexpectedly contains the other-severity emoji %q", subject, tc.skipEmoji)
			}
			if strings.Contains(subject, infoEmoji) {
				t.Errorf("subject %q fell back to info emoji despite valid severity %q", subject, tc.severity)
			}

			html, err := renderEmailHTML(&r, subject, TriggerEventSpike, "")
			if err != nil {
				t.Fatalf("renderEmailHTML: %v", err)
			}
			if html == "" {
				t.Fatal("empty HTML")
			}

			if !strings.Contains(html, tc.wantEmoji) {
				t.Errorf("rendered HTML missing severity emoji %q", tc.wantEmoji)
			}

			// Preview text (hidden div at top of body) must carry channel + observed/expected.
			previewWants := []string{
				spike.Channel,
				"47 events",
				"~3.2",
				"Tail probability",
			}
			for _, want := range previewWants {
				if !strings.Contains(html, want) {
					t.Errorf("rendered HTML missing preview fragment %q", want)
				}
			}

			// Card body rows — label text must be present verbatim.
			cardRows := []string{"Observed vs Expected", "Confirmation Window"}
			for _, want := range cardRows {
				if !strings.Contains(html, want) {
					t.Errorf("rendered HTML missing card row %q", want)
				}
			}

			// Card body values — observed, expected, confirmation count.
			for _, want := range []string{"47", "3.2", "3 of last 3 windows"} {
				if !strings.Contains(html, want) {
					t.Errorf("rendered HTML missing card value %q", want)
				}
			}
		})
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

// withShortSMTPTimeouts shrinks the package-level SMTP timeouts for the
// duration of a test so deadline assertions fire in milliseconds rather than
// tens of seconds. T108.
func withShortSMTPTimeouts(t *testing.T, dial, overall time.Duration) {
	t.Helper()
	origDial := smtpDialTimeout
	origOverall := smtpOverallDeadline
	smtpDialTimeout = dial
	smtpOverallDeadline = overall
	t.Cleanup(func() {
		smtpDialTimeout = origDial
		smtpOverallDeadline = origOverall
	})
}

// TestSMTPStartTLS_SilentGreetingHitsOverallDeadline — T108. A server that
// accepts the TCP connection but never sends a greeting must not hang
// sendSMTPStartTLS indefinitely. The conn.SetDeadline call that comes BEFORE
// smtp.NewClient is what makes this deterministic; without it, NewClient's
// ReadResponse on the greeting would block forever.
func TestSMTPStartTLS_SilentGreetingHitsOverallDeadline(t *testing.T) {
	withShortSMTPTimeouts(t, 2*time.Second, 250*time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Accept but never speak. Goroutine exits when the client hangs up
	// after its deadline fires.
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Drain any bytes the client might write; never respond.
		buf := make([]byte, 256)
		_, _ = conn.Read(buf)
	}()

	target := NotificationTarget{
		URL:  "smtp://" + ln.Addr().String(),
		From: "drainctl@example.test",
		To:   []string{"rcpt@example.test"},
	}

	start := time.Now()
	err = sendSMTPStartTLS(ln.Addr().String(), "localhost", target, []byte("Subject: x\r\n\r\nbody"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("sendSMTPStartTLS returned nil on silent-greeting server; expected deadline error")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("expected net.Error.Timeout()==true, got %T: %v", err, err)
	}
	// Must respect the overall deadline within a generous slop window; also
	// must not have hung significantly longer.
	if elapsed > 3*time.Second {
		t.Errorf("sendSMTPStartTLS took %v — did not respect overall deadline", elapsed)
	}
	<-acceptDone
}

func TestSMTPStartTLS_RefusesCleartextAuth(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	origDial := smtpDial
	smtpDial = func(addr string, timeout time.Duration) (net.Conn, error) {
		return clientConn, nil
	}
	t.Cleanup(func() { smtpDial = origDial })

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer func() { _ = serverConn.Close() }()

		reader := bufio.NewReader(serverConn)
		_, _ = fmt.Fprintf(serverConn, "220 test SMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO "), strings.HasPrefix(cmd, "HELO "):
				_, _ = fmt.Fprintf(serverConn, "250-test.example\r\n250 AUTH PLAIN\r\n")
			case cmd == "QUIT":
				_, _ = fmt.Fprintf(serverConn, "221 bye\r\n")
				return
			default:
				_, _ = fmt.Fprintf(serverConn, "250 ok\r\n")
			}
		}
	}()

	target := NotificationTarget{
		URL:    "smtp://test.example:25",
		From:   "drainctl@example.test",
		To:     []string{"rcpt@example.test"},
		Secret: "x",
	}

	err := sendSMTPStartTLS("ignored", "test.example", target, []byte("Subject: x\r\n\r\nbody"))
	if err == nil {
		t.Fatal("sendSMTPStartTLS returned nil, want refusal")
	}
	if !strings.Contains(err.Error(), "refusing cleartext AUTH") {
		t.Fatalf("err = %q, want substring %q", err.Error(), "refusing cleartext AUTH")
	}
	<-serverDone
}

// testSelfSignedTLS builds an in-memory self-signed certificate suitable for a
// tls.Server in deadline tests. Separate from internal/dashboard's equivalent
// so this package stays self-contained.
func testSelfSignedTLS(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "drainctl-smtps-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestSMTPS_SilentGreetingHitsOverallDeadline — T108. Mirror of the STARTTLS
// variant for the implicit-TLS code path. The server completes the TLS
// handshake but never emits the SMTP greeting, so NewClient's greeting read
// must be bounded by the conn.SetDeadline that sendSMTPS arms BEFORE calling
// smtp.NewClient. Without that ordering the call would hang indefinitely.
func TestSMTPS_SilentGreetingHitsOverallDeadline(t *testing.T) {
	withShortSMTPTimeouts(t, 2*time.Second, 250*time.Millisecond)

	origCfg := smtpsTLSConfig
	smtpsTLSConfig = func(host string) *tls.Config {
		return &tls.Config{ServerName: host, InsecureSkipVerify: true} //nolint:gosec // deadline test against in-process self-signed server
	}
	t.Cleanup(func() { smtpsTLSConfig = origCfg })

	cert := testSelfSignedTLS(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Force the TLS handshake so the client reaches its greeting read,
		// then stay silent until the client's deadline fires.
		if tc, ok := conn.(*tls.Conn); ok {
			_ = tc.Handshake()
		}
		buf := make([]byte, 256)
		_, _ = conn.Read(buf)
	}()

	target := NotificationTarget{
		URL:  "smtps://" + ln.Addr().String(),
		From: "drainctl@example.test",
		To:   []string{"rcpt@example.test"},
	}

	start := time.Now()
	err = sendSMTPS(ln.Addr().String(), "localhost", target, []byte("Subject: x\r\n\r\nbody"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("sendSMTPS returned nil on silent-greeting server; expected deadline error")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("expected net.Error.Timeout()==true, got %T: %v", err, err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("sendSMTPS took %v — did not respect overall deadline", elapsed)
	}
	<-acceptDone
}
