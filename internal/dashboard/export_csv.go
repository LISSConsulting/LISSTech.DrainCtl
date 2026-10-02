//go:build windows

package dashboard

import (
	"encoding/csv"
	"io"
	"sort"
	"strconv"
)

var csvHeader = []string{
	"timestamp_utc",
	"host_name",
	"series_id",
	"series_label",
	"unit",
	"value",
	"aggregation",
	"resolution",
}

// WriteCSV serializes the snapshot's data rows to w as UTF-8 CSV with one
// fixed header row. The column order matches the Excel Data sheet so the two
// export formats preserve equivalent observations (FR-006 and FR-012).
func WriteCSV(w io.Writer, snap Snapshot) error {
	rows := append([]Observation(nil), snap.Rows...)
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].Timestamp.Equal(rows[j].Timestamp) {
			return rows[i].Timestamp.Before(rows[j].Timestamp)
		}
		if rows[i].SeriesID != rows[j].SeriesID {
			return rows[i].SeriesID < rows[j].SeriesID
		}
		return rows[i].HostName < rows[j].HostName
	})

	writer := csv.NewWriter(w)
	if err := writer.Write(csvHeader); err != nil {
		return err
	}
	for _, row := range rows {
		hostName := row.HostName
		if hostName == hostFleetPlaceholder {
			hostName = ""
		}
		value := ""
		if row.Value != nil {
			value = strconv.FormatFloat(*row.Value, 'f', -1, 64)
		}
		if err := writer.Write([]string{
			row.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			hostName,
			row.SeriesID,
			row.SeriesLabel,
			row.Unit,
			value,
			row.Aggregation,
			row.Resolution,
		}); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}
