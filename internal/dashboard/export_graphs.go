//go:build windows

package dashboard

// ExportGraph is the static, server-side description of a supported chart
// that can be exported. The id is the stable contract used in the
// `graph` query parameter (per contracts/http-metrics-export.md); the
// label is the human-readable name rendered in the UI export control
// and on the Excel Context sheet; the defaultCounters are the counter
// names BuildSnapshot pulls from the metrics query when the operator
// did not pass an explicit `counters` parameter.
//
// The registry is small and intentionally static. Adding a new chart
// requires: (1) registering it here, (2) wiring the matching frontend
// export control in MetricsChart.svelte / HostLoadChart.svelte /
// ServerDetail.svelte.
type ExportGraph struct {
	ID              string
	Label           string
	Slug            string
	CounterFamily   string
	DefaultCounters []string
	TierDefault     string
	AllowedTiers    []string
}

// registry holds every supported graph. Keep this list in sync with the
// frontend export-graph-config.js (T030). If a chart is added on either
// side without the other, LookupExportGraph returns false and the
// handler returns 400 unknown_graph.
var registry = map[string]ExportGraph{
	"overview.load": {
		ID:            "overview.load",
		Label:         "Overview Load",
		Slug:          "overview-load",
		CounterFamily: "fleet-load",
		DefaultCounters: []string{
			"cpu_pct",
			"mem_avail_mb",
			"mem_total_mb",
			"input_delay_p95_ms",
		},
		TierDefault:  "auto",
		AllowedTiers: []string{"raw", "1min", "5min", "hourly", "auto"},
	},
	"humanic.input_delay": {
		ID:              "humanic.input_delay",
		Label:           "HIC · Input Delay",
		Slug:            "input-delay",
		CounterFamily:   "hic",
		DefaultCounters: []string{"input_delay_p95_ms"},
		TierDefault:     "auto",
		AllowedTiers:    []string{"raw", "1min", "5min", "hourly", "auto"},
	},
	"humanic.pages_sec": {
		ID:              "humanic.pages_sec",
		Label:           "HIC · Pages / sec",
		Slug:            "pages-sec",
		CounterFamily:   "hic",
		DefaultCounters: []string{"pages_sec"},
		TierDefault:     "auto",
		AllowedTiers:    []string{"raw", "1min", "5min", "hourly", "auto"},
	},
	"humanic.tcp_retrans_sec": {
		ID:              "humanic.tcp_retrans_sec",
		Label:           "HIC · TCP Retransmits / sec",
		Slug:            "tcp-retrans-sec",
		CounterFamily:   "hic",
		DefaultCounters: []string{"tcp_retrans_sec"},
		TierDefault:     "auto",
		AllowedTiers:    []string{"raw", "1min", "5min", "hourly", "auto"},
	},
	"humanic.disk_queue": {
		ID:              "humanic.disk_queue",
		Label:           "HIC · Disk Queue",
		Slug:            "disk-queue",
		CounterFamily:   "hic",
		DefaultCounters: []string{"disk_queue"},
		TierDefault:     "auto",
		AllowedTiers:    []string{"raw", "1min", "5min", "hourly", "auto"},
	},
	"overview.sessions": {
		ID:            "overview.sessions",
		Label:         "Sessions",
		Slug:          "sessions",
		CounterFamily: "sessions",
		DefaultCounters: []string{
			"sessions_total",
			"sessions_active",
			"sessions_max",
		},
		TierDefault:  "auto",
		AllowedTiers: []string{"raw", "1min", "5min", "hourly", "auto"},
	},
	"overview.remotefx": {
		ID:            "overview.remotefx",
		Label:         "RemoteFX",
		Slug:          "remotefx",
		CounterFamily: "remotefx",
		DefaultCounters: []string{
			"rfx_encode_p95_ms",
			"rfx_encode_avg_ms",
			"rfx_frames_sec",
			"rfx_tcp_rtt_ms",
		},
		TierDefault:  "auto",
		AllowedTiers: []string{"raw", "1min", "5min", "hourly", "auto"},
	},
	"host.load": {
		ID:            "host.load",
		Label:         "Per-host Load",
		Slug:          "load",
		CounterFamily: "host-load",
		DefaultCounters: []string{
			"cpu_pct",
			"mem_avail_mb",
			"mem_total_mb",
			"input_delay_p95_ms",
		},
		TierDefault:  "auto",
		AllowedTiers: []string{"raw", "1min", "5min", "hourly", "auto"},
	},
}

// LookupExportGraph resolves a graph id (the `graph` query parameter) into
// the registered ExportGraph. The boolean is false when the id is unknown,
// which the handler surfaces as 400 unknown_graph.
func LookupExportGraph(id string) (ExportGraph, bool) {
	g, ok := registry[id]
	return g, ok
}

// AllowedExportGraphIDs returns the registered graph ids in a stable order.
// Used by T045 (inventory exhaustiveness check) and for diagnostics.
func AllowedExportGraphIDs() []string {
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// sortStrings is a tiny in-place lexicographic sort. Insertion sort is
// adequate because the inventory holds at most a few dozen items and this
// path is only exercised by T045.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
