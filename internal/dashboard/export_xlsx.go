//go:build windows

package dashboard

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	excelize "github.com/xuri/excelize/v2"
)

var xlsxDataHeaders = []string{
	"timestamp_utc", "host_name", "series_id", "series_label",
	"unit", "value", "aggregation", "resolution",
}

// WriteXLSX serializes snap as a formula-safe Excel workbook. Text always uses
// shared strings and metric values are written as numeric cells.
func WriteXLSX(w io.Writer, snap Snapshot) error {
	f := excelize.NewFile()
	defer f.Close()

	if _, err := f.NewSheet("Data"); err != nil {
		return err
	}
	if _, err := f.NewSheet("Context"); err != nil {
		return err
	}
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return err
	}
	if err := f.MoveSheet("Data", "Context"); err != nil {
		return err
	}
	f.SetActiveSheet(0)

	headerStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	wrapStyle, err := f.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{WrapText: true}})
	if err != nil {
		return err
	}
	if err := writeXLSXData(f, headerStyle, snap); err != nil {
		return err
	}
	if err := writeXLSXContext(f, headerStyle, wrapStyle, snap); err != nil {
		return err
	}
	_, err = f.WriteTo(w)
	return err
}

func writeXLSXData(f *excelize.File, headerStyle int, snap Snapshot) error {
	for col, header := range xlsxDataHeaders {
		cell, err := excelize.CoordinatesToCellName(col+1, 1)
		if err != nil {
			return err
		}
		if err := f.SetCellStr("Data", cell, header); err != nil {
			return err
		}
	}
	if err := f.SetCellStyle("Data", "A1", "H1", headerStyle); err != nil {
		return err
	}
	for i, row := range snap.Rows {
		excelRow := i + 2
		values := []string{
			row.Timestamp.UTC().Format(time.RFC3339), row.HostName, row.SeriesID,
			row.SeriesLabel, row.Unit, "", row.Aggregation, row.Resolution,
		}
		if row.HostName == hostFleetPlaceholder {
			values[1] = ""
		}
		for col, value := range values {
			if col == 5 || value == "" {
				continue
			}
			cell, err := excelize.CoordinatesToCellName(col+1, excelRow)
			if err != nil {
				return err
			}
			if err := f.SetCellStr("Data", cell, value); err != nil {
				return err
			}
		}
		if row.Value != nil && !math.IsNaN(*row.Value) && !math.IsInf(*row.Value, 0) {
			cell, err := excelize.CoordinatesToCellName(6, excelRow)
			if err != nil {
				return err
			}
			if err := f.SetCellValue("Data", cell, *row.Value); err != nil {
				return err
			}
		}
	}
	if err := f.SetPanes("Data", &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
		Selection: []excelize.Selection{{SQRef: "A2", ActiveCell: "A2", Pane: "bottomLeft"}},
	}); err != nil {
		return err
	}
	if err := f.AutoFilter("Data", fmt.Sprintf("A1:H%d", len(snap.Rows)+1), []excelize.AutoFilterOptions{}); err != nil {
		return err
	}
	for _, width := range []struct {
		col   string
		width float64
	}{
		{"A", 22}, {"B", 20}, {"C", 20}, {"D", 30}, {"E", 12}, {"F", 16}, {"G", 16}, {"H", 14},
	} {
		if err := f.SetColWidth("Data", width.col, width.col, width.width); err != nil {
			return err
		}
	}
	return nil
}

func writeXLSXContext(f *excelize.File, headerStyle, wrapStyle int, snap Snapshot) error {
	for col, header := range []string{"Field", "Value"} {
		cell, err := excelize.CoordinatesToCellName(col+1, 1)
		if err != nil {
			return err
		}
		if err := f.SetCellStr("Context", cell, header); err != nil {
			return err
		}
	}
	if err := f.SetCellStyle("Context", "A1", "B1", headerStyle); err != nil {
		return err
	}
	rows := xlsxContextRows(snap)
	for i, row := range rows {
		excelRow := i + 2
		if err := f.SetCellStr("Context", fmt.Sprintf("A%d", excelRow), row.field); err != nil {
			return err
		}
		if err := f.SetCellStr("Context", fmt.Sprintf("B%d", excelRow), row.value); err != nil {
			return err
		}
		if row.wrap {
			if err := f.SetCellStyle("Context", fmt.Sprintf("B%d", excelRow), fmt.Sprintf("B%d", excelRow), wrapStyle); err != nil {
				return err
			}
		}
	}
	if err := f.SetPanes("Context", &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	if err := f.SetColWidth("Context", "A", "A", 24); err != nil {
		return err
	}
	return f.SetColWidth("Context", "B", "B", 70)
}

type xlsxContextRow struct {
	field string
	value string
	wrap  bool
}

func xlsxContextRows(snap Snapshot) []xlsxContextRow {
	aggregation, resolution := xlsxSummary(snap)
	rows := []xlsxContextRow{{"graph", snap.GraphLabel, false}, {"export_type", string(snap.ExportType), false}}
	if snap.ExportType == ExportTypeFleet {
		rows = append(rows, xlsxContextRow{"host_filter", strings.Join(snap.HostFilter, "\n"), true}, xlsxContextRow{"host_filter_count", fmt.Sprintf("%d", len(snap.HostFilter)), false})
	} else {
		rows = append(rows, xlsxContextRow{"host_name", snap.HostName, false})
	}
	return append(rows,
		xlsxContextRow{"series", xlsxSeries(snap.Series), true},
		xlsxContextRow{"time_range_start_utc", snap.From.UTC().Format(time.RFC3339), false},
		xlsxContextRow{"time_range_end_utc", snap.To.UTC().Format(time.RFC3339), false},
		xlsxContextRow{"display_timezone", snap.DisplayTimezone, false},
		xlsxContextRow{"aggregation", aggregation, false},
		xlsxContextRow{"resolution", resolution, false},
		xlsxContextRow{"coverage", string(snap.Coverage), false},
		xlsxContextRow{"coverage_note", snap.CoverageNote, false},
		xlsxContextRow{"exported_at_utc", time.Now().UTC().Format(time.RFC3339), false},
		xlsxContextRow{"generator", ResolveGeneratorVersion(), false},
	)
}

func xlsxSeries(series []SeriesDefinition) string {
	items := make([]string, len(series))
	for i, definition := range series {
		items[i] = fmt.Sprintf("%s (%s, %s, %s, %s)", definition.SeriesID, definition.SeriesLabel, definition.Unit, definition.Aggregation, definition.Resolution)
	}
	return strings.Join(items, ", ")
}

func xlsxSummary(snap Snapshot) (aggregation, resolution string) {
	if len(snap.Series) == 0 {
		return "", ""
	}
	return snap.Series[0].Aggregation, snap.Series[0].Resolution
}
