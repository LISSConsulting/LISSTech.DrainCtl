//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// writeJSONError writes a JSON body {"error":"<code>"} with the given HTTP status.
func writeJSONError(w http.ResponseWriter, code string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// resolveMetricsTier maps a request's resolution parameter to a concrete tier.
// Explicit "raw"/"1min"/"5min"/"hourly" pass through. "auto" picks by window
// size (≤15m → raw, ≤1h → 1min, ≤36h → 5min, >36h → hourly) then degrades to
// the next coarser tier if the chosen tier's oldest row does not cover `from`.
// Hourly is never degraded (it is the coarsest tier).
//
// The 1-minute tier is a virtual GROUP BY on metrics_raw — the 1H dashboard
// pill maps to 60 buckets. Short windows (15M pill) render raw so the chart
// shows per-second detail before the minute aggregation kicks in.
func (ds *DashboardServer) resolveMetricsTier(
	ctx context.Context,
	host string,
	from, to time.Time,
	resolution string,
) (telemetry.Tier, error) {
	switch resolution {
	case "raw":
		return telemetry.TierRaw, nil
	case "1min":
		return telemetry.TierOneMin, nil
	case "5min":
		return telemetry.TierFiveMin, nil
	case "hourly":
		return telemetry.TierHourly, nil
	}

	window := to.Sub(from)
	var tier telemetry.Tier
	switch {
	case window <= 15*time.Minute:
		tier = telemetry.TierRaw
	case window <= time.Hour:
		tier = telemetry.TierOneMin
	case window <= 36*time.Hour:
		tier = telemetry.TierFiveMin
	default:
		tier = telemetry.TierHourly
	}

	for tier != telemetry.TierHourly {
		oldest, _, err := ds.ms.BoundsForTier(ctx, host, tier)
		if err != nil {
			return 0, err
		}
		if oldest != nil && !oldest.After(from) {
			return tier, nil
		}
		switch tier {
		case telemetry.TierRaw, telemetry.TierOneMin:
			// Both share metrics_raw's bounds; falling through either goes to 5min.
			tier = telemetry.TierFiveMin
		case telemetry.TierFiveMin:
			tier = telemetry.TierHourly
		}
	}
	return tier, nil
}

// handleMetrics serves GET /api/v1/metrics/{host} per contracts/http-metrics.md.
// When host is "_fleet" the request is routed to handleFleetMetrics.
func (ds *DashboardServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		writeJSONError(w, "unknown_host", http.StatusNotFound)
		return
	}
	if host == "_fleet" {
		ds.handleFleetMetrics(w, r)
		return
	}
	if !ds.state.IsRegistered(host) {
		writeJSONError(w, "unknown_host", http.StatusNotFound)
		return
	}

	q := r.URL.Query()
	fromStr := q.Get("from")
	toStr := q.Get("to")
	if fromStr == "" || toStr == "" {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	if !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}

	resStr := q.Get("resolution")
	if resStr == "" {
		resStr = "auto"
	}
	if resStr != "auto" && resStr != "raw" && resStr != "1min" && resStr != "5min" && resStr != "hourly" {
		writeJSONError(w, "invalid_resolution", http.StatusBadRequest)
		return
	}

	var counters []string
	if csv := q.Get("counters"); csv != "" {
		for _, c := range strings.Split(csv, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				counters = append(counters, c)
			}
		}
	}

	if ds.ms == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tier, err := ds.resolveMetricsTier(ctx, host, from, to, resStr)
	if err != nil {
		slog.Error("metrics: tier resolution failed", "host", host, "error", err) //nolint:gosec // host is validated against registered server list
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	sr, err := ds.ms.QueryRange(ctx, host, from, to, tier, counters)
	if err != nil {
		slog.Error("metrics: query failed", "host", host, "error", err) //nolint:gosec // host is validated against registered server list
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	type counterJSON struct {
		T   []int64   `json:"t"`
		Avg []float64 `json:"avg"`
		Min []float64 `json:"min"`
		Max []float64 `json:"max"`
	}
	type metricsResp struct {
		Host            string                  `json:"host"`
		Tier            string                  `json:"tier"`
		From            string                  `json:"from"`
		To              string                  `json:"to"`
		OldestAvailable *string                 `json:"oldest_available"`
		NewestAvailable *string                 `json:"newest_available"`
		Series          map[string]*counterJSON `json:"series"`
	}

	resp := metricsResp{
		Host:   host,
		Tier:   sr.Tier.TierName(),
		From:   from.UTC().Format(time.RFC3339),
		To:     to.UTC().Format(time.RFC3339),
		Series: make(map[string]*counterJSON, len(sr.Data)),
	}
	if sr.OldestAvailable != nil {
		s := sr.OldestAvailable.UTC().Format(time.RFC3339)
		resp.OldestAvailable = &s
	}
	if sr.NewestAvailable != nil {
		s := sr.NewestAvailable.UTC().Format(time.RFC3339)
		resp.NewestAvailable = &s
	}
	for name, cs := range sr.Data {
		t := cs.T
		if t == nil {
			t = []int64{}
		}
		avg := cs.Avg
		if avg == nil {
			avg = []float64{}
		}
		min := cs.Min
		if min == nil {
			min = []float64{}
		}
		max := cs.Max
		if max == nil {
			max = []float64{}
		}
		resp.Series[name] = &counterJSON{T: t, Avg: avg, Min: min, Max: max}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// resolveFleetMetricsTier picks the concrete tier for a fleet query using the
// same window-size heuristic as resolveMetricsTier, then degrades if the
// fleet-wide oldest timestamp does not cover from.
func (ds *DashboardServer) resolveFleetMetricsTier(
	ctx context.Context,
	hosts []string,
	from, to time.Time,
	resolution string,
) (telemetry.Tier, error) {
	switch resolution {
	case "raw":
		return telemetry.TierRaw, nil
	case "1min":
		return telemetry.TierOneMin, nil
	case "5min":
		return telemetry.TierFiveMin, nil
	case "hourly":
		return telemetry.TierHourly, nil
	}
	window := to.Sub(from)
	var tier telemetry.Tier
	switch {
	case window <= 15*time.Minute:
		tier = telemetry.TierRaw
	case window <= time.Hour:
		tier = telemetry.TierOneMin
	case window <= 36*time.Hour:
		tier = telemetry.TierFiveMin
	default:
		tier = telemetry.TierHourly
	}
	for tier != telemetry.TierHourly {
		oldest, _, err := ds.ms.BoundsForTierFleet(ctx, hosts, tier)
		if err != nil {
			return 0, err
		}
		if oldest != nil && !oldest.After(from) {
			return tier, nil
		}
		switch tier {
		case telemetry.TierRaw, telemetry.TierOneMin:
			tier = telemetry.TierFiveMin
		case telemetry.TierFiveMin:
			tier = telemetry.TierHourly
		}
	}
	return tier, nil
}

// handleFleetMetrics serves GET /api/v1/metrics/_fleet per
// contracts/http-fleet-metrics.md, returning retained history aggregated
// across all registered hosts.
func (ds *DashboardServer) handleFleetMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromStr := q.Get("from")
	toStr := q.Get("to")
	if fromStr == "" || toStr == "" {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	if !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	resStr := q.Get("resolution")
	if resStr == "" {
		resStr = "auto"
	}
	if resStr != "auto" && resStr != "raw" && resStr != "1min" && resStr != "5min" && resStr != "hourly" {
		writeJSONError(w, "invalid_resolution", http.StatusBadRequest)
		return
	}
	var counters []string
	if csv := q.Get("counters"); csv != "" {
		for _, c := range strings.Split(csv, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				counters = append(counters, c)
			}
		}
	}
	if ds.ms == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	infos := ds.state.All()
	hosts := make([]string, len(infos))
	for i, info := range infos {
		hosts[i] = info.Hostname
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tier, err := ds.resolveFleetMetricsTier(ctx, hosts, from, to, resStr)
	if err != nil {
		slog.Error("fleet metrics: tier resolution failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	// Raw-tier bucketing must be wide enough to absorb every agent's poll
	// jitter — clocks drift, perfmon collection takes variable time, and
	// pull-config propagation lag means hosts may briefly run different
	// sample_interval_sec values. A bucket equal to sample_interval still
	// puts hosts on opposite sides of a 30-s boundary when their phases
	// differ by ~T, producing the alternating-host zigzag we already fixed
	// once. 2× sample_interval guarantees every host has at least one
	// sample per bucket regardless of phase. Falls back to the store's
	// default when config isn't available (test harness, hot-swap gap).
	rawBucketMs := int64(telemetry.DefaultRawFleetBucketMs)
	var cfg *dc.Config
	var cerr error
	if ds.testLoadConfigFunc != nil {
		cfg, cerr = ds.testLoadConfigFunc()
	} else {
		cfg, cerr = dc.LoadConfig()
	}
	if cerr == nil && cfg.Performance.SampleIntervalSec > 0 {
		rawBucketMs = int64(cfg.Performance.SampleIntervalSec) * 2 * 1000
	}

	sr, err := ds.ms.QueryRangeFleet(ctx, hosts, from, to, tier, counters, rawBucketMs)
	if err != nil {
		slog.Error("fleet metrics: query failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	type counterJSON struct {
		T   []int64   `json:"t"`
		Avg []float64 `json:"avg"`
		Min []float64 `json:"min"`
		Max []float64 `json:"max"`
	}
	type metricsResp struct {
		Host            string                  `json:"host"`
		Tier            string                  `json:"tier"`
		From            string                  `json:"from"`
		To              string                  `json:"to"`
		OldestAvailable *string                 `json:"oldest_available"`
		NewestAvailable *string                 `json:"newest_available"`
		Series          map[string]*counterJSON `json:"series"`
	}
	resp := metricsResp{
		Host:   "_fleet",
		Tier:   sr.Tier.TierName(),
		From:   from.UTC().Format(time.RFC3339),
		To:     to.UTC().Format(time.RFC3339),
		Series: make(map[string]*counterJSON, len(sr.Data)),
	}
	if sr.OldestAvailable != nil {
		s := sr.OldestAvailable.UTC().Format(time.RFC3339)
		resp.OldestAvailable = &s
	}
	if sr.NewestAvailable != nil {
		s := sr.NewestAvailable.UTC().Format(time.RFC3339)
		resp.NewestAvailable = &s
	}
	for name, cs := range sr.Data {
		t := cs.T
		if t == nil {
			t = []int64{}
		}
		avg := cs.Avg
		if avg == nil {
			avg = []float64{}
		}
		min := cs.Min
		if min == nil {
			min = []float64{}
		}
		max := cs.Max
		if max == nil {
			max = []float64{}
		}
		resp.Series[name] = &counterJSON{T: t, Avg: avg, Min: min, Max: max}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// seedMetricsCounters are the raw-tier counter names queried for the metrics seed route.
var seedMetricsCounters = []string{
	"cpu_pct",
	"mem_avail_mb",
	"mem_total_mb",
	"input_delay_p95_ms",
	"sessions_total",
	"disk_queue",
	"tcp_retrans_sec",
}

// handleSeedMetrics serves GET /api/v1/metrics (no {host}) per
// contracts/http-metrics-seed.md.  Returns bounded recent raw-tier history
// per registered host for sparkline cold-start seeding.
func (ds *DashboardServer) handleSeedMetrics(w http.ResponseWriter, r *http.Request) {
	if ds.ms == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()

	if res := q.Get("resolution"); res != "" && res != "raw" {
		writeJSONError(w, "invalid_resolution", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	from := now.Add(-30 * time.Minute)
	to := now

	if s := q.Get("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		from = t
	}
	if s := q.Get("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		to = t
	}
	if !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}

	limit := 60
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}

	type seedSampleJSON struct {
		Time       int64   `json:"time"`
		CPU        float64 `json:"cpu"`
		Mem        float64 `json:"mem"`
		InputDelay float64 `json:"inputDelay"`
		Sessions   int     `json:"sessions"`
		DiskQueue  float64 `json:"diskQueue"`
		TcpRetrans float64 `json:"tcpRetrans"`
	}

	infos := ds.state.All()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result := make(map[string][]seedSampleJSON, len(infos))
	for _, info := range infos {
		host := info.Hostname
		sr, err := ds.ms.QueryRange(ctx, host, from, to, telemetry.TierRaw, seedMetricsCounters)
		if err != nil {
			slog.Error("seed metrics: query failed", "host", host, "error", err) //nolint:gosec
			writeJSONError(w, "storage_error", http.StatusInternalServerError)
			return
		}

		byTs := make(map[int64]*seedSampleJSON)
		availByTs := make(map[int64]float64)
		totalByTs := make(map[int64]float64)

		for name, cs := range sr.Data {
			for i, ts := range cs.T {
				s, ok := byTs[ts]
				if !ok {
					s = &seedSampleJSON{Time: ts}
					byTs[ts] = s
				}
				v := cs.Avg[i]
				switch name {
				case "cpu_pct":
					s.CPU = v
				case "mem_avail_mb":
					availByTs[ts] = v
				case "mem_total_mb":
					totalByTs[ts] = v
				case "input_delay_p95_ms":
					s.InputDelay = v
				case "sessions_total":
					s.Sessions = int(v)
				case "disk_queue":
					s.DiskQueue = v
				case "tcp_retrans_sec":
					s.TcpRetrans = v
				}
			}
		}

		for ts, avail := range availByTs {
			if total, ok := totalByTs[ts]; ok && total > 0 {
				if s, ok := byTs[ts]; ok {
					s.Mem = (1 - avail/total) * 100
				}
			}
		}

		tss := make([]int64, 0, len(byTs))
		for ts := range byTs {
			tss = append(tss, ts)
		}
		slices.Sort(tss)
		if len(tss) > limit {
			tss = tss[len(tss)-limit:]
		}

		samples := make([]seedSampleJSON, 0, len(tss))
		for _, ts := range tss {
			samples = append(samples, *byTs[ts])
		}
		result[host] = samples
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
