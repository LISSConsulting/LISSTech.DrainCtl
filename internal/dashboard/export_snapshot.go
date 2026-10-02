//go:build windows

package dashboard

import (
	"sort"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// ExportType identifies whether a snapshot describes a fleet or per-host
// query. The flag governs which fields of Snapshot are populated (host_name
// for per_host; host_filter for fleet) and which rows the Context sheet
// emits (per FR-007).
type ExportType string

const (
	ExportTypeFleet   ExportType = "fleet"
	ExportTypePerHost ExportType = "per_host"
)

// Coverage describes how completely the queried tier covered the requested
// time window. The values mirror the existing /api/v1/metrics coverage
// labels so the export Context sheet can be compared 1:1 with the JSON
// response on the same query.
type Coverage string

const (
	CoverageFull    Coverage = "full"
	CoveragePartial Coverage = "partial"
	CoverageEmpty   Coverage = "empty"
)

// hostFleetPlaceholder is the sentinel host_name on fleet observation rows.
// The value is empty in the per-row column but appears in the workbook
// header to remind downstream readers that fleet rows have no host. The CSV
// and XLSX writers translate this constant to an empty cell so the file
// matches FR-006's uniform per-row shape ("host_name" column always
// present, value is empty for fleet rows).
const hostFleetPlaceholder = "_fleet"

// Snapshot is the immutable export context for a single query. BuildSnapshot
// (T005) produces one of these from a metrics query result; the writers
// (export_csv.go / export_xlsx.go) consume it. The struct is internal —
// the JSON tags are deliberately absent because the snapshot is never
// serialized over the wire (per FR-007 the Context sheet is Excel-only and
// the CSV has no Context block).
type Snapshot struct {
	GraphID         string             // e.g. "overview.load", "host.load"
	GraphLabel      string             // e.g. "Overview Load", "Per-host Load"
	GraphSlug       string             // e.g. "overview-load" (sanitized for filename)
	ExportType      ExportType         // fleet or per_host
	HostName        string             // canonical hostname (per_host only; empty for fleet)
	HostFilter      []string           // canonical host names in registration order (fleet only)
	Tier            string             // "raw" | "1min" | "5min" | "hourly"
	From            time.Time          // inclusive lower bound, UTC
	To              time.Time          // exclusive upper bound, UTC
	DisplayTimezone string             // currently always "UTC"
	Coverage        Coverage           // "full" | "partial" | "empty"
	CoverageNote    string             // free-form note when Coverage != "full"
	Series          []SeriesDefinition // declared series (one row per (timestamp × series))
	Rows            []Observation      // denormalized observation rows
}

// SeriesDefinition captures the per-series metadata needed to label each
// observation row and to render the Context sheet's `series` cell. The
// aggregation and resolution are recorded at the snapshot tier so the
// exported rows can be interpreted without re-querying.
type SeriesDefinition struct {
	SeriesID    string // stable id matching the counter name (e.g. "cpu_pct")
	SeriesLabel string // human-readable label (e.g. "CPU p95")
	Unit        string // canonical unit ("%", "MB", "ms"); empty when dimensionless
	Aggregation string // "avg" | "p95" | "max" | "raw" | ...
	Resolution  string // bucket resolution ("1m", "5m", "1h", "raw")
}

// Observation is one row of the export Data sheet. For per_host exports
// HostName is constant across all rows; for fleet exports HostName is the
// hostFleetPlaceholder sentinel that the writers translate to an empty
// cell (no per-host fabrication — see data-fabrication note in BuildSnapshot).
// Value is nil for missing observations (rendered as an empty cell per FR-011).
type Observation struct {
	Timestamp   time.Time
	HostName    string
	SeriesID    string
	SeriesLabel string
	Unit        string
	Value       *float64 // nil ⇒ missing observation (empty cell, never string-encoded)
	Aggregation string
	Resolution  string
}

// BuildSnapshot converts a metrics query result and graph configuration into
// the immutable Snapshot the CSV/XLSX writers serialize.
//
// # ROW MODEL — IMPORTANT
//
// Per_host exports emit one row per (timestamp × series) with HostName set
// to the canonical host. This is direct from telemetry.QueryRange.
//
// Fleet exports ALSO emit one row per (timestamp × series) — but with HostName
// set to the hostFleetPlaceholder sentinel (which the writers translate to
// an empty cell). The fleet row carries the fleet statistic (P50 by default,
// falling back to Avg) computed by telemetry.QueryRangeFleet for the chosen
// host cohort.
//
// We do NOT replicate the fleet statistic across one row per host_filter
// member. That would label a fleet median under each host name and mislead
// operators into reading fleet numbers as per-host numbers, in direct
// violation of FR-005 ("preserve displayed derived-metric and
// fleet-aggregation semantics") and SC-002 ("no invented values").
//
// The cohort itself is recorded in Snapshot.HostFilter so the Excel Context
// sheet can still describe which hosts contributed to the fleet aggregate,
// and the per-row host_name column stays empty so any analyst opening the
// file can see at a glance that the rows are fleet-level, not per-host.
//
// The resulting Snapshot.Rows are sorted chronologically with the
// deterministic tiebreaker from the data model: within the same timestamp,
// rows are ordered by (series_id, host_name), both ascending.
func BuildSnapshot(
	graphID, graphLabel, graphSlug string,
	exportType ExportType,
	hostName string,
	hostFilter []string,
	tier string,
	from, to time.Time,
	displayTimezone string,
	coverage Coverage,
	coverageNote string,
	seriesDefs []SeriesDefinition,
	result *telemetry.Series,
) Snapshot {
	snap := Snapshot{
		GraphID:         graphID,
		GraphLabel:      graphLabel,
		GraphSlug:       graphSlug,
		ExportType:      exportType,
		HostName:        hostName,
		Tier:            tier,
		From:            from.UTC(),
		To:              to.UTC(),
		DisplayTimezone: displayTimezone,
		Coverage:        coverage,
		CoverageNote:    coverageNote,
		Series:          seriesDefs,
	}

	switch exportType {
	case ExportTypePerHost:
		if hostName == "" {
			return snap
		}
	case ExportTypeFleet:
		snap.HostFilter = append([]string(nil), hostFilter...)
		snap.HostName = ""
	default:
		return snap
	}

	if len(seriesDefs) == 0 || result == nil || len(result.Data) == 0 {
		return snap
	}

	ordered := make([]SeriesDefinition, len(seriesDefs))
	copy(ordered, seriesDefs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].SeriesID < ordered[j].SeriesID
	})

	var rowHost string
	if exportType == ExportTypePerHost {
		rowHost = hostName
	} else {
		rowHost = hostFleetPlaceholder
	}

	rows := make([]Observation, 0, estimateRows(ordered, result))
	for _, def := range ordered {
		cs, ok := result.Data[def.SeriesID]
		if !ok || cs == nil {
			continue
		}
		for i := range cs.T {
			ts := time.UnixMilli(cs.T[i]).UTC()
			var val *float64
			switch {
			case len(cs.P50) > i:
				val = cloneFloatPtr(cs.P50[i])
			case len(cs.Avg) > i:
				val = cloneFloatPtr(cs.Avg[i])
			}
			rows = append(rows, Observation{
				Timestamp:   ts,
				HostName:    rowHost,
				SeriesID:    def.SeriesID,
				SeriesLabel: def.SeriesLabel,
				Unit:        def.Unit,
				Value:       val,
				Aggregation: def.Aggregation,
				Resolution:  def.Resolution,
			})
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].Timestamp.Equal(rows[j].Timestamp) {
			return rows[i].Timestamp.Before(rows[j].Timestamp)
		}
		if rows[i].SeriesID != rows[j].SeriesID {
			return rows[i].SeriesID < rows[j].SeriesID
		}
		return rows[i].HostName < rows[j].HostName
	})

	snap.Rows = rows
	return snap
}

// estimateRows gives a capacity hint to avoid the rows slice growing in
// many small reallocations. It does not need to be exact.
func estimateRows(defs []SeriesDefinition, result *telemetry.Series) int {
	maxLen := 0
	for _, def := range defs {
		if cs, ok := result.Data[def.SeriesID]; ok && cs != nil {
			if len(cs.T) > maxLen {
				maxLen = len(cs.T)
			}
		}
	}
	return len(defs) * maxLen
}

// cloneFloatPtr preserves the nil-vs-zero distinction. A measured zero must
// remain a numeric 0 in the export (FR-011); a missing observation must be
// an empty cell. NaN and Inf are propagated as non-nil so the writer can
// choose to emit them as empty (FR-011 forbids reinterpreting them as
// valid numeric measurements).
func cloneFloatPtr(v float64) *float64 {
	cp := v
	return &cp
}
