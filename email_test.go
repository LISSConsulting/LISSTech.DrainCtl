//go:build windows

package drainctl

import (
	"strings"
	"testing"
)

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
