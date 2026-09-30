//go:build windows

package telemetry

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

const workloadTestInstance = "01890f9d-5c00-7000-8000-000000000001"

func workloadSnapshot(host string, sequence uint64, cpuValues []float64) sessiondata.SessionSnapshot {
	sessions := make([]sessiondata.SessionRecord, len(cpuValues))
	for i, value := range cpuValues {
		sessions[i] = sessiondata.SessionRecord{
			SessionID: uint32(i + 1), State: sessiondata.SessionActive, CPUPercent: new(value),
			WorkingSetBytes: new(sessiondata.DecimalUint64(uint64(i+1) * 1024)), Processes: []sessiondata.SessionProcess{},
		}
	}
	return sessiondata.SessionSnapshot{
		Schema: sessiondata.SnapshotSchema, Host: host, AgentInstanceID: workloadTestInstance,
		Sequence: sessiondata.DecimalUint64(sequence), ObservedAtMS: 1, CollectorVersion: "test",
		LogicalCPUCount: 1, Sessions: sessions,
	}
}

func TestSessionWorkloadPoolsUnequalHostsAndReplacesFatalAttempt(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := NewMetricsStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metrics.Close() })
	now := time.Date(2026, 9, 29, 12, 1, 30, 0, time.UTC)
	store.now = func() time.Time { return now }

	hostA := make([]float64, 100)
	for i := range hostA {
		hostA[i] = 5
	}
	for _, snapshot := range []sessiondata.SessionSnapshot{
		workloadSnapshot("host-a.example.test", 1, hostA),
		workloadSnapshot("host-b.example.test", 1, []float64{80, 80}),
	} {
		if result, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil || !result.Accepted {
			t.Fatalf("Apply(%s) = %+v, %v", snapshot.Host, result, err)
		}
	}
	series, err := metrics.QuerySessionWorkload(context.Background(), []string{"HOST-A.EXAMPLE.TEST", "HOST-B.EXAMPLE.TEST"}, now.Add(-time.Minute), now.Add(time.Minute), TierOneMin)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 1 || series.Points[0].CPU == nil {
		t.Fatalf("points = %+v", series.Points)
	}
	point := series.Points[0]
	if math.Abs(point.CPU.AvgPct-(660.0/102.0)) > 1e-9 || math.Abs(point.CPU.P95Pct-5) > .5 {
		t.Fatalf("pooled CPU = %+v", point.CPU)
	}
	if point.CPU.ObservedSessions != 102 || point.Coverage.ContributingHosts != 2 || point.Coverage.Partial {
		t.Fatalf("pooled coverage = %+v", point.Coverage)
	}

	fatal := workloadSnapshot("host-b.example.test", 2, nil)
	fatal.CollectionError = &sessiondata.CollectionError{Code: sessiondata.CollectionErrorWTSEnumerationFailed}
	fatal.Sessions = nil
	if result, err := store.Apply(context.Background(), fatal, sessiondata.PrivacyPolicy{}); err != nil || !result.Accepted || !result.Fatal {
		t.Fatalf("fatal Apply = %+v, %v", result, err)
	}
	series, err = metrics.QuerySessionWorkload(context.Background(), []string{"HOST-A.EXAMPLE.TEST", "HOST-B.EXAMPLE.TEST"}, now.Add(-time.Minute), now.Add(time.Minute), TierOneMin)
	if err != nil {
		t.Fatal(err)
	}
	point = series.Points[0]
	if point.CPU.ObservedSessions != 100 || point.CPU.AvgPct != 5 || point.Coverage.ContributingHosts != 1 || point.Coverage.ErrorHosts != 1 || !point.Coverage.Partial {
		t.Fatalf("fatal replacement point = %+v", point)
	}
}

func TestSessionWorkloadRollupsPreserveWeightedStatistics(t *testing.T) {
	db := openTestDB(t)
	store, _ := NewSessionSnapshotStore(db)
	metrics, _ := NewMetricsStore(context.Background(), db)
	t.Cleanup(func() { _ = metrics.Close() })
	base := time.Date(2026, 9, 29, 11, 40, 0, 0, time.UTC)
	sequence := uint64(1)
	for minute, values := range [][]float64{{0, 10}, {10, 20}, {20, 30}, {30, 40}, {40, 50}} {
		store.now = func() time.Time { return base.Add(time.Duration(minute) * time.Minute) }
		if _, err := store.Apply(context.Background(), workloadSnapshot("rollup.example.test", sequence, values), sessiondata.PrivacyPolicy{}); err != nil {
			t.Fatal(err)
		}
		sequence++
	}
	NewAggregator(db, 60).RollOnce(context.Background(), time.Date(2026, 9, 29, 13, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		tier Tier
		from time.Time
	}{{TierFiveMin, base.Truncate(5 * time.Minute)}, {TierHourly, base.Truncate(time.Hour)}} {
		series, err := metrics.QuerySessionWorkload(context.Background(), []string{"ROLLUP.EXAMPLE.TEST"}, tc.from, tc.from.Add(time.Hour), tc.tier)
		if err != nil {
			t.Fatal(err)
		}
		if len(series.Points) != 1 || series.Points[0].CPU == nil {
			t.Fatalf("tier %v points = %+v", tc.tier, series.Points)
		}
		cpu := series.Points[0].CPU
		if cpu.ObservedSessions != 2 || math.Abs(cpu.AvgPct-25) > 1e-9 || math.Abs(cpu.P95Pct-50) > .5 {
			t.Fatalf("tier %v CPU = %+v", tc.tier, cpu)
		}
	}
}

func TestSessionWorkloadRollupMaxAlignsConcurrentHostSlots(t *testing.T) {
	db := openTestDB(t)
	store, _ := NewSessionSnapshotStore(db)
	metrics, _ := NewMetricsStore(context.Background(), db)
	t.Cleanup(func() { _ = metrics.Close() })
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	for minute, values := range []struct {
		a, b float64
	}{{30, 0}, {0, 30}} {
		store.now = func() time.Time { return base.Add(time.Duration(minute) * time.Minute) }
		sequence := uint64(minute + 1)
		for host, value := range map[string]float64{"host-a.example.test": values.a, "host-b.example.test": values.b} {
			if _, err := store.Apply(context.Background(), workloadSnapshot(host, sequence, []float64{value}), sessiondata.PrivacyPolicy{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	NewAggregator(db, 60).RollOnce(context.Background(), base.Add(20*time.Minute))
	series, err := metrics.QuerySessionWorkload(context.Background(), []string{"HOST-A.EXAMPLE.TEST", "HOST-B.EXAMPLE.TEST"}, base, base.Add(5*time.Minute), TierFiveMin)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 1 || series.Points[0].CPU == nil {
		t.Fatalf("points = %+v", series.Points)
	}
	if got := series.Points[0].CPU.GE20Max; got != 1 {
		t.Fatalf("GE20Max = %d, want aligned fleet maximum 1", got)
	}
}

func TestSessionWorkloadSchemaContainsNoSessionIdentity(t *testing.T) {
	db := openTestDB(t)
	prohibited := map[string]struct{}{
		"user_name": {}, "domain_name": {}, "session_id": {}, "client_name": {}, "client_address": {},
		"station": {}, "processes_json": {}, "logon_at_ms": {}, "action_id": {},
	}
	for _, table := range []string{"session_workload_raw", "session_workload_5min", "session_workload_hourly"} {
		rows, err := db.reader.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, dataType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if _, forbidden := prohibited[name]; forbidden {
				_ = rows.Close()
				t.Fatalf("%s contains prohibited identity column %s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
