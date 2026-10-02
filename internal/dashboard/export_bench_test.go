//go:build windows

package dashboard

import (
	"io"
	"testing"
	"time"
)

func TestExportSerializationCompletesWithinFiveSeconds(t *testing.T) {
	snap := exportSerializationBenchmarkSnapshot()
	for name, write := range map[string]func(io.Writer, Snapshot) error{
		"csv":  WriteCSV,
		"xlsx": WriteXLSX,
	} {
		start := time.Now()
		if err := write(io.Discard, snap); err != nil {
			t.Fatalf("Write%s: %v", name, err)
		}
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Errorf("Write%s took %s, want under 5s", name, elapsed)
		}
	}
}

func BenchmarkExportSerialization10000Rows(b *testing.B) {
	snap := exportSerializationBenchmarkSnapshot()
	for name, write := range map[string]func(io.Writer, Snapshot) error{
		"CSV":  WriteCSV,
		"XLSX": WriteXLSX,
	} {
		b.Run(name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := write(io.Discard, snap); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func exportSerializationBenchmarkSnapshot() Snapshot {
	const timestamps = 100
	const hosts = 100
	const rows = timestamps * hosts

	values := make([]float64, rows)
	observations := make([]Observation, 0, rows)
	hostFilter := make([]string, hosts)
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for host := range hosts {
		hostFilter[host] = "host-" + twoDigit(host)
	}
	for timestamp := range timestamps {
		for host := range hosts {
			index := timestamp*hosts + host
			values[index] = float64(index)
			observations = append(observations, Observation{
				Timestamp:   base.Add(time.Duration(timestamp) * time.Minute),
				HostName:    hostFilter[host],
				SeriesID:    "cpu_pct",
				SeriesLabel: "CPU",
				Unit:        "%",
				Value:       &values[index],
				Aggregation: "avg",
				Resolution:  "1m",
			})
		}
	}
	return Snapshot{
		GraphID:         "overview.load",
		GraphLabel:      "Overview Load",
		GraphSlug:       "overview-load",
		ExportType:      ExportTypeFleet,
		HostFilter:      hostFilter,
		Tier:            "1min",
		From:            base,
		To:              base.Add(timestamps * time.Minute),
		DisplayTimezone: "UTC",
		Coverage:        CoverageFull,
		Rows:            observations,
	}
}

func twoDigit(value int) string {
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
