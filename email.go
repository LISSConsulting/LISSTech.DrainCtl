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
	case "Test":
		statusColor = "#4a90d9"
	}

	cb := changedBy
	if cb == "" {
		cb = "\u2014" // em dash
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
