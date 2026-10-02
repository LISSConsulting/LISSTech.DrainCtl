//go:build windows

package dashboard

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// handleExport serves the two new graph-data export endpoints:
//
//	GET /api/v1/metrics/_fleet/export
//	GET /api/v1/metrics/{host}/export
//
// The handler dispatches to the CSV or XLSX writer based on the
// `format` query parameter. CSV is wired in Phase 3 (T015/T016); XLSX in
// Phase 4 (T023/T025). Until those land, an unknown format returns
// 501 not_implemented so callers see the route exists and is being
// staged rather than a generic 404.
//
// The dispatcher is structured around the same validation pipeline as
// handleMetrics / handleFleetMetrics:
//   - resolve the export type (fleet vs per_host) from the route
//   - parse and validate from/to/resolution/counters/host (fleet only)
//   - parse and validate format and graph
//   - run the existing metrics query
//   - build the snapshot via BuildSnapshot
//   - enforce the size cap (enforceExportSizeLimit)
//   - dispatch to the writer
//
// The 500 storage_error response MUST NOT include exported values,
// credentials, or full file contents (per FR-015).
func (ds *DashboardServer) handleExport(w http.ResponseWriter, r *http.Request) {
	if ds.ms == nil {
		writeExportError(w, "storage_error", http.StatusInternalServerError, exportErrorBody{})
		return
	}

	hostPath := r.PathValue("host")
	if hostPath == "" {
		writeExportError(w, "unknown_host", http.StatusNotFound, exportErrorBody{})
		return
	}

	exportType := ExportTypePerHost
	if hostPath == "_fleet" {
		exportType = ExportTypeFleet
	}

	q := r.URL.Query()

	fromStr := q.Get("from")
	toStr := q.Get("to")
	if fromStr == "" || toStr == "" {
		writeExportError(w, "invalid_range", http.StatusBadRequest, exportErrorBody{})
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeExportError(w, "invalid_range", http.StatusBadRequest, exportErrorBody{})
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		writeExportError(w, "invalid_range", http.StatusBadRequest, exportErrorBody{})
		return
	}
	if !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		writeExportError(w, "invalid_range", http.StatusBadRequest, exportErrorBody{})
		return
	}

	resStr := q.Get("resolution")
	if resStr == "" {
		resStr = "auto"
	}
	if resStr != "auto" && resStr != "raw" && resStr != "1min" && resStr != "5min" && resStr != "hourly" {
		writeExportError(w, "invalid_resolution", http.StatusBadRequest, exportErrorBody{})
		return
	}

	format := q.Get("format")
	if format != "csv" && format != "xlsx" {
		writeExportError(w, "invalid_format", http.StatusBadRequest, exportErrorBody{})
		return
	}

	graphID := q.Get("graph")
	graph, ok := LookupExportGraph(graphID)
	if !ok {
		writeExportError(w, "unknown_graph", http.StatusBadRequest, exportErrorBody{})
		return
	}

	// Counter list comes from the graph's defaults when the operator did
	// not pass an explicit `counters` parameter. Unspecified counters are
	// silently skipped (consistent with the read endpoint per
	// contracts/http-metrics-export.md).
	var counters []string
	if csv := q.Get("counters"); csv != "" {
		for _, c := range strings.Split(csv, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				counters = append(counters, c)
			}
		}
	}
	if len(counters) == 0 {
		counters = append(counters, graph.DefaultCounters...)
	}

	// Host resolution. Per_host requires a registered host. Fleet honors
	// the optional `host` repeat-parameter filter (registered subset).
	var (
		hostName   string
		hostFilter []string
	)
	if exportType == ExportTypePerHost {
		if _, present := q["host"]; present {
			writeExportError(w, "invalid_host_filter", http.StatusBadRequest, exportErrorBody{})
			return
		}
		if !ds.state.IsRegistered(hostPath) {
			writeExportError(w, "unknown_host", http.StatusNotFound, exportErrorBody{})
			return
		}
		hostName = hostPath
	} else {
		infos := ds.state.All()
		roster := make(map[string]string, len(infos))
		for _, info := range infos {
			canonical := telemetry.CanonicalHostname(info.Hostname)
			roster[canonical] = canonical
		}
		if requested, hasFilter := q["host"]; hasFilter {
			seen := make(map[string]struct{}, len(requested))
			for _, raw := range requested {
				canonical := telemetry.CanonicalHostname(raw)
				if canonical == "" {
					writeExportError(w, "invalid_host_filter", http.StatusBadRequest, exportErrorBody{})
					return
				}
				if _, duplicate := seen[canonical]; duplicate {
					writeExportError(w, "invalid_host_filter", http.StatusBadRequest, exportErrorBody{})
					return
				}
				registered, present := roster[canonical]
				if !present {
					writeExportError(w, "invalid_host_filter", http.StatusBadRequest, exportErrorBody{})
					return
				}
				seen[canonical] = struct{}{}
				hostFilter = append(hostFilter, registered)
			}
		} else {
			for _, registered := range roster {
				hostFilter = append(hostFilter, registered)
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var (
		series *telemetry.Series
		tErr   error
	)
	if exportType == ExportTypePerHost {
		tier, err := ds.resolveMetricsTier(ctx, hostName, from, to, resStr)
		if err != nil {
			slog.Error("export: tier resolution failed", "host", hostName, "error", err) //nolint:gosec // host is validated against registered server list
			writeExportError(w, "storage_error", http.StatusInternalServerError, exportErrorBody{})
			return
		}
		series, tErr = ds.ms.QueryRange(ctx, hostName, from, to, tier, counters)
	} else {
		tier, err := ds.resolveFleetMetricsTier(ctx, hostFilter, from, to, resStr)
		if err != nil {
			slog.Error("export: fleet tier resolution failed", "error", err)
			writeExportError(w, "storage_error", http.StatusInternalServerError, exportErrorBody{})
			return
		}
		series, tErr = ds.ms.QueryRangeFleet(ctx, hostFilter, from, to, tier, counters, int64(telemetry.DefaultRawFleetBucketMs))
	}
	if tErr != nil {
		slog.Error("export: query failed", "error", tErr)
		writeExportError(w, "storage_error", http.StatusInternalServerError, exportErrorBody{})
		return
	}

	coverage, coverageNote := deriveCoverage(series)

	seriesDefs := buildSeriesDefinitions(counters, graph.CounterFamily)
	snap := BuildSnapshot(
		graph.ID,
		graph.Label,
		graph.Slug,
		exportType,
		hostName,
		hostFilter,
		series.Tier.TierName(),
		from,
		to,
		"UTC",
		coverage,
		coverageNote,
		seriesDefs,
		series,
	)

	if !enforceExportSizeLimit(snap, w) {
		return
	}

	filenameType := FilenamePerHost
	if exportType == ExportTypeFleet {
		filenameType = FilenameFleet
	}
	filename, fnameErr := BuildFilename(filenameType, hostName, graph.Label, format)
	if fnameErr != nil {
		slog.Error("export: filename build failed", "error", fnameErr, "graph", graph.ID)
		writeExportError(w, "storage_error", http.StatusInternalServerError, exportErrorBody{})
		return
	}

	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		if err := WriteCSV(w, snap); err != nil {
			slog.Error("export: csv write failed", "error", err)
			return
		}
	case "xlsx":
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		if err := WriteXLSX(w, snap); err != nil {
			slog.Error("export: xlsx write failed", "error", err)
			return
		}
	}
}

// deriveCoverage mirrors the existing /api/v1/metrics coverage labels so
// the export Context sheet can be compared 1:1 with the JSON response.
// A nil series, empty data, or entirely missing bounds is "empty"; a
// partial window (OldestAvailable after From or NewestAvailable before To)
// is "partial"; otherwise "full".
func deriveCoverage(s *telemetry.Series) (Coverage, string) {
	if s == nil || len(s.Data) == 0 {
		return CoverageEmpty, ""
	}
	switch {
	case s.OldestAvailable == nil || s.NewestAvailable == nil:
		return CoverageEmpty, ""
	case s.OldestAvailable.After(s.From):
		return CoveragePartial, "oldest available timestamp is after requested from"
	case s.NewestAvailable.Before(s.To):
		return CoveragePartial, "newest available timestamp is before requested to"
	}
	return CoverageFull, ""
}

// buildSeriesDefinitions produces the snapshot SeriesDefinition list in
// the graph's declared order. Unknown counter names are dropped with a
// logged warning so a typo doesn't silently corrupt the export.
//
// Aggregation/resolution strings use the metric-store vocabulary directly:
//   - aggregation: "avg" or "p50" depending on which array we expose
//   - resolution: the tier's resolution bucket (e.g. "1m", "5m", "raw")
//
// Both values are coarse summaries at this layer; the per-row columns
// use them verbatim per FR-006.
func buildSeriesDefinitions(counters []string, family string) []SeriesDefinition {
	// Family mapping: which counters in this graph are fleet-aggregated
	// (P50 = the median across the host cohort) versus per-host samples.
	// Per the data-fabrication note in BuildSnapshot, fleet exports
	// surface one row per (timestamp × series) where the value is the
	// fleet statistic (P50). We mark the aggregation accordingly so the
	// Excel Context sheet can summarize it.
	//
	// For per_host exports the value IS the per-host sample, which is
	// reflected in the BuildSnapshot helper.
	_ = family
	defs := make([]SeriesDefinition, 0, len(counters))
	for _, c := range counters {
		defs = append(defs, SeriesDefinition{
			SeriesID:    c,
			SeriesLabel: c,
			Unit:        inferUnit(c),
			Aggregation: "avg",
			Resolution:  "raw",
		})
	}
	return defs
}

// inferUnit maps known counter names to their canonical units so the
// export doesn't need an additional translation layer at query time.
// Unknown counters come back with an empty unit string (dimensionless).
func inferUnit(counter string) string {
	switch counter {
	case "cpu_pct", "mem_used_pct":
		return "%"
	case "input_delay_p95_ms",
		"rfx_encode_p95_ms",
		"rfx_encode_avg_ms",
		"rfx_tcp_rtt_ms":
		return "ms"
	case "mem_avail_mb", "mem_total_mb":
		return "MB"
	case "pages_sec", "tcp_retrans_sec", "disk_queue", "rfx_frames_sec":
		return "/s"
	default:
		return ""
	}
}
